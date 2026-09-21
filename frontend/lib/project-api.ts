export type Project = {
  id: string;
  title: string;
  state: string;
  revision: number;
  activity_revision: number;
  failure?: string;
};
export type AgentSession = {
  id: string;
  project_id: string;
  title: string;
  state: string;
  revision: number;
  deletion: number;
  failure?: string;
};
export type ProjectTerminal = {
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
export type SessionSnapshot = {
  session: AgentSession;
  messages: Message[];
  turns: Turn[];
  record_cursor: number;
  has_earlier: boolean;
};
export type AgentRecord = {
  cursor: number;
  turn_id: string;
  kind: string;
  payload: Record<string, unknown>;
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
  try {
    response = await fetch(path, {
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
    if (mutation)
      throw new APIError(
        0,
        "The operation's outcome is unknown. Inspect its current state before trying again.",
        unknown,
      );
    throw error;
  }
  if (response.status === 204) return undefined as T;
  let data;
  try {
    data = await response.json();
  } catch {
    throw new APIError(
      response.status,
      "The server returned an incomplete response.",
      mutation ? unknown : undefined,
    );
  }
  if (
    response.status === 401 &&
    typeof window !== "undefined" &&
    !path.startsWith("/api/auth/")
  )
    window.dispatchEvent(new Event("project-auth-expired"));
  if (!response.ok)
    throw new APIError(
      response.status,
      data.state === "unknown"
        ? "The operation's outcome is unknown. Inspect its current state before trying again."
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
  const response = await fetch(url, {
    credentials: "same-origin",
    signal,
    cache: "no-store",
  });
  if (!response.ok || response.status === 202) {
    const data = await response.json().catch(() => ({}));
    throw new APIError(
      response.status,
      data.error || "The file is not available yet.",
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
export function remember(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
    const keys = Object.keys(localStorage).filter((k) =>
      k.startsWith("project-view:"),
    );
    while (keys.length > 16) {
      const oldest = keys.shift();
      if (oldest && oldest !== key) localStorage.removeItem(oldest);
    }
  } catch {
    /* A full/disabled browser store must not block runtime actions. */
  }
}
export function recalled<T>(key: string, fallback: T): T {
  try {
    return JSON.parse(localStorage.getItem(key) || "null") ?? fallback;
  } catch {
    return fallback;
  }
}

export function viewPreferences(key: string): {
  agents: string[];
  terminals: string[];
  file: string;
} {
  const value = recalled<unknown>(key, null);
  const data =
    value && typeof value === "object"
      ? (value as Record<string, unknown>)
      : {};
  const ids = (input: unknown) =>
    Array.isArray(input)
      ? input
          .filter(
            (id): id is string =>
              typeof id === "string" && /^[a-zA-Z0-9_-]{1,128}$/.test(id),
          )
          .slice(-4)
      : [];
  return {
    agents: ids(data.agents),
    terminals: ids(data.terminals),
    file: typeof data.file === "string" ? data.file.slice(0, 4096) : "",
  };
}
