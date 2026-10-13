"use client";
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

import Link from "next/link";
import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
import { date, type AuditEvent, type Operation } from "@/lib/models";
export function AuditView() {
  const tr = useT();
  const params = useSearchParams();
  const [draft, setDraft] = useState({ actor: "", target: params.get("target") || "", action: "", after: "", before: "" });
  const [filter, setFilter] = useState(draft), [cursors, setCursors] = useState([""]);
  const [page, setPage] = useState<{ events: AuditEvent[]; next_cursor?: string }>({ events: [] });
  const [loading, setLoading] = useState(true), [error, setError] = useState(""), [refresh, setRefresh] = useState(0);
  const cursor = cursors[cursors.length - 1];
  useEffect(() => {
    const abort = new AbortController();
    const query = new URLSearchParams({ ...filter, cursor, limit: "50" });
    void api<typeof page>(`/api/admin/audit-events?${query}`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) { setPage(value); setError(""); } })
      .catch(error => { if (!abort.signal.aborted) setError(String(error)); }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [filter, cursor, refresh]);
  return <section aria-labelledby="audit-title"><header className="page-heading"><div><h1 id="audit-title">{tr("Audit")}</h1><p>{tr("Security and configuration changes, without credentials or private content.")}</p></div></header>
    <form className="filters" onSubmit={event => { event.preventDefault(); setLoading(true); setCursors([""]); setFilter({ ...draft, after: draft.after ? new Date(draft.after).toISOString() : "", before: draft.before ? new Date(draft.before).toISOString() : "" }); }}>
      <label>{tr("Actor ID")}<input value={draft.actor} onChange={event => setDraft({ ...draft, actor: event.target.value })} /></label>
      <label>{tr("Target ID")}<input value={draft.target} onChange={event => setDraft({ ...draft, target: event.target.value })} /></label>
      <label>{tr("Action")}<input value={draft.action} placeholder={tr("account.create")} onChange={event => setDraft({ ...draft, action: event.target.value })} /></label>
      <label>{tr("After")}<input type="datetime-local" value={draft.after} onChange={event => setDraft({ ...draft, after: event.target.value })} /></label>
      <label>{tr("Before")}<input type="datetime-local" value={draft.before} onChange={event => setDraft({ ...draft, before: event.target.value })} /></label>
      <button type="submit">{tr("Filter")}</button><button type="button" onClick={() => { setLoading(true); setRefresh(v => v + 1); }}>{tr("Refresh")}</button>
    </form>
    {error && <p className="error" role="alert">{tr(error)}</p>}{loading && <p role="status">{tr("Loading audit…")}</p>}
    <div className="audit-events" aria-busy={loading}>{page.events.map(event => <article key={event.id} className="panel"><header><strong>{tr(event.action)}</strong><time dateTime={event.created_at}>{date(event.created_at)}</time></header>
      <dl><dt>{tr("Actor")}</dt><dd className="identifier">{event.actor_id}</dd><dt>{tr("Target")}</dt><dd className="identifier">{tr(event.target_kind)}: {event.target_id}</dd><dt>{tr("Revision")}</dt><dd>{formatQuantity(event.revision)}</dd>{event.request_id && <><dt>{tr("Request")}</dt><dd className="identifier">{event.request_id}</dd></>}</dl>
      <details><summary>{tr("Change details")}</summary><pre>{JSON.stringify(event.details, null, 2)}</pre></details>
      {typeof event.details.operation_id === "string" && <Link href={`/operations/${encodeURIComponent(event.details.operation_id)}`}>{tr("View operation")}</Link>}
    </article>)}</div>
    {!loading && !error && !page.events.length && <p className="empty">{tr("No matching audit events.")}</p>}
    <nav className="pagination" aria-label={tr("Audit pages")}><button disabled={loading || cursors.length === 1} onClick={() => { setLoading(true); setCursors(values => values.slice(0, -1)); }}>{tr("Previous")}</button><span>{tr("Page")} {formatQuantity(cursors.length)}</span><button disabled={loading || !page.next_cursor} onClick={() => { setLoading(true); setCursors(values => [...values, page.next_cursor!]); }}>{tr("Next")}</button></nav>
  </section>;
}
export function OperationView({ id }: { id: string }) {
  const tr = useT();
  const [targets,setTargets]=useState<{targets:{project_id:string;state:string;phase?:string;error?:string}[];next_cursor?:string}>({targets:[]}),[cursors,setCursors]=useState([""]);
  const cursor=cursors.at(-1)!;
  const [operation, setOperation] = useState<Operation | null>(null), [error, setError] = useState(""), [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    void Promise.all([api<Operation>(`/api/admin/operations/${encodeURIComponent(id)}`, { signal: abort.signal }),api<typeof targets>(`/api/admin/operations/${encodeURIComponent(id)}/targets?limit=25&cursor=${encodeURIComponent(cursor)}`,{signal:abort.signal})]).then(([value,page]) => { if (!abort.signal.aborted) { setOperation(value);setTargets(page); setError(""); } })
      .catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    return () => abort.abort();
  }, [id, refresh,cursor]);
  return <section><header className="page-heading"><h1>{tr("Operation")}</h1><button onClick={() => setRefresh(v => v + 1)}>{tr("Refresh")}</button></header>
    {error && <p role="alert" className="error">{tr(error)}</p>}
    {operation ? <article className="panel"><h2>{tr(operation.action)}</h2><p role="status">{tr(operation.state)}</p><dl><dt>{tr("Target")}</dt><dd className="identifier">{operation.target_id}</dd><dt>{tr("Observed completion")}</dt><dd>{formatQuantity(operation.completed_targets)}  {tr("of")} {formatQuantity(operation.total_targets)}  {tr("targets")}</dd><dt>{tr("Updated")}</dt><dd>{date(operation.updated_at)}</dd></dl>
      {operation.state === "accepted" && <p>{tr("The request is recorded. Resource work has not been confirmed complete.")}</p>}
      {operation.state === "unknown" && <p>{tr("The resource outcome is uncertain. Reconciliation will retry the original intent; refresh to see new evidence. It has not been reported as stopped or complete.")}</p>}
      {operation.error && <p role="alert" className="error">{tr(operation.error)}</p>}
      <div className="table-scroll"><table><thead><tr><th>{tr("Project target")}</th><th>{tr("Observed progress")}</th><th>{tr("Detail")}</th></tr></thead><tbody>{targets.targets.map(target=><tr key={target.project_id}><td><Link href={`/projects/${encodeURIComponent(target.project_id)}`}>{target.project_id}</Link></td><td>{tr(target.state)}{target.phase && <small>{tr(target.phase)}</small>}</td><td>{target.error || "—"}</td></tr>)}</tbody></table></div>
      <nav className="pagination" aria-label={tr("Operation target pages")}><button disabled={cursors.length===1} onClick={()=>setCursors(value=>value.slice(0,-1))}>{tr("Previous targets")}</button><button disabled={!targets.next_cursor} onClick={()=>setCursors(value=>[...value,targets.next_cursor!])}>{tr("Next targets")}</button></nav>

    </article> : !error && <p role="status">{tr("Loading operation…")}</p>}
  </section>;
}
