import type { Page, Route } from "@playwright/test";
import type {
  AgentSession,
  Message,
  TimelineItem,
  Turn,
  TurnSummary,
} from "../../lib/project-api";
import type {
  ConversationHead,
  DisplayItem,
} from "../../lib/conversation-repository";

// Scenarios describe model data; this adapter exposes only the current view
// protocol. It is not a legacy endpoint or a production fallback.
type Scenario = {
  session: AgentSession;
  messages?: Message[];
  timeline?: TimelineItem[];
  turns?: Turn[];
  turn_summaries?: TurnSummary[];
  has_earlier?: boolean;
  has_later?: boolean;
  recovery?: ConversationHead["recovery"];
};
export class ConversationFixture {
  private rows = new Map<string, DisplayItem>();
  private revision = 0;
  private log: {
    revision: string;
    ordinal: number;
    id: string;
    operation: string;
    item?: DisplayItem;
  }[] = [];
  reply(s: Scenario, url: URL) {
    const revision = s.session.revision || 1;
    const timeline: TimelineItem[] =
      s.timeline ||
      s.messages?.map((m) => ({
        id: m.id,
        turn_id: m.turn_id || m.id,
        kind: m.role,
        content: m.content,
        message_id: m.id,
        cursor: m.sequence,
      })) ||
      [];
    const items = timeline.map((item, index): DisplayItem => ({
      ...item,
      position: String(item.cursor || index + 1),
      block: item.order || 0,
      item_revision: String(revision),
    }));
    if (revision > this.revision) {
      const present = new Set(items.map((i) => i.id));
      let ordinal = 0;
      if (!s.has_earlier && !s.has_later)
        for (const id of this.rows.keys())
          if (!present.has(id)) {
            this.log.push({
              revision: String(revision),
              ordinal: ordinal++,
              id,
              operation: "delete",
            });
            this.rows.delete(id);
          }
      for (const item of items)
        this.log.push({
          revision: String(revision),
          ordinal: ordinal++,
          id: item.id,
          operation: "upsert",
          item,
        });
      this.revision = revision;
    }
    for (const item of items) this.rows.set(item.id, item);
    const activeTurn = s.turns?.find(t => ["starting", "running", "stopping", "unknown"].includes(t.state));
    const head: ConversationHead = {
      session: s.session,
      turns: s.turns || [],
      active_run: activeTurn
        ? { id: activeTurn.id, prompt_id: activeTurn.id,
            incarnation_id: "fixture-incarnation", owner: 1, state: "running" }
        : undefined,
      control_revision: String(s.session.control_revision || 1),
      content_revision: String(revision),
      index_revision: String(revision),
      projection_generation: "fixture",
      projection_state: "ready",
      source_frontier: "source",
      projected_frontier: "source",
      has_content: !!items.length || !!s.has_earlier || !!s.has_later,
      recovery: s.recovery || [],
    };
    const action = url.pathname.split("/").at(-1);
    if (action === "head") return head;
    if (action === "turns")
      return {
        projection_generation: "fixture",
        index_revision: String(revision),
        turn_summaries: s.turn_summaries || [],
      };
    if (action === "content") {
      const id = decodeURIComponent(url.pathname.split("/").at(-2)!);
      const text =
        s.messages?.find((m) => m.id === id)?.content ||
        items
          .filter((i) => i.message_id === id && i.kind === "assistant")
          .map((i) => i.content || "")
          .join("\n\n");
      const at = Number(url.searchParams.get("cursor") || 0),
        end = Math.min(text.length, at + 32768);
      return {
        version: id,
        data: text.slice(at, end),
        ...(end < text.length ? { next: String(end) } : {}),
      };
    }
    if (action === "changes") {
      const base = Number(url.searchParams.get("after_revision") || 0),
        offset = Number(url.searchParams.get("cursor") || 0);
      const all = this.log.filter(
          (c) => Number(c.revision) > base && Number(c.revision) <= revision,
        ),
        batch = all.slice(offset, offset + 100),
        more = offset + batch.length < all.length;
      return {
        projection_generation: "fixture",
        base_revision: String(base),
        target_revision: String(revision),
        processed_through: String(more ? base : revision),
        changes: batch,
        has_more: more,
        ...(more ? { cursor: String(offset + batch.length) } : {}),
      };
    }
    const positions = items.map((i) => Number(i.position));
    return {
      head,
      items,
      turn_summaries: s.turn_summaries || [],
      window: "fixture-window",
      ...(s.has_earlier ? { before: String(Math.min(...positions)) } : {}),
      ...(s.has_later ? { after: String(Math.max(...positions)) } : {}),
    };
  }
}
export async function conversationRoute(
  page: Page,
  handler: (route: Route) => unknown | Promise<unknown>,
) {
  const views = new ConversationFixture();
  await page.route("**/sessions/analysis/view/**", async (route) => {
    const proxy = new Proxy(route, {
      get(target, key) {
        if (key === "fulfill")
          return async (options: Parameters<Route["fulfill"]>[0]) => {
            const data = JSON.parse(String(options?.body || "{}"));
            return target.fulfill({
              ...options,
              body: JSON.stringify(
                data.session
                  ? views.reply(data, new URL(route.request().url()))
                  : data,
              ),
            });
          };
        const value = Reflect.get(target, key);
        return typeof value === "function" ? value.bind(target) : value;
      },
    });
    await handler(proxy);
  });
}
export const invalidateConversation = (page: Page) =>
  page.evaluate(() =>
    window.dispatchEvent(
      new CustomEvent("fixture-resource-event", {
        detail: { resource_kind: "content", resource_id: "analysis" },
      }),
    ),
  );
