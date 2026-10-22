"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/api";
import { useT } from "@/lib/i18n";

const labels: Record<string, string> = {
  "volume.read": "Read Studio Volume",
  "volume.author": "Author branches and drafts",
  "volume.review": "Review integrations",
  "volume.merge": "Merge approved integrations",
  "volume.publish": "Publish and manage versions",
  "volume.configure": "Configure Volume policies",
};
type Command = { request_id: string; kind: string; subject_id: string; library_id: string; action: string; effect: string; expected_revision: number };
type Grant = { action: string; effect: string; revision: number; allowed?: boolean };
type Grants = { kind: string; subject_id: string; library_id: string; display_name: string; grants: Grant[] };
const validID = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);

export function StudioPermissionsEditor({ kind, subject, actor, onDirty }: { kind: "account" | "group"; subject: string; actor: string; onDirty: (value: boolean) => void }) {
  const tr = useT(), key = `studio-grants:${actor}:${kind}:${subject}`;
  const [library, setLibrary] = useState(""), [grants, setGrants] = useState<Grants | null>(null);
  const [pending, setPending] = useState<Command | null>(null), [review, setReview] = useState<Command | null>(null);
  const [ready, setReady] = useState(false), [busy, setBusy] = useState(false), [loading, setLoading] = useState(false);
  const [error, setError] = useState(""), [notice, setNotice] = useState("");
  const request = useRef<AbortController | null>(null), inFlight = useRef(false), alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; request.current?.abort(); }; }, []);
  useEffect(() => { onDirty(!!pending || !!review); return () => onDirty(false); }, [pending, review, onDirty]);

  const load = useCallback(async (id: string) => {
    request.current?.abort(); const abort = new AbortController(); request.current = abort;
    setGrants(null); setError(""); setLoading(true);
    try {
      const query = new URLSearchParams({ kind, subject_id: subject, library_id: id });
      const result = await api<Grants>(`/api/admin/studio-grants?${query}`, { signal: abort.signal });
      if (!abort.signal.aborted && alive.current) {
        setGrants(result);
        const url = new URL(location.href); url.searchParams.set("library", id); history.replaceState(history.state, "", url);
      }
    } catch (error) { if (!abort.signal.aborted && alive.current) setError(String(error)); }
    finally { if (!abort.signal.aborted && alive.current) setLoading(false); }
  }, [kind, subject]);

  useEffect(() => {
    const timer = setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(key);
        let id = new URLSearchParams(location.search).get("library") || "";
        if (raw) {
          if (raw.length > 8192) throw new Error();
          const command = JSON.parse(raw) as Command;
          if (!validID(command.request_id) || command.kind !== kind || command.subject_id !== subject || !validID(command.library_id) || !Object.hasOwn(labels, command.action) || !["allow", "deny"].includes(command.effect) || !Number.isSafeInteger(command.expected_revision) || command.expected_revision < 0) throw new Error();
          setPending(command); id = command.library_id;
        }
        setLibrary(id); setReady(true);
        if (validID(id)) void load(id);
      } catch { setError("Saved permission request is invalid. Clear it to continue."); }
    }, 0);
    return () => clearTimeout(timer);
  }, [key, kind, subject, load]);

  async function submit(command: Command) {
    if (inFlight.current) return;
    inFlight.current = true; setBusy(true); setError(""); setNotice("");
    try {
      sessionStorage.setItem(key, JSON.stringify(command)); setPending(command); setReview(null);
      await mutate("/api/admin/studio-grants", command);
      sessionStorage.removeItem(key);
      if (alive.current) { setPending(null); setNotice("Permission change confirmed."); await load(command.library_id); }
    } catch (error) {
      if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.code !== "outcome_unknown") {
        sessionStorage.removeItem(key);
        if (alive.current) { setPending(null); setGrants(null); }
      }
      if (alive.current) setError(error instanceof APIError && error.status === 409 ? "The grant changed. Refresh permissions and review a new change." : String(error));
    } finally { inFlight.current = false; if (alive.current) setBusy(false); }
  }
  const blocked = !ready || busy || !!pending;
  return <section>
    <header className="page-heading"><div><h1>{tr("Studio permissions")}</h1><p>{tr("Assign each Volume capability separately. These grants do not change Project mounts or deploy published versions.")}</p></div><Link href={`/${kind === "account" ? "users" : "groups"}/${encodeURIComponent(subject)}`}>{tr(kind === "account" ? "Back to user" : "Back to group")}</Link></header>
    {error && <p role="alert">{tr(error)}</p>}{notice && <p role="status">{tr(notice)}</p>}
    {!ready && error && <button onClick={() => { sessionStorage.removeItem(key); setReady(true); setError(""); }}>{tr("Clear invalid recovery data")}</button>}
    {pending && <section className="notice"><p>{tr("An original request is awaiting confirmation. Reloading preserves its identity.")}</p><p>{pending.library_id} · {tr(labels[pending.action])} → {tr(pending.effect)}</p><button disabled={busy} onClick={() => void submit(pending)}>{tr("Check original request")}</button></section>}
    <form className="panel" onSubmit={event => { event.preventDefault(); setReview(null); setNotice(""); void load(library); }}>
      <label>{tr("Studio Volume ID")}<input required maxLength={128} pattern="[A-Za-z0-9_\-]{1,128}" value={library} disabled={blocked || !!review} onChange={event => { request.current?.abort(); setLoading(false); setGrants(null); setLibrary(event.target.value); setNotice(""); }} /></label>
      <p>{tr("Use the library ID from the Studio URL. Inspecting grants does not verify that the Volume exists or grant access to its content.")}</p>
      <button disabled={blocked || loading || !!review || !validID(library)}>{tr("Refresh permissions")}</button>
    </form>
    {loading && <p role="status">{tr("Loading permissions…")}</p>}
    {grants && grants.library_id === library && <section className="panel"><h2>{grants.display_name} · {grants.library_id}</h2>
      <p>{tr(kind === "account" ? "Effective access includes account status and all group grants. Any explicit denial overrides an allowance, including for administrators." : "Group rules apply to current members. Account status and other explicit denials may still prevent access; inspect the account to see its effective access.")}</p>
      <div className="table-scroll"><table><thead><tr><th>{tr("Capability")}</th><th>{tr("Explicit grant")}</th>{kind === "account" && <th>{tr("Effective access")}</th>}<th>{tr("Change")}</th></tr></thead><tbody>{grants.grants.map(grant => <tr key={grant.action}><td>{tr(labels[grant.action] || grant.action)}</td><td>{tr(grant.effect)} · {tr("Revision")} {grant.revision}</td>{kind === "account" && <td>{tr(grant.allowed ? "Allowed" : "Denied")}</td>}<td><div className="form-actions">{["allow", "deny"].map(effect => <button key={effect} disabled={blocked || !!review || !Object.hasOwn(labels, grant.action) || grant.effect === effect || !Number.isSafeInteger(grant.revision)} onClick={() => setReview({ request_id: crypto.randomUUID(), kind, subject_id: subject, library_id: library, action: grant.action, effect, expected_revision: grant.revision })}>{tr(effect === "allow" ? "Allow" : "Deny")}</button>)}</div></td></tr>)}</tbody></table></div>
    </section>}
    {review && <section className="panel" aria-label={tr("Review permission change")}><h2>{tr("Review permission change")}</h2><p>{grants?.display_name || subject} · {review.library_id} · {tr(labels[review.action])} → {tr(review.effect)}</p><p>{tr("This changes current authority immediately. Existing exported copies are not erased.")}</p><button disabled={blocked} onClick={() => void submit(review)}>{tr("Apply reviewed permission")}</button><button disabled={busy} onClick={() => setReview(null)}>{tr("Cancel")}</button></section>}
  </section>;
}
