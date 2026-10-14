"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError, requestID, type Version } from "../lib/api";
import { branchAPI, conflictAPI, type Branch, type PreparedIntegration } from "../lib/branches";
import { useT } from "../lib/i18n";
import { ConflictEditor } from "./conflict-workspace";
import { useStudio } from "./studio-provider";

type View = { digest: string; identity: NonNullable<PreparedIntegration["identity"]>; readiness: string; acceptance: string; accepted_commit?: string; reviews: { id: string; verdict: string; rationale: string }[] };
type Change = { path: string; change: string };
type FilePreview = { commit_id: string; text?: string; preview: string };
export function IntegrationReview({ branch }: { branch: Branch }) {
  const tr = useT();
  const studio = useStudio();
  const [workspace, setWorkspace] = useState("");
  const [view, setView] = useState<View>();
  const [changes, setChanges] = useState<Change[]>([]);
  const [next, setNext] = useState("");
  const [conflicts, setConflicts] = useState<PreparedIntegration["conflicts"]>([]);
  const [file, setFile] = useState<{ path: string; main?: FilePreview; result?: FilePreview }>();
  const [rationale, setRationale] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [label, setLabel] = useState("");
  const [note, setNote] = useState("");
  const [release, setRelease] = useState<Version>();
  const root = `/libraries/${encodeURIComponent(branch.library_id)}`;
  const pending = useRef<{ key: string; request: string } | null>(null);
  const mutate = async <T,>(key: string, action: (request: string) => Promise<T>): Promise<T> => {
    if (pending.current && pending.current.key !== key) throw new Error(tr("Resolve the pending operation before starting another action."));
    pending.current ??= { key, request: requestID() };
    try { const result = await action(pending.current.request); pending.current = null; return result; }
    catch (e) { if (e instanceof APIError && e.status < 500) pending.current = null; throw e; }
  };
  useEffect(() => {
    const params = new URL(location.href).searchParams;
    const conflict = params.get("conflict");
    if (conflict) {
      const abort = new AbortController();
      void conflictAPI.read(branch.library_id,conflict,abort.signal).then(value=>{if(!abort.signal.aborted){setWorkspace(value.id);setConflicts(value.conflicts);}}).catch(e=>{if(!abort.signal.aborted)setError(String(e));});
      return ()=>abort.abort();
    }
    const digest = params.get("integration");
    if (!digest) return;
    const abort = new AbortController();
    void Promise.all([
      api<View>(`${root}/integrations/${encodeURIComponent(digest)}`, { signal: abort.signal }),
      api<{ changes: Change[]; next: string }>(`${root}/integrations/${encodeURIComponent(digest)}/changes`, { signal: abort.signal }),
    ]).then(([current, page]) => { if (!abort.signal.aborted) { setView(current); setChanges(page.changes); setNext(page.next); } }).catch(e => { if (!abort.signal.aborted) setError(String(e)); });
    return () => abort.abort();
  }, [root, branch.library_id]);
  useEffect(() => {
    if (!release || !["pending", "validating"].includes(release.state)) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Version>(`${root}/versions/${encodeURIComponent(release.id)}`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setRelease(value); }).catch(e => { if (!abort.signal.aborted) setError(String(e)); });
    }, 2000);
    return () => { clearTimeout(timer); abort.abort(); };
  }, [release, root]);
  const publish = async () => {
    if (!studio.session || !view?.accepted_commit) return;
    const commit = view.accepted_commit;
    const csrf = studio.session.csrf;
    const version = await mutate(JSON.stringify(["release",commit,label,note]), request => branchAPI.release(branch.library_id,commit,label,note,request,csrf));
    setRelease(version); studio.changed();
  };
  const run = async (action: () => Promise<void>) => { setBusy(true); setError(""); try { await action(); } catch (e) { setError(String(e)); } finally { setBusy(false); } };
  const load = async (digest: string) => {
    const current = await api<View>(`${root}/integrations/${encodeURIComponent(digest)}`);
    const page = await api<{ changes: Change[]; next: string }>(`${root}/integrations/${encodeURIComponent(digest)}/changes`);
    setView(current); setChanges(page.changes); setNext(page.next); setFile(undefined);
    const url = new URL(location.href); url.searchParams.set("integration", digest); url.searchParams.delete("conflict"); history.replaceState(null, "", url);
  };
  const prepare = async () => {
    if (!studio.session) return;
    const current = await branchAPI.read(branch.library_id, branch.id);
    const main = await branchAPI.main(branch.library_id);
    const policy = await api<{ revision: number }>(`${root}/merge-policy`);
    const csrf = studio.session.csrf;
    const prepared = await mutate(JSON.stringify(["prepare",current.id,current.head,main.commit_id,policy.revision]), request => branchAPI.prepare(current, main, policy.revision, request, csrf));
    setConflicts(prepared.conflicts);
    setWorkspace(prepared.conflict_workspace_id ?? "");
    if(prepared.conflict_workspace_id){const url=new URL(location.href);url.searchParams.set("conflict",prepared.conflict_workspace_id);url.searchParams.delete("integration");history.replaceState(null,"",url);}
    if (prepared.identity) await load(prepared.digest); else { setView(undefined); setChanges([]); setFile(undefined); }
  };
  const inspect = async (change: Change) => {
    if (!view) return;
    const read = (side: string) => api<FilePreview>(`${root}/integrations/${encodeURIComponent(view.digest)}/file?${new URLSearchParams({ side, path: change.path })}`);
    const [main, result] = await Promise.all([change.change === "added" ? undefined : read("main"), change.change === "removed" ? undefined : read("result")]);
    setFile({ path: change.path, main, result });
  };
  const review = async (verdict: "approve" | "changes_requested" | "reject") => {
    if (!view || !studio.session) return;
    const csrf = studio.session.csrf;
    await mutate(JSON.stringify(["review",view.digest,verdict,rationale]), request => branchAPI.review(branch.library_id, view.digest, verdict, rationale, request, csrf));
    await load(view.digest);
  };
  const merge = async () => {
    if (!view || !studio.session) return;
    const csrf = studio.session.csrf;
    await mutate(JSON.stringify(["merge",view.digest,view.identity.main.commit_id]), request => branchAPI.merge(branch.library_id, view.digest, view.identity.main.commit_id, request, csrf));
    await load(view.digest); studio.changed();
  };
  return <section className="notice" aria-label={tr("Integration review")}>
    <h2>{tr("Integration review")}</h2>
    <p>{tr("Prepare committed changes against main. Saved but uncommitted edits are excluded.")}</p>
    <button disabled={busy} onClick={() => void run(prepare)}>{tr("Prepare integration")}</button>
    {error && <p role="alert">{error}</p>}
    {conflicts.length > 0 && <ul>{conflicts.map(conflict => <li key={conflict.path}><code>{conflict.path}</code> — {tr(conflict.reason)}</li>)}</ul>}
    {workspace && <ConflictEditor library={branch.library_id} id={workspace} onComplete={digest=>{setWorkspace("");setConflicts([]);void run(()=>load(digest));}} />}
    {view && <>
      <p>{tr(view.acceptance === "accepted" ? "Accepted commit" : view.readiness === "ready_for_review" ? "Ready for expert review" : "Integration needs attention")} · <code>{view.accepted_commit ?? view.identity.result.commit_id}</code></p>
      <ul>{changes.map(change => <li key={change.path}><button disabled={busy} onClick={() => void run(() => inspect(change))}>{change.change}: {change.path}</button></li>)}</ul>
      {next && <button disabled={busy} onClick={() => void run(async () => { const page = await api<{ changes: Change[]; next: string }>(`${root}/integrations/${encodeURIComponent(view.digest)}/changes?${new URLSearchParams({ after: next })}`); setChanges(items => [...items, ...page.changes]); setNext(page.next); })}>{tr("Load more")}</button>}
      {file && <div><h3>{file.path}</h3>{(["main", "result"] as const).map(side => <section key={side}><h4>{tr(side === "main" ? "Main before merge" : "Integrated result")}</h4><pre>{file[side]?.text ?? tr(file[side] ? "Binary, directory or large file: text preview unavailable." : "File absent")}</pre></section>)}</div>}
      {view.reviews.map(review => <p key={review.id}>{review.verdict}: {review.rationale}</p>)}
      {view.acceptance === "accepted" && <section aria-label={tr("Publish accepted commit")}>
        <p>{tr("Acceptance is complete. Publishing creates a version; it does not deploy to Projects.")}</p>
        <label>{tr("Version label")} <input value={label} disabled={busy} onChange={event => setLabel(event.target.value)} /></label>
        <label>{tr("Release note")} <textarea value={note} disabled={busy} onChange={event => setNote(event.target.value)} /></label>
        <button disabled={busy || !label.trim()} onClick={() => void run(publish)}>{tr("Publish accepted commit")}</button>
        {release && <p role="status">{release.label}: {tr(release.state)}{release.error ? ` · ${tr(release.error)}` : ""}</p>}
      </section>}
      {view.acceptance === "not_accepted" && <>
        <label>{tr("Review rationale")} <textarea value={rationale} onChange={event => setRationale(event.target.value)} disabled={busy} /></label>
        <button disabled={busy || view.readiness !== "ready_for_review"} onClick={() => void run(() => review("approve"))}>{tr("Approve integration")}</button>
        <button disabled={busy} onClick={() => void run(() => review("changes_requested"))}>{tr("Request changes")}</button>
        <button disabled={busy} onClick={() => void run(() => review("reject"))}>{tr("Reject integration")}</button>
        <button disabled={busy || view.readiness !== "ready_for_review" || !view.reviews.some(review => review.verdict === "approve")} onClick={() => void run(merge)}>{tr("Merge approved integration")}</button>
      </>}
    </>}
  </section>;
}
