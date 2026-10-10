import { resourceQuotaMessage } from "@web/resource-errors";
import { PREVIEW_LIMITS, PreviewFailure } from "./preview-budget";
import { fetchFileRead, FILE_BUSY_MESSAGE } from "./file-read";
import { requestCategory, startTiming } from "./interaction-metrics";

export type Project = {
  id: string;
  title: string;
  state: string;
  revision: number;
  activity_revision: number;
  failure?: string;
};
export type AgentSession = {
  control_revision: number;
  id: string;
  project_id: string;
  title: string;
  state: string;
  revision: number;
  deletion: number;
  failure?: string;
};
export type ProjectTerminal = {
  incarnation?: string;
  id: string;
  project_id: string;
  title: string;
  state: string;
  revision: number;
  deletion: number;
  rows: number;
  cols: number;
  exit_code?: number;
  failure?: string;
};
export type Turn = {
 queue_policy?: "held" | "released";
 disposition?: "pending" | "claimed" | "consumed" | "handled" | "canceled";
  request_id?: string;
  created_at?: string;
  updated_at?: string;
  id: string;
  session_id: string;
  content: string;
  state: string;
  sequence: number;
  prompt_revision?: number;
  delivery_id?: string;
  delivery_state?: string;
  delivery_error?: string;
  dispatch_error?: string;
};
export type PromptQueuePage = {
  items: Turn[];
  total_pending: number;
  queue_revision: number;
  after?: number;
  next_after?: number;
};
export type Message = {
  id: string;
  run_id?: string;
  segment_id?: string;
  role: string;
  content: string;
  sequence: number;
  created_at: string;
  turn_id?: string;
};
export type TimelineItem = {
 run_id?: string;
  segment_id?: string;
  truncated?: boolean;
  detail_version?: string;
  id: string;
  cursor: number;
  order?: number;
  turn_id: string;
  kind: string;
  content?: string;
  message_id?: string;
  name?: string;
  arguments?: string;
  result?: string;
  state?: string;
};
export type TurnSummary = {
 segment_id?: string;
 run_id?: string;
 elapsed_ms?: number;
  sequence?: number;
  id: string;
  state: string;
  created_at?: string;
  finished_at?: string;
  final_message_id?: string;
};
export type FileEntry = {
  name: string;
  kind: string;
  size: number;
  modified: number;
};
export type FilePage = { entries: FileEntry[]; next?: string; previous?: string; snapshot?: string; start?: number; total?: number };
export type Schedule = {
  id: string;
  project_id: string;
  session_id?: string;
  title: string;
  prompt: string;
  cron: string;
  timezone: string;
  state: "active" | "paused" | "exhausted" | "canceled" | "deleted";
  trigger_kind: "at" | "cron" | "interval" | "manual";
  run_at?: string;
  expires_at?: string;
 max_occurrences?: number;
 admitted_count?: number;
 starts_at?: string;
 completed_by_run_id?: string;
 notification_policy?: "each_run" | "on_finish";
  end_reason?: string;
  notify: boolean;
  origin_session_id?: string;
  origin_run_id?: string;
  last_error?: string;
  next_at?: string;
  revision: number;
};
export type ScheduleRun = {
  id: string;
  session_id: string;
  project_id: string;
  turn_id: string;
  state: string;
  due_at: string;
};
export type Notice = {
 schedule_id?: string;
 message_id?: string;
  id: string;
  project_id?: string;
  session_id?: string;
  title: string;
  body: string;
  created_at: string;
  read: boolean;
};
export type SearchResult = {
  key: string;
  project_id: string;
  session_id: string;
  message_id?: string;
  title: string;
  snippet: string;
};
export type Conversation = {
  project_id: string;
  session_id: string;
  title: string;
  revision: number;
};
export type Share = {
  id: string;
  title: string;
  state: string;
  expires_at: string;
  files: { path: string; size: number; digest: string }[];
};
export type Projection = {
  id: number;
  project_id: string;
  session_id?: string;
  terminal_id?: string;
  revision: number;
  kind: string;
};
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
    public outcome?: { id?: string; state?: string; error?: string; code?: string },
  ) {
    super(message);
  }
}
export const encode = encodeURIComponent;
export const projectURL = (id: string) => `/api/projects/${encode(id)}`;
export const sessionURL = (project: string, session: string) =>
  `${projectURL(project)}/sessions/${encode(session)}`;
export const fileURL = (project: string, path: string) =>
  `${projectURL(project)}/files/content?path=${encode(path)}`;
export const fileDownloadURL = (project: string, path: string) =>
  `${projectURL(project)}/files/download?path=${encode(path)}`;
