"use client";
import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/api";
import { resourceDimensions, validResourceInteger, type ResourceDimension, type Entitlement, type ResourceSettings, type GroupResources, type UserUsage, type ResourcePreview } from "@web/resources";
import { FitIssues, SavedPlanIssues, ResourceUsageView } from "./resource-usage";

type Values = Partial<Record<ResourceDimension, Entitlement | string | null>>;
export function ResourceEditor({ kind, id, onDirty }: { kind: "group" | "user"; id: string; onDirty: (value: boolean) => void }) {
  const base = `/api/admin/${kind === "group" ? "groups" : "users"}/${encodeURIComponent(id)}`;
  const path = `${base}/${kind === "group" ? "resource-policy" : "resource-settings"}`;
  const [current, setCurrent] = useState<ResourceSettings | GroupResources | null>(null), [usage, setUsage] = useState<UserUsage | null>(null), [edits, setEdits] = useState<Values>({});
  const [preview, setPreview] = useState<ResourcePreview | null>(null), [cursors, setCursors] = useState([""]), [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState<Record<string, unknown> | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState(""), [attempt, setAttempt] = useState(0);
  const alive = useRef(true);
  const dirty = Object.keys(edits).length > 0 || !!pending;
  useEffect(() => { onDirty(dirty); return () => onDirty(false); }, [dirty, onDirty]);
  useEffect(() => {
    alive.current = true;
    const abort = new AbortController();
    void api<ResourceSettings | GroupResources>(path, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setCurrent(value); }).catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    if (kind === "user") void api<UserUsage>(`${base}/usage`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setUsage(value); }).catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    return () => { alive.current = false; abort.abort(); };
  }, [path, base, kind, attempt]);
  const initial = (dimension: ResourceDimension): Entitlement | string | null => current && "dimensions" in current ? current.dimensions[dimension] ?? null : current && "overrides" in current ? current.overrides[dimension] ?? null : null;
  const selected = (dimension: ResourceDimension) => dimension in edits ? edits[dimension] ?? null : initial(dimension);
  const edit = (dimension: ResourceDimension, value: Entitlement | string | null) => {
    setEdits(old => { const next = { ...old }; if (JSON.stringify(value) === JSON.stringify(initial(dimension))) delete next[dimension]; else next[dimension] = value; return next; });
    setPreview(null); setCursors([""]); setConfirmed(false); setNotice("");
  };
  const changes = () => ({ kind, target_id: id, expected_revision: current!.revision, [kind === "group" ? "dimensions" : "overrides"]: edits });
  const invalid = Object.entries(edits).find(([, value]) => value !== null && value !== undefined && (typeof value === "string" ? !validResourceInteger(value) : !validResourceInteger(value.default) || !validResourceInteger(value.ceiling) || BigInt(value.default) > BigInt(value.ceiling)));
  const refresh = async (clear: boolean) => {
    const latest = await api<ResourceSettings | GroupResources>(path);
    const latestUsage = kind === "user" ? await api<UserUsage>(`${base}/usage`) : null;
    if (!alive.current) return;
    setCurrent(latest); setUsage(latestUsage); setPreview(null); setConfirmed(false); setCursors([""]);
    if (clear) setEdits({});
  };
  const inspect = async (pages = [""]) => {
    setBusy(true); setError(""); setConfirmed(false);
    try { const value = await mutate<ResourcePreview>(`/api/admin/changes/preview?limit=10&cursor=${encodeURIComponent(pages.at(-1)!)}`, changes()); if (alive.current) { setPreview(value); setCursors(pages); } }
    catch (error) { if (alive.current) { setPreview(null); setError(String(error)); } }
    finally { if (alive.current) setBusy(false); }
  };
  const save = async (body: Record<string, unknown>) => {
    setPending(body); setBusy(true); setError(""); setNotice("");
    try {
      const result = await mutate<{ replayed: boolean }>(path, body, "PATCH");
      if (!alive.current) return;
      await refresh(true);
      if (!alive.current) return;
      setPending(null); setNotice(result.replayed ? "Original resource change confirmed. Current settings loaded." : "Resource settings saved. Existing data and saved allocations are retained.");
    } catch (error) {
      if (!alive.current) return;
      if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.code !== "outcome_unknown") { setPending(null); setPreview(null); setConfirmed(false); }
      setError(error instanceof APIError && error.status === 409 ? "Settings or dependencies changed. Reload current settings, review your edits and preview again." : String(error));
    } finally { if (alive.current) setBusy(false); }
  };
  const blocked = busy || !!pending;
  return <section><header className="page-heading"><div><h1>{kind === "group" ? "Group resource policy" : "User resource settings"}</h1><p>{kind === "group" ? "Each member receives these entitlements. Group grants combine by maximum, never by sum." : "Inherited values follow the group default. Zero is an explicit zero allowance. Requested values cannot exceed the effective ceiling."}</p></div><Link href={`/${kind === "group" ? "groups" : "users"}/${encodeURIComponent(id)}`}>Back to {kind}</Link></header>
    {error && <p role="alert" className="error">{error}</p>}{notice && <p role="status">{notice}</p>}
    {!current && (!error ? <p role="status">Loading resource settings…</p> : <button onClick={() => { setError(""); setAttempt(value => value + 1); }}>Retry loading</button>)}
    {pending && !busy && <div className="notice"><p>The outcome is unconfirmed. Resolve the original request before editing again.</p><button onClick={() => void save(pending)}>Check original request</button></div>}
    {current && <><form className="account-form resource-form" onSubmit={event => { event.preventDefault(); void inspect(); }}><fieldset disabled={blocked}>
      {resourceDimensions.map(([dimension, label, unit]) => { const value = selected(dimension), offered = value !== null, entitlement = typeof value === "object" ? value : null; return <div className="resource-row" key={dimension}>
        <label>{label} ({unit})<select aria-label={`${label} mode`} value={offered ? "explicit" : "inherit"} onChange={event => edit(dimension, event.target.value === "inherit" ? null : kind === "group" ? { default: "0", ceiling: "0" } : "0")}><option value="inherit">{kind === "group" ? "Not offered" : "Inherit"}</option><option value="explicit">{kind === "group" ? "Offer entitlement" : "Explicit value"}</option></select></label>
        {offered && (kind === "group" ? <><label>Default<input aria-label={`${label} default`} inputMode="numeric" maxLength={19} value={entitlement?.default ?? "0"} onChange={event => edit(dimension, { default: event.target.value, ceiling: entitlement?.ceiling ?? "0" })} /></label><label>Ceiling<input aria-label={`${label} ceiling`} inputMode="numeric" maxLength={19} value={entitlement?.ceiling ?? "0"} onChange={event => edit(dimension, { default: entitlement?.default ?? "0", ceiling: event.target.value })} /></label></> : <label>Requested<input aria-label={`${label} requested`} inputMode="numeric" maxLength={19} value={typeof value === "string" ? value : ""} onChange={event => edit(dimension, event.target.value)} /></label>)}
      </div>; })}
      <p className="hint">Enter whole nonnegative integers in the shown units. Group defaults must not exceed ceilings. Platform limits always cap effective values.</p>
      {invalid && <p role="alert" className="error">Check {invalid[0]}: enter integers from 0 to 9223372036854775807 with default ≤ ceiling.</p>}
      <button disabled={!dirty || !!invalid}>Preview resource changes</button>
    </fieldset></form>
    <div className="form-actions"><button disabled={blocked} onClick={() => { setBusy(true); void refresh(false).then(() => { if (alive.current) { setError(""); setNotice("Current settings loaded. Your edits are retained for review."); } }).catch(error => { if (alive.current) setError(String(error)); }).finally(() => { if (alive.current) setBusy(false); }); }}>Reload current settings</button><button disabled={blocked} onClick={() => { setEdits({}); setPreview(null); setConfirmed(false); }}>Discard edits</button></div>
    {preview && <section className="panel"><h2>Change preview</h2><p>{preview.affected_accounts} affected accounts. Usage can change before saving; this preview reserves no capacity.</p>
      {preview.users.map(user => <article className="impact-card" key={user.account_id}><h3>{user.username}</h3><div className="table-scroll"><table><thead><tr><th>Resource</th><th>Default before → after</th><th>Effective before → after</th><th>Requested override after</th><th>Used / reserved</th></tr></thead><tbody>{resourceDimensions.filter(([key]) => JSON.stringify(user.before[key]) !== JSON.stringify(user.after[key])).map(([key, label]) => <tr key={key}><td>{label}</td><td>{user.before[key].default} → {user.after[key].default}</td><td>{user.before[key].effective} → {user.after[key].effective} / ceiling {user.after[key].ceiling}</td><td>{user.after[key].override ?? "Inherit"}{user.after[key].capped ? " (capped)" : user.before[key].capped ? " (request restored)" : ""}</td><td>{user.usage[key] ? `${user.usage[key]!.used} / ${user.usage[key]!.reserved}` : "Per Project or request limit"}</td></tr>)}</tbody></table></div>
        <details><summary>Usage overages: {user.before_overages.length} → {user.after_overages.length}</summary><FitIssues issues={user.after_overages} /></details><details><summary>Default environment conflicts: {user.before_plan_issues.length} → {user.after_plan_issues.length}</summary><FitIssues issues={user.after_plan_issues} /></details><details><summary>Saved Project conflicts: {user.before_saved_plan_conflicts.count} → {user.after_saved_plan_conflicts.count}</summary><SavedPlanIssues value={user.after_saved_plan_conflicts} /></details>
      </article>)}
      <nav className="pagination" aria-label="Resource impact pages"><button disabled={blocked || cursors.length === 1} onClick={() => void inspect(cursors.slice(0, -1))}>Previous accounts</button><button disabled={blocked || !preview.next_cursor} onClick={() => void inspect([...cursors, preview.next_cursor!])}>Next accounts</button></nav>
      <label className="check-label"><input type="checkbox" checked={confirmed} disabled={blocked} onChange={event => setConfirmed(event.target.checked)} />I reviewed the impact. Reductions may block new allocations or resume; existing files remain available.</label><button className="primary" disabled={blocked || !confirmed} onClick={() => { const { kind: _kind, target_id: _target, ...body } = changes(); void save({ ...body, expected_dependencies: preview.expected_dependencies, request_id: crypto.randomUUID() }); }}>Save resource settings</button>
    </section>}
    {usage && <ResourceUsageView value={usage} />}</>}
  </section>;
}
