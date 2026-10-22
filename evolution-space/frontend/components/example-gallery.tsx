"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { base, type Session } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";
import type { Revision } from "../lib/evolution";
import { evidenceRules, holdReasons, memoryClasses, nodeKindLabels, pin, type ExampleDetail, type ExamplePage, type ModelPage } from "../lib/examples";
import { WorkflowCanvas } from "./workflow-canvas";
import { refQuery } from "./template-form";

const enc = encodeURIComponent;

async function post<T>(path: string, body: unknown): Promise<{ status: number; value?: T; error?: string }> {
  const session = await (await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" })).json() as Session;
  const response = await fetch(`${base}/api/v1${path}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(body) });
  const value = await response.json().catch(() => undefined);
  return response.ok ? { status: response.status, value: value as T } : { status: response.status, error: value?.error };
}

// Gallery of built-in examples. Cards open a preview drawer with the graph, a
// worked sample and the baseline evaluation; nothing here changes a group.
export function ExampleGallery({ group, mode }: { group: string; mode: "workflows" | "templates" }) {
  const tr = useT();
  const list = useRead<ExamplePage>(`/groups/${enc(group)}/examples`);
  const [open, setOpen] = useState("");
  if (list.loading) return <p role="status">{tr("Loading built-in examples…")}</p>;
  if (list.status || !list.data) return <div role="alert"><p>{tr("Built-in examples are unavailable. Retry when the service is restored.")}</p><button onClick={list.refresh}>{tr("Retry")}</button></div>;
  return <section className="example-gallery" aria-labelledby="examples-heading">
    <div className="section-heading"><div><h2 id="examples-heading">{tr("Built-in examples")}</h2>
      <p className="hint">{tr(mode === "workflows" ? "Ready-made pipelines you can preview, then copy into an editable draft." : "Ready-made goals. Install one, then use it in your configuration.")}</p></div></div>
    <ul className="example-grid">{list.data.items.map(item => {
      const kind = memoryClasses.find(value => value.value === item.memory_class);
      return <li key={item.template}><button type="button" className="example-card" aria-haspopup="dialog" onClick={() => setOpen(item.template)}>
        <span className="example-card-top"><span className={`badge memory-${item.memory_class}`}>{tr(kind?.label ?? item.memory_class)} · {tr(kind?.description ?? "")}</span>{item.installed && <span className="badge installed">{tr("Installed")}</span>}</span>
        <strong>{item.template.replace(/^example-/, "")}</strong>
        <span>{tr(item.summary)}</span>
        <code>{item.prefix}/</code>
      </button></li>;
    })}</ul>
    {open && <ExampleDrawer group={group} template={open} mode={mode} onClose={() => { setOpen(""); list.refresh(); }} />}
  </section>;
}

function ExampleDrawer({ group, template, mode, onClose }: { group: string; template: string; mode: "workflows" | "templates"; onClose: () => void }) {
  const tr = useT(); const router = useRouter(); const root = `/groups/${enc(group)}`;
  const detail = useRead<ExampleDetail>(`${root}/examples/${enc(template)}`);
  const models = useRead<ModelPage>(`${root}/models`);
  const [tab, setTab] = useState<"graph" | "sample" | "fields" | "evaluation">("graph");
  const [graph, setGraph] = useState(0); const [selected, setSelected] = useState("");
  const [model, setModel] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const [draftID, setDraftID] = useState(() => `my-${template.replace(/^example-/, "")}-${Math.random().toString(36).slice(2, 6)}`);
  const value = detail.data;
  const choices = models.data?.items ?? [];
  const chosen: Revision | undefined = value?.model ?? choices.find(item => `${item.id}@${item.revision}` === model) ?? (choices.length === 1 ? choices[0] : undefined);
  async function install(): Promise<{ template: Revision; workflow: Revision } | undefined> {
    if (!chosen) { setError("Choose a registered model."); return; }
    const result = await post<{ template: Revision; workflow: Revision }>(`${root}/examples/${enc(template)}/install`, { model: pin(chosen) });
    if (result.value) return result.value;
    setError(result.error === "installed_for_another_model" ? "This example is already installed for a different model. Copy the installed version instead." : result.status === 403 ? "Installing requires permission to edit workflows." : result.error === "model_not_registered" ? "That model is not registered for native runs. Ask an operator to register it." : "Installation failed. Retry when the service is restored.");
  }
  async function act(kind: "install" | "copy") {
    setBusy(true); setError("");
    try {
      const installed = await install(); if (!installed) return;
      if (kind === "install") { detail.refresh(); router.push(`${root}/templates/${enc(installed.template.id)}/${installed.template.revision}/?digest=${enc(installed.template.digest)}&published=1`); return; }
      if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(draftID)) { setError("Use letters, digits, hyphens or underscores for the draft ID."); return; }
      const draft = await post<{ id: string }>(`${root}/workflows`, { id: draftID, source_workflow: installed.workflow });
      if (!draft.value) { setError(draft.status === 409 ? "That draft ID is taken. Choose another." : "Copying failed. Retry when the service is restored."); return; }
      router.push(`${root}/workflows/${enc(draft.value.id)}/`);
    } catch { setError("The result is unknown. Retry to check whether it was saved."); } finally { setBusy(false); }
  }
  const workflow = value?.workflows[graph];
  const node = (workflow?.workflow.nodes ?? []).find(item => String(item.id) === selected);
  return <div className="drawer-backdrop" onClick={event => { if (event.target === event.currentTarget) onClose(); }}>
    <aside className="drawer" role="dialog" aria-modal="true" aria-labelledby="example-title" onKeyDown={event => { if (event.key === "Escape") onClose(); }}>
      <header className="drawer-header"><div><p className="eyebrow">{tr("Built-in example")}</p><h2 id="example-title">{template.replace(/^example-/, "")}</h2></div><button onClick={onClose} aria-label={tr("Close")}>✕</button></header>
      {detail.loading && <p role="status">{tr("Loading example…")}</p>}
      {detail.status && <p role="alert">{tr("Example detail is unavailable. Retry when the service is restored.")}</p>}
      {value && <>
        <p>{tr(value.summary)}</p>
        <dl className="facts"><div><dt>{tr("Output directory")}</dt><dd><code>{value.prefix}/{value.layout}</code></dd></div><div><dt>{tr("Evidence rule")}</dt><dd>{tr(evidenceRules[value.evidence] ?? value.evidence)}</dd></div><div><dt>{tr("Status")}</dt><dd>{value.installed ? <>{tr("Installed")} · {tr("Revision")} {value.installed.revision}</> : tr("Not installed")}</dd></div></dl>
        <div className="tabs" role="tablist" aria-label={tr("Example sections")}>{([["graph", "Pipeline"], ["sample", "Worked sample"], ["fields", "Output fields"], ["evaluation", "Baseline evaluation"]] as const).map(([key, label]) => <button key={key} role="tab" aria-selected={tab === key} className={tab === key ? "active" : ""} onClick={() => setTab(key)}>{tr(label)}</button>)}</div>
        {tab === "graph" && workflow && <div role="tabpanel">
          {value.workflows.length > 1 && <div className="segmented" role="group" aria-label={tr("Workflow")}>{value.workflows.map((item, index) => <button key={item.ref.id} aria-pressed={graph === index} onClick={() => { setGraph(index); setSelected(""); }}>{index === value.workflows.length - 1 ? tr("Main pipeline") : tr("Sub-workflow")}: {item.ref.id}</button>)}</div>}
          <WorkflowCanvas workflow={workflow.workflow} layout={{}} readOnly selected={selected} onSelect={setSelected} />
          <ol className="node-explainer">{(workflow.workflow.nodes ?? []).map(item => { const id = String(item.id), kind = String(item.kind); return <li key={id} className={selected === id ? "selected" : ""}><button type="button" onClick={() => setSelected(id)}><strong>{id}</strong> <span className="badge">{tr(nodeKindLabels[kind] ?? kind)}</span></button><p>{tr(workflow.node_docs[id] ?? "")}</p>{selected === id && node?.prompt ? <details open><summary>{tr("Prompt")}</summary><pre className="document-preview">{String((node.prompt as { text?: string }).text ?? "")}</pre></details> : null}</li>; })}</ol>
        </div>}
        {tab === "sample" && <div role="tabpanel" className="sample">
          <h3>{tr("Input episodes")}</h3><ul className="episode-list">{value.sample.corpus.episodes.map(item => <li key={item.id}><span className={`badge outcome-${item.outcome}`}>{tr(item.outcome)}</span> <code>{item.id}</code><p>{item.text}</p></li>)}</ul>
          <h3>{tr("Files written")}</h3>{value.sample.files.map(file => <details key={file.path} open><summary><code>{value.prefix}/{file.path}</code></summary><pre className="document-preview">{file.text}</pre></details>)}
          <h3>{tr("Held for review")}</h3><ul className="held-list">{value.sample.held.map(item => <li key={item.id}><strong>{item.title}</strong> <span className="badge warn">{tr(holdReasons[item.hold_reason ?? ""] ?? item.hold_reason ?? "")}</span></li>)}</ul>
        </div>}
        {tab === "fields" && <div role="tabpanel"><table aria-label={tr("Output fields")}><thead><tr><th scope="col">{tr("Field")}</th><th scope="col">{tr("Meaning")}</th></tr></thead><tbody>{value.fields.map(field => <tr key={field.name}><th scope="row"><code>{field.name}</code>{field.required && <small> · {tr("required")}</small>}</th><td>{tr(field.guidance)}{field.enum && <><br /><small>{field.enum.join(" | ")}</small></>}</td></tr>)}</tbody></table></div>}
        {tab === "evaluation" && <div role="tabpanel"><p>{tr(value.baseline.description)}</p><p className="hint">{tr("Dataset")}: {tr(value.baseline.dataset)}</p><pre className="document-preview">{value.baseline.dataset_example}</pre>
          <table aria-label={tr("Baseline thresholds")}><thead><tr><th scope="col">{tr("Metric")}</th><th scope="col">{tr("Threshold")}</th><th scope="col">{tr("Minimum samples")}</th></tr></thead><tbody>{value.baseline.thresholds.map(item => <tr key={item.name}><th scope="row"><code>{item.name}</code></th><td>{item.operator === "gte" ? "≥" : "≤"} {item.value} {item.unit}</td><td>{item.minimum_samples}</td></tr>)}</tbody></table>
          <Link prefetch={false} className="button" href={`${root}/profiles/?preset=${enc(template)}`}>{tr("Create a profile from this baseline")}</Link></div>}
        <footer className="drawer-footer">
          {!value.model && (choices.length > 1 ? <label>{tr("Model")}<select aria-label={tr("Model")} value={model} onChange={event => setModel(event.target.value)}><option value="">{tr("Choose a model")}</option>{choices.map(item => <option key={`${item.id}@${item.revision}`} value={`${item.id}@${item.revision}`}>{item.label ?? item.id} · {item.revision}</option>)}</select></label> : choices.length === 1 ? <p className="hint">{tr("Model")}: <code>{choices[0].label ?? choices[0].id} · {choices[0].revision}</code></p> : <p className="hint">{tr("No model is available yet. You can preview examples; a platform administrator adds one under Settings → Model connections.")}</p>)}
          {mode === "workflows" ? <><label>{tr("New draft ID")}<input aria-label={tr("New draft ID")} value={draftID} onChange={event => setDraftID(event.target.value)} /></label>
            <button className="primary" disabled={busy || !chosen} onClick={() => void act("copy")}>{tr("Copy to my drafts")}</button></>
            : value.installed ? <><Link prefetch={false} className="button primary" href={`${root}/templates/${enc(value.installed.id)}/${value.installed.revision}/?digest=${enc(value.installed.digest)}`}>{tr("Open template")}</Link><Link prefetch={false} className="button" href={`${root}/settings/configuration/?${refQuery("template", value.installed)}#bindings`}>{tr("Use in configuration")}</Link></>
            : <button className="primary" disabled={busy || !chosen} onClick={() => void act("install")}>{tr("Install template")}</button>}
          {error && <p role="alert">{tr(error)}</p>}
        </footer>
      </>}
    </aside>
  </div>;
}
