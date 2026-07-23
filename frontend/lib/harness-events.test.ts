import { describe, expect, it } from "vitest";

import {
  emptyConvo,
  reduceAgentDelta,
  reduceCompactionEnd,
  reduceProxySettled,
  reduceProxyStarted,
  reduceThinkingWhenIdle,
  reduceThinkingWhenIdleOrRetrying,
  reduceToolActivity,
  reduceToolEnd,
  withActivity
} from "./harness-events";

describe("reduceAgentDelta", () => {
  it("appends to the streaming buffer and enters the streaming phase", () => {
    const next = reduceAgentDelta(emptyConvo(), "m1", "hello", "text_delta");
    expect(next.streaming).toEqual([{ id: "m1", text: "hello", deltaType: "text_delta" }]);
    expect(next.activity.phase).toBe("streaming");
    expect(next.activity.startedAt).not.toBeNull();
  });
  it("concatenates onto an existing buffer for the same id", () => {
    const once = reduceAgentDelta(emptyConvo(), "m1", "ab", "text_delta");
    const twice = reduceAgentDelta(once, "m1", "cd", "text_delta");
    expect(twice.streaming).toEqual([{ id: "m1", text: "abcd", deltaType: "text_delta" }]);
  });
});

describe("tool FSM", () => {
  it("pushes an open node on start and enters the tool phase", () => {
    const next = reduceToolActivity(emptyConvo(), { toolName: "bash", args: { command: "ls" } }, true);
    expect(next.activity.phase).toBe("tool");
    expect(next.activity.toolName).toBe("bash");
    expect(next.liveTrace).toHaveLength(1);
    expect(next.liveTrace[0]).toMatchObject({ kind: "tool", toolName: "bash", label: "ls" });
    expect(next.liveTrace[0].durationMs).toBeUndefined();
  });
  it("does not push a node on update but keeps refining the strip", () => {
    const started = reduceToolActivity(emptyConvo(), { toolName: "bash" }, true);
    const updated = reduceToolActivity(started, { toolName: "bash", args: { command: "pwd" } }, false);
    expect(updated.liveTrace).toHaveLength(1);
    expect(updated.activity.toolDetail).toBe("pwd");
  });
  it("finalizes the matching node by toolCallId and returns to thinking", () => {
    const started = reduceToolActivity(emptyConvo(), { toolName: "read", toolCallId: "t1" }, true);
    const ended = reduceToolEnd(started, { toolCallId: "t1", result: "file contents" });
    expect(ended.liveTrace[0].durationMs).toBeGreaterThanOrEqual(0);
    expect(ended.liveTrace[0].resultSummary).toBe("file contents");
    expect(ended.activity.phase).toBe("thinking");
    expect(ended.activity.toolName).toBeNull();
  });
  it("finalizes the last open node when no toolCallId is provided", () => {
    const started = reduceToolActivity(emptyConvo(), { toolName: "write", args: { path: "a.txt", content: "x" } }, true);
    const ended = reduceToolEnd(started, { isError: true });
    expect(ended.liveTrace[0].isError).toBe(true);
    expect(ended.liveTrace[0].diff).toEqual({ path: "a.txt", content: "x" });
  });
});

describe("thinking guards", () => {
  it("only lifts idle to thinking", () => {
    expect(reduceThinkingWhenIdle(emptyConvo()).activity.phase).toBe("thinking");
    const streaming = withActivity(emptyConvo(), { phase: "streaming" });
    expect(reduceThinkingWhenIdle(streaming)).toBe(streaming);
  });
  it("lifts idle or retrying to thinking, leaving others untouched", () => {
    const retrying = withActivity(emptyConvo(), { phase: "retrying" });
    expect(reduceThinkingWhenIdleOrRetrying(retrying).activity.phase).toBe("thinking");
    const tool = withActivity(emptyConvo(), { phase: "tool" });
    expect(reduceThinkingWhenIdleOrRetrying(tool)).toBe(tool);
  });
  it("only ends compaction from the compacting phase", () => {
    const compacting = withActivity(emptyConvo(), { phase: "compacting" });
    expect(reduceCompactionEnd(compacting).activity.phase).toBe("thinking");
    const idle = emptyConvo();
    expect(reduceCompactionEnd(idle)).toBe(idle);
  });
});

describe("proxy in-flight counting", () => {
  it("increments requestCount and inflight on start", () => {
    const next = reduceProxyStarted(emptyConvo());
    expect(next.activity.requestCount).toBe(1);
    expect(next.activity.inflight).toBe(1);
    expect(next.activity.phase).toBe("thinking");
  });
  it("decrements inflight on settle without going negative", () => {
    const started = reduceProxyStarted(emptyConvo());
    const settled = reduceProxySettled(started);
    expect(settled.activity.inflight).toBe(0);
    expect(settled.activity.requestCount).toBe(1);
    expect(reduceProxySettled(settled).activity.inflight).toBe(0);
  });
});
