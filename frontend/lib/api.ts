import { clientRequestSignal, registerCache } from "./cache-lifecycle";
import { BoundedCache } from "./bounded-cache";
import { markWorkbench } from "./performance";
import type {
  ApiArtifact,
  ApiMessage,
  MessagePage,
  ApiNotification,
  ApiSession,
  ApiSessionRuntime,
  ApiSessionShare,
  ApiShareLink,
  ApiSchedule,
  ApiScheduleRun,
  DeploymentCapabilities,
  SearchResponse,
  SessionMode,
  SessionStatus,
  ShareLinkToken,
  ShareVisibility,
  SharedLinkFile,
  SharedLinkMessage,
  SharedLinkSession
} from "./types";

export type RequestResult<T> =
  | { ok: true; data: T; response: Response }
  | { ok: false; status: number; error: string; response?: Response };

// Parse an HTTP error body and pull out the standard `{error, error_class}`
// shape our backend returns. `rawText` is the response body as text: it is
// try-JSON-parsed (a non-JSON body just yields the fallback). `statusText` is
// the HTTP status text, `fallback` the message when neither a JSON `error` nor
// a status text is present. Shared by request<T>, uploadArtifacts, and
// writeArtifact so the parse→extract logic lives in one place; error_class is
// preserved for the callers whose result type carries it.
export function parseApiError(
  rawText: string,
  statusText: string,
  fallback: string
): { error: string; errorClass?: string } {
  let payload: unknown = rawText;
  if (rawText) {
    try {
      payload = JSON.parse(rawText);
    } catch {
      payload = rawText;
    }
  }
  const record = typeof payload === "object" && payload ? (payload as Record<string, unknown>) : null;
  const error = record && typeof record.error === "string" ? record.error : statusText || fallback;
  const errorClass = record && typeof record.error_class === "string" ? record.error_class : undefined;
  return { error, errorClass };
}

export async function request<T>(input: RequestInfo | URL, init?: RequestInit): Promise<RequestResult<T>> {
  try {
    const signal = clientRequestSignal(init?.signal);
    const response = await fetch(input, { cache: "no-store", ...init, signal });
    markWorkbench("response-headers");
    const contentType = response.headers.get("content-type") ?? "";
    const text = await response.text();
    signal.throwIfAborted();
    markWorkbench("response-body");
    let payload: unknown = text;
    if (contentType.includes("application/json") && text) {
      try {
        payload = JSON.parse(text);
        markWorkbench("response-parse");
      } catch {
        payload = text;
      }
    }
    if (!response.ok) {
      // Preserve the original content-type gate: only a JSON body is inspected
      // for an `error` field, otherwise fall back to the status text.
      const jsonBody = contentType.includes("application/json") ? text : "";
      const { error } = parseApiError(jsonBody, response.statusText, "request failed");
      return { ok: false, status: response.status, error, response };
    }
    return { ok: true, data: payload as T, response };
  } catch (err) {
    return { ok: false, status: 0, error: err instanceof Error ? err.message : "network error" };
  }
}

export async function fetchHealth() {
  return request<{ status: string }>("/api/healthz");
}

export async function fetchMe() {
  return request<{
    auth_required: boolean;
    user_id: string;
    role?: "user" | "admin";
    // Per-user upload permission, deny-by-default server-side. Absent on an old
    // backend, which the caller treats as false -- the upload route enforces the
    // same decision independently, so withholding the UI is never the only gate.
    can_upload?: boolean;
  }>("/api/me");
}

export async function login(username: string, password: string) {
  return request<{ user_id: string; role?: "user" | "admin"; can_upload?: boolean }>("/api/login", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ username, password })
  });
}

export async function logout() {
  return request<{ status: string }>("/api/logout", { method: "POST" });
}

export async function fetchSessions() {
  return request<{ sessions: ApiSession[] }>("/api/sessions");
}

