"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
import { date, type AuditEvent, type Operation } from "@/lib/models";
export function AuditView() {
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
  return <section aria-labelledby="audit-title"><header className="page-heading"><div><h1 id="audit-title">Audit</h1><p>Security and configuration changes, without credentials or private content.</p></div></header>
    <form className="filters" onSubmit={event => { event.preventDefault(); setLoading(true); setCursors([""]); setFilter({ ...draft, after: draft.after ? new Date(draft.after).toISOString() : "", before: draft.before ? new Date(draft.before).toISOString() : "" }); }}>
      <label>Actor ID<input value={draft.actor} onChange={event => setDraft({ ...draft, actor: event.target.value })} /></label>
      <label>Target ID<input value={draft.target} onChange={event => setDraft({ ...draft, target: event.target.value })} /></label>
      <label>Action<input value={draft.action} placeholder="account.create" onChange={event => setDraft({ ...draft, action: event.target.value })} /></label>
      <label>After<input type="datetime-local" value={draft.after} onChange={event => setDraft({ ...draft, after: event.target.value })} /></label>
      <label>Before<input type="datetime-local" value={draft.before} onChange={event => setDraft({ ...draft, before: event.target.value })} /></label>
      <button type="submit">Filter</button><button type="button" onClick={() => { setLoading(true); setRefresh(v => v + 1); }}>Refresh</button>
    </form>
    {error && <p className="error" role="alert">{error}</p>}{loading && <p role="status">Loading audit…</p>}
    <div className="audit-events" aria-busy={loading}>{page.events.map(event => <article key={event.id} className="panel"><header><strong>{event.action}</strong><time dateTime={event.created_at}>{date(event.created_at)}</time></header>
      <dl><dt>Actor</dt><dd className="identifier">{event.actor_id}</dd><dt>Target</dt><dd className="identifier">{event.target_kind}: {event.target_id}</dd><dt>Revision</dt><dd>{event.revision}</dd>{event.request_id && <><dt>Request</dt><dd className="identifier">{event.request_id}</dd></>}</dl>
      <details><summary>Change details</summary><pre>{JSON.stringify(event.details, null, 2)}</pre></details>
      {typeof event.details.operation_id === "string" && <Link href={`/operations/${encodeURIComponent(event.details.operation_id)}`}>View operation</Link>}
    </article>)}</div>
    {!loading && !error && !page.events.length && <p className="empty">No matching audit events.</p>}
    <nav className="pagination" aria-label="Audit pages"><button disabled={loading || cursors.length === 1} onClick={() => { setLoading(true); setCursors(values => values.slice(0, -1)); }}>Previous</button><span>Page {cursors.length}</span><button disabled={loading || !page.next_cursor} onClick={() => { setLoading(true); setCursors(values => [...values, page.next_cursor!]); }}>Next</button></nav>
  </section>;
}
export function OperationView({ id }: { id: string }) {
  const [targets,setTargets]=useState<{targets:{project_id:string;state:string;phase?:string;error?:string}[];next_cursor?:string}>({targets:[]}),[cursors,setCursors]=useState([""]);
  const cursor=cursors.at(-1)!;
  const [operation, setOperation] = useState<Operation | null>(null), [error, setError] = useState(""), [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    void Promise.all([api<Operation>(`/api/admin/operations/${encodeURIComponent(id)}`, { signal: abort.signal }),api<typeof targets>(`/api/admin/operations/${encodeURIComponent(id)}/targets?limit=25&cursor=${encodeURIComponent(cursor)}`,{signal:abort.signal})]).then(([value,page]) => { if (!abort.signal.aborted) { setOperation(value);setTargets(page); setError(""); } })
      .catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    return () => abort.abort();
  }, [id, refresh,cursor]);
  return <section><header className="page-heading"><h1>Operation</h1><button onClick={() => setRefresh(v => v + 1)}>Refresh</button></header>
    {error && <p role="alert" className="error">{error}</p>}
    {operation ? <article className="panel"><h2>{operation.action}</h2><p role="status">{operation.state}</p><dl><dt>Target</dt><dd className="identifier">{operation.target_id}</dd><dt>Observed completion</dt><dd>{operation.completed_targets} of {operation.total_targets} targets</dd><dt>Updated</dt><dd>{date(operation.updated_at)}</dd></dl>
      {operation.state === "accepted" && <p>The request is recorded. Resource work has not been confirmed complete.</p>}
      {operation.state === "unknown" && <p>The resource outcome is uncertain. Reconciliation will retry the original intent; refresh to see new evidence. It has not been reported as stopped or complete.</p>}
      {operation.error && <p role="alert" className="error">{operation.error}</p>}
      <div className="table-scroll"><table><thead><tr><th>Project target</th><th>Observed progress</th><th>Detail</th></tr></thead><tbody>{targets.targets.map(target=><tr key={target.project_id}><td><Link href={`/projects/${encodeURIComponent(target.project_id)}`}>{target.project_id}</Link></td><td>{target.state}{target.phase && <small>{target.phase}</small>}</td><td>{target.error || "—"}</td></tr>)}</tbody></table></div>
      <nav className="pagination" aria-label="Operation target pages"><button disabled={cursors.length===1} onClick={()=>setCursors(value=>value.slice(0,-1))}>Previous targets</button><button disabled={!targets.next_cursor} onClick={()=>setCursors(value=>[...value,targets.next_cursor!])}>Next targets</button></nav>

    </article> : !error && <p role="status">Loading operation…</p>}
  </section>;
}
