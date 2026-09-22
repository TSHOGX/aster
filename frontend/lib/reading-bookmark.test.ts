import { expect, it } from "vitest";
import {
  latestReadingBookmark,
  parseReadingBookmark,
} from "./reading-bookmark";

it("retains only bounded position metadata and rejects stale or malformed storage", () => {
  const value = {
    version: 1,
    follow: false,
    item: "item/42",
    block: 5,
    offset: -12.5,
    expanded: false,
    innerTop: 0,
    innerLeft: 0,
  };
  expect(
    parseReadingBookmark({ ...value, content: "Do not retain message text" }),
  ).toEqual(value);
  for (const invalid of [
    null,
    [],
    { scroll: 1000, follow: false },
    { ...value, version: 2 },
    { ...value, item: "" },
    { ...value, item: "x".repeat(1025) },
    { ...value, block: 1.5 },
    { ...value, offset: Infinity },
  ]) {
    expect(parseReadingBookmark(invalid)).toEqual(latestReadingBookmark);
  }
  expect(parseReadingBookmark({ ...value, follow: true })).toEqual(
    latestReadingBookmark,
  );
});
