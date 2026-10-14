// Invoked by the product test with an actual Go-generated imported history.
// Uses the pinned native engine with a mock model; no provider calls are made.
import assert from "node:assert/strict";
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { SessionManager, createAgentSession } from "@earendil-works/pi-coding-agent";
import { getModel } from "@earendil-works/pi-ai";

const directory = mkdtempSync(join(tmpdir(), "imported-history-"));
const file = join(directory, "session.jsonl");
try {
  copyFileSync(process.argv[2], file);
  for (let turn = 0; turn < 2; turn++) {
    const before = readFileSync(file, "utf8");
    const manager = SessionManager.open(file, directory, directory);
    assert.equal(manager.getHeader().version, 1);
    assert.equal(readFileSync(file, "utf8"), before, "opening history must not rewrite IDs or content");
    const { session } = await createAgentSession({
      cwd: directory, agentDir: directory,
      model: getModel("anthropic", "claude-sonnet-4-5"),
      modelRegistry: {
        hasConfiguredAuth: () => true,
        getApiKeyAndHeaders: async () => ({ ok: true, apiKey: "fixture-only" }),
      },
      sessionManager: manager, noTools: "all",
    });
    try {
      let received;
      session.agent.streamFn = async (_model, context) => {
        received = context.messages;
        const message = {
          role: "assistant", content: [{ type: "text", text: "42 mAh" }],
          stopReason: "stop", timestamp: Date.now(),
          usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2 },
        };
        return {
          async *[Symbol.asyncIterator]() {
            yield { type: "start", partial: { ...message, content: [] } };
            yield { type: "done", message };
          },
          async result() { return message; },
        };
      };
      await session.prompt("What was the historical capacity?");
      for (const historical of ["Historical capacity is 42 mAh.", "Recorded capacity: 42 mAh."])
        assert.equal(received.filter(message => JSON.stringify(message).includes(historical)).length, 1);
      assert.equal(received.filter(message => JSON.stringify(message).includes("What was the historical capacity?")).length, turn + 1);
      assert(!JSON.stringify(received).includes("fixture-only"));
      assert(!received.some(message => message.role === "toolResult"));
    } finally {
      session.dispose();
    }
  }
} finally {
  rmSync(directory, { recursive: true, force: true });
}
