"use client";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { useUnsavedNavigation } from "../../../web/unsaved-navigation";
import { Select } from "../../../web/select";
import { api, base } from "../lib/api";
import { useT } from "../lib/i18n";
import type { Revision } from "../lib/evolution";
import { offPolicy, refKey, type CatalogEntry, type EvaluationPolicy, type TemplateDefinition, type WorkflowDefinition } from "../lib/evolution-config";
import { EvaluationEditor } from "./settings/evolution-binding";
import { useGroup, useSession } from "./group-context";

const enc = encodeURIComponent;
const idPattern = /^[A-Za-z0-9_-]{1,128}$/;
export const profiles = [["authored-v1", "Authored content"], ["shared-assets-v1", "Shared assets (AGENTS.md and skills/)"]] as const;
type Form = { id: string; revision: number; goal: string; profile: string; prefix: string; workflows: Revision[]; inputs: Record<string, string>; outputs: Record<string, string>; schema: string; evaluation: EvaluationPolicy };
const blank = (): Form => ({ id: "", revision: 1, goal: "", profile: "authored-v1", prefix: "", workflows: [], inputs: {}, outputs: {}, schema: '{\n  "type": "object"\n}', evaluation: { ...offPolicy } });
function fromTemplate(value: TemplateDefinition, mode: "revision" | "copy"): Form {
  return { id: mode === "copy" ? "" : value.ref.id, revision: mode === "copy" ? 1 : value.ref.revision + 1, goal: value.goal, profile: value.profile, prefix: value.suggested_prefix, workflows: value.workflows, inputs: value.inputs, outputs: value.outputs, schema: JSON.stringify(value.parameters_schema, null, 2), evaluation: structuredClone(value.evaluation) };
}
function toDocument(form: Form) {
  return { ref: { id: form.id, revision: form.revision }, goal: form.goal, parameters_schema: JSON.parse(form.schema), inputs: form.inputs, outputs: form.outputs, profile: form.profile, workflows: form.workflows, suggested_prefix: form.prefix, evaluation: form.evaluation };
}
export function readRef(search: { get(name: string): string | null }, prefix: string): Revision | undefined {
  const id = search.get(`${prefix}_id`) ?? "", revision = Number(search.get(`${prefix}_revision`)), digest = search.get(`${prefix}_digest`) ?? "";
  return idPattern.test(id) && Number.isSafeInteger(revision) && revision > 0 && /^sha256:[a-f0-9]{64}$/.test(digest) ? { id, revision, digest } : undefined;
}
export function refQuery(prefix: string, ref: Revision) {
  return new URLSearchParams({ [`${prefix}_id`]: ref.id, [`${prefix}_revision`]: String(ref.revision), [`${prefix}_digest`]: ref.digest }).toString();
}
const samePorts = (a: Record<string, string>, b: Record<string, string>) => JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort());

