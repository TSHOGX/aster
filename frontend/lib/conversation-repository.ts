import {
  api,
  APIError,
  encode,
  sessionURL,
  type AgentSession,
  type Turn,
  type TurnSummary,
} from "./project-api";
import { ResourceStore, type ResourceKey } from "./resource-store";
import { startTiming } from "./interaction-metrics";

export type DisplayItem = {
 segment_id?: string;
 run_id?: string;
  id: string;
  turn_id: string;
  turn_sequence: string;
  position: string;
  block: number;
  item_revision: string;
  kind: string;
  content?: string;
  message_id?: string;
  name?: string;
  arguments?: string;
  result?: string;
  state?: string;
  truncated?: boolean;
  detail_version?: string;
};
export type ConversationHead = {
 active_run?: { id: string; prompt_id: string; incarnation_id: string; owner: number; state: string };
  session: AgentSession;
  turns: Turn[];
  control_revision: string;
  content_revision: string;
  index_revision: string;
  projection_generation: string;
  projection_state: "building" | "catching_up" | "ready" | "failed";
  source_frontier: string;
  projected_frontier: string;
  has_content: boolean;
  has_more_turns?: boolean;
  error?: string;
  recovery?: {
    id: string;
    state: string;
    cleanup: string;
    recovery: string;
    error?: string;
  }[];
};
export type ConversationPage = {
  head: ConversationHead;
  items: DisplayItem[];
  turn_summaries: TurnSummary[];
  before?: string;
  after?: string;
  window: string;
};
export type ConversationChanges = {
  projection_generation: string;
  base_revision: string;
  target_revision: string;
  processed_through: string;
  changes: {
    revision: string;
    ordinal: number;
    id: string;
    operation: "upsert" | "delete";
    item?: DisplayItem;
  }[];
  cursor?: string;
  has_more: boolean;
};
export type PageSelector = {
  before?: string;
  after?: string;
  around_turn?: string;
  around_message?: string;
  around_item?: string;
};
type Window = ConversationPage & {
  frontier: string;
  lower?: DisplayItem;
  upper?: DisplayItem;
};
type CachedConversation = {
  head: ConversationHead;
  windows: Window[];
  anchor: string;
  appliedSourceFrontier: string | null;
};
export type ConversationState = {
  head: ConversationHead | null;
  applied_source_frontier: string | null;
  items: DisplayItem[];
  summaries: TurnSummary[];
  before?: string;
  after?: string;
  status:
    | "uninitialized"
    | "loading"
    | "available"
    | "empty"
    | "pending"
    | "error"
    | "denied";
  error: string;
};
const compareInt = (a: string, b: string) =>
  BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0;
export const compareItems = (a: DisplayItem, b: DisplayItem) =>
  compareInt(a.position, b.position) ||
  a.block - b.block ||
  a.id.localeCompare(b.id);
const resync = () =>
  new APIError(409, "Conversation changed; rebuilding the visible window.");
class WindowBudget extends Error {}

/** Each loaded page retains its frontier until its own fixed-target catch-up
 * completes. Off-window updates never accumulate an unbounded transcript. */
