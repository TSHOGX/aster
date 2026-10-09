import { api, encode, projectURL } from "./project-api";

export type ReadingCell = {
  column: number;
  text: string;
  span?: number;
  merge?: string;
};
export type ReadingItem = {
  position: number;
  row: number;
  text?: string;
  cells?: ReadingCell[];
  heading?: boolean;
};
export type ReadingSheet = {
  name: string;
  count: number;
  columns: number;
  hidden: boolean;
  merges?: string[];
  frozen_rows?: number;
  frozen_columns?: number;
};
export type ReadingManifest = {
  version: string;
  kind: "docx" | "xlsx";
  coverage: "complete";
  sheets: ReadingSheet[];
  headings: { position: number; text: string }[];
  notices: string[];
};
export type ReadingJob = {
  id: string;
  digest: string;
  state: "indexing" | "ready" | "failed";
  failure?: string;
  manifest?: ReadingManifest;
};
export type ReadingWindow = {
  digest: string;
  start: number;
  total: number;
  coverage: "complete";
  items: ReadingItem[];
};
export type OfficeBookmark = {
  digest?: string;
  sheet: number;
  position: number;
  left: number;
};
export function parseOfficeBookmark(value: unknown): OfficeBookmark {
  const fallback = { sheet: 0, position: 0, left: 0 };
  if (!value || typeof value !== "object") return fallback;
  const item = value as Partial<OfficeBookmark>;
  if (
    !Number.isSafeInteger(item.sheet) ||
    item.sheet! < 0 ||
    item.sheet! > 63 ||
    !Number.isSafeInteger(item.position) ||
    item.position! < 0 ||
    item.position! > 100_000 ||
    !Number.isFinite(item.left) ||
    item.left! < 0 ||
    item.left! > 3_000_000 ||
    typeof item.digest !== "string" ||
    item.digest.length > 128
  )
    return fallback;
  return {
    digest: item.digest,
    sheet: item.sheet!,
    position: item.position!,
    left: item.left!,
  };
}
export const readingURL = (project: string, id?: string) =>
  `${project.startsWith("shared:") ? `/api/shared/${encode(project.slice(7))}` : projectURL(project)}/files/previews${id ? `/${encode(id)}` : ""}`;
export const openReading = (
  project: string,
  path: string,
  kind: "docx" | "xlsx",
  signal: AbortSignal,
) =>
  api<ReadingJob>(
    `${readingURL(project)}?path=${encode(path)}&kind=${kind}&max_bytes=${100 << 20}`,
    { signal },
  );
export const readingStatus = (
  project: string,
  id: string,
  signal: AbortSignal,
) => api<ReadingJob>(readingURL(project, id), { signal });
export const readingWindow = (
  project: string,
  id: string,
  sheet: number,
  start: number,
  signal: AbortSignal,
  fromColumn = -1,
  columns = 0,
) =>
  api<ReadingWindow>(
    `${readingURL(project, id)}/window?sheet=${sheet}&start=${start}&limit=100&from_column=${fromColumn}&columns=${columns}`,
    { signal },
  );
export const readingFind = (
  project: string,
  id: string,
  sheet: number,
  start: number,
  query: string,
  signal: AbortSignal,
) =>
  api<{ position: number }>(
    `${readingURL(project, id)}/find?sheet=${sheet}&start=${start}&q=${encode(query)}`,
    { signal },
  );
export const readingCopy = (
  project: string,
  id: string,
  sheet: number,
  start: number,
  end: number,
  fromColumn: number,
  toColumn: number,
  signal: AbortSignal,
) =>
  api<{ text: string }>(
    `${readingURL(project, id)}/copy?sheet=${sheet}&start=${start}&end=${end}&from_column=${fromColumn}&to_column=${toColumn}`,
    { signal },
  );
export const readingLocate = (
  project: string,
  id: string,
  sheet: number,
  row: number,
  signal: AbortSignal,
) =>
  api<{ position: number }>(
    `${readingURL(project, id)}/locate?sheet=${sheet}&row=${row}`,
    { signal },
  );
export function parseCellAddress(address: string) {
  const match = /^([A-Z]{1,3})([1-9]\d{0,6})$/.exec(
    address.trim().toUpperCase(),
  );
  if (!match) return null;
  let column = 0;
  for (const char of match[1]) column = column * 26 + char.charCodeAt(0) - 64;
  const row = Number(match[2]);
  return column <= 16_384 && row <= 1_048_576
    ? { column: column - 1, row }
    : null;
}
export async function releaseReading(project: string, id: string) {
  await fetch(readingURL(project, id), {
    method: "DELETE",
    credentials: "same-origin",
    keepalive: true,
  }).catch(() => {});
}
export function columnName(column: number) {
  let result = "";
  for (let n = column + 1; n > 0; n = Math.floor((n - 1) / 26))
    result = String.fromCharCode(65 + ((n - 1) % 26)) + result;
  return result;
}
export function pauseReading(signal: AbortSignal) {
  signal.throwIfAborted();
  return new Promise<void>((resolve, reject) => {
    const done = () => {
      signal.removeEventListener("abort", abort);
      resolve();
    };
    const timer = setTimeout(done, 200);
    const abort = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      reject(signal.reason);
    };
    signal.addEventListener("abort", abort, { once: true });
  });
}
