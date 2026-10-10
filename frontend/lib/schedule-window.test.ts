import { expect, it } from "vitest";
import {
  ScheduleWindow,
  windowRows,
  changedRuns,
  type WindowPage,
} from "./schedule-window";

type Item = { id: string; revision: number };
const deferred = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
};
it("coalesces an invalidation during a read, retaining content until its successor commits", async () => {
  const pending = deferred<WindowPage<Item>>();
  let reads = 0;
  const window = new ScheduleWindow<Item>(async () =>
    ++reads === 1 ? pending.promise : { rows: [{ id: "a", revision: 2 }] },
  );
  const first = window.refresh();
  void window.refresh();
  void window.refresh();
  expect(reads).toBe(1);
  pending.resolve({ rows: [{ id: "a", revision: 1 }] });
  await first;
  expect(reads).toBe(2);
  expect(windowRows(window.getSnapshot().pages)).toEqual([
    { id: "a", revision: 2 },
  ]);
});
it("bounds the resident window and can page back after eviction without replaying from the head", async () => {
  const requests: string[] = [];
  const window = new ScheduleWindow<Item>(async (params) => {
    requests.push(params.toString());
    const n = Number(
      params.get("after") || params.get("before") || params.get("anchor") || 0,
    );
    return {
      rows: [{ id: String(n), revision: 1 }],
      next: String(n + 1),
      previous: n ? String(n - 1) : undefined,
    };
  });
  await window.refresh();
  for (let n = 0; n < 12; n++) await window.extend("next");
  expect(window.getSnapshot().pages).toHaveLength(5);
  expect(windowRows(window.getSnapshot().pages)[0].id).toBe("8");
  await window.extend("previous");
  expect(requests.at(-1)).toBe("before=7");
  await window.refresh();
  expect(requests.slice(-5)[0]).toBe("anchor=7");
  expect(window.getSnapshot().pages).toHaveLength(5);
});
it("cancels obsolete selections and ignores loaders that disregard AbortSignal", async () => {
  const pending = deferred<WindowPage<Item>>();
  const window = new ScheduleWindow<Item>(() => pending.promise);
  const first = window.refresh();
  window.dispose();
  pending.resolve({ rows: [{ id: "stale", revision: 1 }] });
  await first;
  expect(window.getSnapshot().pages).toEqual([]);
});
it("retains rows on transient failure and clears them on denied access", async () => {
  let failure = 0;
  const window = new ScheduleWindow<Item>(async () => {
    if (failure) throw { status: failure };
    return { rows: [{ id: "a", revision: 1 }] };
  });
  await window.refresh();
  failure = 503;
  await window.refresh();
  expect(windowRows(window.getSnapshot().pages)).toHaveLength(1);
  failure = 404;
  await window.refresh();
  expect(window.getSnapshot().pages).toEqual([]);
});
it("reconciles a stale continuation once and does not loop under continuous writes", async () => {
  let reads = 0,
    conflicting = false;
  const window = new ScheduleWindow<Item>(async () => {
    reads++;
    if (conflicting) throw { status: 409 };
    return { rows: [{ id: "a", revision: 1 }], next: "cursor" };
  });
  await window.refresh();
  conflicting = true;
  await window.extend("next");
  expect(reads).toBe(3);
  expect(windowRows(window.getSnapshot().pages)).toHaveLength(1);
});
it("deduplicates rows by latest revision and counts authoritative run changes", () => {
  expect(
    windowRows([
      { rows: [{ id: "a", revision: 3 }] },
      { rows: [{ id: "a", revision: 2 }] },
    ]),
  ).toEqual([{ id: "a", revision: 3 }]);
  expect(
    changedRuns(
      [{ id: "a", state: "queued" }],
      [
        { id: "a", state: "running" },
        { id: "b", state: "queued" },
      ],
    ),
  ).toBe(2);
});

it("ignores duplicate and older hints without treating task revisions as outbox positions", async () => {
  const { ScheduleHintGate } = await import("./schedule-window");
  const gate = new ScheduleHintGate();
  const hint = {
    stream_incarnation: "one",
    resource_kind: "run.updated",
    resource_id: "task",
    payload: { project_id: "p" },
    revision: "12",
  };
  expect(gate.accept(hint)).toBe(true);
  expect(gate.accept(hint)).toBe(false);
  expect(gate.accept({ ...hint, revision: "11" })).toBe(false);
  expect(gate.accept({ ...hint, revision: "13" })).toBe(true);
  expect(gate.accept({ ...hint, resource_id: "other", revision: "1" })).toBe(
    true,
  );
  expect(
    gate.accept({ ...hint, stream_incarnation: "replacement", revision: "1" }),
  ).toBe(true);
});
