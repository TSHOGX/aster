"use client";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { api as central } from "../lib/central";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";
import type { Permissions, Revision } from "../lib/evolution";
import type { TemplateDefinition } from "../lib/evolution-config";
import { useGroup } from "./group-context";
import { FlowPosition } from "./flow";
import { PageHeading, useDirtyGuard } from "./group-layout";
import { CatalogEditor } from "./catalog-editor";
import { ExampleGallery } from "./example-gallery";
import { ProfileWizard } from "./profile-wizard";
import { RunHistory, EvaluationHistory } from "./execution-history";
import { WorkflowDrafts } from "./workflow-drafts";
import { TemplateForm, profiles, refQuery } from "./template-form";
import { EvolutionConfiguration } from "./settings/evolution-configuration";
import { EvolutionCapture } from "./settings/evolution-capture";
import { EvolutionManual } from "./settings/evolution-manual";
import { EvolutionPermissionsEditor } from "./settings/evolution-permissions";

const enc = encodeURIComponent;
type CatalogPage = { revisions: Revision[]; next_id: string; next_revision: number; has_more: boolean };

export function TemplatesPage() {
  const tr = useT(); const { group } = useGroup(); const root = `/groups/${enc(group.id)}`;
  const [cursor, setCursor] = useState<{ id: string; revision: number }[]>([]);
  const after = cursor.at(-1);
  const read = useRead<CatalogPage>(`${root}/templates${after ? `?${new URLSearchParams({ after_id: after.id, after_revision: String(after.revision) })}` : ""}`);
  return <>
    <PageHeading title={tr("Goal templates")} description="A template pins a goal, typed ports, an output profile, compatible workflows and evaluation defaults. Publishing a template never changes a group until you activate a configuration that uses it."
      actions={<><button onClick={read.refresh}>{tr("Refresh")}</button>{group.can_edit_workflows && <Link prefetch={false} className="button primary" href={`${root}/templates/new/`}>{tr("New template")}</Link>}</>} />
    <FlowPosition step="templates" />
    <section className="card" aria-label={tr("Published templates")}>
      {read.loading && <p role="status">{tr("Loading templates…")}</p>}
      {read.status && <div role="alert"><p>{tr("Goal templates are unavailable. Retry when the service is restored.")}</p><button onClick={read.refresh}>{tr("Retry")}</button></div>}
      {read.data && (!read.data.revisions.length ? <div className="empty-state"><h2>{tr("No goal templates yet")}</h2><p>{tr("Create a template from a published workflow, then use it in this group's configuration.")}</p></div>
        : <table tabIndex={0} aria-label={tr("Published templates")}><thead><tr><th scope="col">{tr("Template ID")}</th><th scope="col">{tr("Revision")}</th><th scope="col">{tr("Digest")}</th></tr></thead>
          <tbody>{read.data.revisions.map(ref => <tr key={`${ref.id}:${ref.revision}`}><th scope="row"><Link prefetch={false} href={`${root}/templates/${enc(ref.id)}/${ref.revision}/?digest=${enc(ref.digest)}`}>{ref.id}</Link></th><td>{ref.revision}</td><td><code title={ref.digest}>{ref.digest.slice(0, 19)}…</code></td></tr>)}</tbody></table>)}
      <div className="pagination">{cursor.length > 0 && <button onClick={() => setCursor([])}>{tr("First page")}</button>}{read.data?.has_more && <button onClick={() => setCursor(items => [...items, { id: read.data!.next_id, revision: read.data!.next_revision }])}>{tr("Next page")}</button>}</div>
    </section>
    <ExampleGallery group={group.id} mode="templates" />
  </>;
}
export function TemplateCreatePage() {
  const tr = useT(); const { group } = useGroup(); const search = useSearchParams(); const root = `/groups/${enc(group.id)}`;
  const revision = search.has("from_id") && search.get("mode") !== "copy";
  return <><PageHeading title={tr(revision ? "New template revision" : "New goal template")} crumbs={[{ label: tr("Goal templates"), href: `${root}/templates/` }, { label: tr(revision ? "New revision" : "New template") }]} /><TemplateForm key={search.toString()} /></>;
}
export function TemplateDetailPage({ id, revision }: { id: string; revision: number }) {
  const tr = useT(); const { group } = useGroup(); const search = useSearchParams(); const root = `/groups/${enc(group.id)}`;
  const digest = search.get("digest") ?? "";
  const read = useRead<TemplateDefinition>(`${root}/templates/${enc(id)}/${revision}?digest=${enc(digest)}`);
  const template = read.data, ref = template?.ref ?? { id, revision, digest };
  const profile = profiles.find(([value]) => value === template?.profile)?.[1];
  return <>
    <PageHeading title={<>{id} <small className="status">{tr("Revision")} {revision}</small></>} crumbs={[{ label: tr("Goal templates"), href: `${root}/templates/` }, { label: `${id} · ${revision}` }]}
      actions={template && <>{group.can_manage && <Link prefetch={false} className="button primary" href={`${root}/settings/configuration/?${refQuery("template", ref)}#bindings`}>{tr("Use in configuration")}</Link>}
        {group.can_edit_workflows && <><Link prefetch={false} className="button" href={`${root}/templates/new/?${refQuery("from", ref)}`}>{tr("New revision")}</Link><Link prefetch={false} className="button" href={`${root}/templates/new/?${refQuery("from", ref)}&mode=copy`}>{tr("Duplicate as new template")}</Link></>}</>} />
    {search.get("published") === "1" && <p role="status">{tr("Published immutable goal template")}: {id} @{revision}</p>}
    {read.loading && <p role="status">{tr("Loading template…")}</p>}
    {read.status && <div role="alert"><p>{tr("Template detail is unavailable. Retry or check group access.")}</p><button onClick={read.refresh}>{tr("Retry")}</button></div>}
    {template && <div className="detail-grid">
      <section className="card"><h2>{tr("Goal")}</h2><p>{template.goal}</p><dl>
        <div><dt>{tr("Output profile")}</dt><dd>{profile ? tr(profile) : template.profile}</dd></div>
        <div><dt>{tr("Suggested output directory")}</dt><dd><code>{template.suggested_prefix}</code></dd></div>
        <div><dt>{tr("Default evaluation")}</dt><dd>{tr(template.evaluation.mode === "disabled" ? "Off" : template.evaluation.mode === "required" ? "Required for merge" : "Informational")}</dd></div>
      </dl></section>
      <section className="card"><h2>{tr("Compatible workflows")}</h2><ul className="ref-list">{template.workflows.map(workflow => <li key={`${workflow.id}:${workflow.revision}`}><code>{workflow.id} · {workflow.revision}</code>
        {group.can_edit_workflows && <Link prefetch={false} href={`${root}/workflows/?${refQuery("fork", workflow)}`}>{tr("Fork into a draft")}</Link>}</li>)}</ul></section>
      <section className="card"><h2>{tr("Typed ports")}</h2><div className="form-row">{([["Input ports", template.inputs], ["Output ports", template.outputs]] as const).map(([title, ports]) => <div key={title}><h3>{tr(title)}</h3><ul className="port-list">{Object.entries(ports).map(([name, value]) => <li key={name}><code>{name}</code><small title={value}>{value.slice(0, 19)}…</small></li>)}</ul></div>)}</div></section>
      <section className="card"><h2>{tr("Parameters schema")}</h2><pre className="document-preview">{JSON.stringify(template.parameters_schema, null, 2)}</pre></section>
    </div>}
  </>;
}
export function WorkflowsPage() {
  const tr = useT(); const { group } = useGroup();
  return <><PageHeading title={tr("Workflows")} description="Workflows are typed graphs of agents and deterministic steps. Edit and test a draft, publish an immutable revision, then select it in a goal template." /><FlowPosition step="workflows" />
    <WorkflowDrafts group={group.id} editable={group.can_edit_workflows} evidence={group.can_read_evidence} />
    <ExampleGallery group={group.id} mode="workflows" /></>;
}
export function ProfilesPage() {
  const tr = useT(); const { group } = useGroup(); const [published, setPublished] = useState(0);
  return <><PageHeading title={tr("Evaluation profiles")} description="Evaluation is optional. Without it, candidates go straight to expert review in Volume Studio. A profile defines how to measure a candidate; a configuration binding decides whether that measurement is informational or required for merge." />
    {group.can_manage ? <><ProfileWizard group={group.id} onPublished={() => setPublished(value => value + 1)} />
      <details className="card advanced-section"><summary>{tr("Published profiles and JSON editor")}</summary><CatalogEditor key={published} group={group.id} kind="profiles" editable /></details></>
      : <p role="status">{tr("Requires permission to manage configuration.")}</p>}</>;
}
export function RunsPage() {
  const tr = useT(); const { group } = useGroup();
  return <><PageHeading title={tr("Runs")} description="Execution, output handoff and evaluation are tracked separately for every run."
    actions={group.can_run && <Link prefetch={false} className="button primary" href={`/groups/${enc(group.id)}/runs/new/`}>{tr("New run")}</Link>} /><FlowPosition step="runs" />
    <RunHistory group={group.id} /></>;
}
export function NewRunPage() {
  const tr = useT(); const { group } = useGroup(); const onDirty = useDirtyGuard();
  return <><nav aria-label={tr("Breadcrumb")} className="breadcrumbs"><ol><li><Link prefetch={false} href={`/groups/${enc(group.id)}/runs/`}>{tr("Runs")}</Link></li><li><span aria-current="page">{tr("New run")}</span></li></ol></nav>
    <EvolutionManual group={group.id} onDirty={onDirty} />
    <p className="hint">{tr("To try an unpublished change, open a workflow draft and use Test draft. Draft tests never write to Volume Studio.")}</p></>;
}
export function EvaluationsPage() {
  const tr = useT(); const { group } = useGroup();
  return <><PageHeading title={tr("Evaluations")} description="Evaluation reports record what may be merged. Off means no benchmark exists; expert review is still available." /><EvaluationHistory group={group.id} /></>;
}
export function ConfigurationPage() {
  const { group } = useGroup(); const onDirty = useDirtyGuard();
  return <EvolutionConfiguration group={group.id} onDirty={onDirty} />;
}
export function SourcesPage() {
  const { group } = useGroup(); const onDirty = useDirtyGuard();
  return <EvolutionCapture group={group.id} onDirty={onDirty} />;
}
export function PermissionsPage() {
  const tr = useT(); const { group } = useGroup(); const onDirty = useDirtyGuard();
  const [permissions, setPermissions] = useState<Permissions | null>(null); const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    void central<Permissions>(`/api/admin/groups/${enc(group.id)}/optimization/permissions`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setPermissions(value); }).catch(() => { if (!abort.signal.aborted) setError("Grant management is unavailable. Retry or check group access."); });
    return () => abort.abort();
  }, [group.id]);
  const rows: [string, boolean][] = [["Read configuration", true], ["Manage configuration", group.can_manage], ["Edit and publish workflows", group.can_edit_workflows], ["Operate runs", group.can_run], ["Read source evidence", group.can_read_evidence]];
  return <>
    <PageHeading title={tr("Permissions")} description="Each capability is granted explicitly per group. Configuration access never grants source evidence, Studio review or publication authority." />
    <section className="card" aria-labelledby="permissions-heading"><h2 id="permissions-heading">{tr("Your permissions")}</h2><table className="permission-matrix"><thead><tr><th scope="col">{tr("Capability")}</th><th scope="col">{tr("Effective access")}</th></tr></thead><tbody>{rows.map(([label, allowed]) => <tr key={label}><th scope="row">{tr(label)}</th><td>{tr(allowed ? "Allowed" : "Not granted")}</td></tr>)}</tbody></table><p>{tr("Configuration access does not grant source evidence access.")}</p></section>
    {error && <p role="alert">{tr(error)}</p>}
    {permissions && (permissions.can_manage_grants ? <EvolutionPermissionsEditor group={group.id} actor={permissions.account_id} onDirty={onDirty} /> : <p className="hint">{tr("Only platform administrators change member grants.")}</p>)}
  </>;
}
