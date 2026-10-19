"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { evolutionSectionPath } from "../../../web/application-paths";
import { intlLocale } from "../../../web/i18n/locale";
import { useCallback, useEffect, useRef, useState } from "react";
import { base, type GroupPage } from "../lib/api";
import { useRead } from "../lib/use-read";
import { useT } from "../lib/i18n";

type Job = { id: string; state: string; policy_mode: "report_only" | "required"; fence: number; profile: { ref?: { id?: string; revision?: number }; thresholds?: { name: string; unit: string; operator: string; value: number; minimum_samples: number; minimum_coverage: number }[] }; subject: { candidate?: { library_id?: string; commit_id?: string; tree_digest?: string }; reference?: { commit_id?: string } }; created_at: string };
type Completion = { result: { state: string; outcome: string; subject: { dataset?: { id?: string; digest?: string }; scorer?: { id?: string; revision?: number }; runtime?: { id?: string; revision?: number }; model?: { id?: string; revision?: number }; tools?: { id?: string; revision?: number }[] }; metrics: { name: string; unit: string; value: number; samples: number; coverage: number }[]; reports: { id: string; size: number; digest?: string }[]; reproducibility_limit?: string }; usage: { spent: { tokens: number; cost_micros: number }; held: { tokens: number; cost_micros: number } } };

export function EvaluationReport({ group, job }: { group: string; job: string }) {
  return <Report key={`${group}:${job}`} group={group} id={job} />;
}

