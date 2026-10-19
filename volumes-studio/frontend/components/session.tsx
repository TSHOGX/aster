"use client";
import { useT } from "../lib/i18n";

import { useCallback, useEffect, useRef, useState } from "react";
import { useSessionRefresh } from "../../../web/session-refresh";
import { centralSessionMatches } from "../../../web/application-session";
import { api, APIError, base, Session } from "../lib/api";
export function useSession() {
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const current = useRef<Session | null>(null);
  const active = useRef<AbortController | null>(null);
  const refresh = useCallback(() => {
    active.current?.abort();
    const abort = new AbortController();
    active.current = abort;
    void api<Session>("/session", { signal: abort.signal }).then(async value => {
      if (!await centralSessionMatches(value.subject, abort.signal)) throw new APIError(401, "session_changed");
      if (abort.signal.aborted) return;
      if (current.current && (current.current.subject !== value.subject || current.current.issuer !== value.issuer)) {
        // Keep recovery bound to its original account until a full navigation.
        window.dispatchEvent(new Event("studio-auth-expired"));
        return;
      }
      current.current = value;
      setSession(value); setError("");
    }).catch((e: unknown) => {
      if (abort.signal.aborted) return;
      const denied = e instanceof APIError && (e.status === 401 || e.status === 403);
      if (denied && current.current) window.dispatchEvent(new Event("studio-auth-expired"));
      setError(denied ? "Sign in with your central account to continue." : "The identity service is unavailable. Please retry.");
    }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
  }, []);
  useSessionRefresh(refresh);
  useEffect(() => {
    refresh();
    return () => active.current?.abort();
  }, [refresh]);
  return { session, error, loading, refresh };
}
export function SessionGate({
  error,
  loading,
  retry,
}: {
  error: string;
  loading: boolean;
  retry: () => void;
}) {
  const tr = useT();
  return (
    <main className="session-gate">
      <div className="wordmark">
        ◈ <span>{tr("Volume Studio")}</span>
      </div>
      <h1>{loading ? tr("Opening Studio…") : tr("Sign in to Studio")}</h1>
      <p role="status">{loading ? tr("Checking your session…") : tr(error)}</p>
      {!loading && (
        <><a
          className="button primary"
          href={`${base}/auth/login?return=${encodeURIComponent(window.location.pathname + window.location.search)}`}
        >
           {tr("Sign in")} </a><button onClick={retry}>{tr("Retry")}</button></>
      )}
    </main>
  );
}
export function SignOut({ session }: { session: Session }) {
  const tr = useT();
  const [error, setError] = useState("");
  async function logout() {
    const response = await fetch(`${base}/auth/logout`, {
      method: "POST",
      headers: { "X-CSRF-Token": session.csrf },
      credentials: "same-origin",
    }).catch(() => null);
    if (response?.ok) window.location.reload();
    else
      setError(
        "Shared sign out is unavailable. Retry when identity is restored.",
      );
  }
  return (
    <div className="account">
      <span className="muted" title={session.subject}>
         {tr("Administrator")} </span>
      <button type="button" className="quiet" onClick={() => void logout()}>
         {tr("Sign out everywhere in this session")} </button>
      {error && <p role="alert">{tr(error)}</p>}
    </div>
  );
}
