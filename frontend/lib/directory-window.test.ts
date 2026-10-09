import { expect, it } from "vitest";
import {
  openDirectories,
  replaceDirectoryWindow,
  retainDirectoryWindows,
  MAX_DIRECTORY_TREE_BYTES,
  type DirectoryTree,
} from "./directory-window";

it("keeps expansion independent of bounded resident page windows", () => {
  const open = openDirectories(
    Array.from({ length: 100 }, (_, n) => `dir-${n}`),
  );
  expect(open).toHaveLength(100);
  let tree: DirectoryTree = { "": { entries: [] } };
  for (const path of open)
    for (let page = 0; page < 5; page++) {
      tree = replaceDirectoryWindow(
        tree,
        path,
        {
          entries: Array.from({ length: 100 }, (_, n) => ({
            name: `file-${page}-${n}`,
            kind: "file",
            size: 0,
            modified: 0,
          })),
          after: String(page), snapshot: path,
        },
        open,
      );
    }
  expect(Object.keys(tree).length).toBeLessThan(101);
  expect(open).toHaveLength(100);
  expect(JSON.stringify(tree).length * 3).toBeLessThanOrEqual(MAX_DIRECTORY_TREE_BYTES);
  expect(Object.values(tree).reduce((count, p) => count + p.entries.length, 0)).toBeGreaterThan(0);
  expect(Object.keys(retainDirectoryWindows(tree, []))).toEqual([""]);
});
it("retains one contiguous interval while moving forward and backward", () => {
  const page = (start: number) => ({
    entries: Array.from({ length: 100 }, (_, i) => ({ name: String(start + i), kind: "file", size: 0, modified: 0 })),
    snapshot: "one", start, total: 1000, after: `page-${start}`,
    next: `page-${start + 100}`, previous: start ? `page-${start - 100}` : undefined,
  });
  let tree: DirectoryTree = {};
  for (let start = 0; start < 700; start += 100) tree = replaceDirectoryWindow(tree, "", page(start), []);
  expect(tree[""].entries.map(entry => entry.name)).toEqual(Array.from({ length: 500 }, (_, i) => String(i + 200)));
  tree = replaceDirectoryWindow(tree, "", page(100), []);
  expect(tree[""].entries.map(entry => entry.name)).toEqual(Array.from({ length: 500 }, (_, i) => String(i + 100)));
  expect(new Set(tree[""].entries.map(entry => entry.name)).size).toBe(500);
});
it("refuses an oversized page without changing the current window", () => {
  const original: DirectoryTree = {
    "": { entries: [{ name: "selected", kind: "file", size: 0, modified: 0 }] },
  };
  expect(() =>
    replaceDirectoryWindow(
      original,
      "",
      {
        entries: Array.from({ length: 101 }, (_, n) => ({
          name: String(n),
          kind: "file",
          size: 0,
          modified: 0,
        })),
      },
      [],
    ),
  ).toThrow("budget");
  expect(() =>
    replaceDirectoryWindow(
      original,
      "",
      {
        entries: [
          { name: "x".repeat(200000), kind: "file", size: 0, modified: 0 },
        ],
      },
      [],
    ),
  ).toThrow("budget");
  expect(original[""].entries[0].name).toBe("selected");
});
