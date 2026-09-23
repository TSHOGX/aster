import { describe, it, expect, vi, afterEach } from "vitest";
import {
  mergePage,
  navigationURL,
  parsePreferences,
  readNavigation,
  globalNavigation,
} from "./workspace-state";
import { remember, recalled, clearPreferenceMemory } from "./project-api";
afterEach(() => {
  vi.unstubAllGlobals();
  clearPreferenceMemory();
});

describe("route isolation", () => {
  it("round trips schedule selection and filters without selecting a workspace", () => {
    const url="?view=schedules&schedule=task&schedule_project=p&mode=edit&filter_project=other&q=weekly&status=paused";
    const route=readNavigation(url);
    expect(route.project).toBe("");
    expect(readNavigation(navigationURL(route).slice(1))).toEqual(route);
    expect(readNavigation("?view=schedules&mode=create&schedule=old&schedule_project=p")).toMatchObject({scheduleMode:"create"});
    expect(readNavigation("?view=schedules&mode=create&schedule=old&schedule_project=p").schedule).toBeUndefined();
  });
  it.each(["shared", "schedules", "search", "inbox"] as const)("discards workspace selection from %s URLs and transitions", view => {
    const mixed = readNavigation(`?view=${view}&project=p&session=s&message=m&draft=1&history=1`);
    expect(mixed).toEqual(globalNavigation(view));
    expect(navigationURL(mixed)).toBe(`/?view=${view}`);
    expect(navigationURL({ ...readNavigation("?project=p&session=s&message=m&history=1"), view })).toBe(`/?view=${view}`);
  });
  it("uses an explicit received-conversation identity", () => {
    expect(navigationURL(globalNavigation("shared", "received"))).toBe("/?view=shared&conversation=received");
    expect(readNavigation("?view=shared&conversation=received&project=p&session=s")).toEqual(globalNavigation("shared", "received"));
    expect(readNavigation("?project=p&session=s&conversation=received").conversation).toBeUndefined();
  });
  it("keeps draft, message, and history selection meaningful only in project routes", () => {
    expect(readNavigation("?project=p&draft=1&session=s&message=m&history=1")).toMatchObject({ project: "p", session: "", draft: true, message: "", history: false });
    expect(readNavigation("?view=share&project=p&session=s&message=m&history=1")).toMatchObject({ project: "p", session: "s", message: "", history: false });
    expect(readNavigation("?session=s&message=m&history=1")).toMatchObject({ session: "", message: "", history: false });
  });
});
describe("workspace preferences", () => {
  it("uses only the selected Session and leaves panels initially closed", () => {
    expect(
      parsePreferences({
        activeSessionId: "b",
        terminals: ["t"],
        file: "x.md",
      }),
    ).toMatchObject({
      version: 2,
      activeSessionId: "b",
      filesOpen: false,
      terminalOpen: false,
      file: "x.md",
    });
  });
  it("sanitizes malformed storage and clamps dimensions", () => {
    expect(
      parsePreferences({
        activeSessionId: "../bad",
        treeWidth: 900,
        fileWidth: NaN,
        terminals: [null, "ok", "ok"],
      }),
    ).toMatchObject({
      activeSessionId: "",
      treeWidth: 320,
      fileWidth: 58,
      terminals: ["ok"],
    });
  });
  it("never evicts other session drafts when remembering a view", () => {
    const m = new Map<string, string>();
    vi.stubGlobal("localStorage", {
      setItem: (k: string, v: string) => m.set(k, v),
    });
    for (let i = 0; i < 40; i++)
      remember(`project-view:alice:p:draft:${i}`, "draft");
    expect(m.size).toBe(40);
  });
  it("appends pages without regressing newer revisions", () => {
    expect(
      mergePage(
        [{ id: "a", revision: 3 }],
        [
          { id: "a", revision: 1 },
          { id: "b", revision: 2 },
        ],
      ),
    ).toEqual([
      { id: "a", revision: 3 },
      { id: "b", revision: 2 },
    ]);
  });
  it("round trips message and draft navigation", () => {
    const n = readNavigation(
      "?project=p&session=s&message=m&view=share&history=1",
    );
    expect(readNavigation(navigationURL(n).slice(1))).toEqual(n);
  });
});

it("keeps the latest draft in memory when storage rejects a replacement", () => {
  vi.stubGlobal("localStorage", {
    setItem: () => {
      throw new Error("quota");
    },
    getItem: () => JSON.stringify("older draft"),
  });
  remember("project-view:alice:p:draft:s", "latest draft");
  expect(recalled("project-view:alice:p:draft:s", "")).toBe("latest draft");
  expect(recalled("project-view:bob:p:draft:s", "")).toBe("older draft");
  clearPreferenceMemory();
  expect(recalled("project-view:alice:p:draft:s", "")).toBe("older draft");
});
