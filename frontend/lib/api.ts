import type {
  ApiArtifact,
  ApiMessage,
  ApiNotification,
  ApiSession,
  DeploymentCapabilities,
  SearchResponse,
  SessionMode,
  SessionStatus
} from "./types";

export type RequestResult<T> =
  | { ok: true; data: T; response: Response }
  | { ok: false; status: number; error: string; response?: Response };

export async function request<T>(input: RequestInfo | URL, init?: RequestInit): Promise<RequestResult<T>> {
  try {
    const response = await fetch(input, { cache: "no-store", ...init });
    const contentType = response.headers.get("content-type") ?? "";
    const text = await response.text();
    let payload: unknown = text;
    if (contentType.includes("application/json") && text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = text;
      }
    }
    if (!response.ok) {
      const error =
        typeof payload === "object" && payload && "error" in payload && typeof (payload as { error?: unknown }).error === "string"
          ? ((payload as { error: string }).error)
          : response.statusText || "request failed";
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
  return request<{ auth_required: boolean; user_id: string }>("/api/me");
}

export async function login(username: string, password: string) {
  return request<{ user_id: string }>("/api/login", {
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

export async function fetchSession(sessionId: string) {
  return request<ApiSession>(`/api/sessions/${encodeURIComponent(sessionId)}`);
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

export async function interruptSession(sessionId: string) {
  return request<{ status: string }>(`/api/sessions/${encodeURIComponent(sessionId)}/interrupt`, {
    method: "POST"
  });
}

export async function fetchMessages(sessionId: string) {
  return request<{ messages: ApiMessage[] | null }>(`/api/sessions/${encodeURIComponent(sessionId)}/messages`);
}

export async function postMessage(sessionId: string, content: string) {
  return request<{ status: SessionStatus; session_id: string; message: ApiMessage }>(
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
      const error =
        record && typeof record.error === "string" ? record.error : xhr.statusText || "upload failed";
      const errorClass = record && typeof record.error_class === "string" ? record.error_class : undefined;
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
  let payload: unknown = raw;
  if (raw) {
    try {
      payload = JSON.parse(raw);
    } catch {
      payload = raw;
    }
  }
  const record = typeof payload === "object" && payload ? (payload as Record<string, unknown>) : null;

  if (response.ok) {
    // 200 body: {"path":"<clean rel path>"}.
    const savedPath = record && typeof record.path === "string" ? record.path : undefined;
    return { ok: true, path: savedPath };
  }

  const error = record && typeof record.error === "string" ? record.error : response.statusText || "save failed";
  const errorClass = record && typeof record.error_class === "string" ? record.error_class : undefined;
  return {
    ok: false,
    status: response.status,
    error,
    errorClass,
    stale: errorClass === "stale_edit",
    busy: errorClass === "turn_in_progress"
  };
}

export async function fetchArtifactText(sessionId: string, path: string, maxBytes = 2 * 1024 * 1024) {
  const response = await fetch(buildArtifactHref(sessionId, path), { cache: "no-store" });
  if (!response.ok) {
    return { ok: false as const, status: response.status, error: response.statusText || "fetch failed" };
  }
  const sizeHeader = response.headers.get("content-length");
  if (sizeHeader && Number.parseInt(sizeHeader, 10) > maxBytes) {
    return { ok: false as const, status: 413, error: "artifact too large" };
  }
  // Capture the Last-Modified token so an inline edit can round-trip it as
  // If-Unmodified-Since for optimistic-concurrency (409 stale_edit) checks.
  const lastModified = response.headers.get("last-modified");
  const text = await response.text();
  if (text.length > maxBytes) {
    return { ok: false as const, status: 413, error: "artifact too large" };
  }
  return { ok: true as const, text, lastModified };
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
