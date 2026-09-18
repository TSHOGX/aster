import { describe, expect, it } from "vitest";
import { boundedMessage, mergeMessageWindow } from "./message-window";
import { BoundedCache } from "./bounded-cache";
import { clientRequestSignal, clearClientResources } from "./cache-lifecycle";
import type { ApiMessage } from "./types";
const message = (id: number, content = String(id)): ApiMessage => ({
  id,
  session_id: "s",
  role: "assistant",
  content,
  created_at: "2026-09-18T00:00:00Z",
});
describe("bounded transcript reconciliation", () => {
  it("keeps SSE and feedback received after a request started", () => {
    const old = message(1);
    const rated = { ...old, feedback: "up" as const };
    const added = message(2);
    const rows = mergeMessageWindow([rated, added], [old], [old]);
    expect(rows).toEqual([rated, added]);
    expect(rows[0]).toBe(rated);
    expect(rows[1]).toBe(added);
  });
  it("retains identities for equal responses, sorts and deduplicates", () => {
    const old = message(1);
    const rows = mergeMessageWindow(
      [old],
      [{ ...old }, message(3), message(2)],
      [old],
    );
    expect(rows.map((r) => r.id)).toEqual([1, 2, 3]);
    expect(rows[0]).toBe(old);
  });
  it("bounds windows around a historical anchor without dropping it", () => {
    const rows = mergeMessageWindow(
      [],
      Array.from({ length: 500 }, (_, i) => message(i + 1)),
      [],
      250,
    );
    expect(rows).toHaveLength(200);
    expect(rows.some((m) => m.id === 250)).toBe(true);
  });
  it("bounds Unicode preview and removes eager trace", () => {
    const m = boundedMessage({
      ...message(1, "🔋".repeat(10000)),
      trace: [{ kind: "thinking" }],
    });
    expect([...m.content]).toHaveLength(2048);
    expect(m.content_length).toBe(10000);
    expect(m.has_trace).toBe(true);
    expect(m.trace).toBeUndefined();
  });
  it("evicts by bytes and recency and rejects oversize cache entries", () => {
    const c = new BoundedCache<string>(10);
    c.set("a", "a", 4);
    c.set("b", "b", 4);
    c.get("a");
    c.set("c", "c", 4);
    expect(c.get("b")).toBeUndefined();
    expect(c.get("a")).toBe("a");
    c.set("large", "x", 100);
    expect(c.sizeBytes).toBeLessThanOrEqual(10);
  });
  it("aborts all in-flight reads at an auth boundary", () => {
    const previous = clientRequestSignal();
    clearClientResources();
    expect(previous.aborted).toBe(true);
    expect(clientRequestSignal().aborted).toBe(false);
  });
});
