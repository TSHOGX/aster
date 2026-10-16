"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { base } from "../lib/api";
import { useT } from "../lib/i18n";

type Budget = { max_tokens: number; max_cost_micros: number; max_attempts: number; timeout_seconds: number; max_artifact_bytes: number };
type Graph = { nodes: { id: string }[] } & Record<string, unknown>;
type Preview = { eligible: boolean; checkpoint: { id: string }; dependencies: { id: string }[]; reused: { call: { id: string; instance: string } }[]; reasons?: string[] };
type Request = { id: string; source_run_id: string; checkpoint_id: string; start_node: string; workflow: Graph; budget: Budget };

export function RunExperiment({ group, run, workflow, budget, enabled }: { group: string; run: string; workflow: string; budget: Budget; enabled: boolean }) {
  const tr = useT(); const router = useRouter();
  const [open, setOpen] = useState(false); const [busy, setBusy] = useState(false); const [text, setText] = useState(""); const [start, setStart] = useState("");
  const [tokens, setTokens] = useState(budget.max_tokens); const [cost, setCost] = useState(budget.max_cost_micros);
  const [preview, setPreview] = useState<Preview>(); const [request, setRequest] = useState<Request>(); const [error, setError] = useState("");
  const root = `${base}/api/v1/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(run)}`;
  let graph: Graph | undefined;
  try { const value = JSON.parse(text) as Graph; if (Array.isArray(value.nodes)) graph = value; } catch { /* Editing JSON remains local until preview. */ }
  async function session() { const response = await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" }); if (!response.ok) throw new Error(); return await response.json() as { subject: string; csrf: string }; }
  const key = (subject: string) => `evaluation-experiment:${subject}:${group}:${run}`;
  async function load() {
    setOpen(true); setBusy(true); setError(""); setPreview(undefined);
    try {
      const identity = await session(); const saved = sessionStorage.getItem(key(identity.subject));
      if (saved) {
        const prior = JSON.parse(saved) as Request;
        if (prior.source_run_id !== run || !prior.id || !prior.workflow?.nodes || !prior.budget) throw new Error();
        setRequest(prior); setText(JSON.stringify(prior.workflow, null, 2)); setStart(prior.start_node); setTokens(prior.budget.max_tokens); setCost(prior.budget.max_cost_micros); return;
      }
      const response = await fetch(`${base}/api/v1/groups/${encodeURIComponent(group)}/workflows/${encodeURIComponent(workflow)}/draft`, { credentials: "same-origin", cache: "no-store" });
      if (!response.ok) throw new Error(); const draft = await response.json() as { workflow: Graph };
      setText(JSON.stringify(draft.workflow, null, 2)); setStart(draft.workflow.nodes[0]?.id ?? "");
    } catch { setError("Experiment creation is unavailable or the checkpoint is no longer eligible."); } finally { setBusy(false); }
  }
  async function inspect() {
    if (!graph || !start) return; setBusy(true); setError(""); setPreview(undefined);
    try {
      const identity = await session(); const candidate = { source_run_id: run, start_node: start, workflow: graph, budget: { ...budget, max_tokens: tokens, max_cost_micros: cost } };
      const response = await fetch(`${root}/experiment-preview`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": identity.csrf }, body: JSON.stringify(candidate) });
      if (!response.ok) throw new Error(); const next = await response.json() as Preview; setPreview(next);
    } catch { setError("Experiment creation is unavailable or the checkpoint is no longer eligible."); } finally { setBusy(false); }
  }
  async function create() {
    if (!request && (!graph || !preview?.eligible)) return; setBusy(true); setError("");
    try {
      const identity = await session();
      const pending = request ?? { id: crypto.randomUUID(), source_run_id: run, checkpoint_id: preview!.checkpoint.id, start_node: start, workflow: graph!, budget: { ...budget, max_tokens: tokens, max_cost_micros: cost } };
      setRequest(pending); sessionStorage.setItem(key(identity.subject), JSON.stringify(pending));
      const response = await fetch(`${root}/experiment`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": identity.csrf }, body: JSON.stringify(pending) });
      if (!response.ok) throw new Error(); const created = await response.json() as { id: string };
      sessionStorage.removeItem(key(identity.subject)); router.push(`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(created.id)}`);
    } catch { setError("Experiment creation is unavailable or the checkpoint is no longer eligible."); } finally { setBusy(false); }
  }
  if (!open) return <button disabled={!enabled || busy} onClick={() => void load()}>{tr("Run from here")}</button>;
  return <section className="card experiment-form" aria-label={tr("Checkpoint experiment")}><h3>{tr("Checkpoint experiment")}</h3>
    <p>{tr("Choose a start node and preview the exact upstream outputs to reuse. The source remains unchanged.")}</p>
    <Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/workflows/${encodeURIComponent(workflow)}`}>{tr("Open workflow editor")}</Link>
    <fieldset disabled={busy || !!request}><label>{tr("Frozen experiment workflow")}<textarea aria-label={tr("Frozen experiment workflow")} rows={10} value={text} onChange={event => { setText(event.target.value); setPreview(undefined); }} /></label>
      <label>{tr("Start node")}<select aria-label={tr("Start node")} value={start} onChange={event => { setStart(event.target.value); setPreview(undefined); }}><option value="">{tr("Choose a node")}</option>{graph?.nodes.map(node => <option key={node.id} value={node.id}>{node.id}</option>)}</select></label>
      <label>{tr("Token budget")}<input aria-label={tr("Token budget")} type="number" min={1} max={budget.max_tokens} value={tokens} onChange={event => { setTokens(Number(event.target.value)); setPreview(undefined); }} /></label>
      <label>{tr("Cost budget (micros)")}<input aria-label={tr("Cost budget (micros)")} type="number" min={0} max={budget.max_cost_micros} value={cost} onChange={event => { setCost(Number(event.target.value)); setPreview(undefined); }} /></label>
      <button disabled={!graph || !start || !enabled} onClick={() => void inspect()}>{tr("Preview checkpoint reuse")}</button>
    </fieldset>
    {preview && <div role="status"><p>{tr(preview.eligible ? "Eligible dependencies" : "Experiment unavailable")}</p><ul>{preview.reused.map(record => <li key={record.call.id}>{record.call.instance} · {tr("Reused from checkpoint")}</li>)}</ul>{preview.eligible && !preview.reused.length && <p>{tr("No upstream outputs will be reused.")}</p>}<ul>{preview.dependencies.map(item => <li key={item.id}><code>{item.id}</code></li>)}</ul>{preview.reasons?.map(reason => <p key={reason}>{reason}</p>)}</div>}
    {request && <p role="status">{tr("An experiment request is pending. Retry recovers the same experiment.")}</p>}
    {error && <p role="alert">{tr(error)}</p>}
    <button disabled={busy || (!request && (!enabled || !preview?.eligible))} onClick={() => void create()}>{tr(request ? "Recover experiment" : "Create experiment")}</button>
    <button disabled={busy} onClick={() => setOpen(false)}>{tr("Close")}</button>
  </section>;
}