export async function fetchSchedules() { return request<{ schedules: ApiSchedule[] }>("/api/schedules"); }
export async function createSchedule(input: Partial<ApiSchedule> & { name: string; prompt: string }) {
  return request<ApiSchedule>("/api/schedules", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(input) });
}
export async function runSchedule(scheduleId: string) { return request<ApiScheduleRun>(`/api/schedules/${encodeURIComponent(scheduleId)}/run`, { method: "POST" }); }
export async function fetchScheduleRuns(scheduleId: string) { return request<{ runs: ApiScheduleRun[] }>(`/api/schedules/${encodeURIComponent(scheduleId)}/runs`); }
export async function updateSchedule(scheduleId: string, input: Record<string, unknown>) {
  return request<ApiSchedule>(`/api/schedules/${encodeURIComponent(scheduleId)}`, { method: "PATCH", headers: { "content-type": "application/json" }, body: JSON.stringify(input) });
}
export async function deleteSchedule(scheduleId: string) {
  return request<{ status: string }>(`/api/schedules/${encodeURIComponent(scheduleId)}`, { method: "DELETE" });
}

export async function fetchSession(sessionId: string) {
  return request<ApiSession>(`/api/sessions/${encodeURIComponent(sessionId)}`);
}

export async function fetchSessionRuntime(sessionId: string) {
  return request<ApiSessionRuntime>(`/api/sessions/${encodeURIComponent(sessionId)}/runtime`);
}

export async function fetchDeploymentCapabilities() {
  return request<DeploymentCapabilities>("/api/deployment-capabilities");
}

export async function createSession(mode: SessionMode) {
  return request<ApiSession>("/api/sessions", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ mode })
  });
}

export async function destroySession(sessionId: string) {
  return request<{ status: SessionStatus }>(`/api/sessions/${encodeURIComponent(sessionId)}`, { method: "DELETE" });
}

export async function renameSession(sessionId: string, title: string) {
  return request<ApiSession>(`/api/sessions/${encodeURIComponent(sessionId)}`, {
    method: "PATCH",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ title })
  });
}

/**
 * Stop the current turn, and only that turn. The abort reaches the agent inside
 * the sandbox, so the sandbox, the workspace and the agent process all survive.
 *
 * Anything queued behind it survives too, but parked: `pending_queued` counts the
 * rows now waiting on the user, each released with sendQueuedTurn. Nothing runs on
 * its own behind a Stop — a prompt written for the turn that was just stopped is
 * usually stale, and starting it a moment later is what Stop was pressed to avoid.
 */
export async function interruptSession(sessionId: string) {
  return request<{ status: string; pending_queued?: number; runtime?: ApiSessionRuntime }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/interrupt`,
    { method: "POST" }
  );
}

/** Withdraw a turn the user queued but no longer wants. 409 once it is claimed. */
export async function cancelQueuedTurn(sessionId: string, turnId: number) {
  return request<ApiSessionRuntime>(
    `/api/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(String(turnId))}`,
    { method: "DELETE" }
  );
}

/**
 * Send one queued turn now.
 *
 * The server picks the mechanism from state the client cannot see reliably: with a
 * turn executing the text is steered into it (`status: "steered"`, and the prompt
 * becomes a transcript message immediately, because the agent has been told); with
 * nothing executing the parked turn is released to run on its own
 * (`status: "released"`).
 *
 * 409 means the row moved on between render and click — claimed, cancelled, or
 * already sent. Re-read the projection rather than retrying.
 */
export async function sendQueuedTurn(sessionId: string, turnId: number) {
  return request<{ status: string; message?: ApiMessage; runtime?: ApiSessionRuntime }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(String(turnId))}/send`,
    { method: "POST" }
  );
}

export async function fetchMessages(sessionId: string, signal?: AbortSignal) {
  return request<{ messages: ApiMessage[] | null }>(`/api/sessions/${encodeURIComponent(sessionId)}/messages`, { signal });
}

export async function fetchMessagePage(
  sessionId: string,
  options: { beforeId?: number; afterId?: number; aroundId?: number; limit?: number; signal?: AbortSignal } = {}
) {
  const params = new URLSearchParams();
  if (options.aroundId) params.set("around_id", String(options.aroundId));
  if (options.beforeId) params.set("before_id", String(options.beforeId));
  if (options.afterId) params.set("after_id", String(options.afterId));
  if (options.limit) params.set("limit", String(options.limit));
  const query = params.toString();
  return request<MessagePage>(
    `/api/sessions/${encodeURIComponent(sessionId)}/messages/page${query ? `?${query}` : ""}`,
    { signal: options.signal }
  );
}

export async function fetchMessageContent(sessionId: string, messageId: number, signal?: AbortSignal, offset = 0) {
  return request<{ content: string; next_offset: number; total: number; has_more: boolean }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(String(messageId))}/content?offset=${offset}`,
    { signal }
  );
}

export async function fetchMessageTrace(sessionId: string, messageId: number, signal?: AbortSignal, offset = 0) {
  return request<{ trace: ApiMessage["trace"]; next_offset: number; has_more: boolean }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(String(messageId))}/trace?offset=${offset}`,
    { signal }
  );
}

