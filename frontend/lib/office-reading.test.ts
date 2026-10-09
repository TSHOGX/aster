import { expect, it } from "vitest";
import {
  columnName,
  parseCellAddress,
  parseOfficeBookmark,
} from "./office-reading";
it("bounds persisted semantic bookmarks without retaining source text", () => {
  const valid = {
    digest: "a".repeat(64),
    sheet: 1,
    position: 22_961,
    left: 1600,
  };
  expect(parseOfficeBookmark(valid)).toEqual(valid);
  for (const value of [
    null,
    { ...valid, sheet: 64 },
    { ...valid, position: Infinity },
    { ...valid, left: -1 },
    { ...valid, digest: {} },
  ])
    expect(parseOfficeBookmark(value)).toEqual({
      sheet: 0,
      position: 0,
      left: 0,
    });
});
it("names natural worksheet columns through XFD", () => {
  expect([0, 25, 26, 16383].map(columnName)).toEqual(["A", "Z", "AA", "XFD"]);
});
it("bounds cell navigation addresses", () => {
  expect(parseCellAddress("B12")).toEqual({ column: 1, row: 12 });
  expect(parseCellAddress("XFD1048576")).toEqual({
    column: 16383,
    row: 1048576,
  });
  expect(parseCellAddress("A0")).toBeNull();
  expect(parseCellAddress("XFE1")).toBeNull();
});
