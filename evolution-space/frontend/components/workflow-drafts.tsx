"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { base, type Group, type Session } from "../lib/api";
import { useRead } from "../lib/use-read";
import { useT } from "../lib/i18n";

type Draft = { id: string; revision: number; layout_revision: number };
type Page = { items: Draft[]; next_after: string; has_more: boolean };
type Creation = { id: string; source_draft_id?: string; source_revision?: number; source_run_id?: string };

export function WorkflowDrafts({ group, editable, evidence }: { group: string; editable: boolean; evidence: boolean }) {
 const tr = useT(); const router = useRouter();
 const [after, setAfter] = useState(""); const read = useRead<Page>(`/groups/${encodeURIComponent(group)}/workflows?limit=25&after=${encodeURIComponent(after)}`);
 const [open, setOpen] = useState(false); const [busy, setBusy] = useState(false); const [allowed, setAllowed] = useState(false);
 const [error, setError] = useState(""); const [pending, setPending] = useState<Creation>();
 const [account, setAccount] = useState("");
 const [newID, setNewID] = useState(""); const [mode, setMode] = useState("empty"); const [source, setSource] = useState(""); const [revision, setRevision] = useState(1);
 const root = `${base}/api/v1/groups/${encodeURIComponent(group)}`;
 const key = (session: Session) => `evaluation-draft-create:${session.issuer}:${session.subject}:${group}`;
 async function authorize(runSource = false) {
  const response = await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" }); if (!response.ok) throw new Error();
  const session = await response.json() as Session;
  const access = await fetch(root, { credentials: "same-origin", cache: "no-store" }); if (!access.ok) throw new Error();
  const result = await access.json() as { items: Group[] }; const current = result.items.find(item => item.id === group);
  if (!current?.can_edit_workflows || runSource && !current.can_read_evidence) throw new Error();
  return session;
 }
 function clear() { setAllowed(false); setAccount(""); setPending(undefined); setNewID(""); setSource(""); setMode("empty"); setRevision(1); }
 async function load() {
  setOpen(true); setBusy(true); setError(""); clear();
  try {
   const session = await authorize(); const raw = sessionStorage.getItem(key(session));
   if (raw) {
    const request = JSON.parse(raw) as Creation;
    if (!request.id || request.source_draft_id && (!Number.isSafeInteger(request.source_revision) || request.source_revision! < 1 || request.source_run_id)) throw new Error();
    if (request.source_run_id) await authorize(true);
    setPending(request); setNewID(request.id); setSource(request.source_draft_id ?? request.source_run_id ?? ""); setRevision(request.source_revision ?? 1); setMode(request.source_draft_id ? "draft" : request.source_run_id ? "run" : "empty");
   } else setNewID(`workflow-${crypto.randomUUID()}`);
   setAccount(key(session)); setAllowed(true);
  } catch { clear(); setError("Draft creation is unavailable. Check workflow and source permissions, then retry."); } finally { setBusy(false); }
 }
 const valid = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(newID) && (mode === "empty" || /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(source)) && (mode !== "draft" || Number.isSafeInteger(revision) && revision > 0);
 async function create() {
  if (!allowed || !pending && !valid) return; setBusy(true); setError("");
  try {
   let session: Session;
   try { session = await authorize(!!pending?.source_run_id || mode === "run"); if (key(session) !== account) throw new Error(); } catch { clear(); setError("Draft creation is unavailable. Check workflow and source permissions, then retry."); return; }
   const request = pending ?? { id: newID, ...(mode === "draft" ? { source_draft_id: source, source_revision: revision } : mode === "run" ? { source_run_id: source } : {}) };
   sessionStorage.setItem(key(session), JSON.stringify(request)); setPending(request);
   const response = await fetch(`${root}/workflows`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(request) });
   if (!response.ok) {
    if ([400, 404, 409].includes(response.status)) { sessionStorage.removeItem(key(session)); setPending(undefined); setError("Draft creation was rejected. Choose a fresh ID and an accessible source revision."); return; }
    if (response.status === 401 || response.status === 403) clear();
    throw new Error();
   }
   const result = await response.json() as Draft; if (result.id !== request.id) throw new Error();
   sessionStorage.removeItem(key(session)); router.push(`/groups/${encodeURIComponent(group)}/workflows/${encodeURIComponent(result.id)}/`);
  } catch { setError("Draft creation response is unavailable. Recover the pending request before creating another draft."); } finally { setBusy(false); }
 }
 return <section className="card experiment-form" aria-label={tr("Workflow drafts")}><div className="section-heading"><h2>{tr("Workflow drafts")}</h2><button onClick={read.refresh}>{tr("Refresh")}</button></div>
  {read.loading && <p role="status">{tr("Loading workflow drafts…")}</p>}
  {read.status && <p role="alert">{tr("Workflow drafts are unavailable. Retry or check group access.")}</p>}
  {read.data && <>{!read.data.items.length && <p>{tr("No saved workflow drafts yet.")}</p>}<ul>{read.data.items.map(item => <li key={item.id}><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/workflows/${encodeURIComponent(item.id)}/`}>{item.id}</Link> · {tr("Draft revision")} {item.revision}</li>)}</ul>{after && <button onClick={() => setAfter("")}>{tr("First page")}</button>}{read.data.has_more && <button onClick={() => setAfter(read.data!.next_after)}>{tr("Next page")}</button>}</>}
  {editable && !open && <button onClick={() => void load()}>{tr("New or fork draft")}</button>}
  {open && <><p>{tr("Create an empty draft or copy an exact saved draft revision or frozen run workflow. The source stays unchanged.")}</p>
   <fieldset disabled={busy || !allowed || !!pending}><label>{tr("New draft ID")}<input aria-label={tr("New draft ID")} value={newID} onChange={event => setNewID(event.target.value)} /></label>
    <label>{tr("Draft starting point")}<select aria-label={tr("Draft starting point")} value={mode} onChange={event => { setMode(event.target.value); setSource(""); }}><option value="empty">{tr("Empty draft")}</option><option value="draft">{tr("Saved draft revision")}</option>{(evidence || mode === "run") && <option value="run">{tr("Frozen run workflow")}</option>}</select></label>
    {mode !== "empty" && <label>{tr(mode === "draft" ? "Source draft ID" : "Source run ID")}<input aria-label={tr(mode === "draft" ? "Source draft ID" : "Source run ID")} value={source} onChange={event => setSource(event.target.value)} /></label>}
    {mode === "draft" && <label>{tr("Source draft revision")}<input aria-label={tr("Source draft revision")} type="number" min={1} value={revision} onChange={event => setRevision(Number(event.target.value))} /></label>}
   </fieldset>
   {pending && <p role="status">{tr("A draft creation request is pending. Recovery opens the same draft.")} <code>{pending.id}</code></p>}
   {error && <p role="alert">{tr(error)}</p>}
   <button disabled={busy || !allowed || !pending && !valid} onClick={() => void create()}>{tr(pending ? "Recover draft creation" : "Create draft")}</button>
   {!allowed && <button disabled={busy} onClick={() => void load()}>{tr("Retry")}</button>}
   <button disabled={busy} onClick={() => { setOpen(false); clear(); }}>{tr("Close")}</button>
  </>}
 </section>;
}
