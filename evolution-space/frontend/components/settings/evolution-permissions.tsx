"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/central";
import { useT } from "@/lib/i18n";
type UserPage = { users: { id: string; username: string; display_name: string }[]; next_cursor?: string };
import { Select } from "@web/select";
const actionLabels: Record<string, string> = { "config.read": "Read group configuration", "config.manage": "Manage group configuration", "run.start": "Start group runs", "source.read": "Read source evidence", "workflow.edit": "Edit and publish group workflows" };
type Grant = { action: string; effect: string; revision: number; allowed: boolean };
type AccountGrants = { account_id: string; display_name: string; role: string; status: string; member: boolean; grants: Grant[] };
type Command = { request_id: string; account_id: string; action: string; effect: string; expected_revision: number };
export function EvolutionPermissionsEditor({ group, actor, onDirty }: { group: string; actor: string; onDirty: (value: boolean) => void }) {
 const tr = useT(), base = `/api/admin/groups/${encodeURIComponent(group)}/optimization/grants`, key = `evolution-grants:${actor}:${group}`;
 const [search, setSearch] = useState(""), [query, setQuery] = useState(""), [cursor, setCursor] = useState("");
 const [users, setUsers] = useState<UserPage>({ users: [] }), [account, setAccount] = useState("");
 const [grants, setGrants] = useState<AccountGrants | null>(null), [listError, setListError] = useState(""), [readError, setReadError] = useState(""), [error, setError] = useState(""), [notice, setNotice] = useState("");
 const [pending, setPending] = useState<Command | null>(null), [review, setReview] = useState<Command | null>(null), [busy, setBusy] = useState(false), [ready, setReady] = useState(false);
 const request = useRef<AbortController | null>(null), inFlight = useRef(false), alive = useRef(true);
 useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
 useEffect(() => { onDirty(!!pending || !!review); return () => onDirty(false); }, [pending, review, onDirty]);
 useEffect(() => { const abort = new AbortController(); void api<UserPage>(`/api/admin/users?limit=25&search=${encodeURIComponent(query)}&cursor=${encodeURIComponent(cursor)}`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) { setUsers(value); setListError(""); } }).catch(error => { if (!abort.signal.aborted) { setUsers({ users: [] }); setListError(String(error)); } }); return () => abort.abort(); }, [query, cursor]);
 const load = useCallback(async (id: string) => {
  request.current?.abort(); const abort = new AbortController(); request.current = abort;
  setGrants(null); setReadError(""); if (!id) return;
  try { const value = await api<AccountGrants>(`${base}/${encodeURIComponent(id)}`, { signal: abort.signal }); if (!abort.signal.aborted) setGrants(value); }
  catch (error) { if (!abort.signal.aborted) setReadError(error instanceof Error ? error.message : String(error)); }
 }, [base]);
 useEffect(() => { const timer = setTimeout(() => void load(account), 0); return () => { clearTimeout(timer); request.current?.abort(); }; }, [account, load]);
 useEffect(() => { const timer = setTimeout(() => {
  try { const raw = sessionStorage.getItem(key); if (raw) { if (raw.length > 8192) throw new Error("Saved request is invalid."); const command = JSON.parse(raw) as Command; if (!command.request_id || !command.account_id || !Object.hasOwn(actionLabels, command.action) || !["allow", "deny"].includes(command.effect) || !Number.isSafeInteger(command.expected_revision) || command.expected_revision < 0) throw new Error("Saved request is invalid."); setPending(command); setAccount(command.account_id); } setReady(true); }
  catch (error) { setError(String(error)); }
 }, 0); return () => clearTimeout(timer); }, [key]);
 async function submit(command: Command) {
  if (inFlight.current) return; inFlight.current = true; setBusy(true); setError(""); setNotice("");
  try {
   sessionStorage.setItem(key, JSON.stringify(command)); setPending(command); setReview(null);
   await mutate<{ revision: number }>(base, command, "PUT");
   sessionStorage.removeItem(key);
   if (alive.current) { setPending(null); setNotice("Permission change confirmed."); await load(command.account_id); }
  } catch (error) {
   if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.code !== "outcome_unknown") { sessionStorage.removeItem(key); if (alive.current) setPending(null); }
   if (alive.current) setError(error instanceof APIError && error.status === 409 ? "The grant changed. Refresh permissions and review a new change." : error instanceof Error ? error.message : String(error));
  } finally { inFlight.current = false; if (alive.current) setBusy(false); }
 }
 const blocked = busy || !!pending || !ready;
 return <section><header className="page-heading"><div><h2>{tr("Manage member grants")}</h2><p>{tr("Grant each capability explicitly. Configuration access never grants source evidence, Studio review or publication authority.")}</p></div></header>
  {error && <p role="alert">{tr(error)}</p>}{notice && <p role="status">{tr(notice)}</p>}{!ready && error && <button onClick={() => { sessionStorage.removeItem(key); setReady(true); setError(""); }}>{tr("Clear invalid recovery data")}</button>}
  {pending && <section className="notice"><p>{tr("An original request is awaiting confirmation. Reloading preserves its identity.")}</p><button disabled={busy} onClick={() => void submit(pending)}>{tr("Check original request")}</button></section>}
  <section className="panel"><form className="filters" onSubmit={event => { event.preventDefault(); setQuery(search); setCursor(""); }}><label>{tr("Find users")}<input value={search} maxLength={256} onChange={event => setSearch(event.target.value)} /></label><button disabled={blocked}>{tr("Search users")}</button></form>{listError && <p role="alert">{tr(listError)}</p>}<label>{tr("Account permissions")}<Select aria-label={tr("Account permissions")} value={account} disabled={blocked || !!review} onChange={event => { setAccount(event.target.value); setNotice(""); }}><option value="">{tr("Choose an account")}</option>{account && !users.users.some(user => user.id === account) && <option value={account}>{grants?.display_name || account}</option>}{users.users.map(user => <option key={user.id} value={user.id}>{user.display_name} (@{user.username})</option>)}</Select></label><div className="form-actions">{users.next_cursor && <button disabled={blocked} onClick={() => setCursor(users.next_cursor!)}>{tr("Next users")}</button>}{cursor && <button disabled={blocked} onClick={() => setCursor("")}>{tr("First users")}</button>}<button disabled={blocked || !account} onClick={() => { setReview(null); void load(account); }}>{tr("Refresh permissions")}</button></div></section>
  {readError && <p role="alert">{tr(readError)}</p>}{account && !grants && !readError && <p role="status">{tr("Loading permissions…")}</p>}
  {grants && grants.account_id === account && <section className="panel"><h2>{grants.display_name}</h2><p>{tr(grants.status)} · {tr(grants.role)} · {tr(grants.member ? "Current group member" : "Not a group member")}</p><p>{tr("Ordinary accounts need current group membership. Explicit denial also applies to administrators; source evidence always requires an explicit allow.")}</p><div className="table-scroll"><table><thead><tr><th>{tr("Capability")}</th><th>{tr("Explicit grant")}</th><th>{tr("Effective access")}</th><th>{tr("Change")}</th></tr></thead><tbody>{grants.grants.map(grant => <tr key={grant.action}><td>{tr(actionLabels[grant.action] || grant.action)}</td><td>{tr(grant.effect)} · {tr("Revision")} {grant.revision}</td><td>{tr(grant.allowed ? "Allowed" : "Denied")}</td><td><div className="form-actions">{["allow", "deny"].map(effect => <button key={effect} disabled={blocked || !!review || grant.effect === effect || !Number.isSafeInteger(grant.revision)} onClick={() => setReview({ request_id: crypto.randomUUID(), account_id: account, action: grant.action, effect, expected_revision: grant.revision })}>{tr(effect === "allow" ? "Allow" : "Deny")}</button>)}</div></td></tr>)}</tbody></table></div></section>}
  {review && <section className="panel" aria-label={tr("Review permission change")}><h2>{tr("Review permission change")}</h2><p>{grants?.display_name || review.account_id} · {tr(actionLabels[review.action])} → {tr(review.effect)}</p><p>{tr("This changes current authority immediately. Existing exported copies are not erased.")}</p><button disabled={blocked} onClick={() => void submit(review)}>{tr("Apply reviewed permission")}</button><button disabled={busy} onClick={() => setReview(null)}>{tr("Cancel")}</button></section>}
 </section>;
}