export class ConversationRepository {
  private windows: Window[] = [];
  private head: ConversationHead | null = null;
  private appliedSourceFrontier: string | null = null;
  private anchor = "";
  private listeners = new Set<() => void>();
  private abort = new AbortController();
  private navigation = 0;
  private work: Promise<void> = Promise.resolve();
  private queuedReads = 0;
  private peakReads = 0;
  private navigationDemand: { selector: PageSelector; epoch: number } | null =
    null;
  private navigationFlight: Promise<void> | null = null;
  private pendingPage: {
    selector: PageSelector;
    mode: "replace" | "older" | "newer";
    epoch: number;
  } | null = null;
  private dirty = 0;
  private refreshFlight: Promise<void> | null = null;
  private retry: ReturnType<typeof setTimeout> | undefined;
  private closed = false;
  private unpin: (() => void) | undefined;
  private key: ResourceKey;
  readonly base: string;
  private current: ConversationState = {
    head: null,
    applied_source_frontier: null,
    items: [],
    summaries: [],
    status: "uninitialized",
    error: "",
  };
  constructor(
    private resources: ResourceStore,
    project: string,
    session: string,
  ) {
    this.key = {
      project,
      kind: "conversation",
      identity: `conversation:${session}`,
    };
    this.base = sessionURL(project, session) + "/view";
    const cached = resources.peek<CachedConversation>(this.key);
    if (cached) {
      this.windows = cached.windows;
      this.head = cached.head;
      this.appliedSourceFrontier = cached.appliedSourceFrontier;
      this.anchor = cached.anchor;
      this.emit();
    }
  }
  getSnapshot = () => this.current;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  setAnchor(id: string, following = false) {
    this.anchor = following ? "" : id;
  }
  activate() {
    if (this.closed) {
      this.closed = false;
      this.abort = new AbortController();
      this.navigation++;
    }
    if (!this.unpin) this.unpin = this.resources.pin(this.key);
  }
  close() {
    this.closed = true;
    this.navigation++;
    this.abort.abort();
    this.navigationDemand = null;
    clearTimeout(this.retry);
    this.retry = undefined;
    this.listeners.clear();
    this.unpin?.();
    this.unpin = undefined;
  }
  clear() {
    this.navigation++;
    this.navigationDemand = null;
    this.pendingPage = null;
    this.windows = [];
    this.head = null;
    this.appliedSourceFrontier = null;
    this.resources.clear(this.key.project);
    this.emit("denied", "This conversation is unavailable.");
  }
  private read<T>(suffix: string, pending = false): Promise<T> {
    const url = this.base + suffix;
    return this.resources.read(
      { ...this.key, identity: url },
      (signal) => api<T>(url, { signal }, pending),
      this.abort.signal,
    );
  }
  private emit(status?: ConversationState["status"], error = "") {
    if (this.closed) return;
    const done = startTiming("conversation", "commit");
    const items = new Map<string, DisplayItem>(),
      summaries = new Map<string, TurnSummary>();
    for (const window of this.windows) {
      for (const item of window.items) {
        const old = items.get(item.id);
        if (!old || compareInt(item.item_revision, old.item_revision) > 0)
          items.set(item.id, item);
      }
      for (const summary of window.turn_summaries)
        summaries.set(summary.id, summary);
    }
    let ordered = [...items.values()].sort(compareItems);
    if (
      ordered.length === this.current.items.length &&
      ordered.every((item, i) => item === this.current.items[i])
    )
      ordered = this.current.items;
    let summaryRows = [...summaries.values()];
    if (
      summaryRows.length === this.current.summaries.length &&
      summaryRows.every((item, i) => item === this.current.summaries[i])
    )
      summaryRows = this.current.summaries;
    this.current = {
      head: this.head,
      applied_source_frontier: this.appliedSourceFrontier,
      items: ordered,
      summaries: summaryRows,
      before: this.windows[0]?.before,
      after: this.windows.at(-1)?.after,
      status:
        status ||
        (this.windows.length
          ? ordered.length || this.head?.has_content
            ? "available"
            : "empty"
          : "loading"),
      error,
    };
    if (this.head && this.windows.length)
      this.resources.put(this.key, {
        head: this.head,
        windows: this.windows,
        anchor: this.anchor,
        appliedSourceFrontier: this.appliedSourceFrontier,
      });
    for (const listener of this.listeners) listener();
    done();
  }
  private acceptHead(head: ConversationHead) {
    if (
      this.head &&
      head.projection_generation === this.head.projection_generation &&
      head.session.revision < this.head.session.revision
    )
      return;
    this.head = head;
  }
  private handle(error: unknown) {
    if (
      this.closed ||
      (error instanceof DOMException && error.name === "AbortError")
    )
      return;
    if (error instanceof APIError && [401, 403, 404].includes(error.status)) {
      this.clear();
      return;
    }
    const pending = error instanceof APIError && error.status === 202;
    this.emit(
      pending ? "pending" : "error",
      error instanceof Error ? error.message : String(error),
    );
    if (pending || (error instanceof APIError && error.status === 503))
      this.schedule();
  }
  private schedule() {
    if (this.retry || this.closed) return;
    this.retry = setTimeout(() => {
      this.retry = undefined;
      if (typeof document === "undefined" || !document.hidden)
        void this.refresh();
    }, 1000);
  }
  workStats() {
    return {
      reads: this.queuedReads,
      peakReads: this.peakReads,
      navigation: Number(!!this.navigationFlight),
    };
  }
  private enqueue(fn: () => Promise<void>, navigation = false): Promise<void> {
    if (this.closed) return Promise.resolve();
    if (!navigation && this.queuedReads >= 8) {
      this.handle(
        new Error(
          "Conversation read capacity reached. Retry after pending reads settle.",
        ),
      );
      return Promise.resolve();
    }
    if (!navigation) {
      this.queuedReads++;
      this.peakReads = Math.max(this.peakReads, this.queuedReads);
    }
    const next = this.work.then(async () => {
      try {
        if (!this.closed) await fn();
      } finally {
        if (!navigation) this.queuedReads--;
      }
    });
    this.work = next.catch(() => {});
    return next;
  }
  load(
    selector: PageSelector = {},
    mode: "replace" | "older" | "newer" = "replace",
  ) {
    const epoch = mode === "replace" ? ++this.navigation : this.navigation;
    if (mode === "replace") this.pendingPage = null;
    if (mode === "replace" && !Object.keys(selector).length) this.anchor = "";
    if (mode !== "replace")
      return this.enqueue(() => this.loadNow(selector, mode, epoch));
    this.navigationDemand = { selector, epoch };
    if (this.navigationFlight) return this.navigationFlight;
    const flight = this.enqueue(async () => {
      try {
        while (this.navigationDemand && !this.closed) {
          const demand = this.navigationDemand;
          this.navigationDemand = null;
          await this.loadNow(demand.selector, "replace", demand.epoch);
        }
      } finally {
        // Release synchronously with the worker's last demand check. A new
        // microtask demand must not attach to an already-settling old flight.
        this.navigationFlight = null;
      }
    }, true);
    this.navigationFlight = flight;
    const release = () => {
      if (this.navigationFlight === flight) this.navigationFlight = null;
    };
    void flight.then(release, release);
    return flight;
  }
  private async loadNow(
    selector: PageSelector,
    mode: "replace" | "older" | "newer",
    epoch: number,
  ) {
    if (this.closed || epoch !== this.navigation) return;
    if (!this.windows.length && !this.current.items.length)
      this.emit("loading");
    try {
      const query = new URLSearchParams(selector).toString();
      const page = await this.read<ConversationPage>(`/pages?${query}`);
      if (this.closed || epoch !== this.navigation) return;
      if (
        this.head &&
        this.head.projection_generation !== page.head.projection_generation &&
        mode !== "replace"
      )
        throw resync();
      const window: Window = {
        ...page,
        frontier: page.head.content_revision,
        lower: page.items[0],
        upper: page.items.at(-1),
      };
      // Catch-up precedes merging a newly loaded page into newer cached windows.
      const candidate =
        mode === "replace"
          ? [window]
          : mode === "older"
            ? [window, ...this.windows]
            : [...this.windows, window];
      const caught = await this.catchUp(candidate, undefined, epoch);
      if (this.closed || epoch !== this.navigation) return;
      this.acceptHead(page.head);
      this.windows = caught;
      this.appliedSourceFrontier = this.head?.projected_frontier ?? page.head.projected_frontier;
      this.pendingPage = null;
      this.budget(mode);
      this.emit();
      await this.refreshNow(epoch);
    } catch (error) {
      if (this.closed || epoch !== this.navigation) return;
      if (
        error instanceof WindowBudget ||
        (error instanceof APIError && error.status === 409)
      ) {
        await this.resyncNow();
        return;
      }
      if (
        error instanceof APIError &&
        error.status === 404 &&
        selector.around_item
      ) {
        await this.loadNow({}, "replace", epoch);
        return;
      }
      if (
        error instanceof APIError &&
        error.status === 404 &&
        (selector.around_turn || selector.around_message)
      ) {
        try {
          this.acceptHead(await this.read<ConversationHead>("/head", true));
          this.emit("error", "This conversation target is unavailable.");
        } catch (authorityError) {
          this.handle(authorityError);
        }
        return;
      }
      if (error instanceof APIError && error.status === 202)
        this.pendingPage = { selector, mode, epoch };
      this.handle(error);
    }
  }
  private budget(mode: string) {
    // Bound active decoded state as well as inactive store entries. Keep the
    // requested edge and the page containing the current reading anchor.
    while (
      this.windows.length > 16 ||
      JSON.stringify(this.windows).length * 3 > 8 * 1024 * 1024
    ) {
      if (this.windows.length <= 1) break;
      let remove = mode === "older" ? this.windows.length - 1 : 0;
      if (this.windows[remove].items.some((i) => i.id === this.anchor))
        remove = remove === 0 ? this.windows.length - 1 : 0;
      // Evict an edge only: removing an interior page would silently join two
      // disconnected windows. Each retained edge keeps its original seek cursor.
      this.windows = this.windows.filter((_, i) => i !== remove);
    }
  }
  private async resyncNow() {
    const anchor = this.anchor;
    this.windows = [];
    this.appliedSourceFrontier = null;
    await this.loadNow(
      anchor ? { around_item: anchor } : {},
      "replace",
      ++this.navigation,
    );
  }
  refresh = (): Promise<void> => {
    if (this.closed) return Promise.resolve();
    this.dirty++;
    if (this.refreshFlight) return this.refreshFlight;
    const flight = this.enqueue(async () => {
      for (;;) {
        const dirty = this.dirty;
        try {
          await this.refreshNow(this.navigation);
        } catch (error) {
          if (
            error instanceof WindowBudget ||
            (error instanceof APIError && error.status === 409)
          ) {
            await this.resyncNow();
            return;
          }
          this.handle(error);
          return;
        }
        if (this.closed || dirty === this.dirty) return;
      }
    });
    this.refreshFlight = flight;
    void flight.finally(() => {
      if (this.refreshFlight === flight) this.refreshFlight = null;
    });
    return flight;
  };
  private async refreshNow(epoch: number) {
    const head = await this.read<ConversationHead>("/head", true);
    if (this.closed || epoch !== this.navigation) return;
    if (
      this.head &&
      head.projection_generation &&
      this.head.projection_generation !== head.projection_generation
    )
      throw resync();
    this.acceptHead(head);
    this.emit();
    if (head.projection_state === "failed")
      throw new Error(head.error || "Conversation display is unavailable.");
    if (this.pendingPage) {
      const pending = this.pendingPage;
      this.pendingPage = null;
      await this.loadNow(pending.selector, pending.mode, pending.epoch);
      return;
    }
    if (!this.windows.length) {
      await this.loadNow({}, "replace", epoch);
      return;
    }
    const windows = await this.catchUp(
      this.windows,
      head.content_revision,
      epoch,
    );
    if (this.closed || epoch !== this.navigation) return;
    this.windows = windows;
    this.budget("newer");
    await this.updateSummaries(head, epoch);
    this.appliedSourceFrontier = head.projected_frontier;
    this.emit();
    if (head.projection_state !== "ready") this.schedule();
  }
  private async catchUp(
    windows: Window[],
    target: string | undefined,
    epoch: number,
  ): Promise<Window[]> {
    let result = windows.map((w) => ({
      ...w,
      items: [...w.items],
      turn_summaries: [...w.turn_summaries],
    }));
    for (let wi = 0; wi < result.length; wi++) {
      const window = result[wi];
      if (target !== undefined && compareInt(window.frontier, target) >= 0)
        continue;
      const base = window.frontier;
      let cursor = "",
        through = base;
      for (let rounds = 0; ; rounds++) {
        if (rounds >= 128)
          throw new Error(
            "Conversation catch-up exceeded its bounded budget. Reload this window.",
          );
        const changes = await this.read<ConversationChanges>(
          `/changes?after_revision=${base}&cursor=${encode(cursor)}`,
        );
        if (this.closed || epoch !== this.navigation) return windows;
        if (changes.projection_generation !== window.head.projection_generation)
          throw resync();
        const rows = new Map(window.items.map((i) => [i.id, i]));
        for (const change of changes.changes) {
          if (change.operation === "delete") {
            const old = rows.get(change.id);
            if (old && compareInt(change.revision, old.item_revision) >= 0)
              rows.delete(change.id);
            continue;
          }
          const item = change.item;
          if (!item) throw new Error("Invalid conversation delta");
          const old = rows.get(item.id);
          const nextLower = result[wi + 1]?.lower;
          const inWindow =
            (!window.before ||
              !window.lower ||
              compareItems(item, window.lower) >= 0) &&
            (nextLower
              ? compareItems(item, nextLower) < 0
              : !window.after ||
                !window.upper ||
                compareItems(item, window.upper) <= 0);
          if (!old && !inWindow) continue;
          if (!old || compareInt(item.item_revision, old.item_revision) > 0)
            rows.set(item.id, item);
        }
        window.items = [...rows.values()].sort(compareItems);
        through = changes.processed_through;
        if (
          window.items.length > 200 ||
          JSON.stringify(window.items).length * 3 > 1024 * 1024
        )
          throw new WindowBudget();
        if (!changes.has_more) break;
        if (!changes.cursor || changes.cursor === cursor)
          throw new Error("Conversation delta made no progress");
        cursor = changes.cursor;
      }
      window.frontier = through;
    }
    result = result.filter((w) => w.items.length || result.length === 1);
    return result;
  }
  private async updateSummaries(head: ConversationHead, epoch: number) {
    if (
      this.windows.every((w) => w.head.index_revision === head.index_revision)
    )
      return;
    const ids = [
      ...new Set(this.windows.flatMap((w) => w.items.map((i) => i.turn_id))),
    ].filter(Boolean);
    const summaries = new Map<string, TurnSummary>();
    for (let offset = 0; offset < ids.length; offset += 50) {
      const reply = await this.read<{
        projection_generation: string;
        index_revision: string;
        turn_summaries: TurnSummary[];
      }>(`/turns?ids=${encode(ids.slice(offset, offset + 50).join(","))}`);
      if (this.closed || epoch !== this.navigation) return;
      if (reply.projection_generation !== head.projection_generation)
        throw resync();
      for (const item of reply.turn_summaries) summaries.set(item.id, item);
    }
    this.windows = this.windows.map((w) => ({
      ...w,
      head: { ...w.head, index_revision: head.index_revision },
      turn_summaries: [...new Set(w.items.map((i) => i.turn_id))].flatMap(
        (id) => (summaries.has(id) ? [summaries.get(id)!] : []),
      ),
    }));
  }
  async messageText(message: string) {
    return this.textChunks(`/messages/${encode(message)}/content?`);
  }
  async detailText(id: string, version: string) {
    const full = JSON.parse(
      await this.textChunks(
        `/items/${encode(id)}/detail?version=${encode(version)}&`,
      ),
    ) as DisplayItem;
    return (
      full.content || full.result || full.arguments || JSON.stringify(full)
    );
  }
  private async textChunks(path: string) {
    let cursor = "",
      bytes = 0;
    const parts: string[] = [];
    for (;;) {
      const chunk = await this.read<{ data: string; next?: string }>(
        `${path}cursor=${encode(cursor)}`,
      );
      bytes += chunk.data.length * 2;
      if (bytes > 16 * 1024 * 1024)
        throw new Error("This answer exceeds the copy budget.");
      parts.push(chunk.data);
      if (!chunk.next) return parts.join("");
      if (chunk.next === cursor)
        throw new Error("Message download made no progress");
      cursor = chunk.next;
    }
  }
}
