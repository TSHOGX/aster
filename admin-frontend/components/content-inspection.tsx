"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError } from "@/lib/api";
import type { EffectiveAccess } from "@web/capabilities";
type Inspection = { account_id:string; project_id:string; session_id:string; title:string; audit_id:number; next_after?:number; messages:{id:string;role:string;content:string;sequence:number;created_at:string;truncated:boolean}[] };
export function ContentInspection({accountID}:{accountID:string}) {
  const [allowed,setAllowed]=useState(false),[session,setSession]=useState(""),[purpose,setPurpose]=useState(""),[page,setPage]=useState<Inspection|null>(null),[busy,setBusy]=useState(false),[error,setError]=useState("");
  const inspectionGeneration=useRef({generation:0});
  const [inspected,setInspected]=useState<{session:string;purpose:string}|null>(null);
  useEffect(()=>{
    let gone=false, generation=0;
    const inspection=inspectionGeneration.current;
    const check=async()=>{const current=++generation;try{const access=await api<EffectiveAccess>("/api/me/capabilities");if(!gone&&current===generation){const permit=access.account_status==="active"&&access.management_role==="admin"&&access.actions["admin.content.read"]?.allowed===true;setAllowed(permit);if(!permit){inspection.generation++;setBusy(false);setPage(null);setInspected(null);}}}catch{if(!gone&&current===generation){inspection.generation++;setBusy(false);setAllowed(false);setPage(null);setInspected(null);}}};
    const denied=()=>{inspection.generation++;setBusy(false);setAllowed(false);setPage(null);setInspected(null);};
    void check();window.addEventListener("focus",check);window.addEventListener("admin-authority-denied",denied);
    return()=>{gone=true;inspection.generation++;window.removeEventListener("focus",check);window.removeEventListener("admin-authority-denied",denied);};
  },[accountID]);
  const inspect=async(after=0)=>{
    const current=++inspectionGeneration.current.generation;
    const target=after&&inspected?inspected:{session,purpose};setBusy(true);setError("");setPage(null);
    try{const result=await api<Inspection>(`/api/admin/users/${encodeURIComponent(accountID)}/conversations/${encodeURIComponent(target.session)}/inspect`,{method:"POST",body:JSON.stringify({purpose:target.purpose,after_sequence:after,limit:20})});if(current===inspectionGeneration.current.generation){setPage(result);setInspected(target);}}
    catch(error){if(current!==inspectionGeneration.current.generation)return;setError(error instanceof APIError&&error.status===403?"An explicit admin.content.read grant is required.":"Inspection did not return content. Retry explicitly; each successful read is audited.");}
    finally{if(current===inspectionGeneration.current.generation)setBusy(false);}
  };
  return <section className="panel" aria-labelledby="content-inspection-title"><h2 id="content-inspection-title">Private conversation inspection</h2><p>Read stored messages for a specific support or review purpose. Every page is audited. This does not grant access to the account’s live workspace or native runtime.</p>
    {!allowed?<p>An explicit admin.content.read grant is required in addition to the administrator role.</p>:<form className="account-form" onSubmit={e=>{e.preventDefault();void inspect();}}><fieldset disabled={busy}><label>Conversation ID<input required maxLength={128} value={session} onChange={e=>setSession(e.target.value)}/></label><label>Inspection purpose<input required maxLength={512} value={purpose} onChange={e=>setPurpose(e.target.value)} placeholder="Case reference and reason; omit private message content"/></label><button disabled={!session.trim()||!purpose.trim()} className="primary">Inspect conversation</button></fieldset></form>}
    {error&&<p role="alert" className="error">{error}</p>}{busy&&<p role="status">Reading and recording audit…</p>}
    {page&&<div className="inspection-results"><h3>{page.title}</h3><p>Conversation {page.session_id} · Audit {page.audit_id}</p><p>Purpose: {inspected?.purpose}</p>{page.messages.map(message=><article key={message.id} className="impact-card"><h4>{message.role} · Message {message.sequence}</h4><time>{new Date(message.created_at).toLocaleString()}</time><pre>{message.content}</pre>{message.truncated&&<p>This message was shortened for bounded inspection.</p>}</article>)}{!page.messages.length&&<p>No stored messages on this page.</p>}<div className="form-actions">{page.next_after&&<button disabled={busy||!allowed} onClick={()=>void inspect(page.next_after)}>Read next messages</button>}<button onClick={()=>{inspectionGeneration.current.generation++;setBusy(false);setPage(null);setInspected(null);}}>Clear inspected content</button></div></div>}
  </section>;
}
