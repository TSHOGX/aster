export const base = '/volumes-studio';
export type Session = { issuer: string; subject: string; csrf: string };
export type Library = { id: string; name: string; description: string; metadata_revision: number; draft_revision: number; base_version: string; archived: boolean };
export type Entry = { path: string; kind: 'file' | 'directory'; size: string; digest: string };
export type Version = { publisher_id: string; id: string; library_id: string; label: string; note: string; source_revision: number; descriptor_digest: string; state: string; publisher: { issuer: string; subject: string }; error?: string };
export type Upload = { id: string; path: string; size: string; offset: string; digest: string; state: string; target_identity: string; current_target?: string; current_revision?: number; owner: { issuer: string; subject: string } };
export class APIError extends Error {
  constructor(public status: number, public code: string) { super(code.replaceAll('_', ' ')); }
}
export async function api<T>(path: string, options: RequestInit = {}, csrf?: string): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set('Accept', 'application/json');
  if (options.body && typeof options.body === 'string') headers.set('Content-Type', 'application/json');
  if (csrf) headers.set('X-CSRF-Token', csrf);
  const response = await fetch(`${base}/api/v1${path}`, { ...options, headers, credentials: 'same-origin', cache: 'no-store' });
  if (!response.ok) { const error = await response.json().catch(() => ({})) as { error?: string }; throw new APIError(response.status, error.error ?? 'request_failed'); }
  return response.status === 204 ? undefined as T : response.json() as Promise<T>;
}
export const requestID = () => crypto.randomUUID();
export const bytes = (value: string | number) => { const n = Number(value); return n >= 2 ** 30 ? `${(n / 2 ** 30).toFixed(1)} GiB` : n >= 2 ** 20 ? `${(n / 2 ** 20).toFixed(1)} MiB` : n >= 1024 ? `${(n / 1024).toFixed(1)} KiB` : `${n} B`; };
