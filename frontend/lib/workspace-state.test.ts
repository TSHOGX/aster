import { describe, it, expect, vi, afterEach } from "vitest";
import {
  mergePage,
  navigationURL,
  parsePreferences,
  readNavigation,
} from "./workspace-state";
import { remember, recalled, clearPreferenceMemory } from "./project-api";
afterEach(() => {
  vi.unstubAllGlobals();
  clearPreferenceMemory();
});
describe("workspace preferences", () => {
  it("migrates the last legacy agent without opening old panels", () => {
    expect(
      parsePreferences({ agents: ["a", "b"], terminals: ["t"], file: "x.md" }),
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
