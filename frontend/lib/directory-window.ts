import type { FilePage } from "./project-api";

export const MAX_OPEN_DIRECTORIES = 16;
export const MAX_DIRECTORY_PAGE_ROWS = 100;
export const MAX_DIRECTORY_TREE_BYTES = 8 * 1024 * 1024;
export class DirectoryWindowLimitError extends Error {
  constructor() {
    super(
      "Directory view exceeds its display budget. Close another directory and retry.",
    );
  }
}
export type DirectoryWindow = FilePage & {
  after?: string;
  error?: string;
  notice?: string;
  loading?: boolean;
};
export type DirectoryTree = Record<string, DirectoryWindow>;

export function openDirectories(paths: readonly string[]) {
  return [...new Set(paths.filter(Boolean))].slice(0, MAX_OPEN_DIRECTORIES);
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
  const next = retainDirectoryWindows({ ...tree, [path]: page }, open);
  if (
    page.entries.length > MAX_DIRECTORY_PAGE_ROWS ||
    JSON.stringify(page).length * 3 > 512 * 1024 ||
    JSON.stringify(next).length * 3 > MAX_DIRECTORY_TREE_BYTES
  )
    throw new DirectoryWindowLimitError();
  return next;
}
