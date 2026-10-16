"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError, base, bytes, requestID, type Entry, type Version } from "../lib/api";
import { branchAPI, conflictAPI, type Branch, type BranchChange, type PreparedIntegration } from "../lib/branches";
import { currentReviews, type ReviewDecision } from "../lib/reviews";
import { useT } from "../lib/i18n";
import { ConflictEditor } from "./conflict-workspace";
import { useStudio } from "./studio-provider";

type Evaluation = {id:string;requirement_digest:string;state:string;outcome:string};
type View = { evaluations: Evaluation[]; evaluation: string; digest: string; identity: NonNullable<PreparedIntegration["identity"]>; readiness: string; acceptance: string; accepted_commit?: string; reviews: ReviewDecision[] };
type Change = { path: string; change: string };
type FilePreview = { commit_id: string; entry: Entry; text?: string; preview: string };
type CompositionOption = { branch: Branch; changes: BranchChange[] };
type CompositionSelection = { branch_id: string; head: string; selected_paths: string[]; deferred_paths?: string[] };
export function IntegrationReview({ branch }: { branch: Branch }) {
  const tr = useT();
  const studio = useStudio();
  const [workspace, setWorkspace] = useState("");
  const [conflictBlocked, setConflictBlocked] = useState(false);
  const [replacementWorkspace, setReplacementWorkspace] = useState("");
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
  const [tokens,setTokens]=useState("10000"),[cost,setCost]=useState("1000000"),[timeout,setTimeoutSeconds]=useState("60");
  const [release, setRelease] = useState<Version>();
  const [composition, setComposition] = useState<CompositionOption[]>([]);
  const [selectedComposition, setSelectedComposition] = useState<Record<string, string[]>>({});
  const [compositionResult, setCompositionResult] = useState<CompositionSelection[]>([]);
  const root = `/libraries/${encodeURIComponent(branch.library_id)}`;
  const readRequest = useRef<AbortController>(undefined), running = useRef(false), mounted = useRef(true);
  const [reloadRevision, setReloadRevision] = useState(0);
  const evidenceRevision = useRef(0);
  const unavailable = "Integration evidence is unavailable. Reload or check Volume access.";
  const hideEvidence = (preserveWorkspace = false) => {
    evidenceRevision.current++;
    setView(undefined); setChanges([]); setNext(""); setFile(undefined); setComposition([]); setCompositionResult([]); setRelease(undefined); if (!preserveWorkspace) { setConflicts([]); setWorkspace(""); }
  };
  const beginRead = () => { readRequest.current?.abort(); const abort = new AbortController(); readRequest.current = abort; return abort; };
  const pending = useRef<{ key: string; request: string } | null>(null);
  const mutate = async <T,>(key: string, action: (request: string) => Promise<T>): Promise<T> => {
    if (pending.current && pending.current.key !== key) throw new Error(tr("Resolve the pending operation before starting another action."));
    pending.current ??= { key, request: requestID() };
    try { const result = await action(pending.current.request); pending.current = null; return result; }
    catch (e) { if (e instanceof APIError && e.status < 500) pending.current = null; throw e; }
  };
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; readRequest.current?.abort(); };
  }, []);
  useEffect(() => {
    const revision = evidenceRevision.current;
    const params = new URL(location.href).searchParams;
    const conflict = params.get("conflict");
    if (conflict) {
      const abort = beginRead();
      // eslint-disable-next-line react-hooks/set-state-in-effect -- Restore only currently authorized URL-pinned evidence.
      setBusy(true);
      void conflictAPI.read(branch.library_id,conflict,abort.signal).then(value=>{if(!abort.signal.aborted&&revision===evidenceRevision.current){setWorkspace(value.id);setConflicts(value.conflicts);}}).catch(()=>{if(!abort.signal.aborted){hideEvidence();setError(unavailable);}}).finally(()=>{if(!abort.signal.aborted)setBusy(false);});
      return ()=>abort.abort();
    }
    const digest = params.get("integration");
    if (!digest) return;
    const abort = beginRead();
    setBusy(true);setError("");setView(undefined);setChanges([]);setFile(undefined);
    void Promise.all([
      api<View>(`${root}/integrations/${encodeURIComponent(digest)}`, { signal: abort.signal }),
      api<{ changes: Change[]; next: string }>(`${root}/integrations/${encodeURIComponent(digest)}/changes`, { signal: abort.signal }),
    ]).then(([current, page]) => { if (!abort.signal.aborted && revision === evidenceRevision.current) { setView(current); setChanges(page.changes); setNext(page.next); } }).catch(() => { if (!abort.signal.aborted) { hideEvidence(); setError(unavailable); } }).finally(() => { if (!abort.signal.aborted) setBusy(false); });
    return () => abort.abort();
  }, [root, branch.library_id, reloadRevision]);
  useEffect(() => {
    const abort = new AbortController();
    const revision = evidenceRevision.current;
    void (async () => {
      const page = await branchAPI.list(branch.library_id, "", abort.signal);
      const all = page.branches.some(item => item.id === branch.id) ? page.branches : [branch, ...page.branches];
      const options = await Promise.all(all.filter(item => item.head !== "" && item.head !== item.initial_base).map(async item => ({ branch: item, changes: (await branchAPI.headDiff(item, "", abort.signal)).changes })));
      if (abort.signal.aborted || revision !== evidenceRevision.current) return;
      setComposition(options);
      setSelectedComposition(previous => {
        const next: Record<string, string[]> = {};
        for (const option of options) next[option.branch.id] = previous[option.branch.id] ?? (option.branch.id === branch.id ? option.changes.map(change => change.path) : []);
        return next;
      });
    })().catch(() => { if (!abort.signal.aborted) { hideEvidence(true); setError(unavailable); } });
    return () => abort.abort();
  }, [branch, reloadRevision]);
  useEffect(() => {
    if (!release || !["pending", "validating"].includes(release.state)) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Version>(`${root}/versions/${encodeURIComponent(release.id)}`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setRelease(value); }).catch(() => { if (!abort.signal.aborted) { hideEvidence(); setError(unavailable); } });
    }, 2000);
    return () => { clearTimeout(timer); abort.abort(); };
  }, [release, root]);
  const publish = async () => {
    if (!studio.session || !view?.accepted_commit) return;
    const commit = view.accepted_commit;
    const csrf = studio.session.csrf;
    const version = await mutate(JSON.stringify(["release",commit,label,note]), request => branchAPI.release(branch.library_id,commit,label,note,request,csrf));
    if (mounted.current) { setRelease(version); studio.changed(); }
  };
  const run = async (action: () => Promise<void>) => {
    if (running.current) return;
    running.current = true; readRequest.current?.abort(); setBusy(true); setError("");
    try { await action(); } catch (e) {
      if (mounted.current) {
        if (e instanceof APIError && [401,403,404].includes(e.status)) hideEvidence();
        setError(e instanceof APIError ? String(e) : unavailable);
      }
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  };
  const load = async (digest: string) => {
    if (!mounted.current) return;
    const url = new URL(location.href); url.searchParams.set("integration", digest); url.searchParams.delete("conflict"); history.replaceState(null, "", url);
    const abort = beginRead(), revision = evidenceRevision.current;
    if (view?.digest !== digest) setRelease(undefined);
    setView(undefined); setChanges([]); setNext(""); setFile(undefined);
    try {
      const [current, page] = await Promise.all([
        api<View>(`${root}/integrations/${encodeURIComponent(digest)}`, { signal: abort.signal }),
        api<{ changes: Change[]; next: string }>(`${root}/integrations/${encodeURIComponent(digest)}/changes`, { signal: abort.signal }),
      ]);
      if (!abort.signal.aborted && revision === evidenceRevision.current) { setView(current); setChanges(page.changes); setNext(page.next); }
    } catch (cause) { if (!abort.signal.aborted) hideEvidence(); throw cause; }
  };
  const prepare = async (rebindDigest = "") => {
    if (!studio.session) return;
    const current = await branchAPI.read(branch.library_id, branch.id);
    const main = await branchAPI.main(branch.library_id);
    const policy = await api<{ revision: number }>(`${root}/merge-policy`);
    const csrf = studio.session.csrf;
    const prepared = await mutate(JSON.stringify(["prepare",current.id,current.head,main.commit_id,policy.revision,rebindDigest]), request => branchAPI.prepare(current, main, policy.revision, request, csrf, rebindDigest));
    if (!mounted.current) return;
    setReplacementWorkspace("");
    setConflicts(prepared.conflicts);
    setWorkspace(prepared.conflict_workspace_id ?? "");
    if(prepared.conflict_workspace_id){const url=new URL(location.href);url.searchParams.set("conflict",prepared.conflict_workspace_id);url.searchParams.delete("integration");history.replaceState(null,"",url);}
    if (prepared.identity) await load(prepared.digest); else { setView(undefined); setChanges([]); setFile(undefined); }
  };
  const compose = async () => {
    if (!studio.session) return;
    const sources = composition.filter(option => (selectedComposition[option.branch.id] ?? []).length > 0).map(option => ({ branch_id: option.branch.id, expected_head: option.branch.head, selected_paths: selectedComposition[option.branch.id] ?? [] }));
    if (!sources.length) return;
    const [main, policy] = await Promise.all([branchAPI.main(branch.library_id), api<{ revision: number }>(`${root}/merge-policy`)]);
    const result = await mutate(JSON.stringify(["compose", main.commit_id, policy.revision, sources]), request => branchAPI.compose(branch.library_id, main.commit_id, policy.revision, sources, request, studio.session!.csrf));
    if (!mounted.current) return;
    setCompositionResult(result.selections);
    setConflicts(result.conflicts ?? []);
    setWorkspace(result.conflict_workspace_id ?? "");
    if (result.conflict_workspace_id) {
      const url = new URL(location.href); url.searchParams.set("conflict", result.conflict_workspace_id); url.searchParams.delete("integration"); history.replaceState(null, "", url);
    }
    if (result.identity) await load(result.digest);
    else { setView(undefined); setChanges([]); setNext(""); setFile(undefined); setRelease(undefined); }
  };
  const inspect = async (change: Change) => {
    if (!view) return;
    const abort = beginRead(), revision = evidenceRevision.current; setFile(undefined);
    const read = (side: string) => api<FilePreview>(`${root}/integrations/${encodeURIComponent(view.digest)}/file?${new URLSearchParams({ side, path: change.path })}`, { signal: abort.signal });
    try {
      const [main, result] = await Promise.all([change.change === "added" ? undefined : read("main"), change.change === "removed" ? undefined : read("result")]);
      if (!abort.signal.aborted && revision === evidenceRevision.current) setFile({ path: change.path, main, result });
    } catch (cause) { if (!abort.signal.aborted) hideEvidence(); throw cause; }
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
  const requestEvaluation=async(requirement:string)=>{
   if(!view||!studio.session)return;
   const budget={max_tokens:Number(tokens),max_cost_micros:Number(cost),max_attempts:1,timeout_seconds:Number(timeout),max_artifact_bytes:1048576};
   const csrf=studio.session.csrf;
   await mutate(JSON.stringify(["evaluation",view.digest,requirement,budget]),request=>api(`${root}/integrations/${encodeURIComponent(view.digest)}/evaluations`,{method:"POST",body:JSON.stringify({request_id:request,requirement_digest:requirement,budget,seed:0})},csrf));
   await load(view.digest);
  };
  const reconcileEvaluation=async(job:Evaluation)=>{
   if(!studio.session||!view)return;
   await api(`${root}/evaluations/${encodeURIComponent(job.id)}/${job.state==="pending"?"retry":"refresh"}`,{method:"POST",body:"{}"},studio.session.csrf);
   await load(view.digest);
  };
  const cancelEvaluation=async(job:Evaluation)=>{
   if(!studio.session||!view)return;
   if(await studio.ask("Cancel evaluation", "Stop this evaluation? Dispatched work may still be reconciling and charged.", ["Keep evaluation","Cancel evaluation"])!=="Cancel evaluation")return;
   await api(`${root}/evaluations/${encodeURIComponent(job.id)}/cancel`,{method:"POST",body:"{}"},studio.session.csrf);
   await load(view.digest);
  };
  return <section className="notice" aria-label={tr("Integration review")}>
    <h2>{tr("Integration review")}</h2>
    <p>{tr("Prepare committed changes against main. Saved but uncommitted edits are excluded.")}</p>
    <button disabled={busy || conflictBlocked} onClick={() => void run(() => prepare())}>{tr("Prepare integration")}</button>
    <button disabled={busy || conflictBlocked} onClick={() => { hideEvidence(); setError(""); setReloadRevision(value => value + 1); }}>{tr("Reload integration review")}</button>
    {view?.acceptance === "not_accepted" && view.identity.sources.length === 1 && !view.identity.selections?.length && <button disabled={busy} onClick={() => void run(async () => { if (await studio.ask("Rebind current policy", "Replace retained evaluation requirements with current policy? This requires a new approval.", ["Keep current requirements", "Rebind current policy"]) === "Rebind current policy") await prepare(view.digest); })}>{tr("Rebind current policy")}</button>}
    {conflictBlocked && <p role="status">{tr("Save or discard local conflict work before replacing this workspace.")}</p>}
    {composition.length > 0 && <section aria-label={tr("Compose selected changes")}>
      <h3>{tr("Compose selected changes")}</h3>
      <p>{tr("Freeze committed paths from more than one branch into one reviewable integration. Unselected paths remain deferred.")}</p>
      {composition.map(option => <fieldset key={option.branch.id}>
        <legend>{option.branch.name}</legend>
        {option.changes.length === 0 ? <p>{tr("No committed changes")}</p> : option.changes.map(change => <label key={change.path}>
          <input type="checkbox" checked={(selectedComposition[option.branch.id] ?? []).includes(change.path)} onChange={event => setSelectedComposition(previous => ({ ...previous, [option.branch.id]: event.target.checked ? [...(previous[option.branch.id] ?? []), change.path] : (previous[option.branch.id] ?? []).filter(path => path !== change.path) }))} />
          <code>{change.path}</code> · {tr(change.change)}
        </label>)}
      </fieldset>)}
      <button disabled={busy || conflictBlocked || composition.every(option => (selectedComposition[option.branch.id] ?? []).length === 0)} onClick={() => void run(compose)}>{tr("Prepare selected composition")}</button>
      {compositionResult.length > 0 && <ul aria-label={tr("Composition selection result")}>{compositionResult.map(selection => <li key={selection.branch_id}><code>{selection.branch_id}</code>: {selection.selected_paths.join(", ") || tr("No paths selected")}{selection.deferred_paths?.length ? ` · ${tr("Deferred")}: ${selection.deferred_paths.join(", ")}` : ""}</li>)}</ul>}
    </section>}
    {error && <p role="alert">{tr(error)}</p>}
    {conflicts.length > 0 && !workspace && <ul>{conflicts.map(conflict => <li key={conflict.path}><code>{conflict.path}</code> — {tr(conflict.reason)}</li>)}</ul>}
    {replacementWorkspace && <p role="status">{tr("Replacement saved to the working draft. Commit saved draft, then prepare integration again.")} <a href={`?${new URLSearchParams({ branch: branch.id, conflict: replacementWorkspace })}`}>{tr("Earlier conflict workspace")}</a></p>}
    {workspace && <ConflictEditor key={`${branch.library_id}:${workspace}`} library={branch.library_id} id={workspace} onBlockedChange={setConflictBlocked} onReplacement={() => { setReplacementWorkspace(workspace); setWorkspace(""); setConflicts([]); const url = new URL(location.href); url.searchParams.delete("conflict"); history.replaceState(null, "", url); studio.changed(); }} onComplete={digest=>{setWorkspace("");setConflicts([]);void run(()=>load(digest));}} />}
    {view && <>
      <section aria-label={tr("Evaluation checks")}>
       <h3>{tr("Evaluation checks")}</h3>
       {view.evaluation === "not_requested" ? <p>{tr("Evaluation is off. No benchmark result was requested.")}</p> : <>
        <p>{tr(view.evaluation==="pass"?"Evaluation evidence passed for this exact integrated result.":view.identity.checks.some(check=>check.mode==="required")?"Required checks need passing evidence for this exact integrated result.":"Informational checks do not block approval or merge.")}</p>
        <div className="evaluation-budget">
         <label>{tr("Evaluation token limit")} <input type="number" min="1" value={tokens} onChange={event=>setTokens(event.target.value)} disabled={busy}/></label>
         <label>{tr("Evaluation cost limit (micros)")} <input type="number" min="0" value={cost} onChange={event=>setCost(event.target.value)} disabled={busy}/></label>
         <label>{tr("Evaluation timeout (seconds)")} <input type="number" min="1" value={timeout} onChange={event=>setTimeoutSeconds(event.target.value)} disabled={busy}/></label>
        </div>
        <ul>{view.identity.checks.map(check => <li key={check.digest}>
         <strong>{tr(check.mode === "required" ? "Required evaluation" : "Informational evaluation")}</strong> · <code>{check.path}</code>
         <p>{tr("Evaluation profile")}: {check.profile.id} · {check.profile.revision}</p>
         <p>{tr("Evaluation service")}: {check.service.id} · {check.service.revision}</p>
         <p>{tr("Reference strategy")}: {tr(check.reference_strategy)} {check.reference && <code>{check.reference.commit_id}</code>}</p>
         <p>{tr("Policy")}: <code>{check.policy_id}</code> · {check.policy_revision}</p>
         {view.evaluations.filter(job=>job.requirement_digest===check.digest).map(job=><div key={job.id}><p><code>{job.id}</code> · {tr(job.state)} · {tr(job.outcome)}</p>{(job.state==="pending"||job.state==="admitted"||job.state==="cancelling")&&<button disabled={busy} onClick={()=>void run(()=>reconcileEvaluation(job))}>{tr(job.state==="pending"?"Retry evaluation admission":"Refresh evaluation")}</button>}{(job.state==="pending"||job.state==="admitted")&&<button disabled={busy} onClick={()=>void run(()=>cancelEvaluation(job))}>{tr("Cancel evaluation")}</button>}{job.state==="cancelling"&&<p>{tr("Cancellation requested; spending remains reserved until reconciliation finishes.")}</p>}</div>)}
         <button disabled={busy||view.evaluations.some(job=>job.requirement_digest===check.digest&&(job.state==="pending"||job.state==="admitted"||job.state==="cancelling"))} onClick={()=>void run(()=>requestEvaluation(check.digest))}>{tr("Request evaluation")}</button>
        </li>)}</ul>
       </>}
      </section>
      <p>{tr(view.acceptance === "accepted" ? "Accepted commit" : view.readiness === "ready_for_review" ? "Ready for expert review" : "Integration needs attention")} · <code>{view.accepted_commit ?? view.identity.result.commit_id}</code></p>
      <ul>{changes.map(change => <li key={change.path}><button disabled={busy} onClick={() => void run(() => inspect(change))}>{tr(change.change)}: {change.path}</button></li>)}</ul>
      {next && <button disabled={busy} onClick={() => void run(async () => { const abort = beginRead(), revision = evidenceRevision.current; try { const page = await api<{ changes: Change[]; next: string }>(`${root}/integrations/${encodeURIComponent(view.digest)}/changes?${new URLSearchParams({ after: next })}`, { signal: abort.signal }); if (!abort.signal.aborted && revision === evidenceRevision.current) { setChanges(items => [...items, ...page.changes]); setNext(page.next); } } catch (cause) { if (!abort.signal.aborted) hideEvidence(); throw cause; } })}>{tr("Load more")}</button>}
      {file && <section aria-label={tr("Integrated file comparison")}><h3>{file.path}</h3>{(["main", "result"] as const).map(side => {
        const preview = file[side];
        return <section key={side} aria-label={tr(side === "main" ? "Main before merge" : "Integrated result")}>
          <h4>{tr(side === "main" ? "Main before merge" : "Integrated result")}</h4>
          {!preview ? <p>{tr("File absent")}</p> : <>
            <p><code>{preview.commit_id}</code></p><p>{tr(preview.entry.kind === "file" ? "File" : "Directory")} · {bytes(preview.entry.size)}</p><code>{preview.entry.digest}</code>
            <pre>{preview.text ?? tr(preview.preview === "binary" ? "Binary file: text preview unavailable." : preview.preview === "too_large" ? "File exceeds the 1 MiB text preview limit." : "Directory")}</pre>
            {preview.entry.kind === "file" && <a href={`${base}/api/v1${root}/commits/${preview.commit_id}/content?${new URLSearchParams({ path: file.path })}`}>{tr("Download exact file")}</a>}
          </>}
        </section>;
      })}</section>}
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
        <button disabled={busy || view.readiness !== "ready_for_review" || !currentReviews(view.reviews).some(review => review.verdict === "approve")} onClick={() => void run(merge)}>{tr("Merge approved integration")}</button>
      </>}
    </>}
  </section>;
}