/**
 * Send a turn.
 *
 * Returns the turn id and the post-send projection — no message. The prompt has
 * been accepted but not yet asked: it becomes a transcript entry (arriving as a
 * `message.created` event) only when the agent starts it, so its timestamp
 * reflects when the model actually received it. Until then the queue renders it.
 */
export async function postMessage(sessionId: string, content: string) {
  return request<{
    status: SessionStatus;
    session_id: string;
    turn_id: number;
    runtime: ApiSessionRuntime;
  }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/messages`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ content })
    }
  );
}

export async function postMessageFeedback(
  sessionId: string,
  messageId: number,
  rating: "up" | "down" | null
) {
  return request<{ feedback: string }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(String(messageId))}/feedback`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ rating })
    }
  );
}

export async function fetchArtifacts(sessionId: string) {
  return request<{ artifacts: ApiArtifact[] | null }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/artifacts`
  );
}

export async function fetchUsers() {
  return request<{ users: { user_id: string }[] }>("/api/users");
}

export async function fetchSessionShares(sessionId: string) {
  return request<{ shares: ApiSessionShare[] }>(`/api/sessions/${encodeURIComponent(sessionId)}/shares`);
}

export async function createSessionShare(sessionId: string, grantee: string, visibility: ShareVisibility) {
  return request<ApiSessionShare>(`/api/sessions/${encodeURIComponent(sessionId)}/shares`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ grantee, visibility })
  });
}

export async function revokeSessionShare(sessionId: string, shareId: string) {
  return request<{ status: string }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/shares/${encodeURIComponent(shareId)}`,
    { method: "DELETE" }
  );
}

export async function fetchShareLinks(sessionId: string) {
  return request<{ links: ApiShareLink[] }>(`/api/sessions/${encodeURIComponent(sessionId)}/share-links`);
}

/**
 * Mint a public link. The raw token comes back exactly once, in this response:
 * the server stores only its hash, so it can never be shown again. A lost link
 * is replaced by rotating, which invalidates the old URL.
 */
export async function createShareLink(
  sessionId: string,
  visibility: ShareVisibility,
  ttlSeconds?: number,
  // Workspace-relative paths to freeze into the link. Anything not listed here
  // is not reachable through the link at all.
  files?: string[]
) {
  return request<ShareLinkToken>(`/api/sessions/${encodeURIComponent(sessionId)}/share-links`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ visibility, ttl_seconds: ttlSeconds ?? 0, files: files ?? [] })
  });
}

export async function rotateShareLink(sessionId: string, linkId: string) {
  return request<ShareLinkToken>(
    `/api/sessions/${encodeURIComponent(sessionId)}/share-links/${encodeURIComponent(linkId)}/rotate`,
    { method: "POST" }
  );
}

export async function revokeShareLink(sessionId: string, linkId: string) {
  return request<{ status: string }>(
    `/api/sessions/${encodeURIComponent(sessionId)}/share-links/${encodeURIComponent(linkId)}`,
    { method: "DELETE" }
  );
}

// The anonymous read surface. These hit /api/shared/*, which is served OUTSIDE
// the orchestrator's auth middleware, so they must never send credentials and
// must never be retried through the authed client on failure.

export async function fetchSharedSession(token: string) {
  return request<SharedLinkSession>(`/api/shared/${encodeURIComponent(token)}`);
}

export async function fetchSharedMessages(token: string) {
  return request<{ messages: SharedLinkMessage[] | null }>(
    `/api/shared/${encodeURIComponent(token)}/messages`
  );
}

export async function fetchSharedArtifacts(token: string) {
  return request<{ artifacts: SharedLinkFile[] | null }>(
    `/api/shared/${encodeURIComponent(token)}/artifacts`
  );
}

export async function searchSessions(q: string, limit = 50) {
  return request<SearchResponse>(`/api/search?q=${encodeURIComponent(q)}&limit=${limit}`);
}

export async function fetchNotifications() {
  return request<{ notifications: ApiNotification[] }>("/api/notifications");
}

export async function markNotificationRead(id: string) {
  return request<{ status: string }>(`/api/notifications/${encodeURIComponent(id)}/read`, {
    method: "POST"
  });
}

