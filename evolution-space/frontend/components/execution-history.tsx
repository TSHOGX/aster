"use client";
import Link from "next/link";
import { evolutionContextQuery } from "../../../web/application-paths";
import { useSearchParams } from "next/navigation";
import { useLocale } from "../../../web/i18n";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";

type Revision = { id: string; revision: number };
type Run = { id: string; state: string; handoff: string; evaluation_state: string; created_at: string; pins: { template: Revision; workflow: Revision; base: { commit_id: string } } };
type Evaluation = { id: string; state: string; profile: { ref: Revision } };
type Page<T> = { items: T[]; next_after: string; has_more: boolean };
const fields = [
  ["template", "Template ID"], ["workflow", "Workflow ID"],
  ["workflow_revision", "Workflow revision"], ["base_commit", "Base commit"],
  ["source", "Source snapshot ID"], ["author", "Requester account ID"],
  ["from", "Created from (inclusive)"], ["until", "Created until (exclusive)"],
] as const;
const states = ["queued", "running", "pause_requested", "paused", "cancelling", "completed", "failed", "cancelled"];
function navigate(values: Record<string, string>) {
  const url = new URL(window.location.href);
  for (const [key, value] of Object.entries(values)) {
    if (value) url.searchParams.set(key, value); else url.searchParams.delete(key);
  }
  window.history.pushState(null, "", url.pathname + url.search + url.hash);
}
function HistoryPages({ prefix, after, next, more, loading }: { prefix: string; after: string; next?: string; more?: boolean; loading: boolean }) {
  const tr = useT();
  return <div className="pagination">{after && <button onClick={() => navigate({ [prefix + "after"]: "" })}>{tr("First page")}</button>}{more && next && <button disabled={loading} onClick={() => navigate({ [prefix + "after"]: next })}>{tr("Next page")}</button>}</div>;
}
export function RunHistory({ group }: { group: string }) {
  const tr = useT(); const locale = useLocale(); const search = useSearchParams();
  const query = new URLSearchParams({ limit: "25" });
  for (const field of ["after", "state", ...fields.map(([key]) => key)]) {
    const value = search.get("runs_" + field);
    if (value) query.set(field, value);
  }
  const read = useRead<Page<Run>>(`/groups/${encodeURIComponent(group)}/runs?${query}`);
  return <section className="card execution-history" aria-labelledby="runs-heading">
    <div className="section-heading"><h2 id="runs-heading">{tr("Run history")}</h2><button onClick={read.refresh}>{tr("Refresh")}</button></div>
    <p>{tr("Browse retained runs in stable ID order. Filters apply to the whole group history.")}</p>
    <form key={query.toString()} onSubmit={event => {
      event.preventDefault();
      const data = new FormData(event.currentTarget);
      const values: Record<string, string> = { runs_after: "" };
      for (const key of ["state", ...fields.map(([field]) => field)]) values["runs_" + key] = String(data.get(key) ?? "").trim();
      navigate(values);
    }}>
      <label>{tr("Execution state")}<select aria-label={tr("Execution state")} name="state" defaultValue={query.get("state") ?? ""}><option value="">{tr("All states")}</option>{states.map(state => <option key={state} value={state}>{tr(state === "pause_requested" ? "Pausing" : state)}</option>)}</select></label>
      <details open={fields.some(([key]) => query.has(key))}><summary>{tr("Filter run history")}</summary><div className="history-filters">{fields.map(([key, label]) => <label key={key}>{tr(label)}<input name={key} defaultValue={query.get(key) ?? ""} type={key === "workflow_revision" ? "number" : "text"} min={key === "workflow_revision" ? 1 : undefined} maxLength={256} placeholder={key === "from" || key === "until" ? tr("YYYY-MM-DDTHH:mm:ss+08:00") : undefined} /></label>)}</div><p>{tr("Enter dates with a timezone. IDs match exactly; leave fields empty for all values.")}</p></details>
      <div className="editor-actions"><button type="submit">{tr("Apply filters")}</button><button type="button" onClick={() => navigate(Object.fromEntries(["after", "state", ...fields.map(([key]) => key)].map(key => ["runs_" + key, ""])))}>{tr("Clear filters")}</button></div>
    </form>
    {read.loading && <p role="status">{tr("Loading runs…")}</p>}
    {read.status && <div role="alert"><p>{tr(read.status === 400 ? "Run filters are invalid. Check IDs, revision and timezone dates." : "Runs are unavailable. Retry when the service is restored.")}</p><button onClick={read.refresh}>{tr("Retry")}</button></div>}
    {read.data && (!read.data.items.length ? <p role="status">{tr("No matching runs.")}</p> : <table tabIndex={0} aria-label={tr("Run history")}><thead><tr>{["Run", "Execution state", "Handoff", "Evaluation", "Template", "Workflow", "Created at"].map(label => <th key={label} scope="col">{tr(label)}</th>)}</tr></thead><tbody>{read.data.items.map(run => <tr key={run.id}><th scope="row"><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(run.id)}/${evolutionContextQuery(search)}`}>{run.id}</Link></th><td>{tr(run.state === "pause_requested" ? "Pausing" : run.state)}</td><td>{tr(run.handoff)}</td><td>{tr(run.evaluation_state)}</td><td>{run.pins.template.id} · {run.pins.template.revision}</td><td>{run.pins.workflow.id} · {run.pins.workflow.revision}</td><td><time dateTime={run.created_at}>{new Date(run.created_at).toLocaleString(locale)}</time></td></tr>)}</tbody></table>)}
    <HistoryPages prefix="runs_" after={query.get("after") ?? ""} next={read.data?.next_after} more={read.data?.has_more} loading={read.loading} />
  </section>;
}
export function EvaluationHistory({ group }: { group: string }) {
  const tr = useT(); const search = useSearchParams(); const after = search.get("evaluations_after") ?? "";
  const read = useRead<Page<Evaluation>>(`/groups/${encodeURIComponent(group)}/evaluations?${new URLSearchParams({ limit: "25", after })}`);
  return <section className="card execution-history" aria-labelledby="evaluations-heading"><div className="section-heading"><h2 id="evaluations-heading">{tr("Evaluation history")}</h2><button onClick={read.refresh}>{tr("Refresh")}</button></div>
    {read.loading && <p role="status">{tr("Loading evaluations…")}</p>}
    {read.status && <div role="alert"><p>{tr("Evaluations are unavailable. Retry when the service is restored.")}</p><button onClick={read.refresh}>{tr("Retry")}</button></div>}
    {read.data && (!read.data.items.length ? <p role="status">{tr("No evaluations yet.")}</p> : <ul>{read.data.items.map(job => <li key={job.id}><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/evaluations/${encodeURIComponent(job.id)}/${evolutionContextQuery(search)}`}>{job.id}</Link> · {tr(job.state)} · {job.profile.ref?.id ?? "—"}</li>)}</ul>)}
    <HistoryPages prefix="evaluations_" after={after} next={read.data?.next_after} more={read.data?.has_more} loading={read.loading} />
  </section>;
}
