"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError, base, requestID, type Entry } from "../lib/api";
import { type Branch } from "../lib/branches";
import { readRevertCommand, type RevertCommand } from "../lib/revert-recovery";
import { useT } from "../lib/i18n";
import { useStudio } from "./studio-provider";

// Retain uncertain commands across workbench view switches when storage fails.
const localCommands = new Map<string, RevertCommand | undefined>();
const undurable = new Set<string>();
const warnUnsaved = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
function protectUnpersisted(key: string, value: boolean) {
 if(value)undurable.add(key);else undurable.delete(key);
 window.removeEventListener("beforeunload",warnUnsaved);
 if(undurable.size)window.addEventListener("beforeunload",warnUnsaved);
}
type Selection = {id:string;parents:string[]};
type Comparison = {base:{commit_id:string};target:{commit_id:string};changes:{path:string;change:string;before?:Entry;after?:Entry}[];next:string;total:number};
export function ReviewedRevert({library,selection,authorized}:{library:string;selection?:Selection;authorized:boolean}) {
 const tr=useT(),studio=useStudio();
 const root=`/libraries/${encodeURIComponent(library)}`;
 const key=`studio-revert:${JSON.stringify([studio.session?.issuer,studio.session?.subject,library])}`;
 const [pending,setPending]=useState<RevertCommand>();
 const retained=useRef<RevertCommand>(undefined),hydrated=useRef(false),running=useRef(false);
 const [invalid,setInvalid]=useState(false),[ready,setReady]=useState(false),[storageError,setStorageError]=useState(false);
 const [parent,setParent]=useState({commit:"",parent:""}),[name,setName]=useState("");
 const [preview,setPreview]=useState<Comparison>(),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const [reload,setReload]=useState(0);
 const active=useRef<AbortController>(undefined);
 const unavailable="Revert evidence is unavailable. Reload or check Volume access.";
 const remember=(value?:RevertCommand)=>{
  retained.current=value;localCommands.set(key,value);setPending(value);
  try{if(value)sessionStorage.setItem(key,JSON.stringify(value));else sessionStorage.removeItem(key);setStorageError(false);protectUnpersisted(key,false);}catch{setStorageError(true);protectUnpersisted(key,!!value);}
 };
 useEffect(()=>{
  const abort=new AbortController();active.current=abort;
  // eslint-disable-next-line react-hooks/set-state-in-effect -- Restore pending commands only after current scoped authority succeeds.
  setReady(false);setPreview(undefined);setError("");
  if(!authorized)return()=>abort.abort();
  void api(`${root}?mode=workbench`,{signal:abort.signal}).then(()=>{
   if(abort.signal.aborted)return;
   if(!hydrated.current){if(localCommands.has(key)){retained.current=localCommands.get(key);setStorageError(undurable.has(key));}else try{const raw=sessionStorage.getItem(key);if(raw){try{retained.current=readRevertCommand(raw,library);}catch{setInvalid(true);}}}catch{setStorageError(true);}hydrated.current=true;}
   setPending(retained.current);setReady(true);
  }).catch(()=>{if(!abort.signal.aborted)setError(unavailable);});
  return()=>{abort.abort();active.current?.abort();};
 },[key,root,library,reload,authorized]);
 const body=pending?JSON.parse(pending.body) as {name:string;commit:string;parent:string}:undefined;
 const commit=body?.commit??selection?.id??"";
 const selectedParent=body?.parent??(parent.commit===selection?.id&&selection?.parents.includes(parent.parent)?parent.parent:"");
 const matching=preview?.base.commit_id===commit&&preview?.target.commit_id===selectedParent;
 async function run(action:(signal:AbortSignal)=>Promise<void>){
  if(running.current)return;running.current=true;active.current?.abort();const abort=new AbortController();active.current=abort;setBusy(true);setError("");
  try{await action(abort.signal);}catch(cause){if(!abort.signal.aborted){setPreview(undefined);if(cause instanceof APIError&&[401,403].includes(cause.status))setReady(false);setError(retained.current?"Revert creation is unconfirmed. Recover the original request before starting another revert.":unavailable);}}finally{running.current=false;if(!abort.signal.aborted)setBusy(false);}
 }
 async function create(){
  if(!studio.session)return;
  const command=retained.current??{library,body:JSON.stringify({request_id:requestID(),name:name.trim(),commit,parent:selectedParent})};remember(command);
  let branch:Branch;
  try{branch=await api<Branch>(`${root}/branches/revert`,{method:"POST",body:command.body},studio.session.csrf);}catch(cause){if(cause instanceof APIError&&cause.status<500&&![401,403].includes(cause.status))remember();throw cause;}
  const url=new URL(location.href);url.search="";url.searchParams.set("branch",branch.id);url.searchParams.set("view","changes");
  // Confirmed identity is pinned before any dependent metadata read.
  window.history.replaceState(null,"",url);remember();window.location.assign(url);
 }
 return <section aria-label={tr("Reviewed revert")}>
  <h3>{tr("Reviewed revert")}</h3>
  <p>{tr("Create an inverse draft for one commit and its selected immediate parent. Review and commit the draft, then prepare against current main for fresh checks and approval.")}</p>
  {error&&<p role="alert">{tr(error)}</p>}
  {storageError&&<p role="alert">{tr("Local operation recovery is unavailable. Keep this tab open until the operation is confirmed.")}</p>}
  {!ready&&<button disabled={busy} onClick={()=>setReload(value=>value+1)}>{tr("Reload revert")}</button>}
  {ready&&authorized&&<>
   {invalid&&<p role="alert">{tr("Local revert recovery is invalid. Discard it before starting another revert.")}</p>}
   {pending&&<p role="status">{tr("Revert creation is unconfirmed. Recover the original request before starting another revert.")}</p>}
   {(pending||invalid)&&<button disabled={busy} onClick={()=>void run(async()=>{if(await studio.ask("Discard revert recovery","The original request may already have created a branch. Check branch history before discarding recovery.",["Keep recovery","Discard revert recovery"])!=="Discard revert recovery")return;remember();setInvalid(false);setPreview(undefined);})}>{tr("Discard revert recovery")}</button>}
   {commit&&<>
    <p>{tr("Commit to revert")}: <code>{commit}</code></p>
    {body?<p>{tr("Selected parent")}: <code>{body.parent}</code></p>:<label>{tr("Selected parent")} <select aria-label={tr("Selected parent")} value={selectedParent} disabled={busy||invalid} onChange={event=>{setParent({commit,parent:event.target.value});setPreview(undefined);}}><option value="">{tr("Select an immediate parent")}</option>{selection?.parents.map(value=><option key={value} value={value}>{value}</option>)}</select></label>}
    <label>{tr("Revert branch name")} <input value={body?.name??name} disabled={busy||!!pending||invalid} onChange={event=>setName(event.target.value)} maxLength={128}/></label>
    <button disabled={busy||invalid||!selectedParent} onClick={()=>void run(async signal=>{setPreview(undefined);const value=await api<Comparison>(`${root}/compare?${new URLSearchParams({base:commit,target:selectedParent})}`,{signal});if(!signal.aborted)setPreview(value);})}>{tr("Preview inverse change")}</button>
    {matching&&preview&&<div aria-label={tr("Inverse change preview")}>
     <p><code>{preview.base.commit_id}</code> → <code>{preview.target.commit_id}</code></p>
     <p>{tr("Showing {loaded} of {total} committed paths.",{loaded:preview.changes.length,total:preview.total})}</p>
     <ul>{preview.changes.map(change=><li key={change.path}><code>{change.path}</code> · {tr(change.change)} {(["before","after"] as const).map(side=>change[side]?.kind==="file"&&<a key={side} href={`${base}/api/v1${root}/commits/${side==="before"?commit:selectedParent}/content?${new URLSearchParams({path:change.path})}`}>{tr(side==="before"?"Download base file":"Download target file")}</a>)}</li>)}</ul>
     {preview.next&&<button disabled={busy} onClick={()=>void run(async signal=>{const value=await api<Comparison>(`${root}/compare?${new URLSearchParams({base:commit,target:selectedParent,cursor:preview.next})}`,{signal});if(!signal.aborted)setPreview({...value,changes:[...preview.changes,...value.changes]});})}>{tr("Load more changes")}</button>}
    </div>}
    <button disabled={busy||invalid||(!pending&&(!matching||!name.trim()||name.trim()==="main"))} onClick={()=>void run(create)}>{tr(pending?"Recover revert creation":"Create revert branch")}</button>
   </>}
   {!commit&&<p>{tr("Choose a commit in history to review its inverse change.")}</p>}
  </>}
 </section>;
}
