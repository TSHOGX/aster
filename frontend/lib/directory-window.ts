import type { FileEntry, FilePage } from "./project-api";

export const MAX_DIRECTORY_PAGE_ROWS = 100;
export const MAX_DIRECTORY_PAGES = 5;
export const MAX_DIRECTORY_TREE_BYTES = 8 * 1024 * 1024;
export class DirectoryWindowLimitError extends Error {
  constructor() {
    super(
      "This directory response exceeds its display budget. Retry with a smaller window.",
    );
  }
}
export type DirectoryWindow = FilePage & {
  after?: string;
  error?: string;
  notice?: string;
  loading?: boolean;
  pages?: (FilePage & { after?: string })[];
  used?: number;
};
export type DirectoryTree = Record<string, DirectoryWindow>;

/** Preserve explicit scrolling; before the first scroll, retain a revealed selection. */
export function directoryReadingAnchor(
  page: DirectoryWindow | undefined,
  directory: string,
  selected: string,
  visible?: string,
): string | undefined {
  if (visible) return visible;
  const relative = directory
    ? selected.startsWith(`${directory}/`) ? selected.slice(directory.length + 1) : ""
    : selected;
  const child = relative.split("/")[0];
  return page?.entries.find(entry => entry.name === child)?.name ?? page?.entries[0]?.name;
}

export function openDirectories(paths: readonly string[]) {
  return [...new Set(paths.filter(path => path && path.length <= 4096 && path.split("/").length <= 64))];
}
export function retainDirectoryWindows(
  tree: DirectoryTree,
  open: readonly string[],
): DirectoryTree {
  const retained = new Set(["", ...openDirectories(open)]);
  return Object.fromEntries(
    Object.entries(tree).filter(([path]) => retained.has(path)),
  );
}
export function replaceDirectoryWindow(
  tree: DirectoryTree,
  path: string,
  page: DirectoryWindow,
  open: readonly string[],
): DirectoryTree {
  if (
    page.entries.length > MAX_DIRECTORY_PAGE_ROWS ||
    JSON.stringify(page).length * 3 > 512 * 1024
  )
    throw new DirectoryWindowLimitError();
  const old = tree[path];
  const continuous = !!page.after && (!page.snapshot || !old?.snapshot || page.snapshot === old.snapshot);
  let pages = continuous ? [...(old?.pages ?? (old ? [old] : []))] : [];
  const incoming = { ...page, pages: undefined };
  const existing = pages.findIndex(item => item.after === page.after);
  if (existing >= 0) pages[existing] = incoming;
  else if (continuous && page.after === old?.previous) pages.unshift(incoming);
  else pages.push(incoming);
  const backward = continuous && page.after === old?.previous;
  if (pages.length > MAX_DIRECTORY_PAGES) pages = backward ? pages.slice(0, MAX_DIRECTORY_PAGES) : pages.slice(-MAX_DIRECTORY_PAGES);
  const entries = new Map<string, FileEntry>();
  for (const item of pages) for (const entry of item.entries) entries.set(entry.name, entry);
  const current: DirectoryWindow = {
    ...page, pages, entries: [...entries.values()],
    previous: pages[0]?.previous, start: pages[0]?.start, after: pages[0]?.after,
    next: pages.at(-1)?.next, used: performance.now(),
  };
  const next = retainDirectoryWindows({ ...tree, [path]: current }, open);
  while (JSON.stringify(next).length * 3 > MAX_DIRECTORY_TREE_BYTES) {
    const victim = Object.keys(next).filter(key => key !== path && key !== "")
      .sort((a, b) => (next[a].used ?? 0) - (next[b].used ?? 0))[0];
    if (!victim) throw new DirectoryWindowLimitError();
    delete next[victim];
  }
  return next;
}

export type TreeRow = { id: string; directory: string; depth: number } & (
  | { kind: "entry"; path: string; entry: FileEntry; expanded: boolean }
  | { kind: "edge"; direction: "previous" | "next"; cursor: string; loading: boolean }
  | { kind: "status"; message: "loading" | "empty" | "error" | "notice"; detail?: string }
);

export function flattenDirectoryTree(tree: DirectoryTree, expanded: readonly string[]): TreeRow[] {
  const rows: TreeRow[] = [];
  const open = new Set(expanded);
  const visit = (directory: string, depth: number) => {
    const page = tree[directory];
    if (!page) { rows.push({ id: `${directory}:loading`, directory, depth, kind: "status", message: "loading" }); return; }
    if (page.previous) rows.push({ id: `${directory}:previous`, directory, depth, kind: "edge", direction: "previous", cursor: page.previous, loading: !!page.loading });
    for (const entry of page.entries) {
      const path = directory ? `${directory}/${entry.name}` : entry.name;
      rows.push({ id: path, kind: "entry", directory, depth, path, entry, expanded: entry.kind === "directory" && open.has(path) });
      if (entry.kind === "directory" && open.has(path) && depth < 64) visit(path, depth + 1);
    }
    if (page.loading) rows.push({ id: `${directory}:busy`, directory, depth, kind: "status", message: "loading" });
    if (page.error) rows.push({ id: `${directory}:error`, directory, depth, kind: "status", message: "error", detail: page.error });
    if (page.notice) rows.push({ id: `${directory}:notice`, directory, depth, kind: "status", message: "notice", detail: page.notice });
    if (!page.loading && !page.error && !page.entries.length) rows.push({ id: `${directory}:empty`, directory, depth, kind: "status", message: "empty" });
    if (page.next) rows.push({ id: `${directory}:next`, directory, depth, kind: "edge", direction: "next", cursor: page.next, loading: !!page.loading });
  };
  visit("", 0);
  return rows;
}
