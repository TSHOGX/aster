"use client";
import { useEffect, useState } from "react";
import { api, base, bytes, type Entry } from "../lib/api";
import { branchAPI } from "../lib/branches";
import { useT } from "../lib/i18n";
import { intlLocale } from "../../../web/i18n/locale";
import { TextDiff } from "./changes";

type Commit = {id:string;parents:string[];created_at:number;message:string};
type HistoryPage = {head:string;commits:Commit[];next:string};
type Change = {path:string;change:string;before?:Entry;after?:Entry};
type Comparison = {base:{commit_id:string};target:{commit_id:string};changes:Change[];next:string;total:number};
type Preview = {commit_id:string;entry:Entry;text?:string;preview:string};

export function History({library,branch}:{library:string;branch?:string}) {
 const tr=useT();
 const root=`/libraries/${encodeURIComponent(library)}`;
 const [head,setHead]=useState("");
 const [from,setFrom]=useState("");
 const [to,setTo]=useState("");
 const [history,setHistory]=useState<HistoryPage>();
 const [comparison,setComparison]=useState<Comparison>();
 const [selected,setSelected]=useState<{path:string;before?:Preview;after?:Preview}>();
 const [error,setError]=useState("");
 const [busy,setBusy]=useState(false);
 const [attempt,setAttempt]=useState(0);
 useEffect(()=>{
  const abort=new AbortController();
  void(async()=>{
   const params=new URL(location.href).searchParams;
   const pinned=params.get("history_head") || (await branchAPI.main(library,abort.signal)).commit_id;
   const page=await api<HistoryPage>(`${root}/history?${new URLSearchParams({head:pinned})}`,{signal:abort.signal});
   if(abort.signal.aborted)return;
   const baseCommit=params.get("compare_base");
   const targetCommit=params.get("compare_target");
   const restored=baseCommit&&targetCommit?await api<Comparison>(`${root}/compare?${new URLSearchParams({base:baseCommit,target:targetCommit})}`,{signal:abort.signal}):undefined;
   if(abort.signal.aborted)return;
   setHead(pinned);setHistory(page);setFrom(baseCommit||page.commits[0]?.parents[0]||pinned);setTo(targetCommit||pinned);setComparison(restored);setError("");
   const url=new URL(location.href);url.searchParams.set("history_head",pinned);window.history.replaceState(null,"",url);
  })().catch(e=>{if(!abort.signal.aborted)setError(String(e));});
  return()=>abort.abort();
 },[library,root,attempt]);
 const run=async(action:()=>Promise<void>)=>{setBusy(true);setError("");try{await action();}catch(e){setError(String(e));}finally{setBusy(false);}};
 const pin=(values:Record<string,string>)=>{
  const url=new URL(location.href);for(const [key,value]of Object.entries(values))url.searchParams.set(key,value);
  window.history.replaceState(null,"",url);
 };
 const loadHead=async(pinned:string)=>{
  const page=await api<HistoryPage>(`${root}/history?${new URLSearchParams({head:pinned})}`);
  setHistory(page);setHead(pinned);pin({history_head:pinned});
 };
 const compare=async()=>{
  const page=await api<Comparison>(`${root}/compare?${new URLSearchParams({base:from,target:to})}`);
  setComparison(page);setSelected(undefined);pin({compare_base:from,compare_target:to});
 };
 const inspect=async(change:Change)=>{
  if(!comparison)return;
  const read=(commit:string)=>api<Preview>(`${root}/commits/${commit}/file?${new URLSearchParams({path:change.path})}`);
  const [before,after]=await Promise.all([change.before?read(comparison.base.commit_id):undefined,change.after?read(comparison.target.commit_id):undefined]);
  setSelected({path:change.path,before,after});
 };
 return <section className="history-panel" aria-label={tr("Commit history and comparison")}>
  <h2>{tr("History")}</h2>
  <p>{tr("History and comparisons use immutable commits. Saved drafts and later branch changes are excluded.")}</p>
  <div className="actions">
   <button disabled={busy} onClick={()=>void run(async()=>loadHead((await branchAPI.main(library)).commit_id))}>{tr("Load current main")}</button>
   {branch&&<button disabled={busy} onClick={()=>void run(async()=>loadHead((await branchAPI.read(library,branch)).head))}>{tr("Load branch head")}</button>}
  </div>
  <form onSubmit={event=>{event.preventDefault();void run(()=>loadHead(head));}}>
   <label>{tr("History head")} <input value={head} onChange={event=>setHead(event.target.value)} disabled={busy}/></label>
   <button disabled={busy||!head}>{tr("Load history")}</button>
  </form>
  {error&&<div role="alert"><p>{tr(error)}</p><button disabled={busy} onClick={()=>setAttempt(value=>value+1)}>{tr("Retry")}</button></div>}
  {!history&&!error&&<p role="status">{tr("Loading history…")}</p>}
  {history&&<>
   <p>{tr("Pinned history head")}: <code>{history.head}</code></p>
   <ol className="commit-list">{history.commits.map(commit=><li key={commit.id}>
    <code>{commit.id}</code><p className="preserve-lines">{commit.message}</p>
    <time dateTime={new Date(commit.created_at*1000).toISOString()}>{new Date(commit.created_at*1000).toLocaleString(intlLocale())}</time>
    <div>{tr("Parents")}: {commit.parents.length?commit.parents.map(parent=><button key={parent} disabled={busy} onClick={()=>void run(()=>loadHead(parent))}><code>{parent}</code></button>):tr("None")}</div>
    <button disabled={busy} onClick={()=>setFrom(commit.id)}>{tr("Use as comparison base")}</button>
    <button disabled={busy} onClick={()=>setTo(commit.id)}>{tr("Use as comparison target")}</button>
   </li>)}</ol>
   {history.next&&<button disabled={busy} onClick={()=>void run(async()=>{const page=await api<HistoryPage>(`${root}/history?${new URLSearchParams({head:history.head,cursor:history.next})}`);setHistory({...page,commits:[...history.commits,...page.commits]});})}>{tr("Load more commits")}</button>}
  </>}
  <h2>{tr("Compare snapshots")}</h2>
  <form onSubmit={event=>{event.preventDefault();void run(compare);}}>
   <label>{tr("Base commit")} <input value={from} disabled={busy} onChange={event=>setFrom(event.target.value)}/></label>
   <label>{tr("Target commit")} <input value={to} disabled={busy} onChange={event=>setTo(event.target.value)}/></label>
   <button disabled={busy||!from||!to}>{tr("Compare snapshots")}</button>
  </form>
  {comparison&&<>
   <p><code>{comparison.base.commit_id}</code> → <code>{comparison.target.commit_id}</code></p>
   {comparison.total===0&&<p>{tr("These snapshots have identical content.")}</p>}
   <ul>{comparison.changes.map(change=><li key={change.path}><button disabled={busy} onClick={()=>void run(()=>inspect(change))}>{change.path} · {tr(change.change)}</button></li>)}</ul>
   {comparison.next&&<button disabled={busy} onClick={()=>void run(async()=>{const page=await api<Comparison>(`${root}/compare?${new URLSearchParams({base:comparison.base.commit_id,target:comparison.target.commit_id,cursor:comparison.next})}`);setComparison({...page,changes:[...comparison.changes,...page.changes]});})}>{tr("Load more changes")}</button>}
  </>}
  {selected&&<section aria-label={tr("Snapshot file comparison")}>
   <h3>{selected.path}</h3>
   <div className="snapshot-metadata">{(["before","after"]as const).map(side=>{const preview=selected[side];return <section key={side}>
    <h4>{tr(side==="before"?"Base commit":"Target commit")}</h4>
    {!preview?<p>{tr("File absent")}</p>:<><p>{tr(preview.preview)} · {bytes(preview.entry.size)}</p><code>{preview.entry.digest}</code>{preview.entry.kind==="file"&&<a href={`${base}/api/v1${root}/commits/${preview.commit_id}/content?${new URLSearchParams({path:selected.path})}`}>{tr(side==="before"?"Download base file":"Download target file")}</a>}</>}
   </section>;})}</div>
   {(!selected.before||selected.before.preview==="text")&&(!selected.after||selected.after.preview==="text")?<TextDiff before={selected.before?.text??""} after={selected.after?.text??""}/>:<p>{tr("Binary, directory or large file: text preview unavailable.")}</p>}
  </section>}
 </section>;
}