// Templates are immutable revisions. This form composes the exact document the
// server validates; the JSON view shows the same document, not another format.
export function TemplateForm() {
  const tr = useT(); const router = useRouter(); const search = useSearchParams(); const session = useSession(); const { group } = useGroup();
  const source = readRef(search, "from"), mode = search.get("mode") === "copy" ? "copy" : "revision", seedWorkflow = readRef(search, "workflow");
  const [form, setForm] = useState<Form>(blank); const [saved, setSaved] = useState(() => JSON.stringify(blank()));
  const [loading, setLoading] = useState(!!source || !!seedWorkflow); const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  const [workflowDefinitions, setWorkflowDefinitions] = useState<Record<string, WorkflowDefinition>>({});
  const [advanced, setAdvanced] = useState(false);
  const dirty = JSON.stringify(form) !== saved;
  useUnsavedNavigation(dirty && !busy, base);
  const root = `/groups/${enc(group.id)}`;
  useEffect(() => {
    const abort = new AbortController();
    void (async () => {
      try {
        let next = blank();
        if (source) next = fromTemplate(await api<TemplateDefinition>(`${root}/templates/${enc(source.id)}/${source.revision}?digest=${enc(source.digest)}`, abort.signal), mode);
        // Unsaved state is measured from the loaded template, not from added seeds.
        const original = JSON.stringify(next);
        if (seedWorkflow && !next.workflows.some(ref => refKey(ref) === refKey(seedWorkflow))) {
          const entry = await publishedWorkflow(group.id, seedWorkflow, abort.signal);
          setWorkflowDefinitions(items => ({ ...items, [refKey(seedWorkflow)]: entry.definition }));
          next = { ...next, workflows: [...next.workflows, seedWorkflow], ...(next.workflows.length ? {} : { inputs: entry.definition.inputs, outputs: entry.definition.outputs }) };
        }
        if (!abort.signal.aborted) { setForm(next); setSaved(original); }
      } catch { if (!abort.signal.aborted) setError("The starting template or workflow is unavailable. Start from a blank template or retry."); }
      finally { if (!abort.signal.aborted) setLoading(false); }
    })();
    return () => abort.abort();
    // The starting point is fixed by the URL that opened the form.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const schemaValid = useMemo(() => { try { const value = JSON.parse(form.schema); return !!value && typeof value === "object" && !Array.isArray(value) && value.type === "object"; } catch { return false; } }, [form.schema]);
  const mismatched = form.workflows.filter(ref => { const definition = workflowDefinitions[refKey(ref)]; return definition && (!samePorts(definition.inputs, form.inputs) || !samePorts(definition.outputs, form.outputs)); });
  const problems = [
    !idPattern.test(form.id) && "Template ID uses letters, digits, underscores or hyphens.",
    !form.goal.trim() && "Describe the goal.",
    !form.workflows.length && "Select at least one published workflow.",
    (!Object.keys(form.inputs).length || !Object.keys(form.outputs).length) && "Typed input and output ports come from the selected workflow.",
    !schemaValid && "Parameters schema must be a JSON object schema.",
    !form.prefix.trim() && "Choose a suggested output directory.",
    mismatched.length > 0 && "Every workflow must use the template's exact port schemas.",
  ].filter(Boolean) as string[];
  const update = (patch: Partial<Form>) => setForm(value => ({ ...value, ...patch }));
  function addWorkflow(entry: CatalogEntry) {
    const definition = entry.definition as WorkflowDefinition;
    if (form.workflows.some(ref => refKey(ref) === refKey(entry.ref))) return;
    setWorkflowDefinitions(items => ({ ...items, [refKey(entry.ref)]: definition }));
    update({ workflows: [...form.workflows, entry.ref], ...(form.workflows.length ? {} : { inputs: definition.inputs, outputs: definition.outputs }) });
  }
  async function publish() {
    if (problems.length || busy) return;
    setBusy(true); setError("");
    try {
      const response = await fetch(`${base}/api/v1${root}/templates`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(toDocument(form)) });
      if (!response.ok) throw new Error(response.status === 409 ? "This template ID or revision is already used, or another group owns it. Duplicate it under a new ID." : response.status === 401 || response.status === 403 ? "Publishing templates requires permission to edit workflows." : "Template publication failed. Check ownership, next revision, parameter schema and exact compatible workflows. Copy shared templates using a new ID and revision 1.");
      const ref = await response.json() as Revision;
      setSaved(JSON.stringify(form));
      router.push(`${root}/templates/${enc(ref.id)}/${ref.revision}/?digest=${enc(ref.digest)}&published=1`);
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); setBusy(false); }
  }
  if (!group.can_edit_workflows) return <p role="status">{tr("Creating templates requires permission to edit workflows.")}</p>;
  if (loading) return <p role="status">{tr("Loading template…")}</p>;
  return <form className="card template-form" noValidate onSubmit={event => { event.preventDefault(); void publish(); }}>
    {error && <p role="alert">{tr(error)}</p>}
    <fieldset disabled={busy}>
      <section aria-labelledby="template-identity"><h2 id="template-identity">{tr("Identity")}</h2>
        <div className="form-row">
          <label>{tr("Template ID")}<input aria-label={tr("Template ID")} value={form.id} readOnly={!!source && mode === "revision"} maxLength={128} onChange={event => update({ id: event.target.value })} aria-describedby="template-id-help" /></label>
          <label>{tr("Revision")}<input aria-label={tr("Revision")} type="number" min={1} value={form.revision} readOnly={!source || mode === "copy"} onChange={event => update({ revision: Number(event.target.value) })} /></label>
        </div>
        <p id="template-id-help" className="hint">{tr(source && mode === "revision" ? "A new revision keeps the ID. Earlier revisions stay unchanged for existing configurations." : "A new template starts at revision 1 and belongs to this group.")}</p>
        <label>{tr("Goal")}<textarea aria-label={tr("Goal")} rows={3} maxLength={4000} value={form.goal} onChange={event => update({ goal: event.target.value })} placeholder={tr("For example: extract reusable skills from resolved support conversations")} /></label>
      </section>
      <section aria-labelledby="template-workflows"><h2 id="template-workflows">{tr("Compatible workflows")}</h2>
        <p className="hint">{tr("Choose published workflow revisions. The first one sets the typed input and output ports; later ones must match exactly.")}</p>
        {form.workflows.length ? <ul className="ref-list">{form.workflows.map(ref => <li key={refKey(ref)}><code>{ref.id} · {ref.revision}</code>{mismatched.includes(ref) && <span role="alert">{tr("Port schemas differ")}</span>}<button type="button" onClick={() => { const workflows = form.workflows.filter(item => refKey(item) !== refKey(ref)); update({ workflows, ...(workflows.length ? {} : { inputs: {}, outputs: {} }) }); }}>{tr("Remove")}</button></li>)}</ul> : <p>{tr("No workflow selected yet.")}</p>}
        <WorkflowPicker group={group.id} selected={form.workflows} onChoose={addWorkflow} />
        <p><Link prefetch={false} href={`${root}/workflows/`}>{tr("Build or publish a workflow")}</Link></p>
        <div className="form-row"><PortList title="Input ports" ports={form.inputs} /><PortList title="Output ports" ports={form.outputs} /></div>
      </section>
      <section aria-labelledby="template-output"><h2 id="template-output">{tr("Output")}</h2>
        <div className="form-row">
          <label>{tr("Output profile")}<Select aria-label={tr("Output profile")} value={form.profile} onChange={event => update({ profile: event.target.value })}>{profiles.map(([value, label]) => <option key={value} value={value}>{tr(label)}</option>)}</Select></label>
          <label>{tr("Suggested output directory")}<input aria-label={tr("Suggested output directory")} value={form.prefix} maxLength={256} placeholder="skills" onChange={event => update({ prefix: event.target.value })} /></label>
        </div>
      </section>
      <section aria-labelledby="template-evaluation"><h2 id="template-evaluation">{tr("Default evaluation")}</h2>
        <p className="hint">{tr("Groups inherit this default and may override it per binding. Off sends output straight to expert review.")}</p>
        <EvaluationEditor group={group.id} value={form.evaluation} update={evaluation => update({ evaluation })} />
      </section>
      <section aria-labelledby="template-parameters"><h2 id="template-parameters">{tr("Parameters")}</h2>
        <label>{tr("Parameters schema (JSON)")}<textarea aria-label={tr("Parameters schema (JSON)")} className="code" rows={6} spellCheck={false} value={form.schema} onChange={event => update({ schema: event.target.value })} aria-invalid={!schemaValid} /></label>
      </section>
      <details open={advanced} onToggle={event => setAdvanced(event.currentTarget.open)}><summary>{tr("Template document")}</summary>
        {advanced && <pre className="document-preview">{schemaValid ? JSON.stringify(toDocument(form), null, 2) : tr("Parameters schema must be a JSON object schema.")}</pre>}
      </details>
    </fieldset>
    {problems.length > 0 && <ul className="form-problems" aria-label={tr("Before publishing")}>{problems.map(problem => <li key={problem}>{tr(problem)}</li>)}</ul>}
    <div className="form-actions"><button className="primary" type="submit" disabled={busy || problems.length > 0}>{tr(busy ? "Publishing…" : "Publish template revision")}</button><Link prefetch={false} className="button" href={`${root}/templates/`}>{tr("Cancel")}</Link></div>
    <p className="hint">{tr("Publishing does not change any group. Add the template to the configuration, then review and activate.")}</p>
  </form>;
}
function PortList({ title, ports }: { title: string; ports: Record<string, string> }) {
  const tr = useT(); const entries = Object.entries(ports);
  return <div><h3>{tr(title)}</h3>{entries.length ? <ul className="port-list">{entries.map(([name, digest]) => <li key={name}><code>{name}</code><small title={digest}>{digest.slice(0, 19)}…</small></li>)}</ul> : <p className="hint">{tr("Set by the first workflow.")}</p>}</div>;
}
async function publishedWorkflow(group: string, ref: Revision, signal?: AbortSignal): Promise<CatalogEntry<WorkflowDefinition>> {
  const definition = await api<WorkflowDefinition>(`/groups/${enc(group)}/published-workflows/${enc(ref.id)}/${ref.revision}?digest=${enc(ref.digest)}`, signal);
  return { ref, definition };
}
type WorkflowPage = { revisions: Revision[]; next_id: string; next_revision: number; has_more: boolean };
function WorkflowPicker({ group, selected, onChoose }: { group: string; selected: Revision[]; onChoose: (entry: CatalogEntry) => void }) {
  const tr = useT(); const [pages, setPages] = useState<{ id: string; revision: number }[]>([]); const [choice, setChoice] = useState(""); const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  const after = pages.at(-1);
  const [list, setList] = useState<WorkflowPage>();
  useEffect(() => {
    const abort = new AbortController();
    void api<WorkflowPage>(`/groups/${enc(group)}/published-workflows${after ? `?${new URLSearchParams({ after_id: after.id, after_revision: String(after.revision) })}` : ""}`, abort.signal)
      .then(page => { if (!abort.signal.aborted) { setList(previous => after && previous ? { ...page, revisions: [...previous.revisions, ...page.revisions] } : page); setError(""); } })
      .catch(() => { if (!abort.signal.aborted) setError("Published workflows are unavailable. Retry or check group access."); });
    return () => abort.abort();
  }, [group, after]);
  const options = (list?.revisions ?? []).filter(ref => !selected.some(item => refKey(item) === refKey(ref)));
  async function add() {
    const ref = options.find(item => refKey(item) === choice); if (!ref) return;
    setBusy(true); setError("");
    try { onChoose(await publishedWorkflow(group, ref)); setChoice(""); }
    catch { setError("Published workflows are unavailable. Retry or check group access."); }
    finally { setBusy(false); }
  }
  return <div className="workflow-picker"><label>{tr("Add published workflow")}<Select aria-label={tr("Add published workflow")} value={choice} disabled={busy} onChange={event => setChoice(event.target.value)}><option value="">{tr("Choose a published revision")}</option>{options.map(ref => <option key={refKey(ref)} value={refKey(ref)}>{ref.id} · {ref.revision}</option>)}</Select></label>
    <div className="form-actions"><button type="button" disabled={!choice || busy} onClick={() => void add()}>{tr("Add workflow")}</button>{list?.has_more && <button type="button" onClick={() => setPages(items => [...items, { id: list.next_id, revision: list.next_revision }])}>{tr("Load more workflows")}</button>}</div>
    {list && !list.revisions.length && <p className="hint">{tr("No published workflows yet. Publish a workflow draft first.")}</p>}
    {error && <p role="alert">{tr(error)}</p>}</div>;
}