export function buildArtifactHref(sessionId: string, path: string) {
  const encodedPath = path
    .split("/")
    .filter(Boolean)
    .map(encodeURIComponent)
    .join("/");
  return `/artifacts/${encodeURIComponent(sessionId)}/${encodedPath}`;
}

export type UploadEntry = { relPath: string; file: File };

export type UploadResult =
  | { ok: true; uploaded: string[]; response?: unknown }
  | { ok: false; status: number; error: string; errorClass?: string };

// Single point of change for the multipart wire scheme (confirmed with backend):
// each file is appended as its own multipart file part whose FIELD NAME is the
// workspace-relative path (forward slashes). The part filename is IGNORED by the
// backend — only the field name sets the destination — so we send a fixed
// placeholder. If the scheme ever changes, only this function changes.
function buildUploadFormData(entries: UploadEntry[]): FormData {
  const form = new FormData();
  for (const { relPath, file } of entries) {
    // Normalize the relative path: strip any leading "./" or "/" and collapse
    // backslashes so nested folder uploads carry a clean POSIX-style path.
    const clean = relPath.replace(/\\/g, "/").replace(/^\.?\/+/, "");
    form.append(clean, file, "upload");
  }
  return form;
}

// Upload one or more files (optionally with nested relative paths) to a
// session's workspace. Uses XMLHttpRequest rather than fetch so upload progress
// can be reported via xhr.upload.onprogress.
export function uploadArtifacts(
  sessionId: string,
  entries: UploadEntry[],
  onProgress?: (fraction: number) => void
): Promise<UploadResult> {
  return new Promise<UploadResult>((resolve) => {
    if (entries.length === 0) {
      resolve({ ok: true, uploaded: [] });
      return;
    }
    const form = buildUploadFormData(entries);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/sessions/${encodeURIComponent(sessionId)}/artifacts`);
    xhr.responseType = "text";

    if (onProgress) {
      xhr.upload.onprogress = (event) => {
        if (event.lengthComputable && event.total > 0) {
          onProgress(event.loaded / event.total);
        }
      };
    }

    xhr.onload = () => {
      const raw = typeof xhr.responseText === "string" ? xhr.responseText : "";
      let payload: unknown = raw;
      if (raw) {
        try {
          payload = JSON.parse(raw);
        } catch {
          payload = raw;
        }
      }
      const record = typeof payload === "object" && payload ? (payload as Record<string, unknown>) : null;
      if (xhr.status >= 200 && xhr.status < 300) {
        // 200 body: {"uploaded":[<clean rel paths>...]}.
        const uploaded =
          record && Array.isArray(record.uploaded)
            ? (record.uploaded.filter((p) => typeof p === "string") as string[])
            : entries.map((e) => e.relPath);
        onProgress?.(1);
        resolve({ ok: true, uploaded, response: payload });
        return;
      }
      const { error, errorClass } = parseApiError(raw, xhr.statusText, "upload failed");
      resolve({ ok: false, status: xhr.status, error, errorClass });
    };

    xhr.onerror = () => resolve({ ok: false, status: 0, error: "network error" });
    xhr.onabort = () => resolve({ ok: false, status: 0, error: "upload aborted" });

    xhr.send(form);
  });
}

export type WriteArtifactResult =
  | { ok: true; path?: string }
  | { ok: false; status: number; error: string; errorClass?: string; stale?: boolean; busy?: boolean };

// Overwrite (or create) a single workspace file with raw new contents. When a
// lastModified token (the Last-Modified header from the GET download) is
// provided it is echoed back as If-Unmodified-Since so the backend can reject
// (409 stale_edit) an edit that would clobber a newer agent write. A plain
// content-type is used so the streaming proxy forwards the raw body unchanged
// (never multipart). Omitting the token = unconditional write.
export async function writeArtifact(
  sessionId: string,
  path: string,
  text: string,
  lastModified?: string
): Promise<WriteArtifactResult> {
  const headers: Record<string, string> = { "content-type": "text/plain; charset=utf-8" };
  if (lastModified) {
    // The token is already an HTTP-date emitted by the server; send it verbatim.
    headers["If-Unmodified-Since"] = lastModified;
  }
  // Direct fetch (not the request<T> wrapper) so the JSON error_class survives
  // on the error path; the wrapper collapses the body to just `error`.
  let response: Response;
  try {
    response = await fetch(buildArtifactHref(sessionId, path), {
      method: "PUT",
      headers,
      body: text,
      cache: "no-store"
    });
  } catch (err) {
    return { ok: false, status: 0, error: err instanceof Error ? err.message : "network error" };
  }

  const raw = await response.text();

  if (response.ok) {
    // 200 body: {"path":"<clean rel path>"}.
    let payload: unknown = raw;
    if (raw) {
      try {
        payload = JSON.parse(raw);
      } catch {
        payload = raw;
      }
    }
    const record = typeof payload === "object" && payload ? (payload as Record<string, unknown>) : null;
    const savedPath = record && typeof record.path === "string" ? record.path : undefined;
    return { ok: true, path: savedPath };
  }

  const { error, errorClass } = parseApiError(raw, response.statusText, "save failed");
  return {
    ok: false,
    status: response.status,
    error,
    errorClass,
    stale: errorClass === "stale_edit",
    busy: errorClass === "turn_in_progress"
  };
}

type FetchArtifactTextResult =
  | { ok: true; text: string; lastModified: string | null }
  | { ok: false; status: number; error: string };

// Workspace reads always revalidate. Concurrent requests share one promise;
// the bounded memory cache can paint a versioned preview while revalidation
// runs. Authentication changes clear both maps and abort their requests.
const inFlightArtifactText = new Map<string, Promise<FetchArtifactTextResult>>();
const artifactTextCache = new BoundedCache<FetchArtifactTextResult>(4 * 1024 * 1024);
registerCache(() => { inFlightArtifactText.clear(); artifactTextCache.clear(); });

export async function fetchArtifactText(
  sessionId: string,
  path: string,
  maxBytes = 2 * 1024 * 1024,
  signal?: AbortSignal,
  version = ""
): Promise<FetchArtifactTextResult> {
  // maxBytes participates in the key so callers with different caps don't share
  // a result that was accepted/rejected under a different limit.
  const key = `${sessionId}:${maxBytes}:${path}:${version}`;
  const existing = inFlightArtifactText.get(key);
  if (existing) return existing;

  const requestSignal = clientRequestSignal(signal);
  const promise = (async (): Promise<FetchArtifactTextResult> => {
    const response = await fetch(buildArtifactHref(sessionId, path), { cache: "no-store", signal: requestSignal });
    if (!response.ok) {
      return { ok: false, status: response.status, error: response.statusText || "fetch failed" };
    }
    const sizeHeader = response.headers.get("content-length");
    if (sizeHeader && Number.parseInt(sizeHeader, 10) > maxBytes) {
      return { ok: false, status: 413, error: "artifact too large" };
    }
    // Capture the Last-Modified token so an inline edit can round-trip it as
    // If-Unmodified-Since for optimistic-concurrency (409 stale_edit) checks.
    const lastModified = response.headers.get("last-modified");
    const text = await response.text();
    if (text.length > maxBytes) {
      return { ok: false, status: 413, error: "artifact too large" };
    }
    requestSignal.throwIfAborted();
    const result: FetchArtifactTextResult = { ok: true, text, lastModified };
    artifactTextCache.set(key, result, text.length * 2);
    return result;
  })().catch((error): FetchArtifactTextResult => ({ ok: false, status: 0, error: error instanceof Error ? error.message : "read failed" })).finally(() => {
    inFlightArtifactText.delete(key);
  });

  inFlightArtifactText.set(key, promise);
  return promise;
}

export async function fetchArtifactBlob(sessionId: string, path: string, maxBytes = 25 * 1024 * 1024) {
  const response = await fetch(buildArtifactHref(sessionId, path), { cache: "no-store" });
  if (!response.ok) {
    return { ok: false as const, status: response.status, error: response.statusText || "fetch failed" };
  }
  const sizeHeader = response.headers.get("content-length");
  if (sizeHeader && Number.parseInt(sizeHeader, 10) > maxBytes) {
    return { ok: false as const, status: 413, error: "artifact too large" };
  }
  const buffer = await response.arrayBuffer();
  if (buffer.byteLength > maxBytes) {
    return { ok: false as const, status: 413, error: "artifact too large" };
  }
  return { ok: true as const, buffer };
}

export function cachedArtifactText(sessionId: string, path: string, version: string) { return artifactTextCache.get(`${sessionId}:${2 * 1024 * 1024}:${path}:${version}`); }
