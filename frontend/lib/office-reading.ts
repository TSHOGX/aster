import { api, encode, projectURL } from "./project-api";

export type ReadingCell = { column: number; text: string; span?: number; merge?: string };
export type ReadingItem = { position: number; row: number; text?: string; cells?: ReadingCell[]; heading?: boolean };
export type ReadingSheet = { name: string; count: number; columns: number; hidden: boolean; merges?: string[]; frozen_rows?: number; frozen_columns?: number };
export type ReadingManifest = { version: string; kind: "docx" | "xlsx"; coverage: "complete"; sheets: ReadingSheet[]; headings: { position: number; text: string }[]; notices: string[] };
export type ReadingJob = { id: string; digest: string; state: "indexing" | "ready" | "failed"; failure?: string; manifest?: ReadingManifest };
export type ReadingWindow = { start: number; total: number; coverage: "complete"; items: ReadingItem[] };
export const readingURL = (project: string, id?: string) => `${project.startsWith("shared:") ? `/api/shared/${encode(project.slice(7))}` : projectURL(project)}/files/previews${id ? `/${encode(id)}` : ""}`;
export const openReading = (project: string, path: string, kind: "docx" | "xlsx", signal: AbortSignal) =>
  api<ReadingJob>(`${readingURL(project)}?path=${encode(path)}&kind=${kind}&max_bytes=${100 << 20}`, { signal });
export const readingStatus = (project: string, id: string, signal: AbortSignal) => api<ReadingJob>(readingURL(project, id), { signal });
export const readingWindow = (project: string, id: string, sheet: number, start: number, signal: AbortSignal) =>
  api<ReadingWindow>(`${readingURL(project, id)}/window?sheet=${sheet}&start=${start}&limit=100`, { signal });
export const readingFind = (project: string, id: string, sheet: number, start: number, query: string, signal: AbortSignal) =>
  api<{ position: number }>(`${readingURL(project, id)}/find?sheet=${sheet}&start=${start}&q=${encode(query)}`, { signal });
export const readingCopy = (project:string,id:string,sheet:number,start:number,end:number,fromColumn:number,toColumn:number,signal:AbortSignal) =>
  api<{text:string}>(`${readingURL(project,id)}/copy?sheet=${sheet}&start=${start}&end=${end}&from_column=${fromColumn}&to_column=${toColumn}`,{signal});
export async function releaseReading(project: string, id: string) {
  await fetch(readingURL(project, id), { method: "DELETE", credentials: "same-origin", keepalive: true }).catch(() => {});
}
export function columnName(column: number) {
  let result = "";
  for (let n = column + 1; n > 0; n = Math.floor((n - 1) / 26)) result = String.fromCharCode(65 + (n - 1) % 26) + result;
  return result;
}
export function pauseReading(signal: AbortSignal) {
  signal.throwIfAborted();
  return new Promise<void>((resolve, reject) => {
    const done = () => { signal.removeEventListener("abort", abort); resolve(); };
    const timer = setTimeout(done, 200);
    const abort = () => { clearTimeout(timer); signal.removeEventListener("abort", abort); reject(signal.reason); };
    signal.addEventListener("abort", abort, { once: true });
  });
}
