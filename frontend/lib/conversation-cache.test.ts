import { expect, it } from "vitest";
import { mergeConversationPage } from "./conversation-cache";
import type { SessionSnapshot } from "./project-api";
const page = (from: number, to: number, revision = 1): SessionSnapshot => ({
  session: {
    id: "s",
    project_id: "p",
    title: "chat",
    state: "idle",
    revision,
    control_revision: 1,
    deletion: 0,
  },
  turns: [],
  record_cursor: to,
  has_earlier: from > 1,
  messages: Array.from({ length: to - from + 1 }, (_, i) => ({
    id: `m${from + i}`,
    role: "user",
    content: `message ${from + i}`,
    sequence: from + i,
    turn_id: `t${from + i}`,
    created_at: "",
  })),
  timeline: Array.from({ length: to - from + 1 }, (_, i) => ({
    id: `m${from + i}`,
    kind: "user",
    content: `message ${from + i}`,
    cursor: from + i,
    turn_id: `t${from + i}`,
  })),
});
it("prepends all intermediate pages while keeping the live end after polling", () => {
  let cache = page(201, 300);
  cache = mergeConversationPage(cache, page(101, 200), true);
  cache = mergeConversationPage(cache, page(1, 100), true);
  cache = mergeConversationPage(cache, page(203, 302, 2));
  expect(cache.messages).toHaveLength(302);
  expect(cache.timeline).toHaveLength(302);
  expect(cache.messages[0].id).toBe("m1");
  expect(cache.messages.at(-1)?.id).toBe("m302");
  expect(cache.has_earlier).toBe(false);
});
it("keeps newer content on overlapping pages but accepts missing older rows", () => {
  const latest = page(90, 110, 4);
  latest.messages[0].content = "new";
  const merged = mergeConversationPage(latest, page(1, 100, 3), true);
  expect(merged.messages).toHaveLength(110);
  expect(merged.messages.find((m) => m.id === "m90")?.content).toBe("new");
  expect(merged.session.revision).toBe(4);
});
it("does not merge a foreign session into the transcript", () => {
  const current = page(5, 10),
    foreign = page(1, 4);
  foreign.session.id = "other";
  expect(mergeConversationPage(current, foreign, true)).toBe(current);
});

it("keeps both neighboring sides while incrementally extending a historical window", () => {
  let cache = { ...page(40, 60), has_later: true };
  cache = mergeConversationPage(
    cache,
    { ...page(20, 39), has_later: true },
    true,
  ) as typeof cache;
  cache = mergeConversationPage(cache, {
    ...page(61, 80),
    has_later: true,
  }) as typeof cache;
  expect(cache.messages[0].sequence).toBe(20);
  expect(cache.messages.at(-1)?.sequence).toBe(80);
  expect(cache.has_earlier).toBe(true);
  expect(cache.has_later).toBe(true);
});

it("preserves native block order when execution updates share a message cursor", () => {
  const snapshot = page(1, 1);
  snapshot.timeline = [
    {
      id: "text",
      cursor: 2,
      order: 0,
      turn_id: "t1",
      kind: "assistant",
      message_id: "m1",
    },
    {
      id: "tool",
      cursor: 2,
      order: 1,
      turn_id: "t1",
      kind: "tool",
      state: "pending",
    },
    { id: "thinking", cursor: 2, order: 2, turn_id: "t1", kind: "thinking" },
  ];
  const update = {
    ...snapshot,
    timeline: [{ ...snapshot.timeline[1], state: "completed" }],
  };
  const merged = mergeConversationPage(snapshot, update);
  expect(merged.timeline?.map((row) => row.id)).toEqual([
    "text",
    "tool",
    "thinking",
  ]);
  expect(merged.timeline?.[1].state).toBe("completed");
});

it("does not promote stale history when only control metadata advances", () => {
  let cache = mergeConversationPage(null, page(1, 2, 1));
  cache = mergeConversationPage(cache, {
    ...cache,
    session: { ...cache.session, revision: 5 },
  });
  const content = page(1, 2, 4);
  content.timeline![0].content = "recovered output";
  cache = mergeConversationPage(cache, content, true);
  expect(cache.session.revision).toBe(5);
  expect(cache.timeline![0].content).toBe("recovered output");
  cache = mergeConversationPage(cache, page(1, 2, 2), true);
  expect(cache.timeline![0].content).toBe("recovered output");
});
