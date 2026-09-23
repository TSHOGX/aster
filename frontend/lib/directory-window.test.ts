import { expect, it } from "vitest";
import {
  openDirectories,
  replaceDirectoryWindow,
  retainDirectoryWindows,
  type DirectoryTree,
} from "./directory-window";

it("retains only root and sixteen open branches with one page each", () => {
  const open = openDirectories(
    Array.from({ length: 100 }, (_, n) => `dir-${n}`),
  );
  expect(open).toHaveLength(16);
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
          after: String(page),
        },
        open,
      );
    }
  expect(Object.keys(tree)).toHaveLength(17);
  expect(
    Object.values(tree).reduce((count, p) => count + p.entries.length, 0),
  ).toBe(1600);
  expect(Object.keys(retainDirectoryWindows(tree, []))).toEqual([""]);
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
