import { validLanguage } from "./i18n/locale";
export class PreferenceAPIError extends Error {
  constructor(public status: number, message: string) { super(message); }
}
export async function preferenceAPI<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, { ...init, credentials: "same-origin", cache: "no-store", headers: { "Content-Type": "application/json", ...init.headers } });
  const data = await response.json();
  if (!response.ok) throw new PreferenceAPIError(response.status, data.error || "Settings unavailable");
  if (path === "/api/me/preferences") {
    if (data.language !== undefined && !validLanguage(data.language)) throw new PreferenceAPIError(0, "Invalid language preference");
    data.language ??= "system";
  }
  return data;
}
export function preferenceCommand<T>(path: string, body: unknown, method: string) { return preferenceAPI<T>(path, { method, body: JSON.stringify(body) }); }