function Report({ group, id }: { group: string; id: string }) {
  const tr = useT();
  const search = useSearchParams();
  const access = useRead<GroupPage>(`/groups/${encodeURIComponent(group)}`);
  const canRun = access.data?.items[0]?.can_run === true;
  const [job, setJob] = useState<Job>();
  const [result, setResult] = useState<Completion>();
  const [resultState, setResultState] = useState<"pending" | "ready" | "unavailable">("pending");
  const [error, setError] = useState("");
  const [cancelling, setCancelling] = useState(false);
  const activeRead = useRef<AbortController | null>(null);
  const root = `${base}/api/v1/groups/${encodeURIComponent(group)}/evaluations/${encodeURIComponent(id)}`;
  const read = useCallback(async () => {
    activeRead.current?.abort();
    const controller = new AbortController();
    activeRead.current = controller;
    const options = { credentials: "same-origin", cache: "no-store", signal: controller.signal } as const;
    // Drop settled evidence during revalidation. An old response must never
    // restore it after a newer denial or navigation to another job.
    setResult(undefined);
    try {
      const response = await fetch(root, options);
      if (!response.ok) throw new Error();
      const current = await response.json() as Job;
      if (controller.signal.aborted) return;
      setJob(current);
      const report = await fetch(`${root}/result`, options);
      const completion = report.ok ? await report.json() as Completion : undefined;
      if (controller.signal.aborted) return;
      setResult(completion);
      setResultState(completion ? "ready" : report.status === 404 && ["queued", "running", "cancelling"].includes(current.state) ? "pending" : "unavailable");
      setError("");
    } catch {
      if (controller.signal.aborted) return;
      setJob(undefined);
      setResult(undefined);
      setResultState("unavailable");
      setError("Evaluation report is unavailable. Retry or check group access.");
    }
  }, [root]);
  useEffect(() => {
    // Initial read and interval are external projection synchronization.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void read();
    const timer = window.setInterval(() => void read(), 4000);
    return () => { window.clearInterval(timer); activeRead.current?.abort(); };
  }, [read]);
  async function cancel() {
    if (!canRun || cancelling) return;
    setCancelling(true); setError("");
    try {
      const response = await fetch(`${base}/api/v1/session`, { credentials: "same-origin" });
      if (!response.ok) throw new Error();
      const session = await response.json() as { csrf: string };
      const cancelled = await fetch(`${root}/cancel`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf } });
      if (!cancelled.ok) throw new Error();
      await read();
    } catch { setError("Evaluation cancellation failed. Refresh and check run permissions before retrying."); }
    finally { setCancelling(false); }
  }
  if (!job && error) return <div className="session-gate"><h1>{tr("Evaluation report")}</h1><p role="alert">{tr(error)}</p><button onClick={() => void read()}>{tr("Retry")}</button><Link href={evolutionSectionPath(group, "evaluations", search)}>{tr("Back to evaluations")}</Link></div>;
  return <div className="editor-page"><header className="section-heading"><div><Link href={evolutionSectionPath(group, "evaluations", search)}>{tr("Back to evaluations")}</Link><h1>{tr("Evaluation report")}: {id}</h1><p>{tr("Reports identify the exact candidate, dataset and evaluator conditions.")}</p></div><button onClick={() => void read()}>{tr("Refresh")}</button></header>{error && <p role="alert">{tr(error)}</p>}{job && <><section className="card"><h2>{tr("Evaluation status")}: {tr(job.state)}</h2><dl><div><dt>{tr("Policy mode")}</dt><dd>{job.policy_mode ? tr(job.policy_mode) : "—"}</dd></div><div><dt>{tr("Profile")}</dt><dd>{job.profile.ref?.id ?? "—"} @{job.profile.ref?.revision ?? "—"}</dd></div><div><dt>{tr("Candidate tree")}</dt><dd>{job.subject.candidate?.tree_digest ?? "—"}</dd></div><div><dt>{tr("Reference")}</dt><dd>{job.subject.reference?.commit_id ?? tr("None")}</dd></div><div><dt>{tr("Created")}</dt><dd>{new Date(job.created_at).toLocaleString(intlLocale())}</dd></div></dl>{job.subject.candidate?.library_id && job.subject.candidate.commit_id && <p><a href={`/volumes-studio/libraries/${encodeURIComponent(job.subject.candidate.library_id)}/?${new URLSearchParams({ view: "history", history_head: job.subject.candidate.commit_id })}`}>{tr("View candidate in Volume Studio")}</a></p>}{["queued", "running", "cancelling"].includes(job.state) && <button disabled={cancelling || !canRun} onClick={() => void cancel()}>{tr("Cancel evaluation")}</button>}</section>{result ? <section className="card"><h2>{tr("Outcome")}: {tr(result.result.outcome)}</h2><p>{tr("The outcome is derived from observed metrics and coverage.")}</p><h3>{tr("Frozen evaluator conditions")}</h3><dl><div><dt>{tr("Dataset")}</dt><dd>{result.result.subject.dataset?.id ?? "—"} · {result.result.subject.dataset?.digest ?? tr("Redacted or unavailable")}</dd></div><div><dt>{tr("Scorer")}</dt><dd>{result.result.subject.scorer?.id ?? "—"} @{result.result.subject.scorer?.revision ?? "—"}</dd></div><div><dt>{tr("Runtime")}</dt><dd>{result.result.subject.runtime?.id ?? "—"} @{result.result.subject.runtime?.revision ?? "—"}</dd></div><div><dt>{tr("Model")}</dt><dd>{result.result.subject.model?.id ?? "—"} @{result.result.subject.model?.revision ?? "—"}</dd></div></dl>{result.result.reproducibility_limit && <p role="status"><strong>{tr("Reproducibility limit")}</strong>: {result.result.reproducibility_limit}</p>}<p role="status">{tr("Held-out answers never appear in reports or prompts; only retained report artifacts are downloadable.")}</p><h3>{tr("Metrics")}</h3><table><thead><tr><th>{tr("Name")}</th><th>{tr("Value")}</th><th>{tr("Samples")}</th><th>{tr("Coverage")}</th></tr></thead><tbody>{result.result.metrics.map(metric => <tr key={metric.name}><td>{metric.name}</td><td>{metric.value} {metric.unit}</td><td>{metric.samples}</td><td>{Math.round(metric.coverage * 100)}%</td></tr>)}</tbody></table><h3>{tr("Report artifacts")}</h3>{result.result.reports.length ? <ul>{result.result.reports.map(report => <li key={report.id}><code>{report.id}</code> · {report.size} bytes{report.digest ? ` · ${report.digest}` : ""} <a href={`${root}/reports/${encodeURIComponent(report.id)}?max_bytes=1048576`}>{tr("Open bounded report")}</a></li>)}</ul> : <p>{tr("No report artifacts were retained.")}</p>}</section> : <p role={resultState === "unavailable" ? "alert" : "status"}>{tr(resultState === "unavailable" ? "Settled report is temporarily unavailable. Retry after the evaluator or service recovers." : "No settled report yet. The evaluator may still be running.")}</p>}</>}
  </div>;
}
