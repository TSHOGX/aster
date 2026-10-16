/* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps -- effects synchronize remote drafts and bounded account-local recovery. */
"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { base, type Session } from "../lib/api";
import { useT } from "../lib/i18n";
import { WorkflowDraftTest } from "./workflow-draft-test";

type JSONMap = Record<string, unknown>;
type Workflow = JSONMap & { id?: string; nodes?: JSONMap[]; edges?: JSONMap[] };
type Draft = { id: string; revision: number; layout_revision: number; workflow: Workflow; layout: JSONMap };
type FieldName = "tools" | "input_ports" | "output_ports";
type FieldDrafts = Record<string, Partial<Record<FieldName, string>>>;
type FieldError = { path: string; message: string };
type Snapshot = { workflow: string; layout: string; fields?: FieldDrafts; selected?: string; nodeID?: string };

const nodeKinds = ["agent", "transform", "validator", "router", "join", "map", "subgraph", "loop"];
const recoveryKey = (account: string, group: string, workflow: string) => `evaluation-space:draft:${account}:${group}:${workflow}`;

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
 return <Editor key={`${group}:${workflow}`} group={group} workflow={workflow} />;
}
function Editor({ group, workflow }: { group: string; workflow: string }) {
  const tr = useT();
  const [session, setSession] = useState<Session>();
  const [draft, setDraft] = useState<Draft>();
  const [text, setText] = useState("");
  const [layout, setLayout] = useState("{}");
  const [selected, setSelected] = useState("");
  const [nodeID, setNodeID] = useState("");
  const [fieldDrafts, setFieldDrafts] = useState<FieldDrafts>({});
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const [statusKey, setStatusKey] = useState("");
  const [errorKey, setErrorKey] = useState("");
  const editor = useRef<HTMLElement>(null);
  const [validationField, setValidationField] = useState<FieldError>();
  const [focusField, setFocusField] = useState("");
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
  const jsonDrafts = { tools: fieldDrafts[selected]?.tools ?? pretty(selectedNode?.tools ?? []), input_ports: fieldDrafts[selected]?.input_ports ?? pretty(selectedNode?.input_ports ?? {}), output_ports: fieldDrafts[selected]?.output_ports ?? pretty(selectedNode?.output_ports ?? {}) };
  function parseField(field: FieldName, value: string): unknown | undefined {
    if (field !== "tools") return parseObject(value);
    try { const parsed: unknown = JSON.parse(value); return Array.isArray(parsed) ? parsed : undefined; } catch { return undefined; }
  }
  const invalidFields = Object.entries(fieldDrafts).flatMap(([id, fields]) => Object.entries(fields).filter(([field, raw]) => parseField(field as FieldName, raw) === undefined).map(([field]) => `${id}.${field}`));
  const sourcePorts = [...Object.keys((parsed?.inputs as JSONMap) ?? {}).map(port => `$input.${port}`), ...nodes.flatMap(node => Object.keys((node.output_ports as JSONMap) ?? {}).map(port => `${String(node.id)}.${port}`))];
  const targetPorts = [...nodes.flatMap(node => Object.keys((node.input_ports as JSONMap) ?? {}).map(port => `${String(node.id)}.${port}`)), ...Object.keys((parsed?.outputs as JSONMap) ?? {}).map(port => `$output.${port}`)];
  function snapshot(): Snapshot { return { workflow: text, layout, fields: fieldDrafts, selected, nodeID }; }


  function remember() { setValidationField(undefined); setPast(items => [...items.slice(-19), snapshot()]); setFuture([]); setDirty(true); setStatusKey(""); setDigest(""); }
  function replaceWorkflow(next: Workflow) { remember(); setText(pretty(next)); }
  function updateNode(mutator: (node: JSONMap) => void) {
    if (!parsed || !selectedNode) return;
    const next = structuredClone(parsed) as Workflow;
    const node = nodeList(next).find(item => String(item.id ?? "") === selected);
    if (!node) return;
    const oldID = String(node.id); mutator(node); const newID = String(node.id);
    if (oldID !== newID) {
      if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(newID) || nodeList(next).some(item => item !== node && item.id === newID)) { setErrorKey("Node ID must be unique and contain letters, digits, underscores or hyphens."); return; }
      next.edges = (next.edges ?? []).map(edge => ({ ...edge, from: String(edge.from).startsWith(`${oldID}.`) ? `${newID}${String(edge.from).slice(oldID.length)}` : edge.from, to: String(edge.to).startsWith(`${oldID}.`) ? `${newID}${String(edge.to).slice(oldID.length)}` : edge.to }));
      setFieldDrafts(fields => { const changed = { ...fields }; if (fields[oldID]) changed[newID] = fields[oldID]; delete changed[oldID]; return changed; }); setSelected(newID); setNodeID(newID);
    }
    replaceWorkflow(next);
  }
  function selectNode(id: string) { if (selectedNode && nodeID !== selected) { setErrorKey("Apply or reset the node name before changing selection."); return; } setSelected(id); setNodeID(id); }
  function updateJSONField(field: FieldName, value: string) {
    setFieldDrafts(fields => ({ ...fields, [selected]: { ...fields[selected], [field]: value } }));
    const parsedValue = parseField(field, value);
    if (parsedValue === undefined) { remember(); return; }
    updateNode(node => { node[field] = parsedValue; });
  }
  async function preparedWorkflow(): Promise<Workflow> {
    if (!parsed || invalidFields.length || selectedNode && nodeID !== selected) throw new Error("Fix invalid node fields before saving or validating.");
    const next = structuredClone(parsed);
    // Freeze a snapshot before hashing. Editing controls are disabled while this
    // snapshot is prepared and submitted, so a late hash cannot overwrite edits.
    for (const node of nodeList(next)) {
      const prompt = node.prompt as JSONMap | undefined;
      if (prompt && typeof prompt.text === "string") prompt.digest = await digestText(prompt.text);
    }
    return next;
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
    next.nodes = [...existing, node]; replaceWorkflow(next); setSelected(id); setNodeID(id); setStatusKey("Node added to the draft.");
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
    setFuture(items => [...items, snapshot()]); setPast(items => items.slice(0, -1)); setText(previous.workflow); setLayout(previous.layout); setFieldDrafts(previous.fields ?? {}); setSelected(previous.selected ?? ""); setNodeID(previous.nodeID ?? ""); if (previous.workflow !== text) setDigest(""); setDirty(true);
  }
  function redo() {
    const next = future.at(-1); if (!next) return;
    setPast(items => [...items, snapshot()]); setFuture(items => items.slice(0, -1)); setText(next.workflow); setLayout(next.layout); setFieldDrafts(next.fields ?? {}); setSelected(next.selected ?? ""); setNodeID(next.nodeID ?? ""); if (next.workflow !== text) setDigest(""); setDirty(true);
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
      setDraft(value); setText(pretty(value.workflow)); setLayout(pretty(value.layout)); setFieldDrafts({}); setDirty(false); setPast([]); setFuture([]); setDigest("");
      if (window.sessionStorage.getItem(recoveryKey(current.subject, group, workflow))) setStatusKey("A local recovery draft is available. Review it before restoring.");
    } catch { setErrorKey("Workflow draft is unavailable. Retry or ask a workflow editor."); }
  }
  useEffect(() => { void load(); }, [group, workflow]);
  useEffect(() => {
    const handler = (event: BeforeUnloadEvent) => { if (dirty) { event.preventDefault(); event.returnValue = ""; } };
    const navigate = (event: MouseEvent) => {
      const anchor = (event.target as Element | null)?.closest("a");
      if (dirty && anchor && anchor.href !== location.href && !window.confirm(tr("Discard unsaved workflow changes?"))) { event.preventDefault(); event.stopPropagation(); }
    };
    window.addEventListener("beforeunload", handler); document.addEventListener("click", navigate, true);
    return () => { window.removeEventListener("beforeunload", handler); document.removeEventListener("click", navigate, true); };
  }, [dirty]);
  function recover() {
    if (!session) return;
    const raw = window.sessionStorage.getItem(recoveryKey(session.subject, group, workflow)); const value = raw ? parseObject(raw) : undefined;
    if (value && typeof value.workflow === "string" && typeof value.layout === "string") { remember(); setText(value.workflow); setLayout(value.layout); setFieldDrafts((value.fields ?? {}) as FieldDrafts); setSelected(String(value.selected ?? "")); setNodeID(String(value.nodeID ?? "")); setStatusKey("Recovered local draft. Save it when ready."); }
  }
  function saveRecovery() {
    if (!session) return;
    const raw = JSON.stringify(snapshot());
    if (raw.length > 4 * 1024 * 1024) { setErrorKey("Recovery draft exceeds the size limit."); return; }
    window.sessionStorage.setItem(recoveryKey(session.subject, group, workflow), raw); setStatusKey("Local recovery draft saved.");
  }
  async function save() {
    if (!session || !draft || !parsed || !parseObject(layout) || invalidFields.length) { setErrorKey("Workflow and layout must be valid JSON."); return; }
    setBusy(true); setErrorKey(""); setStatusKey("");
    try {
      const prepared = await preparedWorkflow();
      const response = await fetch(`${base}${path}`, { method: "PUT", credentials: "same-origin", headers: { "Content-Type": "application/json", Origin: location.origin, "X-CSRF-Token": session.csrf }, body: JSON.stringify({ id: workflow, expected_revision: draft.revision, expected_layout_revision: draft.layout_revision, workflow: prepared, layout: parseObject(layout) }) });
      if (!response.ok) { if (response.status === 409) throw new Error("conflict"); throw new Error("invalid"); }
      const value = await response.json() as Draft; setDraft(value); setText(pretty(value.workflow)); setLayout(pretty(value.layout)); setDirty(false); setFieldDrafts({}); window.sessionStorage.removeItem(recoveryKey(session?.subject ?? "", group, workflow)); setStatusKey("Draft saved.");
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
  function revealField(field: FieldError, graph: Workflow) {
    setValidationField(field);
    const match = /^nodes\[(\d+)\](?:\.(.*))?$/.exec(field.path);
    const node = match ? nodeList(graph)[Number(match[1])] : nodeList(graph).find(node => field.path.startsWith(`${String(node.id)}.`));
    const known = ["id", "kind", "prompt.text", "prompt.variables", "model_ref.id", "model_ref.revision", "model_ref.digest", "tools", "input_ports", "output_ports", "max_attempts"];
    const suffix = match?.[2] ?? (node ? "input_ports" : "");
    if (node) {
      setSelected(String(node.id)); setNodeID(String(node.id));
      setFocusField(known.find(name => suffix === name || suffix.startsWith(`${name}.`)) ?? "id");
    } else { setShowConfig(true); setFocusField("workflow"); }
  }
  useEffect(() => {
    if (busy || !focusField) return;
    editor.current?.querySelector<HTMLElement>(`[data-field="${focusField}"]`)?.focus();
    setFocusField("");
  }, [busy, focusField, selected, showConfig]);
  async function validateDraft() {
    if (!session || !parsed || invalidFields.length) { setErrorKey("Fix invalid node fields before saving or validating."); return; }
    setBusy(true); setErrorKey(""); setStatusKey("");
    try {
      const prepared = await preparedWorkflow();
      const response = await fetch(`${base}${path}/validate`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Origin: location.origin, "X-CSRF-Token": session.csrf }, body: JSON.stringify({ workflow: prepared }) });
      if (!response.ok) {
        const detail = await response.json().catch(() => ({})) as { fields?: FieldError[] };
        if (detail.fields?.[0]) revealField(detail.fields[0], prepared);
        throw new Error("Workflow validation failed. Check the field errors and graph connections.");
      }
      const result = await response.json().catch(() => ({})) as { digest?: string };
      setText(pretty(prepared)); setDigest(result.digest ?? "");
      setValidationField(undefined); setStatusKey("Draft validation passed.");
    } catch (error) { setErrorKey(error instanceof Error ? error.message : "Workflow validation failed. Check the field errors and graph connections."); }
    finally { setBusy(false); }
  }
  if (errorKey && !draft) return <main className="session-gate"><h1>{tr("Workflow editor")}</h1><p role="alert">{tr(errorKey)}</p><button onClick={() => void load()}>{tr("Retry")}</button><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/`}>{tr("Back to group")}</Link></main>;
  return <main ref={editor} className="editor-page">
    <header className="section-heading"><div><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/`}>{tr("Back to group")}</Link><h1>{tr("Workflow editor")}: {workflow}</h1><p>{tr("Draft, published revision and group activation are separate steps.")}</p></div><div className="editor-actions"><button disabled={busy || !past.length} onClick={undo}>{tr("Undo")}</button><button disabled={busy || !future.length} onClick={redo}>{tr("Redo")}</button><button onClick={saveRecovery}>{tr("Save recovery")}</button><button disabled={busy || !draft || !parsed || invalidFields.length > 0 || !!selectedNode && nodeID !== selected || !parseObject(layout)} onClick={() => void save()}>{tr("Save draft")}</button><button disabled={busy || !draft || dirty || invalidFields.length > 0} onClick={() => void publish()}>{tr("Publish revision")}</button><a className="button" href={`/admin/groups/${encodeURIComponent(group)}/optimization`}>{tr("Activate from configuration")}</a></div></header>
    {statusKey && <p role="status">{tr(statusKey)}</p>}{errorKey && <p role="alert">{tr(errorKey)}</p>}{validationField && <p id="workflow-field-error" role="alert"><code>{validationField.path}</code>: {tr(validationField.message)}</p>}
    {errorKey === "The draft changed elsewhere. Reload before saving." && <button onClick={() => { saveRecovery(); void load(); }}>{tr("Keep recovery and reload latest")}</button>}
    {statusKey === "A local recovery draft is available. Review it before restoring." && <button onClick={recover}>{tr("Restore recovery")}</button>}
    <div className="workbench-toolbar"><button onClick={() => setShowConfig(value => !value)} aria-expanded={showConfig}>{tr(showConfig ? "Hide configuration" : "Show configuration")}</button><span>{tr("Draft revision")} {draft?.revision ?? "—"} · {tr("Layout revision")} {draft?.layout_revision ?? "—"}</span><span>{dirty ? tr("Unsaved changes") : tr("Saved")}</span>{digest && <span role="status">{tr("Executable digest")}: <code>{digest}</code></span>}</div>
    <fieldset disabled={busy} className="workbench-fields"><div className="workbench-grid">
      <aside className="palette card"><h2>{tr("Node palette")}</h2><p>{tr("Keyboard alternative: add a node, then edit it in the inspector.")}</p>{nodeKinds.map(kind => <button key={kind} onClick={() => addNode(kind)}>{tr("Add {kind}", { kind })}</button>)}<h2>{tr("Connect ports")}</h2><label>{tr("From node")}<select aria-label={tr("From node")} value={source} onChange={event => setSource(event.target.value)}><option value="">{tr("Choose source")}</option>{sourcePorts.map(port => <option key={port} value={port}>{port}</option>)}</select></label><label>{tr("To node")}<select aria-label={tr("To node")} value={target} onChange={event => setTarget(event.target.value)}><option value="">{tr("Choose target")}</option>{targetPorts.map(port => <option key={port} value={port}>{port}</option>)}</select></label><button disabled={!source || !target} onClick={connect}>{tr("Connect")}</button></aside>
      <section className="canvas card" aria-label={tr("Workflow canvas")}><h2>{tr("Workflow canvas")}</h2>{!nodes.length && <p>{tr("No nodes yet. Use the keyboard node palette to start.")}</p>}<ol className="node-list">{nodes.map(node => <li key={String(node.id)}><button className={selected === String(node.id) ? "selected-node" : ""} onClick={() => selectNode(String(node.id))} aria-pressed={selected === String(node.id)}><strong>{String(node.id)}</strong><span>{tr(String(node.kind ?? "unknown"))}</span><small>{tr("Inputs")}: {Object.keys((node.input_ports as JSONMap | undefined) ?? {}).length} · {tr("Outputs")}: {Object.keys((node.output_ports as JSONMap | undefined) ?? {}).length}</small></button></li>)}</ol><h3>{tr("Connections")}</h3>{(Array.isArray(parsed?.edges) ? parsed.edges : []).length ? <ul>{(parsed?.edges as JSONMap[]).map((edge, index) => <li key={`${String(edge.from)}-${String(edge.to)}-${index}`}><code>{String(edge.from)} → {String(edge.to)}</code><button type="button" onClick={() => removeEdge(index)} aria-label={`${tr("Remove connection")} ${String(edge.from)} → ${String(edge.to)}`}>{tr("Remove")}</button></li>)}</ul> : <p>{tr("No connections yet.")}</p>}<div className="editor-actions"><button disabled={busy} onClick={() => void validateDraft()}>{tr("Validate draft")}</button></div></section>
      <aside className="inspector card"><h2>{tr("Node inspector")}</h2>{selectedNode ? <><p><strong>{String(selectedNode.id)}</strong> · {tr(String(selectedNode.kind ?? "unknown"))}</p><label>{tr("Node ID")}<input data-field="id" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Node ID")} value={nodeID} onChange={event => { remember(); setNodeID(event.target.value); }} /></label><button disabled={nodeID === selected} onClick={() => updateNode(node => { node.id = nodeID; })}>{tr("Rename node")}</button>{nodeID !== selected && <button onClick={() => setNodeID(selected)}>{tr("Reset node name")}</button>}<label>{tr("Kind")}<select data-field="kind" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Kind")} value={String(selectedNode.kind ?? "")} onChange={event => updateNode(node => { node.kind = event.target.value; })}>{nodeKinds.map(kind => <option key={kind} value={kind}>{tr(kind)}</option>)}</select></label><label>{tr("Prompt text")}<textarea data-field="prompt.text" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Prompt text")} value={String((selectedNode.prompt as JSONMap | undefined)?.text ?? "")} onChange={event => updateNode(node => { const prompt = (node.prompt ?? {}) as JSONMap; prompt.text = event.target.value; node.prompt = prompt; })} /></label><label>{tr("Prompt variables")}<input data-field="prompt.variables" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Prompt variables")} value={Array.isArray((selectedNode.prompt as JSONMap | undefined)?.variables) ? ((selectedNode.prompt as JSONMap).variables as unknown[]).join(", ") : ""} onChange={event => updateNode(node => { const prompt = (node.prompt && typeof node.prompt === "object" ? node.prompt : {}) as JSONMap; prompt.variables = event.target.value.split(",").map(value => value.trim()).filter(Boolean); node.prompt = prompt; })} /></label><label>{tr("Model revision")}<input data-field="model_ref.id" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Model revision")} value={String((selectedNode.model_ref as JSONMap | undefined)?.id ?? "")} onChange={event => updateNode(node => { const model = (node.model_ref && typeof node.model_ref === "object" ? node.model_ref : {}) as JSONMap; model.id = event.target.value; node.model_ref = model; })} /></label><label>{tr("Model version")}<input data-field="model_ref.revision" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Model version")} type="number" min="1" value={String((selectedNode.model_ref as JSONMap | undefined)?.revision ?? "")} onChange={event => updateNode(node => { node.model_ref = { ...((node.model_ref as JSONMap) ?? {}), revision: Number(event.target.value) }; })} /></label><label>{tr("Model digest")}<input data-field="model_ref.digest" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Model digest")} value={String((selectedNode.model_ref as JSONMap | undefined)?.digest ?? "")} onChange={event => updateNode(node => { node.model_ref = { ...((node.model_ref as JSONMap) ?? {}), digest: event.target.value }; })} /></label><label>{tr("Tools JSON")}<textarea data-field="tools" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Tools JSON")} value={jsonDrafts.tools} onChange={event => updateJSONField("tools", event.target.value)} aria-invalid={parseField("tools", jsonDrafts.tools) === undefined} spellCheck={false} /></label><label>{tr("Input ports JSON")}<textarea data-field="input_ports" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Input ports JSON")} value={jsonDrafts.input_ports} onChange={event => updateJSONField("input_ports", event.target.value)} aria-invalid={parseField("input_ports", jsonDrafts.input_ports) === undefined} spellCheck={false} /></label><label>{tr("Output ports JSON")}<textarea data-field="output_ports" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Output ports JSON")} value={jsonDrafts.output_ports} onChange={event => updateJSONField("output_ports", event.target.value)} aria-invalid={parseField("output_ports", jsonDrafts.output_ports) === undefined} spellCheck={false} /></label><label>{tr("Max attempts")}<input data-field="max_attempts" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Max attempts")} type="number" min="1" value={Number(selectedNode.max_attempts ?? 1)} onChange={event => updateNode(node => { node.max_attempts = Number(event.target.value); })} /></label><button onClick={() => { if (!parsed) return; const impact = (parsed.edges ?? []).filter(edge => String(edge.from).startsWith(`${selected}.`) || String(edge.to).startsWith(`${selected}.`)).length; if (!window.confirm(tr("Remove this node and {count} connections?", { count: String(impact) }))) return; const next = structuredClone(parsed) as Workflow; next.nodes = nodeList(next).filter(node => String(node.id ?? "") !== selected); next.edges = (Array.isArray(next.edges) ? next.edges : []).filter(edge => !String(edge.from).startsWith(`${selected}.`) && !String(edge.to).startsWith(`${selected}.`)); replaceWorkflow(next); setFieldDrafts(fields => { const remaining = { ...fields }; delete remaining[selected]; return remaining; }); setSelected(""); setNodeID(""); }}>{tr("Remove node")}</button></> : <p>{tr("Select a node to inspect inputs, prompt, model and limits.")}</p>}</aside>
    </div>
    {invalidFields.length > 0 && <p role="alert">{tr("Fix invalid node fields before saving or validating.")} {invalidFields.join(", ")}</p>}
    {showConfig && <section className="editor-grid config-editors"><label>{tr("Executable workflow JSON")}<textarea data-field="workflow" aria-describedby={validationField ? "workflow-field-error" : undefined} aria-label={tr("Executable workflow JSON")} value={text} onChange={event => { remember(); setText(event.target.value); setFieldDrafts({}); }} spellCheck={false} /></label><label>{tr("Canvas layout JSON")}<textarea aria-label={tr("Canvas layout JSON")} value={layout} onChange={event => { setPast(items => [...items.slice(-19), snapshot()]); setFuture([]); setLayout(event.target.value); setDirty(true); }} spellCheck={false} /></label></section>}
  </fieldset>{draft && <WorkflowDraftTest group={group} draft={workflow} revision={draft.revision} nodes={nodes.map(node => ({ id: String(node.id), kind: String(node.kind) }))} saved={!busy && !dirty && invalidFields.length === 0} />}</main>;
}
