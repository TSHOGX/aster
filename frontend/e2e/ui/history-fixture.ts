import type { Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import {
  conversationRoute,
  invalidateConversation,
} from "./conversation-fixture";

/** 1,000 synthetic turns; ten turns per response and fixed-size process bodies. */
export async function historyFixture(
  page: Page,
  count = 1000,
  paragraphs = 12,
) {
  const workspace = await workspaceFixture(page);
  const prose = Array.from(
    { length: paragraphs },
    (_, i) =>
      `### Observation ${i}\n\nBattery measurements remain reproducible across **three samples**. 中文结果.\n\n| Sample | Capacity |\n| --- | --- |\n| A | 100 |\n| B | 95 |\n\n`,
  ).join("");
  const answer = (n: number) =>
    `Answer ${n}\n\n${prose}\n\n[Report](/workspace/reports/summary.md)\n\n${n === 500 ? "Full answer beyond the visible excerpt.\n".repeat(2000) : ""}Complete answer ${n}.`;
  const turns = Array.from({ length: count }, (_, i) => ({
    id: `turn-${i + 1}`,
    sequence: i + 1,
    state: "completed",
    created_at: "2026-09-23T00:00:00Z",
    finished_at: "2026-09-23T00:00:05Z",
    final_message_id: `answer-${i + 1}`,
    user_preview: `Question ${i + 1}`,
    assistant_preview: `Answer ${i + 1}`,
  }));
  const messages = turns.map((t) => ({
    id: t.final_message_id,
    turn_id: t.id,
    sequence: t.sequence * 5 + 4,
    role: "assistant",
    content: answer(t.sequence),
    created_at: t.created_at,
  }));
  let revision = 1,
    streaming = false;
  const calls: string[] = [];
  await page.route("**/sessions/analysis/turn-index?**", (route) => {
    const before = Number(
      new URL(route.request().url()).searchParams.get("before") || 0,
    );
    const available = turns.filter((t) => !before || t.sequence < before),
      entries = available.slice(-100);
    return route.fulfill({
      json: {
        turns: entries,
        ...(available.length > 100 ? { next_before: entries[0].sequence } : {}),
      },
    });
  });
  await conversationRoute(page, (route) => {
    const url = new URL(route.request().url()),
      q = url.searchParams;
    calls.push(url.pathname + url.search);
    const target =
      q.get("around_turn") || q.get("around_message") || q.get("around_item");
    const at = target ? Number(target.split("-").at(-1)) : 0;
    const before = Number(q.get("before") || 0),
      after = Number(q.get("after") || 0);
    let entries = target
      ? turns.slice(Math.max(0, at - 5), at + 5)
      : before
        ? turns.filter((t) => t.sequence * 5 + 4 < before).slice(-10)
        : after
          ? turns.filter((t) => t.sequence * 5 > after).slice(0, 10)
          : turns.slice(-10);
    if (q.has("ids"))
      entries = turns.filter((t) => q.get("ids")!.split(",").includes(t.id));
    return route.fulfill({
      body: JSON.stringify({
        session: {
          ...workspace.sessions[0],
          revision,
          control_revision: revision,
          state: streaming ? "running" : "idle",
        },
        turns: streaming
          ? [
              {
                id: turns.at(-1)!.id,
                session_id: "analysis",
                sequence: turns.length,
                state: "running",
                content: turns.at(-1)!.user_preview,
                created_at: turns.at(-1)!.created_at,
              },
            ]
          : [],
        messages: url.pathname.endsWith("/content")
          ? messages.filter(
              (message) => message.id === url.pathname.split("/").at(-2),
            )
          : entries.map((t) => messages[t.sequence - 1]),
        turn_summaries: entries.map((t) => ({
          ...t,
          state: t.state,
        })),
        timeline: entries.flatMap((t) => [
          {
            id: `user-${t.sequence}`,
            message_id: `user-${t.sequence}`,
            cursor: t.sequence * 5,
            turn_id: t.id,
            kind: "user",
            content: t.user_preview,
          },
          {
            id: `thinking-${t.sequence}`,
            cursor: t.sequence * 5 + 1,
            turn_id: t.id,
            kind: "thinking",
            content: "Reviewing measurements.\n".repeat(200),
          },
          {
            id: `tool-${t.sequence}`,
            cursor: t.sequence * 5 + 2,
            turn_id: t.id,
            kind: "tool",
            name: "bash",
            state: "succeeded",
            arguments: "Inspect sample results",
            result: "Sample results are consistent.\n".repeat(100),
          },
          {
            id: t.final_message_id,
            message_id: t.final_message_id,
            cursor: t.sequence * 5 + 4,
            turn_id: t.id,
            kind: "assistant",
            content: messages[t.sequence - 1].content.slice(0, 4096),
            truncated: messages[t.sequence - 1].content.length > 4096,
            state: t.state === "running" ? "streaming" : "completed",
          },
        ]),
        has_earlier: entries[0]?.sequence > 1,
        has_later: (entries.at(-1)?.sequence || turns.length) < turns.length,
      }),
    });
  });
  return {
    calls,
    answer,
    async stream() {
      if (!streaming) {
        const n = turns.length + 1;
        turns.push({
          ...turns[0],
          id: `turn-${n}`,
          sequence: n,
          state: "running",
          finished_at: "",
          final_message_id: `answer-${n}`,
          user_preview: `Question ${n}`,
          assistant_preview: "",
        });
        messages.push({
          ...messages[0],
          id: `answer-${n}`,
          turn_id: `turn-${n}`,
          sequence: n * 5 + 4,
          content: answer(n),
        });
      }
      streaming = true;
      revision++;
      messages.at(-1)!.content += `\n\nStream revision ${revision}.`;
      await invalidateConversation(page);
    },
  };
}
