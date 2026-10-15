/* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps -- effects synchronize remote drafts and bounded account-local recovery. */
"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { base, type Session } from "../lib/api";
import { useT } from "../lib/i18n";

type JSONMap = Record<string, unknown>;
type Workflow = JSONMap & { id?: string; nodes?: JSONMap[]; edges?: JSONMap[] };
type Draft = { id: string; revision: number; layout_revision: number; workflow: Workflow; layout: JSONMap };
type Snapshot = { workflow: string; layout: string };

const nodeKinds = ["agent", "transform", "validator", "router", "join", "map", "subgraph", "loop"];
const recoveryKey = (group: string, workflow: string) => `evaluation-space:draft:${group}:${workflow}`;

function parseObject(value: string): JSONMap | undefined {
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as JSONMap : undefined;
  } catch { return undefined; }
}
function pretty(value: unknown): string { return JSON.stringify(value, null, 2); }
function nodeList(workflow: Workflow): JSONMap[] { return Array.isArray(workflow.nodes) ? workflow.nodes.filter(node => node && typeof node === "object") : []; }
async function digestText(value: string): Promise<string> {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return `sha256:${Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, "0")).join("")}`;
}

export function WorkflowEditor({ group, workflow }: { group: string; workflow: string }) {
  const tr = useT();
  const [session, setSession] = useState<Session>();
  const [draft, setDraft] = useState<Draft>();
  const [text, setText] = useState("");
  const [layout, setLayout] = useState("{}");
  const [selected, setSelected] = useState("");
  const [promptDraft, setPromptDraft] = useState("");
  const [jsonDrafts, setJSONDrafts] = useState<{ tools: string; input_ports: string; output_ports: string }>({ tools: "[]", input_ports: "{}", output_ports: "{}" });
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const [statusKey, setStatusKey] = useState("");
  const [errorKey, setErrorKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [past, setPast] = useState<Snapshot[]>([]);
  const [future, setFuture] = useState<Snapshot[]>([]);
  const [dirty, setDirty] = useState(false);
  const [showConfig, setShowConfig] = useState(true);
  const [digest, setDigest] = useState("");
  const path = `/api/v1/groups/${encodeURIComponent(group)}/workflows/${encodeURIComponent(workflow)}/draft`;
  const parsed = useMemo(() => parseObject(text) as Workflow | undefined, [text]);
  const nodes = useMemo(() => parsed ? nodeList(parsed) : [], [parsed]);
  const selectedNode = nodes.find(node => String(node.id ?? "") === selected);

  function remember() { setPast(items => [...items.slice(-19), { workflow: text, layout }]); setFuture([]); setDirty(true); setStatusKey(""); }
  function replaceWorkflow(next: Workflow) { remember(); setText(pretty(next)); }
  function updateNode(mutator: (node: JSONMap) => void) {
    if (!parsed || !selectedNode) return;
    const next = structuredClone(parsed) as Workflow;
    const node = nodeList(next).find(item => String(item.id ?? "") === selected);
    if (!node) return;
    mutator(node); replaceWorkflow(next);
  }
  function selectNode(id: string) {
    setSelected(id);
    const node = nodes.find(item => String(item.id ?? "") === id);
    setPromptDraft(String((node?.prompt as JSONMap | undefined)?.text ?? ""));
    setJSONDrafts({ tools: pretty(node?.tools ?? []), input_ports: pretty(node?.input_ports ?? {}), output_ports: pretty(node?.output_ports ?? {}) });
  }
  function updateJSONField(field: "input_ports" | "output_ports" | "tools", value: string) {
    const parsedValue = field === "tools" ? (() => { try { const result: unknown = JSON.parse(value); return Array.isArray(result) ? result : undefined; } catch { return undefined; } })() : parseObject(value);
    if (parsedValue === undefined) return;
    updateNode(node => { node[field] = parsedValue; });
  }
  async function refreshPromptDigest(value: string) {
    if (!parsed || !selectedNode) return;
    const digest = await digestText(value);
    const next = structuredClone(parsed) as Workflow;
    const node = nodeList(next).find(item => String(item.id ?? "") === selected);
    if (!node) return;
    const prompt = (node.prompt && typeof node.prompt === "object" ? node.prompt : {}) as JSONMap;
    prompt.text = value; prompt.digest = digest; node.prompt = prompt;
    replaceWorkflow(next);
  }
  function addNode(kind: string) {
    if (!parsed) return;
    const next = structuredClone(parsed) as Workflow;
    const existing = nodeList(next); const baseID = `${kind}-node`; let id = baseID; let number = 2;
    while (existing.some(node => node.id === id)) id = `${baseID}-${number++}`;
    const node: JSONMap = { id, kind, input_ports: {}, output_ports: {}, max_attempts: 1, timeout_seconds: 60 };
    if (kind === "agent") node.prompt = { text: "", variables: [] };
    if (kind === "router") { node.routes = []; node.default_route = ""; node.predicate = ""; }
    if (kind === "join") node.inactive_inputs = "skip";
    if (kind === "map") { node.item_inputs = {}; node.item_outputs = {}; node.max_items = 1; node.concurrency = 1; node.failure_mode = "fail_fast"; }
    if (kind === "loop") { node.max_iterations = 1; node.on_limit = "stop"; node.predicate = ""; }
    next.nodes = [...existing, node]; replaceWorkflow(next); setSelected(id); setStatusKey("Node added to the draft.");
  }
  function connect() {
    if (!parsed || !source || !target) return;
    const next = structuredClone(parsed) as Workflow;
    const edges = Array.isArray(next.edges) ? next.edges : [];
    if (edges.some(edge => String(edge.from) === source && String(edge.to) === target)) {
      setErrorKey("That connection already exists.");
      return;
    }
    next.edges = [...edges, { from: source, to: target }];
    replaceWorkflow(next); setSource(""); setTarget(""); setStatusKey("Connection added to the draft.");
  }
  function removeEdge(index: number) {
    if (!parsed || !Array.isArray(parsed.edges)) return;
    const next = structuredClone(parsed) as Workflow;
    const edges = Array.isArray(next.edges) ? next.edges : [];
    next.edges = edges.filter((_, edgeIndex) => edgeIndex !== index);
    replaceWorkflow(next);
    setStatusKey("Connection removed from the draft.");
  }
  function undo() {
    const previous = past.at(-1); if (!previous) return;
    setFuture(items => [...items, { workflow: text, layout }]); setPast(items => items.slice(0, -1)); setText(previous.workflow); setLayout(previous.layout); setDirty(true);
  }
  function redo() {
    const next = future.at(-1); if (!next) return;
    setPast(items => [...items, { workflow: text, layout }]); setFuture(items => items.slice(0, -1)); setText(next.workflow); setLayout(next.layout); setDirty(true);
  }
  async function load() {
    setErrorKey("");
    try {
      const sessionResponse = await fetch(`${base}/api/v1/session`, { credentials: "same-origin" });
      if (!sessionResponse.ok) throw new Error("session");
      const current = await sessionResponse.json() as Session; setSession(current);
      const response = await fetch(`${base}${path}`, { credentials: "same-origin", cache: "no-store" });
      if (!response.ok) throw new Error(String(response.status));
      const value = await response.json() as Draft;
      setDraft(value); setText(pretty(value.workflow)); setLayout(pretty(value.layout)); setDirty(false); setPast([]); setFuture([]); setDigest("");
      if (window.sessionStorage.getItem(recoveryKey(group, workflow))) setStatusKey("A local recovery draft is available. Review it before restoring.");
    } catch { setErrorKey("Workflow draft is unavailable. Retry or ask a workflow editor."); }
  }
  useEffect(() => { void load(); }, [group, workflow]);
  useEffect(() => {
    const handler = (event: BeforeUnloadEvent) => { if (dirty) { event.preventDefault(); event.returnValue = ""; } };
    window.addEventListener("beforeunload", handler); return () => window.removeEventListener("beforeunload", handler);
  }, [dirty]);
  function recover() {
    const raw = window.sessionStorage.getItem(recoveryKey(group, workflow)); const value = raw ? parseObject(raw) : undefined;
    if (value && typeof value.workflow === "object" && typeof value.layout === "object") { remember(); setText(pretty(value.workflow)); setLayout(pretty(value.layout)); setStatusKey("Recovered local draft. Save it when ready."); }
  }
  function saveRecovery() { window.sessionStorage.setItem(recoveryKey(group, workflow), JSON.stringify({ workflow: parsed, layout: parseObject(layout) ?? {} })); setStatusKey("Local recovery draft saved."); }
  async function save() {
    if (!session || !draft || !parsed) { setErrorKey("Workflow and layout must be valid JSON."); return; }
    setBusy(true); setErrorKey(""); setStatusKey("");
    try {
      const response = await fetch(`${base}${path}`, { method: "PUT", credentials: "same-origin", headers: { "Content-Type": "application/json", Origin: location.origin, "X-CSRF-Token": session.csrf }, body: JSON.stringify({ id: workflow, expected_revision: draft.revision, expected_layout_revision: draft.layout_revision, workflow: parsed, layout: parseObject(layout) ?? {} }) });
      if (!response.ok) { if (response.status === 409) throw new Error("conflict"); throw new Error("invalid"); }
      const value = await response.json() as Draft; setDraft(value); setText(pretty(value.workflow)); setLayout(pretty(value.layout)); setDirty(false); setDigest(""); window.sessionStorage.removeItem(recoveryKey(group, workflow)); setStatusKey("Draft saved.");
    } catch (error) { setErrorKey(error instanceof Error && error.message === "conflict" ? "The draft changed elsewhere. Reload before saving." : "Workflow validation failed. Check the field errors and graph connections."); }
    finally { setBusy(false); }
  }
  async function publish() {
    if (!session || !draft || dirty) { setErrorKey("Publishing requires workflow-edit permission and a valid saved draft."); return; }
    setBusy(true); setErrorKey("");
    try {
      const response = await fetch(`${base}${path}/publish`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Origin: location.origin, "X-CSRF-Token": session.csrf }, body: JSON.stringify({ revision: draft.revision }) });
      if (!response.ok) throw new Error("publish"); setStatusKey("Published immutable workflow revision.");
    } catch { setErrorKey("Publishing requires workflow-edit permission and a valid saved draft."); }
    finally { setBusy(false); }
  }
  async function validateDraft(test: boolean) {
    if (!session || !parsed) { setErrorKey("Workflow and layout must be valid JSON."); return; }
    setBusy(true); setErrorKey(""); setStatusKey("");
    try {
      const response = await fetch(`${base}${path}/${test ? "test" : "validate"}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Origin: location.origin, "X-CSRF-Token": session.csrf }, body: JSON.stringify({ workflow: parsed }) });
      if (!response.ok) { const detail = await response.json().catch(() => ({})) as { detail?: string }; throw new Error(detail.detail || "Workflow validation failed. Check the field errors and graph connections."); }
      const result = await response.json().catch(() => ({})) as { digest?: string };
      setDigest(result.digest ?? "");
      setStatusKey(test ? "Draft test passed without paid execution." : "Draft validation passed.");
    } catch (error) { setErrorKey(error instanceof Error ? error.message : "Workflow validation failed. Check the field errors and graph connections."); }
    finally { setBusy(false); }
  }
  if (errorKey && !draft) return <main className="session-gate"><h1>{tr("Workflow editor")}</h1><p role="alert">{tr(errorKey)}</p><button onClick={() => void load()}>{tr("Retry")}</button><Link href={`/groups/${encodeURIComponent(group)}/`}>{tr("Back to group")}</Link></main>;
  return <main className="editor-page">
    <header className="section-heading"><div><Link href={`/groups/${encodeURIComponent(group)}/`}>{tr("Back to group")}</Link><h1>{tr("Workflow editor")}: {workflow}</h1><p>{tr("Draft, published revision and group activation are separate steps.")}</p></div><div className="editor-actions"><button disabled={!past.length} onClick={undo}>{tr("Undo")}</button><button disabled={!future.length} onClick={redo}>{tr("Redo")}</button><button onClick={saveRecovery}>{tr("Save recovery")}</button><button disabled={busy || !draft || !parsed} onClick={() => void save()}>{tr("Save draft")}</button><button disabled={busy || !draft || dirty} onClick={() => void publish()}>{tr("Publish revision")}</button><a className="button" href={`/admin/groups/${encodeURIComponent(group)}/optimization`}>{tr("Activate from configuration")}</a></div></header>
    {statusKey && <p role="status">{tr(statusKey)}</p>}{errorKey && <p role="alert">{tr(errorKey)}</p>}
    {statusKey === "A local recovery draft is available. Review it before restoring." && <button onClick={recover}>{tr("Restore recovery")}</button>}
    <div className="workbench-toolbar"><button onClick={() => setShowConfig(value => !value)} aria-expanded={showConfig}>{tr(showConfig ? "Hide configuration" : "Show configuration")}</button><span>{tr("Draft revision")} {draft?.revision ?? "—"} · {tr("Layout revision")} {draft?.layout_revision ?? "—"}</span><span>{dirty ? tr("Unsaved changes") : tr("Saved")}</span>{digest && <span role="status">{tr("Executable digest")}: <code>{digest}</code></span>}</div>
    <div className="workbench-grid">
      <aside className="palette card"><h2>{tr("Node palette")}</h2><p>{tr("Keyboard alternative: add a node, then edit it in the inspector.")}</p>{nodeKinds.map(kind => <button key={kind} onClick={() => addNode(kind)}>{tr("Add {kind}", { kind })}</button>)}<h2>{tr("Connect ports")}</h2><label>{tr("From node")}<select value={source} onChange={event => setSource(event.target.value)}><option value="">{tr("Choose source")}</option>{nodes.map(node => <option key={String(node.id)} value={`${String(node.id)}.${Object.keys((node.output_ports as JSONMap | undefined) ?? {})[0] ?? "output"}`}>{String(node.id)}</option>)}</select></label><label>{tr("To node")}<select value={target} onChange={event => setTarget(event.target.value)}><option value="">{tr("Choose target")}</option>{nodes.map(node => <option key={String(node.id)} value={`${String(node.id)}.${Object.keys((node.input_ports as JSONMap | undefined) ?? {})[0] ?? "input"}`}>{String(node.id)}</option>)}</select></label><button disabled={!source || !target} onClick={connect}>{tr("Connect")}</button></aside>
      <section className="canvas card" aria-label={tr("Workflow canvas")}><h2>{tr("Workflow canvas")}</h2>{!nodes.length && <p>{tr("No nodes yet. Use the keyboard node palette to start.")}</p>}<ol className="node-list">{nodes.map(node => <li key={String(node.id)}><button className={selected === String(node.id) ? "selected-node" : ""} onClick={() => selectNode(String(node.id))} aria-pressed={selected === String(node.id)}><strong>{String(node.id)}</strong><span>{tr(String(node.kind ?? "unknown"))}</span><small>{tr("Inputs")}: {Object.keys((node.input_ports as JSONMap | undefined) ?? {}).length} · {tr("Outputs")}: {Object.keys((node.output_ports as JSONMap | undefined) ?? {}).length}</small></button></li>)}</ol><h3>{tr("Connections")}</h3>{(Array.isArray(parsed?.edges) ? parsed.edges : []).length ? <ul>{(parsed?.edges as JSONMap[]).map((edge, index) => <li key={`${String(edge.from)}-${String(edge.to)}-${index}`}><code>{String(edge.from)} → {String(edge.to)}</code><button type="button" onClick={() => removeEdge(index)} aria-label={`${tr("Remove connection")} ${String(edge.from)} → ${String(edge.to)}`}>{tr("Remove")}</button></li>)}</ul> : <p>{tr("No connections yet.")}</p>}<div className="editor-actions"><button disabled={busy} onClick={() => void validateDraft(false)}>{tr("Validate draft")}</button><button disabled={busy} onClick={() => void validateDraft(true)}>{tr("Test draft")}</button></div></section>
      <aside className="inspector card"><h2>{tr("Node inspector")}</h2>{selectedNode ? <><p><strong>{String(selectedNode.id)}</strong> · {tr(String(selectedNode.kind ?? "unknown"))}</p><label>{tr("Node ID")}<input value={String(selectedNode.id ?? "")} onChange={event => { const nextID = event.target.value; updateNode(node => { node.id = nextID; }); setSelected(nextID); }} /></label><label>{tr("Kind")}<select value={String(selectedNode.kind ?? "")} onChange={event => updateNode(node => { node.kind = event.target.value; })}>{nodeKinds.map(kind => <option key={kind} value={kind}>{tr(kind)}</option>)}</select></label><label>{tr("Prompt text")}<textarea value={promptDraft} onChange={event => setPromptDraft(event.target.value)} onBlur={() => void refreshPromptDigest(promptDraft)} /></label><label>{tr("Prompt variables")}<input value={Array.isArray((selectedNode.prompt as JSONMap | undefined)?.variables) ? ((selectedNode.prompt as JSONMap).variables as unknown[]).join(", ") : ""} onChange={event => updateNode(node => { const prompt = (node.prompt && typeof node.prompt === "object" ? node.prompt : {}) as JSONMap; prompt.variables = event.target.value.split(",").map(value => value.trim()).filter(Boolean); node.prompt = prompt; })} /></label><label>{tr("Model revision")}<input value={String((selectedNode.model_ref as JSONMap | undefined)?.id ?? "")} onChange={event => updateNode(node => { const model = (node.model_ref && typeof node.model_ref === "object" ? node.model_ref : {}) as JSONMap; model.id = event.target.value; node.model_ref = model; })} /></label><label>{tr("Tools JSON")}<textarea value={jsonDrafts.tools} onChange={event => setJSONDrafts(value => ({ ...value, tools: event.target.value }))} onBlur={() => updateJSONField("tools", jsonDrafts.tools)} spellCheck={false} /></label><label>{tr("Input ports JSON")}<textarea value={jsonDrafts.input_ports} onChange={event => setJSONDrafts(value => ({ ...value, input_ports: event.target.value }))} onBlur={() => updateJSONField("input_ports", jsonDrafts.input_ports)} spellCheck={false} /></label><label>{tr("Output ports JSON")}<textarea value={jsonDrafts.output_ports} onChange={event => setJSONDrafts(value => ({ ...value, output_ports: event.target.value }))} onBlur={() => updateJSONField("output_ports", jsonDrafts.output_ports)} spellCheck={false} /></label><label>{tr("Max attempts")}<input type="number" min="1" value={Number(selectedNode.max_attempts ?? 1)} onChange={event => updateNode(node => { node.max_attempts = Number(event.target.value); })} /></label><button onClick={() => { if (!parsed) return; const next = structuredClone(parsed) as Workflow; next.nodes = nodeList(next).filter(node => String(node.id ?? "") !== selected); next.edges = (Array.isArray(next.edges) ? next.edges : []).filter(edge => !String(edge.from).startsWith(`${selected}.`) && !String(edge.to).startsWith(`${selected}.`)); replaceWorkflow(next); setSelected(""); }}>{tr("Remove node")}</button></> : <p>{tr("Select a node to inspect inputs, prompt, model and limits.")}</p>}</aside>
    </div>
    {showConfig && <section className="editor-grid config-editors"><label>{tr("Executable workflow JSON")}<textarea aria-label={tr("Executable workflow JSON")} value={text} onChange={event => { setText(event.target.value); setDirty(true); setDigest(""); }} spellCheck={false} /></label><label>{tr("Canvas layout JSON")}<textarea aria-label={tr("Canvas layout JSON")} value={layout} onChange={event => { setLayout(event.target.value); setDirty(true); }} spellCheck={false} /></label></section>}
  </main>;
}
