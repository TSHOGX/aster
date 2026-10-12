export class APIError extends Error {
  constructor(public status: number, public code: string, message: string, public field?: string) { super(message); }
}
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  if (!path.startsWith("/api/")) throw new Error("same-origin API path required");
  const mutation = !!init.method && !["GET", "HEAD"].includes(init.method.toUpperCase());
  let response: Response;
  try {
    response = await fetch(path, { ...init, credentials: "same-origin", cache: "no-store", headers: {
      ...(typeof init.body === "string" ? { "Content-Type": "application/json" } : {}), ...init.headers,
    } });
  } catch {
    throw new APIError(0, mutation ? "outcome_unknown" : "network_unavailable", mutation
      ? "The result is unknown. Retry to check whether this change was saved."
      : "The server is unavailable. Retry to load current data.");
  }
  if (response.status === 401 && path !== "/api/auth/session" && typeof window !== "undefined") window.dispatchEvent(new Event("admin-auth-expired"));
  if (response.status === 403 && typeof window !== "undefined") window.dispatchEvent(new Event("admin-authority-denied"));
  if (response.status === 204) return undefined as T;
  const data = await response.json().catch(() => { throw new APIError(response.status, mutation ? "outcome_unknown" : "response_invalid", "The server returned an incomplete response."); });
  if (!response.ok) throw new APIError(response.status, data.code || "request_failed", data.error || `Request failed (${response.status}).`, data.field);
  return data as T;
}
export function mutate<T>(path: string, body: unknown, method = "POST") {
  return api<T>(path, { method, body: JSON.stringify(body) });
}
