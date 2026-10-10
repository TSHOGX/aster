import { afterEach, expect, it, vi } from "vitest";
import { ProjectRealtime, type ResourceEvent } from "./project-realtime";
import { ResourceStore } from "./resource-store";
class Source extends EventTarget {
  closed = false;
  onerror: (() => void) | null = null;
  constructor(readonly url: string) {
    super();
  }
  close() {
    this.closed = true;
  }
  emit(kind: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(kind, { data: JSON.stringify(data) }));
  }
}
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
it("shares a connection, resumes accepted cursors and does not invalidate content on rotation", async () => {
  vi.useFakeTimers();
  const sources: Source[] = [];
  const rt = new ProjectRealtime(new ResourceStore("actor"), (url) => {
    const s = new Source(url);
    sources.push(s);
    return s as unknown as EventSource;
  });
  const a: ResourceEvent[] = [],
    b: ResourceEvent[] = [];
  const offA = rt.subscribe("p", (e) => a.push(e)),
    offB = rt.subscribe("p", (e) => b.push(e));
  await Promise.resolve();
  a.length = 0;
  b.length = 0;
  expect(sources).toHaveLength(1);
  sources[0].emit("resource", {
    protocol_version: 1,
    stream_incarnation: "hub",
    cursor: "accepted",
    resource_kind: "files",
    payload: { path: "a.txt" },
  });
  offA();
  expect(sources[0].closed).toBe(false);
  sources[0].emit("rotate", { cursor: "accepted" });
  await vi.advanceTimersByTimeAsync(100);
  expect(sources).toHaveLength(2);
  expect(sources[1].url).toContain("cursor=accepted");
  sources[1].emit("resource", {
    protocol_version: 1,
    stream_incarnation: "hub",
    cursor: "accepted",
    resource_kind: "ready",
  });
  expect(b.map((e) => e.resource_kind)).toEqual(["files", "ready"]);
  offB();
  expect(sources[1].closed).toBe(true);
  rt.clear();
});
it("does not advance a cursor after a rejected delivery and clears revoked content", async () => {
  vi.useFakeTimers();
  const sources: Source[] = [];
  const store = new ResourceStore("actor");
  const key = { project: "p", kind: "conversation" as const, identity: "s" };
  store.put(key, "private content");
  const rt = new ProjectRealtime(store, (url) => {
    const s = new Source(url);
    sources.push(s);
    return s as unknown as EventSource;
  });
  const off = rt.subscribe("p", (e) => {
    if (e.resource_kind === "content") throw new Error("not accepted");
  });
  await Promise.resolve();
  sources[0].emit("resource", {
    protocol_version: 1,
    stream_incarnation: "hub",
    cursor: "unsafe",
    resource_kind: "content",
  });
  await vi.advanceTimersByTimeAsync(500);
  expect(sources[1].url).not.toContain("unsafe");
  sources[1].emit("closed", { reason: "authorization" });
  expect(store.peek(key)).toBeUndefined();
  expect(sources[1].closed).toBe(true);
  off();
  rt.clear();
});

it("hidden tabs release transport and revalidate visible scopes before becoming live", async () => {
  vi.useFakeTimers();
  const doc = new EventTarget() as EventTarget & { hidden: boolean };
  doc.hidden = false;
  vi.stubGlobal("document", doc);
  const sources: Source[] = [],
    events: ResourceEvent[] = [],
    health: string[] = [];
  const rt = new ProjectRealtime(new ResourceStore("actor"), (url) => {
    const s = new Source(url);
    sources.push(s);
    return s as unknown as EventSource;
  });
  const off = rt.subscribe(
    "p",
    (e) => events.push(e),
    (s) => health.push(s),
  );
  await Promise.resolve();
  sources[0].emit("resource", {
    protocol_version: 1,
    stream_incarnation: "hub",
    cursor: "1",
    resource_kind: "ready",
  });
  doc.hidden = true;
  doc.dispatchEvent(new Event("visibilitychange"));
  expect(sources[0].closed).toBe(true);
  await vi.advanceTimersByTimeAsync(120000);
  expect(sources).toHaveLength(1);
  doc.hidden = false;
  doc.dispatchEvent(new Event("visibilitychange"));
  expect(sources).toHaveLength(2);
  expect(events.at(-1)?.reason).toBe("visibility");
  expect(health.at(-1)).toBe("reconnecting");
  sources[1].emit("resource", {
    protocol_version: 1,
    stream_incarnation: "hub",
    cursor: "1",
    resource_kind: "ready",
  });
  expect(health.at(-1)).toBe("live");
  off();
  rt.clear();
});

it("shares one owner Scheduled stream and replays it with the same transport guarantees", async () => {
  vi.useFakeTimers();
  const sources: Source[] = [];
  const rt = new ProjectRealtime(new ResourceStore("actor"), (url) => {
    const s = new Source(url);
    sources.push(s);
    return s as unknown as EventSource;
  });
  const received: ResourceEvent[] = [];
  const off = rt.subscribe("", (e) => received.push(e));
  const off2 = rt.subscribe("", () => {});
  await Promise.resolve();
  received.length = 0;
  expect(sources).toHaveLength(1);
  expect(sources[0].url).toBe("/api/schedules/events?cursor=");
  const hint = {
    protocol_version: 1,
    stream_incarnation: "owner",
    cursor: "opaque",
    resource_kind: "run.updated",
    revision: "12",
  };
  sources[0].emit("resource", hint);
  sources[0].emit("resource", hint);
  expect(received).toHaveLength(1);
  sources[0].emit("rotate", {});
  await vi.advanceTimersByTimeAsync(100);
  expect(sources[1].url).toBe("/api/schedules/events?cursor=opaque");
  off();
  off2();
  rt.clear();
});
