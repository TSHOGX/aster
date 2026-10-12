"use client";
import { Select } from "@web/select";
import Link from "next/link";
import { PolicyResourceGrants, type Grant } from "./configuration-controls";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { productActions } from "@web/capabilities";
import type { Policy, PolicyEffect, PolicyResult } from "@/lib/policy-models";
import { MutationStatus, usePolicyMutation } from "./policy-mutation";
export function PolicyEditor({id,onDirty}:{id?:string;onDirty:(value:boolean)=>void}) {
  const [policy,setPolicy]=useState<Policy|null>(null), [name,setName]=useState(""), [description,setDescription]=useState(""), [statements,setStatements]=useState<Policy["statements"]>({}), [error,setError]=useState(""), [deleted,setDeleted]=useState(false),[loadAttempt,setLoadAttempt]=useState(0);
  const mutation=usePolicyMutation();
 const [draftResources,setDraftResources]=useState<Grant[]>([]);
 const [resourceDirty,setResourceDirty]=useState(false);
  const dirty=resourceDirty||!!mutation.pending || (policy ? name!==policy.name || description!==policy.description || productActions.some(action=>(statements[action]||"unset")!==(policy.statements[action]||"unset")) : !!name || !!description || Object.values(statements).some(value=>value!=="unset"));
  useEffect(()=>{onDirty(dirty);return ()=>onDirty(false);},[dirty,onDirty]);
  const accept=(value:Policy)=>{setPolicy(value);setName(value.name);setDescription(value.description);setStatements(value.statements);};
  useEffect(()=>{if(!id)return;const abort=new AbortController();void api<Policy>(`/api/admin/policies/${encodeURIComponent(id)}`,{signal:abort.signal}).then(value=>{if(!abort.signal.aborted)accept(value);}).catch(error=>{if(!abort.signal.aborted)setError(String(error));});return()=>abort.abort();},[id,loadAttempt]);
  const saved=async(result:PolicyResult)=>{
    if(result.deleted_id){setDeleted(true);onDirty(false);return;}
    if(result.policy){accept(result.policy);if(result.replayed)accept(await api<Policy>(`/api/admin/policies/${encodeURIComponent(result.policy.id)}`));}
  };
  const reload=async()=>{if(!policy)return;try{const latest=await api<Policy>(`/api/admin/policies/${encodeURIComponent(policy.id)}`);setName(current=>current===policy.name?latest.name:current);setDescription(current=>current===policy.description?latest.description:current);setStatements(current=>Object.fromEntries(productActions.map(action=>[action,(current[action]||"unset")===(policy.statements[action]||"unset")?(latest.statements[action]||"unset"):(current[action]||"unset")])));setPolicy(latest);setError("");}catch(error){setError(String(error));}};
  if(deleted)return <section><h1>Policy deleted</h1><Link href="/policies">Back to policies</Link></section>;
  return <section><header className="page-heading"><div><h1>{policy?policy.name:id?"Policy details":"Create policy"}</h1><p>Each action is unset, allowed or denied. A deny in any bound policy takes precedence.</p></div><Link href="/policies">Back to policies</Link></header>
    {error&&<p role="alert" className="error">{error}</p>}<MutationStatus state={mutation} saved={saved}/>{id&&!policy&&(!error?<p role="status">Loading policy…</p>:<button onClick={()=>{setError("");setLoadAttempt(v=>v+1);}}>Retry loading</button>)}
    {(!id||policy)&&<form id="policy-details-form" className="account-form policy-form" onSubmit={event=>{event.preventDefault();const body:Record<string,unknown>={request_id:crypto.randomUUID(),expected_revision:policy?.revision||0};if(!policy||name!==policy.name)body.name=name;if(!policy||description!==policy.description)body.description=description;if(!policy||productActions.some(action=>(statements[action]||"unset")!==(policy.statements[action]||"unset")))body.statements=statements;if(!policy)body.resources=draftResources;void mutation.submit({path:policy?`/api/admin/policies/${encodeURIComponent(policy.id)}`:"/api/admin/policies",method:policy?"PATCH":"POST",body},saved);}}>
      <fieldset disabled={mutation.busy||!!mutation.pending||!!policy&&resourceDirty}><label>Name<input required maxLength={256} value={name} onChange={e=>setName(e.target.value)}/></label><label>Description<textarea maxLength={1024} value={description} onChange={e=>setDescription(e.target.value)}/></label>
        <div className="policy-actions">{productActions.map(action=><label key={action}><span>{action}</span><Select aria-label={action} value={statements[action]||"unset"} onChange={e=>setStatements(values=>({...values,[action]:e.target.value as PolicyEffect}))}><option value="unset">Unset</option><option value="allow">Allow</option><option value="deny">Deny</option></Select></label>)}</div>
        <p className="hint">admin.content.read also requires the administrator role. Private reads are audited; management access alone does not grant content access.</p>
        {policy&&<button className="primary" disabled={!dirty}>Save policy</button>}</fieldset>
    </form>}
    {(!id||policy)&&<PolicyResourceGrants key={policy?.id||"new"} policy={policy||undefined} onDraft={setDraftResources} saved={accept} onDirty={setResourceDirty} disabled={mutation.busy||!!mutation.pending||!!policy&&(name!==policy.name||description!==policy.description||productActions.some(action=>(statements[action]||"unset")!==(policy.statements[action]||"unset")))} />}
    {!id&&!policy&&<button type="submit" form="policy-details-form" className="primary" disabled={!dirty||mutation.busy||!!mutation.pending}>Save policy</button>}
    {policy&&<section className="panel"><p>Revision {policy.revision}</p><div className="form-actions"><button disabled={mutation.busy||!!mutation.pending||resourceDirty} onClick={()=>void reload()}>Reload current data</button><button disabled={mutation.busy||!!mutation.pending||resourceDirty} onClick={()=>accept(policy)}>Discard edits</button><button disabled={dirty||mutation.busy} onClick={()=>{if(window.confirm(`Delete policy “${policy.name}”? Bound policies must be detached first.`))void mutation.submit({path:`/api/admin/policies/${encodeURIComponent(policy.id)}`,method:"DELETE",body:{request_id:crypto.randomUUID(),expected_revision:policy.revision}},saved);}}>Delete policy</button><Link href={`/audit?target=${encodeURIComponent(policy.id)}`}>View policy audit</Link></div></section>}
  </section>;
}
