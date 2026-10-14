"use client";
import { useEffect, useState } from "react";
import { api, requestID } from "../lib/api";
import { conflictAPI, type ConflictWorkspace } from "../lib/branches";
import { useT } from "../lib/i18n";
import { useStudio } from "./studio-provider";
type Preview = { text?: string; preview: string; commit_id: string };
export function ConflictEditor({ library, id, onComplete }: { library: string; id: string; onComplete: (digest: string) => void }) {
  const tr = useT(); const studio = useStudio();
  const [workspace, setWorkspace] = useState<ConflictWorkspace>();
  const [path, setPath] = useState("");
  const [previews, setPreviews] = useState<Preview[]>([]);
  const [choice, setChoice] = useState<"main" | "branch" | "delete" | "text">("main");
  const [text, setText] = useState(""); const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  useEffect(() => { const abort = new AbortController(); void conflictAPI.read(library,id,abort.signal).then(value => { setWorkspace(value); }).catch(e => { if (!abort.signal.aborted) setError(String(e)); }); return () => abort.abort(); }, [library,id]);
  const run = async (action: () => Promise<void>) => { setBusy(true);setError("");try{await action();}catch(e){setError(String(e));}finally{setBusy(false);} };
  const inspect = async (selected: string) => {
    const values = await Promise.all(["base","main","branch"].map(side => api<Preview>(`/libraries/${library}/conflicts/${id}/file?${new URLSearchParams({side,path:selected})}`)));
    setPath(selected);setPreviews(values);const saved=workspace?.resolutions.find(item=>item.path===selected);setChoice(saved?.choice ?? "main");setText(saved?.text ?? "");
  };
  const save = async () => { if (!workspace || !studio.session) return; await conflictAPI.save(workspace,path,choice,choice==="text"?text:"",requestID(),studio.session.csrf);setWorkspace(await conflictAPI.read(library,id)); };
  const complete = async () => { if (!workspace || !studio.session) return;const prepared=await conflictAPI.complete(workspace,requestID(),studio.session.csrf);if(prepared.identity)onComplete(prepared.digest); };
  return <section aria-label={tr("Resolve merge conflicts")}>
    <h3>{tr("Resolve merge conflicts")}</h3>
    {error && <p role="alert">{error}</p>}
    {workspace?.stale && <p role="alert">{tr("Inputs changed. Prepare a new integration before resolving.")}</p>}
    <ul>{workspace?.conflicts.map(conflict=><li key={conflict.path}><button disabled={busy} onClick={()=>void run(()=>inspect(conflict.path))}>{conflict.path}</button> — {workspace.resolutions.some(item=>item.path===conflict.path)?tr("Resolution saved"):tr("Unresolved")}</li>)}</ul>
    {path && <><h4>{path}</h4><div className="conflict-sides">{previews.map((preview,i)=><section key={i}><h4>{tr(["Common ancestor","Main before merge","Branch candidate"][i])}</h4><pre>{preview.text ?? tr(preview.preview)}</pre></section>)}</div>
      <label>{tr("Resolution choice")} <select aria-label={tr("Resolution choice")} value={choice} disabled={busy||workspace?.stale} onChange={event=>setChoice(event.target.value as typeof choice)}><option value="main">{tr("Use main")}</option><option value="branch">{tr("Use branch")}</option><option value="delete">{tr("Delete path")}</option><option value="text">{tr("Write resolved text")}</option></select></label>
      {choice==="text"&&<label>{tr("Resolved text")} <textarea value={text} onChange={event=>setText(event.target.value)} disabled={busy||workspace?.stale}/></label>}
      <button disabled={busy||workspace?.stale} onClick={()=>void run(save)}>{tr("Save resolution")}</button></>}
    <button disabled={busy||!workspace||workspace.stale||workspace.resolutions.length!==workspace.conflicts.length} onClick={()=>void run(complete)}>{tr("Prepare resolved integration")}</button>
  </section>;
}
