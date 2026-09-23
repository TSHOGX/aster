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
  request_id?: string;
  created_at?: string;
  updated_at?: string;
  id: string;
  session_id: string;
  content: string;
  state: string;
  sequence: number;
};
export type Message = {
  id: string;
  role: string;
  content: string;
  sequence: number;
  created_at: string;
  turn_id?: string;
};
export type TimelineItem = {
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
export type FilePage = { entries: FileEntry[]; next?: string };
export type Schedule = {
  id: string;
  project_id: string;
  session_id?: string;
  title: string;
  prompt: string;
  cron: string;
  timezone: string;
  enabled: boolean;
  deleted: boolean;
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
export type Identity = { username: string; role: string; development: boolean };
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
    public outcome?: { id?: string; state?: string; error?: string },
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
export async function upload(project: string, path: string, bytes: Blob) {
  if (bytes.size > 256 * 1024 * 1024)
    throw new Error("Uploads are limited to 256 MiB.");
  const digest = Array.from(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", await bytes.arrayBuffer()),
    ),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
  // Exactly one mutation attempt. Reconnecting never resends these bytes.
  return api<{ id: string; state: string }>(fileURL(project, path), {
    method: "PUT",
    body: bytes,
    headers: { "X-Content-SHA256": digest, "Idempotency-Key": newID() },
  });
}
export async function readPreview(
  url: string,
  signal: AbortSignal,
  max = 16 << 20,
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
    throw new APIError(
      response.status,
      [202, 429, 503].includes(response.status) ? FILE_BUSY_MESSAGE : data.error || "The file is not available yet.",
    );
  }
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Missing file content.");
  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > max)
        throw new Error(
          "This file is too large to preview. Download it instead.",
        );
      chunks.push(value);
    }
  } catch (error) {
    await reader.cancel();
    throw error;
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

export type ScheduleOverview = Pick<Schedule, "id" | "project_id" | "session_id" | "title" | "cron" | "timezone" | "enabled" | "next_at" | "revision"> & { project_title: string };
export type SchedulePage = { schedules: ScheduleOverview[]; next?: string };

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
export type ScheduleRunPage = { runs: ScheduleRun[]; next?: string };