export async function api<T>(
  path: string,
  init: RequestInit = {},
  allowPending = false,
): Promise<T> {
  const mutation = !!init.method && !["GET", "HEAD"].includes(init.method);
  const unknown = {
    state: "unknown",
    id: new Headers(init.headers).get("Idempotency-Key") || undefined,
  };
  let response: Response;
  const category = requestCategory(path, mutation);
  const networkDone = startTiming(category, "network");
  try {
    response = await (!mutation && /^\/api\/projects\/[^/]+\/files(?:\?|$|\/content\?)/.test(path) ? fetchFileRead : fetch)(path, {
      ...init,
      credentials: "same-origin",
      cache: "no-store",
      headers: {
        ...(typeof init.body === "string"
          ? { "Content-Type": "application/json" }
          : {}),
        ...init.headers,
      },
    });
  } catch (error) {
    networkDone(0);
    if (mutation)
      throw new APIError(
        0,
        "The operation's outcome is unknown. Inspect its current state before trying again.",
        unknown,
      );
    throw error;
  }
  networkDone(response.status);
  if (response.status === 403 && typeof window !== "undefined" && path !== "/api/me/capabilities")
    window.dispatchEvent(new Event("project-permission-denied"));
  if (
    response.status === 401 &&
    typeof window !== "undefined" &&
    !path.startsWith("/api/auth/")
  )
    window.dispatchEvent(new Event("project-auth-expired"));
  if (response.status === 204) return undefined as T;
  let data;
  const parseDone = startTiming(category, "parse");
  try {
    data = await response.json();
  } catch {
    throw new APIError(
      response.status,
      "The server returned an incomplete response.",
      mutation ? unknown : undefined,
    );
  } finally {
    parseDone(response.status);
  }
  if (!response.ok)
    throw new APIError(
      response.status,
      data.state === "unknown"
        ? "The operation's outcome is unknown. Inspect its current state before trying again."
        : data.code === "quota_exceeded" ? resourceQuotaMessage(data.quota)
        : [429, 503].includes(response.status) && !mutation && path.includes("/files")
          ? FILE_BUSY_MESSAGE
          : data.error || `Request failed (${response.status})`,
      data,
    );
  if (response.status === 202 && !allowPending)
    throw new APIError(202, "The Project is getting ready. Try again shortly.");
  return data as T;
}
export const command = <T>(path: string, body?: unknown, method = "POST") =>
  api<T>(
    path,
    { method, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) },
    true,
  );
export const newID = () => crypto.randomUUID();
export const upload = (project: string, path: string, bytes: Blob) =>
  writeFileBytes(fileURL(project, path), bytes);
export const saveEditorFile = (project: string, path: string, bytes: Blob) =>
  writeFileBytes(`${projectURL(project)}/files/editor-content?path=${encode(path)}`, bytes);
async function writeFileBytes(url: string, bytes: Blob) {
  if (bytes.size > 256 * 1024 * 1024)
    throw new Error("Uploads are limited to 256 MiB.");
  const digest = Array.from(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", await bytes.arrayBuffer()),
    ),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
  // Exactly one mutation attempt. Reconnecting never resends these bytes.
  return api<{ id: string; state: string }>(url, {
    method: "PUT",
    body: bytes,
    headers: { "X-Content-SHA256": digest, "Idempotency-Key": newID() },
  });
}
export async function readPreview(
  url: string,
  signal: AbortSignal,
  max: number = PREVIEW_LIMITS.blobBytes,
  onProgress?: (received: number, total?: number) => void,
): Promise<Blob> {
  const response = await fetchFileRead(url, {
    credentials: "same-origin",
    signal,
    cache: "no-store",
  });
  if (!response.ok || response.status === 202) {
    if (response.status === 401 && typeof window !== "undefined")
      window.dispatchEvent(new Event("project-auth-expired"));
    const data = await response.json().catch(() => ({}));
    if (response.status === 413 && data.code === "preview_too_large")
      throw new PreviewFailure("source_too_large", "download", { limit: max, metric: "bytes" });
    throw new APIError(
      response.status,
      data.code === "quota_exceeded" ? resourceQuotaMessage(data.quota) : [202, 429, 503].includes(response.status) ? FILE_BUSY_MESSAGE : data.error || "The file is not available yet.",
      data,
    );
  }
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Missing file content.");
  const declared = Number(response.headers.get("content-length"));
  const total = !response.headers.get("content-encoding") && Number.isSafeInteger(declared) && declared > 0
    ? declared : undefined;
  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let size = 0;
  try {
    if (total && total > max) throw new PreviewFailure("source_too_large", "download", { actual: total, limit: max, metric: "bytes" });
    onProgress?.(0, total);
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      // Once a stream is cut short, its complete size is unknown.
      if (size > max) throw new PreviewFailure("source_too_large", "download", { limit: max, metric: "bytes" });
      chunks.push(value);
      onProgress?.(size, total);
    }
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
  return new Blob(chunks);
}
const memoryPreferences = new Map<string, unknown>();
export function clearPreferenceMemory() {
  memoryPreferences.clear();
}
export function remember(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
    memoryPreferences.delete(key);
  } catch {
    memoryPreferences.set(key, value);
    if (typeof window !== "undefined")
      window.dispatchEvent(new Event("project-storage-unavailable"));
  }
}
export function recalled<T>(key: string, fallback: T): T {
  if (memoryPreferences.has(key)) return memoryPreferences.get(key) as T;
  try {
    return (
      JSON.parse(localStorage.getItem(key) || "null") ??
      (memoryPreferences.get(key) as T | undefined) ??
      fallback
    );
  } catch {
    return (memoryPreferences.get(key) as T | undefined) ?? fallback;
  }
}

export type ScheduleOverview = Pick<Schedule, "id" | "project_id" | "session_id" | "title" | "cron" | "timezone" | "state" | "trigger_kind" | "run_at" | "end_reason" | "origin_session_id" | "last_error" | "next_at" | "revision"> & { project_title: string; last_state?: string; last_session_id?: string; last_turn_id?: string };
export type SchedulePage = { schedules: ScheduleOverview[]; next?: string; previous?: string };

export type TurnIndexItem = {
  id: string;
  sequence: number;
  state: string;
  created_at: string;
  user_preview: string;
  assistant_preview: string;
};
export type TurnIndexPage = {
  turns: TurnIndexItem[];
  next_before?: number;
  next_after?: number;
};

export type SchedulePreview = { valid: boolean; cron: string; timezone: string; reference_time: string; occurrences: string[]; errors?: Record<string, string> };
export type ScheduleRunPage = { runs: ScheduleRun[]; next?: string; previous?: string };
