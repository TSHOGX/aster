import { afterEach, expect, it, vi } from "vitest";
import {
  ConversationRepository,
  compareItems,
  type ConversationHead,
  type ConversationPage,
  type DisplayItem,
} from "./conversation-repository";
import { ResourceStore } from "./resource-store";

const head = (revision = "1", generation = "g"): ConversationHead => ({
  session: {
    id: "s",
    project_id: "p",
    title: "Session",
    state: "idle",
    revision: Number(revision),
    control_revision: 1,
    deletion: 0,
  },
  turns: [],
  control_revision: "1",
  content_revision: revision,
  index_revision: revision,
  projection_generation: generation,
  projection_state: "ready",
  source_frontier: "source",
  projected_frontier: "source",
  has_content: true,
});
const item = (
  position: number,
  revision = "1",
  content = `item ${position}`,
): DisplayItem => ({
  id: `i${position}`,
  turn_id: "t",
  turn_sequence: "1",
  position: String(position),
  block: 0,
  item_revision: revision,
  kind: "assistant",
  content,
});
const page = (
  items: DisplayItem[],
  h = head(),
  before?: string,
  after?: string,
): ConversationPage => ({
  head: h,
  items,
  turn_summaries: [],
  window: "window",
  before,
  after,
});
const changes = (
  base: string,
  target: string,
  rows: DisplayItem[] = [],
  deleted: string[] = [],
  generation = "g",
) => ({
  projection_generation: generation,
  base_revision: base,
  target_revision: target,
  processed_through: target,
  has_more: false,
  changes: [
    ...rows.map((item) => ({
      revision: item.item_revision,
      ordinal: 0,
      id: item.id,
      operation: "upsert",
      item,
    })),
    ...deleted.map((id) => ({
      revision: target,
      ordinal: 1,
      id,
      operation: "delete",
    })),
  ],
});
function server(fn: (url: URL) => unknown | Promise<unknown>) {
  const calls: string[] = [];
  vi.stubGlobal("fetch", async (path: string) => {
    calls.push(path);
    const result = await fn(new URL(path, "http://fixture"));
    return result instanceof Response
      ? result
      : new Response(JSON.stringify(result));
  });
  return calls;
}
const repos: ConversationRepository[] = [];
const repo = (
  store = new ResourceStore("actor"),
  project = "p",
  session = "s",
) => {
  const r = new ConversationRepository(store, project, session);
  repos.push(r);
  return r;
};
afterEach(() => {
  for (const r of repos) r.close();
  repos.length = 0;
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

it("retains exact-session content and does no heavy read on healthy unchanged heads", async () => {
  let revision = "1";
  const calls = server((url) =>
    url.pathname.endsWith("/pages")
      ? page([item(1)])
      : url.pathname.endsWith("/head")
        ? head(revision)
        : url.pathname.endsWith("/turns")
          ? {
              projection_generation: "g",
              index_revision: revision,
              turn_summaries: [],
            }
          : changes(
              url.searchParams.get("after_revision")!,
              revision,
              revision === "2" ? [item(1, "2", "new"), item(2, "2")] : [],
            ),
  );
  const store = new ResourceStore("actor"),
    a = repo(store);
  await a.load();
  expect(a.getSnapshot().items[0].content).toBe("item 1");
  const returnA = repo(store),
    b = repo(store, "p", "b");
  expect(returnA.getSnapshot().items[0].content).toBe("item 1");
  expect(b.getSnapshot().status).toBe("uninitialized");
  calls.length = 0;
  for (let n = 0; n < 12; n++) await a.refresh();
  expect(calls).toHaveLength(12);
  expect(calls.every((c) => c.endsWith("/head"))).toBe(true);
  revision = "2";
  await a.refresh();
  expect(a.getSnapshot().items.map((i) => i.content)).toEqual([
    "new",
    "item 2",
  ]);
});

it("bridges page frontiers, boundary insertions and deletes without retaining off-window history", async () => {
  server((url) => {
    const base = url.searchParams.get("after_revision") || "";
    if (url.pathname.endsWith("/head")) return head("5");
    if (url.pathname.endsWith("/pages"))
      return url.searchParams.has("before")
        ? page([item(50), item(60)], head("1"), "earlier", "later")
        : page([item(100, "5", "fresh"), item(110, "5")], head("5"), "older");
    if (url.pathname.endsWith("/turns"))
      return {
        projection_generation: "g",
        index_revision: "5",
        turn_summaries: [],
      };
    return base === "1"
      ? changes(
          base,
          "5",
          [item(10, "2"), item(70, "2"), item(100, "2", "stale")],
          ["i50"],
        )
      : changes(base, "5");
  });
  const r = repo();
  await r.load();
  await r.load({ before: "older" }, "older");
  expect(r.getSnapshot().items.map((i) => i.id)).toEqual([
    "i60",
    "i70",
    "i100",
    "i110",
  ]);
  expect(r.getSnapshot().items.find((i) => i.id === "i100")?.content).toBe(
    "fresh",
  );
});

it("consumes intra-revision cursors before publishing a complete page frontier", async () => {
  let revision = "1",
    batches = 0;
  server((url) => {
    if (url.pathname.endsWith("/head")) return head(revision);
    if (url.pathname.endsWith("/pages")) return page([item(1)]);
    if (url.pathname.endsWith("/turns"))
      return {
        projection_generation: "g",
        index_revision: revision,
        turn_summaries: [],
      };
    const base = url.searchParams.get("after_revision")!;
    if (revision === "1") return changes(base, "1");
    batches++;
    if (!url.searchParams.get("cursor"))
      return {
        ...changes(base, "2", [item(2, "2")]),
        processed_through: "1",
        has_more: true,
        cursor: "second-operation",
      };
    expect(base).toBe("1");
    return changes(base, "2", [], ["i1"]);
  });
  const r = repo();
  await r.load();
  revision = "2";
  await r.refresh();
  expect(batches).toBe(2);
  expect(r.getSnapshot().items.map((i) => i.id)).toEqual(["i2"]);
});

it("resyncs only the reading anchor after a generation change", async () => {
  let generation = "g";
  const calls = server((url) => {
    if (url.pathname.endsWith("/head")) return head("1", generation);
    if (url.pathname.endsWith("/pages"))
      return page(
        [item(100), item(110)],
        head("1", generation),
        "earlier",
        "later",
      );
    return changes("1", "1", [], [], generation);
  });
  const r = repo();
  await r.load();
  r.setAnchor("i110");
  calls.length = 0;
  generation = "rebuilt";
  await r.refresh();
  expect(calls.filter((c) => c.includes("/pages?"))).toEqual([
    "/api/projects/p/sessions/s/view/pages?around_item=i110",
  ]);
  expect(r.getSnapshot().head?.projection_generation).toBe("rebuilt");
});

it("does not turn pending or nonempty out-of-range pages into an empty conversation", async () => {
  vi.useFakeTimers();
  server(() => new Response('{"status":"pending"}', { status: 202 }));
  const r = repo();
  await r.load();
  expect(r.getSnapshot().status).toBe("pending");
  r.close();
  server((url) =>
    url.pathname.endsWith("/pages")
      ? page([])
      : url.pathname.endsWith("/head")
        ? head()
        : changes("1", "1"),
  );
  const other = repo();
  await other.load();
  expect(other.getSnapshot().status).toBe("available");
});

it("retains a pending anchor demand and retries it instead of the latest page", async () => {
  vi.useFakeTimers();
  let pending = true;
  const calls = server((url) => {
    if (url.pathname.endsWith("/pages"))
      return pending
        ? new Response('{"status":"pending"}', { status: 202 })
        : page([item(70)]);
    if (url.pathname.endsWith("/head")) return head();
    return changes("1", "1");
  });
  const r = repo();
  await r.load({ around_message: "message-70" });
  expect(r.getSnapshot().status).toBe("pending");
  pending = false;
  await vi.advanceTimersByTimeAsync(1001);
  expect(r.getSnapshot().items[0].id).toBe("i70");
  expect(calls.filter((c) => c.includes("/pages?"))).toEqual([
    "/api/projects/p/sessions/s/view/pages?around_message=message-70",
    "/api/projects/p/sessions/s/view/pages?around_message=message-70",
  ]);
});

it("fences late navigation and revoked-content callbacks", async () => {
  let finish!: (value: unknown) => void;
  let delayed = false;
  server((url) => {
    if (url.pathname.endsWith("/pages")) {
      if (url.searchParams.get("around_turn") === "old") {
        delayed = true;
        return new Promise((resolve) => {
          finish = resolve;
        });
      }
      return page([item(2)]);
    }
    return url.pathname.endsWith("/head") ? head() : changes("1", "1");
  });
  const r = repo();
  const old = r.load({ around_turn: "old" });
  await vi.waitFor(() => expect(delayed).toBe(true));
  const next = r.load({ around_turn: "new" });
  finish(page([item(1)]));
  await Promise.all([old, next]);
  expect(r.getSnapshot().items.map((i) => i.id)).toEqual(["i2"]);
  delayed = false;
  const late = r.load({ around_turn: "old" });
  await vi.waitFor(() => expect(delayed).toBe(true));
  r.clear();
  finish(page([item(1)]));
  await late;
  expect(r.getSnapshot().status).toBe("denied");
  expect(r.getSnapshot().items).toEqual([]);
});

it("bounds a growing live tail by reacquiring a current page and keeps 64-bit order exact", async () => {
  let revision = "1",
    pages = 0;
  server((url) => {
    if (url.pathname.endsWith("/pages")) {
      pages++;
      return pages === 1
        ? page([item(1)])
        : page(
            Array.from({ length: 50 }, (_, i) => item(i + 180, "2")),
            head("2"),
            "older",
          );
    }
    if (url.pathname.endsWith("/head")) return head(revision);
    if (url.pathname.endsWith("/turns"))
      return {
        projection_generation: "g",
        index_revision: revision,
        turn_summaries: [],
      };
    const base = url.searchParams.get("after_revision")!;
    return revision === "2" && base === "1"
      ? changes(
          base,
          "2",
          Array.from({ length: 201 }, (_, i) => item(i + 2, "2")),
        )
      : changes(base, revision);
  });
  const r = repo();
  await r.load();
  revision = "2";
  await r.refresh();
  expect(pages).toBe(2);
  expect(r.getSnapshot().items).toHaveLength(50);
  expect(
    compareItems(
      { ...item(1), position: "9007199254740992" },
      { ...item(2), position: "9007199254740993" },
    ),
  ).toBe(-1);
});
