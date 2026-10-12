'use client';
import { useEffect, useState } from 'react';
import { api, APIError, base, Session } from '../lib/api';
export function useSession() {
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const abort = new AbortController();
    api<Session>('/session', { signal: abort.signal }).then(setSession).catch((e: unknown) => {
      if (!abort.signal.aborted) setError(e instanceof APIError && e.status === 401 ? 'Sign in with your central account to continue.' : 'The identity service is unavailable. Please retry.');
    }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, []);
  return { session, error, loading };
}
export function SessionGate({ error, loading }: { error: string; loading: boolean }) {
  return <main className="shell"><p className="eyebrow">VOLUME STUDIO</p><h1>Your files, ready to share.</h1><p role="status">{loading ? 'Checking your session…' : error}</p>{!loading && <a className="button" href={`${base}/auth/login?return=${encodeURIComponent(window.location.pathname + window.location.search)}`}>Sign in</a>}</main>;
}
export function SignOut({ session }: { session: Session }) {
  const [error, setError] = useState('');
  async function logout() {
    const response = await fetch(`${base}/auth/logout`, { method: 'POST', headers: { 'X-CSRF-Token': session.csrf }, credentials: 'same-origin' }).catch(() => null);
    if (response?.ok) window.location.reload();
    else setError('Shared sign out is unavailable. Retry when identity is restored.');
  }
  return <div className="account"><span className="muted" title={session.subject}>Administrator</span><button type="button" className="quiet" onClick={() => void logout()}>Sign out everywhere in this session</button>{error && <p role="alert">{error}</p>}</div>;
}
