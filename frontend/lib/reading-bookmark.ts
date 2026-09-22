/** Position metadata is independent of evictable page bodies and contains no text. */
export type PausedReadingBookmark = {
  version: 1;
  follow: false;
  item: string;
  block: number;
  offset: number;
  expanded: boolean;
  innerTop: number;
  innerLeft: number;
};
export type ReadingBookmark =
  | { version: 1; follow: true }
  | PausedReadingBookmark;
export const latestReadingBookmark: ReadingBookmark = Object.freeze({
  version: 1,
  follow: true,
});

export function parseReadingBookmark(value: unknown): ReadingBookmark {
  if (!value || typeof value !== "object") return latestReadingBookmark;
  const saved = value as Record<string, unknown>;
  if (saved.version !== 1 || typeof saved.follow !== "boolean")
    return latestReadingBookmark;
  if (saved.follow) return latestReadingBookmark;
  const id = (value: unknown) =>
    typeof value === "string" && value.length <= 1024;
  if (
    !id(saved.item) ||
    !saved.item ||
    typeof saved.block !== "number" ||
    !Number.isInteger(saved.block) ||
    saved.block < -1 ||
    saved.block > 100_000 ||
    typeof saved.offset !== "number" ||
    !Number.isFinite(saved.offset) ||
    Math.abs(saved.offset) > 1_000_000 ||
    typeof saved.expanded !== "boolean" ||
    ![saved.innerTop, saved.innerLeft].every(
      (n) =>
        typeof n === "number" && Number.isFinite(n) && n >= 0 && n <= 1_000_000,
    )
  )
    return latestReadingBookmark;
  return {
    version: 1,
    follow: false,
    item: saved.item as string,
    block: saved.block,
    offset: saved.offset,
    expanded: saved.expanded,
    innerTop: saved.innerTop as number,
    innerLeft: saved.innerLeft as number,
  };
}
export const readingBlockSelector = ".markdown-body > *, .tool-panel section";
