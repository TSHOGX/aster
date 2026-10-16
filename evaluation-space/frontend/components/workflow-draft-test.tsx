"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { base, type Group, type Session } from "../lib/api";
import { useT } from "../lib/i18n";

type Budget = { max_tokens: number; max_cost_micros: number; max_attempts: number; timeout_seconds: number; max_artifact_bytes: number };
type Request = { id: string; draft_id: string; revision: number; source_run_id: string; kind: string; node_id?: string; budget: Budget };
type Source = { id: string; budget: Budget };
type Node = { id: string; kind: string };
const budgetFields: [keyof Budget, string, number][] = [["max_tokens", "Token budget", 1], ["max_cost_micros", "Cost budget (micros)", 0], ["max_attempts", "Max attempts", 1], ["timeout_seconds", "Timeout (seconds)", 1], ["max_artifact_bytes", "Artifact budget (bytes)", 1]];

export function WorkflowDraftTest({ group, draft, revision, nodes, saved }: { group: string; draft: string; revision: number; nodes: Node[]; saved: boolean }) {
 const tr = useT(); const router = useRouter();
 const [open, setOpen] = useState(false); const [busy, setBusy] = useState(false); const [allowed, setAllowed] = useState(false);
 const [error, setError] = useState(""); const [request, setRequest] = useState<Request>();
 const [sources, setSources] = useState<Source[]>([]); const [after, setAfter] = useState(""); const [more, setMore] = useState(false);
 const [source, setSource] = useState(""); const [kind, setKind] = useState("workflow"); const [node, setNode] = useState(""); const [budget, setBudget] = useState<Budget>();
 const root = `${base}/api/v1/groups/${encodeURIComponent(group)}`;
 const key = (identity: Session) => `evaluation-draft-test:${identity.issuer}:${identity.subject}:${group}:${draft}`;
 async function identity() { const response = await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" }); if (!response.ok) throw new Error(); return await response.json() as Session; }
 async function access() {
  const response = await fetch(root, { credentials: "same-origin", cache: "no-store" });
  if (!response.ok) throw new Error(); const data = await response.json() as { items: Group[] }; const current = data.items.find(item => item.id === group);
  if (!current?.can_edit_workflows || !current.can_run || !current.can_read_evidence) throw new Error();
 }
 function clear() { setAllowed(false); setRequest(undefined); setSources([]); setSource(""); setBudget(undefined); setMore(false); }
 async function page(cursor: string) {
  const response = await fetch(`${root}/runs?limit=25&after=${encodeURIComponent(cursor)}`, { credentials: "same-origin", cache: "no-store" });
  if (!response.ok) throw new Error(); const result = await response.json() as { items: Source[]; next_after: string; has_more: boolean };
  setSources(prior => cursor ? [...prior, ...result.items.filter(item => !prior.some(old => old.id === item.id))] : result.items); setAfter(result.next_after); setMore(result.has_more);
 }
 async function load() {
  setOpen(true); setBusy(true); setError(""); clear();
  try {
   const session = await identity(); await access();
   const response = await fetch(`${root}/workflows/${encodeURIComponent(draft)}/draft`, { credentials: "same-origin", cache: "no-store" });
   if (!response.ok) throw new Error();
   const raw = sessionStorage.getItem(key(session));
   if (raw) {
    const pending = JSON.parse(raw) as Request;
    if (pending.draft_id !== draft || !pending.id || !pending.source_run_id || !pending.budget || !["workflow", "node", "subgraph"].includes(pending.kind)) throw new Error();
    setRequest(pending); setSource(pending.source_run_id); setKind(pending.kind); setNode(pending.node_id ?? ""); setBudget(pending.budget);
   }
   await page(""); setAllowed(true);
  } catch { clear(); setError("Draft testing is unavailable. Check workflow, run and source permissions, then retry."); } finally { setBusy(false); }
 }
 async function loadMore() { setBusy(true); setError(""); try { await access(); await page(after); } catch { clear(); setError("Draft testing is unavailable. Check workflow, run and source permissions, then retry."); } finally { setBusy(false); } }
 const selectedSource = sources.find(item => item.id === source);
 const validBudget = budget && selectedSource && budgetFields.every(([field, , min]) => Number.isSafeInteger(budget[field]) && budget[field] >= min && budget[field] <= selectedSource.budget[field]);
 const availableNodes = kind === "subgraph" ? nodes.filter(item => ["subgraph", "map", "loop"].includes(item.kind)) : nodes;
 async function create() {
  if (!allowed || !request && (!saved || !validBudget || kind !== "workflow" && !node)) return;
  setBusy(true); setError("");
  try {
   const session = await identity();
   try { await access(); } catch { clear(); throw new Error(); }
   const pending = request ?? { id: crypto.randomUUID(), draft_id: draft, revision, source_run_id: source, kind, ...(kind === "workflow" ? {} : { node_id: node }), budget: budget! };
   sessionStorage.setItem(key(session), JSON.stringify(pending)); setRequest(pending);
   const response = await fetch(`${root}/workflows/${encodeURIComponent(draft)}/draft/tests`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(pending) });
   if (!response.ok) {
    if ([400, 404, 409, 422].includes(response.status)) { sessionStorage.removeItem(key(session)); setRequest(undefined); setError("Draft test rejected. Check the saved revision, graph, source inputs and budget before trying again."); return; }
    if (response.status === 401 || response.status === 403) clear();
    throw new Error();
   }
   const result = await response.json() as { id: string }; if (result.id !== pending.id) throw new Error();
   sessionStorage.removeItem(key(session)); router.push(`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(result.id)}`);
  } catch { setError("Draft test response is unavailable. Recover the pending request before starting another test."); } finally { setBusy(false); }
 }
 if (!open) return <button disabled={!saved} onClick={() => void load()}>{tr("Test saved draft")}</button>;
 return <section className="card experiment-form" aria-label={tr("Draft test")}><h2>{tr("Draft test")}</h2>
  <p>{tr("Tests use exact source inputs and fresh model calls. Partial tests include required ancestors. Tests do not create Studio branches or production evaluations.")}</p>
  {!saved && <p role="status">{tr("Save all changes before starting a new draft test.")}</p>}
  <fieldset disabled={busy || !allowed || !!request}>
   <label>{tr("Source run")}<select aria-label={tr("Source run")} value={source} onChange={event => { setSource(event.target.value); setBudget(sources.find(item => item.id === event.target.value)?.budget); }}><option value="">{tr("Choose a source run")}</option>{request && !selectedSource && <option value={request.source_run_id}>{request.source_run_id}</option>}{sources.map(item => <option key={item.id} value={item.id}>{item.id}</option>)}</select></label>
   {more && <button onClick={() => void loadMore()}>{tr("Load more source runs")}</button>}
   <label>{tr("Test scope")}<select aria-label={tr("Test scope")} value={kind} onChange={event => { setKind(event.target.value); setNode(""); }}><option value="workflow">{tr("Full workflow")}</option><option value="node">{tr("Single node and dependencies")}</option><option value="subgraph">{tr("Subgraph and dependencies")}</option></select></label>
   {kind !== "workflow" && <label>{tr("Test node")}<select aria-label={tr("Test node")} value={node} onChange={event => setNode(event.target.value)}><option value="">{tr("Choose a node")}</option>{availableNodes.map(item => <option key={item.id} value={item.id}>{item.id}</option>)}</select></label>}
   {budget && budgetFields.map(([field, label, min]) => <label key={field}>{tr(label)}<input aria-label={tr(label)} type="number" min={min} max={selectedSource?.budget[field]} value={budget[field]} onChange={event => setBudget({ ...budget, [field]: Number(event.target.value) })} /></label>)}
  </fieldset>
  {request && <p role="status">{tr("A draft test request is pending. Recovery uses the same saved revision, scope and budget.")} <code>{request.id}</code> · {tr("Draft revision")} {request.revision}</p>}
  {error && <p role="alert">{tr(error)}</p>}
  <button disabled={busy || !allowed || !request && (!saved || !validBudget || kind !== "workflow" && !node)} onClick={() => void create()}>{tr(request ? "Recover draft test" : "Start draft test")}</button>
  {!allowed && <button disabled={busy} onClick={() => void load()}>{tr("Retry")}</button>}
  <button disabled={busy} onClick={() => { setOpen(false); clear(); }}>{tr("Close")}</button>
 </section>;
}
