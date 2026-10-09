export type WindowPage<T> = { rows: T[]; next?: string; previous?: string };
export type WindowSnapshot<T> = {
  pages: WindowPage<T>[];
  loading: boolean;
  updating: boolean;
  error?: unknown;
  syncedAt?: number;
  resyncs?: number;
};
export const SCHEDULE_WINDOW_PAGES = 5;
export const SCHEDULE_PAGE_SIZE = 50;

/** A single-flight, cancelable, bounded continuous window. Transport replies
 * are never retained separately from the assembled pages. */
export class ScheduleWindow<T extends { id: string; revision?: number }> {
  private snapshot: WindowSnapshot<T> = {
    pages: [],
    loading: true,
    updating: false,
  };
  private listeners = new Set<() => void>();
  private abort?: AbortController;
  private generation = 0;
  private active?: Promise<void>;
  private dirty = false;
  private disposed = false;
  private resyncs = 0;
  constructor(
    private read: (
      params: URLSearchParams,
      signal: AbortSignal,
    ) => Promise<WindowPage<T>>,
    initial?: WindowPage<T>[],
  ) {
    if (initial?.length)
      this.snapshot = {
        pages: initial.slice(-SCHEDULE_WINDOW_PAGES),
        loading: false,
        updating: false,
      };
  }
  getSnapshot = () => this.snapshot;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private emit(next: WindowSnapshot<T>) {
    this.snapshot = { ...next, resyncs: this.resyncs };
    for (const listener of this.listeners) listener();
  }
  private async page(params: URLSearchParams, signal: AbortSignal) {
    const value = await this.read(params, signal);
    if (
      value.rows.length > SCHEDULE_PAGE_SIZE ||
      JSON.stringify(value).length * 3 > 1024 * 1024
    )
      throw new Error("Schedule page exceeds its display budget.");
    return value;
  }
  dispose() {
    this.disposed = true;
    this.generation++;
    this.abort?.abort();
    this.active = undefined;
    this.dirty = false;
  }
  resume() {
    this.disposed = false;
  }
  replace(page: WindowPage<T>) {
    this.generation++;
    this.abort?.abort();
    this.active = undefined;
    this.dirty = false;
    this.emit({
      pages: [page],
      loading: false,
      updating: false,
      syncedAt: Date.now(),
    });
  }
  async refresh(anchor?: string, allowSuccessor = true): Promise<void> {
    if (this.disposed) return;
    if (this.active) {
      this.dirty = true;
      return this.active;
    }
    const count = Math.max(1, this.snapshot.pages.length);
    const resident = this.snapshot.pages[0];
    const start =
      anchor ?? (resident?.previous ? resident.rows[0]?.id : undefined);
    return this.run(async (signal) => {
      const pages: WindowPage<T>[] = [];
      let params = new URLSearchParams(start ? { anchor: start } : {});
      for (let n = 0; n < count; n++) {
        const page = await this.page(params, signal);
        pages.push(page);
        if (!page.next) break;
        params = new URLSearchParams({ after: page.next });
      }
      return pages;
    }, allowSuccessor);
  }
  async extend(direction: "next" | "previous"): Promise<void> {
    if (this.active || this.disposed) return;
    const pages = this.snapshot.pages;
    const cursor =
      direction === "next" ? pages.at(-1)?.next : pages[0]?.previous;
    if (!cursor) return;
    return this.run(async (signal) => {
      const page = await this.page(
        new URLSearchParams({
          [direction === "next" ? "after" : "before"]: cursor,
        }),
        signal,
      );
      const next = direction === "next" ? [...pages, page] : [page, ...pages];
      return direction === "next"
        ? next.slice(-SCHEDULE_WINDOW_PAGES)
        : next.slice(0, SCHEDULE_WINDOW_PAGES);
    });
  }
  private async run(
    load: (signal: AbortSignal) => Promise<WindowPage<T>[]>,
    allowSuccessor = true,
  ): Promise<void> {
    const generation = ++this.generation;
    const abort = (this.abort = new AbortController());
    this.emit({ ...this.snapshot, updating: true, error: undefined });
    const flight = (async () => {
      try {
        const pages = await load(abort.signal);
        if (
          abort.signal.aborted ||
          generation !== this.generation ||
          this.disposed
        )
          return;
        this.emit({
          pages,
          loading: false,
          updating: false,
          syncedAt: Date.now(),
        });
      } catch (error) {
        if (
          abort.signal.aborted ||
          generation !== this.generation ||
          this.disposed
        )
          return;
        // A catalog cursor is a snapshot, not a permanent address. Reconcile
        // the resident anchor rather than repeatedly replaying stale cursors.
        if ((error as { status?: number }).status === 409) {
          this.dirty = true;
          this.resyncs++;
        }
        const denied = [401, 403, 404].includes(
          (error as { status?: number }).status || 0,
        );
        this.emit({
          ...this.snapshot,
          pages: denied ? [] : this.snapshot.pages,
          loading: false,
          updating: false,
          error,
        });
      }
    })();
    this.active = flight;
    await flight;
    if (this.active === flight) this.active = undefined;
    if (generation !== this.generation || this.disposed) return;
    // At most one successor per finite burst. New events after it can schedule
    // another refresh, but a continuous writer cannot starve displayed data.
    if (this.dirty && allowSuccessor && !this.disposed) {
      this.dirty = false;
      const follow = this.refresh(undefined, false);
      this.dirty = false;
      await follow;
    }
  }
}

export function windowRows<T extends { id: string; revision?: number }>(
  pages: WindowPage<T>[],
): T[] {
  const merged = new Map<string, T>();
  for (const page of pages)
    for (const item of page.rows) {
      const old = merged.get(item.id);
      if (!old || (item.revision ?? 0) >= (old.revision ?? 0))
        merged.set(item.id, item);
    }
  return [...merged.values()];
}
export function changedRuns<T extends { id: string; state: string }>(
  old: T[],
  fresh: T[],
): number {
  const map = new Map(old.map((item) => [item.id, item.state]));
  return fresh.filter(
    (item) => !map.has(item.id) || map.get(item.id) !== item.state,
  ).length;
}

/** Bounded deduplication of disposable outbox hints, independent of task
 * revisions and authoritative page state. A new transport incarnation resets
 * its ordering evidence. */
export class ScheduleHintGate {
  private incarnation = "";
  private revisions = new Map<string, bigint>();
  accept(event: {
    stream_incarnation: string;
    resource_kind: string;
    resource_id?: string;
    revision?: string;
    payload?: unknown;
  }): boolean {
    if (
      event.stream_incarnation &&
      event.stream_incarnation !== this.incarnation
    ) {
      this.incarnation = event.stream_incarnation;
      this.revisions.clear();
    }
    if (
      event.resource_kind === "resync" ||
      !event.revision ||
      !/^\d{1,20}$/.test(event.revision)
    )
      return true;
    const payload = event.payload as { project_id?: string } | undefined;
    const key = `${event.resource_kind}/${payload?.project_id || ""}/${event.resource_id || ""}`;
    const revision = BigInt(event.revision);
    if ((this.revisions.get(key) ?? BigInt(-1)) >= revision) return false;
    this.revisions.delete(key);
    this.revisions.set(key, revision);
    if (this.revisions.size > 256)
      this.revisions.delete(this.revisions.keys().next().value!);
    return true;
  }
}
