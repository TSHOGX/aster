import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, expect, test, vi } from "vitest";
import { conversationTurns } from "@/lib/conversation-turns";
import { TurnView } from "./turn-view";

vi.mock("@/components/workbench/markdown-view", () => ({
  MarkdownView: ({ content }: { content: string }) => <span>{content}</span>,
}));

beforeEach(() => {
  vi.stubGlobal("localStorage", { getItem: () => null, setItem: () => {}, removeItem: () => {} });
});

test("continued segments are successful boundaries, not unknown outcomes", () => {
  const turn = conversationTurns(
    [{ id: "user", turn_id: "prompt", segment_id: "segment", kind: "user", content: "input", cursor: 1 },
      { id: "answer", turn_id: "prompt", segment_id: "segment", kind: "assistant", content: "answer", message_id: "answer", cursor: 2 }],
    [{ id: "prompt", segment_id: "segment", run_id: "run", state: "continued", created_at: "2026-09-24T00:00:00Z", finished_at: "2026-09-24T00:00:02Z", elapsed_ms: 2000, final_message_id: "answer" }],
  )[0];
  const html = renderToStaticMarkup(<TurnView turn={turn} files={[]} activeRuns={new Set(["run"])} loadMessage={async () => "answer"} loadDetail={async () => ""} actor="a" project="p" session="s" targetMessage="" />);
  expect(html).not.toContain("Outcome unknown");
  expect(html).toContain("Worked for 2s");
});
