import { centralSessionMatches } from "../../../web/application-session";
export const base = "/evolution-space";
export type Session = { subject: string; issuer: string; csrf: string; entitlements: string[] };
export type Group = {
  id: string; name: string; description: string;
  can_edit_workflows: boolean; can_manage: boolean; can_run: boolean; can_read_evidence: boolean;
  desired_revision: number; active_revision: number; auto_enabled: boolean; state: string;
};
export type GroupPage = { items: Group[]; next_after: string; has_more: boolean };
export class APIError extends Error { constructor(public status: number) { super(`HTTP ${status}`); } }
export async function api<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`${base}/api/v1${path}`, { credentials: "same-origin", cache: "no-store", signal });
  if (!response.ok) throw new APIError(response.status);
  const value = await response.json();
  if (path === "/session" && !await centralSessionMatches(value.subject, signal)) throw new APIError(401);
  return value as T;
}
