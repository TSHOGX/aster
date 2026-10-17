"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { budgetFields, budgetLabels, budgetText, dependencySelection, type Budget, type BudgetText, type Configuration, type ConfigurationState, type Permissions } from "@/lib/evolution";

type Request = { request_id: string; configuration_revision: number; budgets: Record<string,Budget> };
type Result = { batch_id: string; configuration_revision: number; runs: Record<string,string> };
type Loaded = { permissions: Permissions; configuration: Configuration };
function parseRequest(raw: string): Request {
 if(raw.length>65536)throw new Error("Saved request is invalid.");
 const value=JSON.parse(raw) as Request;
 if(!value || !/^[a-zA-Z0-9_-]{1,128}$/.test(value.request_id) || !Number.isSafeInteger(value.configuration_revision) || value.configuration_revision<1 || !value.budgets || Array.isArray(value.budgets) || !Object.keys(value.budgets).length || Object.keys(value.budgets).length>32)throw new Error("Saved request is invalid.");
 for(const [id,budget] of Object.entries(value.budgets))if(!/^[a-zA-Z0-9_-]{1,128}$/.test(id) || budgetFields.some(key=>!Number.isSafeInteger(budget[key]) || budget[key]<(key==="max_cost_micros"?0:1)))throw new Error("Saved request is invalid.");
 return value;
}
export function EvolutionManual({group,onDirty}:{group:string;onDirty:(dirty:boolean)=>void}){
 const tr=useT();const [loaded,setLoaded]=useState<Loaded>();const [error,setError]=useState("");
 useEffect(()=>{
  let abort=new AbortController();
  const load=()=>{abort.abort();abort=new AbortController();const signal=abort.signal;void (async()=>{
   try{
    const root=`/api/admin/groups/${encodeURIComponent(group)}/optimization`;
    const permissions=await api<Permissions>(root+"/permissions",{signal});
    const state=await api<ConfigurationState>(root,{signal});
    if(state.state!=="active" || !state.active_revision)throw new Error("An active configuration is required for manual runs.");
    const value=await api<{configuration:Configuration}>(`${root}/revisions/${state.active_revision}`,{signal});
    if(!signal.aborted){setError("");setLoaded({permissions,configuration:value.configuration});}
   }catch(failure){if(!signal.aborted){setLoaded(undefined);setError(failure instanceof Error?failure.message:String(failure))}}
  })()};load();const refresh=()=>{if(!document.hidden)load()};const timer=setInterval(refresh,15000);window.addEventListener("focus",refresh);return()=>{abort.abort();clearInterval(timer);window.removeEventListener("focus",refresh)};
 },[group]);
 return <section className="evolution-config"><header className="page-heading"><h1>{tr("Run configured workflows")}</h1><a href={`/evolution-space/groups/${encodeURIComponent(group)}/`}>{tr("Open Evolution Space")}</a></header>{error?<p role="alert">{tr(error)} <button onClick={()=>location.reload()}>{tr("Retry loading")}</button></p>:loaded?<ManualForm key={`${group}:${loaded.permissions.account_id}`} group={group} {...loaded} onDirty={onDirty}/>:<p role="status">{tr("Loading configuration…")}</p>}</section>;
}
function ManualForm({group,permissions,configuration,onDirty}:{group:string;permissions:Permissions;configuration:Configuration;onDirty:(dirty:boolean)=>void}){
 const tr=useT(),key=`evolution-manual:${permissions.account_id}:${group}`;
 const [selected,setSelected]=useState<string[]>([]),[budgets,setBudgets]=useState<Record<string,BudgetText>>({}),[reviewed,setReviewed]=useState(0);
 const [pending,setPending]=useState(""),[ready,setReady]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(""),[result,setResult]=useState<Result>();
 const allowed=permissions.can_run; const inFlight=useRef(false);
 useEffect(()=>{const timer=setTimeout(()=>{try{const raw=sessionStorage.getItem(key);if(raw){parseRequest(raw);setPending(raw)}setReady(true)}catch{setError("Saved request is invalid.")}},0);return()=>clearTimeout(timer)},[key]);
 useEffect(()=>{onDirty(!!pending || selected.length>0&&!result);return()=>onDirty(false)},[pending,selected.length,result,onDirty]);
 let ids:string[]=[];try{ids=dependencySelection(configuration.bindings,selected)}catch{/* The server also checks the selected dependency closure. */}
 const needsEvidence=ids.some(id=>Object.values(configuration.bindings.find(binding=>binding.id===id)?.inputs||{}).some(port=>port.sources?.length));
 async function submit(raw:string){
  if(inFlight.current)return;inFlight.current=true;setBusy(true);setError("");
  try{
   const request=parseRequest(raw);
   const value=await mutate<Result>(`/api/admin/groups/${encodeURIComponent(group)}/evolution/manual`,request);
   if(!value.batch_id || !value.runs || Object.keys(request.budgets).some(id=>!value.runs[id]))throw new Error("The server returned an incomplete response.");
   setResult(value);setSelected([]);setPending("");sessionStorage.removeItem(key);
  }catch(failure){setError(failure instanceof Error?failure.message:String(failure));if(failure instanceof APIError && (failure.status===401||failure.status===403))window.dispatchEvent(new Event("focus"))}finally{inFlight.current=false;setBusy(false)}
 }
 function start(){
  if(inFlight.current)return;
  try{
   const requested:Record<string,Budget>={};
   for(const id of ids){const binding=configuration.bindings.find(binding=>binding.id===id)!;const values=budgets[id]||budgetText(binding.budget);
    requested[id]=Object.fromEntries(budgetFields.map(field=>{const value=Number(values[field]);if(!/^\d+$/.test(values[field]) || !Number.isSafeInteger(value) || value<(field==="max_cost_micros"?0:1) || value>binding.budget[field])throw new Error("Every budget must be a whole number within the binding ceiling.");return[field,value]})) as Budget;
   }
   const raw=JSON.stringify({request_id:crypto.randomUUID(),configuration_revision:configuration.revision,budgets:requested});parseRequest(raw);
   sessionStorage.setItem(key,raw);setPending(raw);setResult(undefined);void submit(raw);
  }catch(failure){setError(failure instanceof Error?failure.message:String(failure))}
 }
 return <><p>{permissions.group_name} · {tr("Active revision")} {configuration.revision}</p><p>{tr("Manual runs work while automatic optimization is off. They use the next bounded captured input delta for each binding, or its configured immutable artifacts. They do not capture historical work.")}</p><p>{tr("Each selected workflow includes its prerequisites. Inputs, output base and spending ceilings freeze before dispatch; successful runs advance only their own source frontier. Busy bindings must settle before another run.")}</p>
 {!allowed&&<p role="alert">{tr("Run permission is required.")}</p>}
 {error&&<p role="alert">{tr(error)}</p>}
 {pending&&allowed&&<section className="panel"><p>{tr("A manual run request is unconfirmed. Retry the exact request before starting another batch.")}</p><button disabled={busy} onClick={()=>void submit(pending)}>{tr("Check original request")}</button><button disabled={busy} onClick={()=>{if(window.confirm(tr("Discard this recovery record? The original batch may already have started."))){try{sessionStorage.removeItem(key);setPending("");setReady(true);setError("")}catch{setError("Saved request is invalid.")}}}}>{tr("Discard recovery")}</button></section>}
 {!ready&&!pending&&error&&<button onClick={()=>{if(!window.confirm(tr("Discard this recovery record? The original batch may already have started.")))return;try{sessionStorage.removeItem(key);setReady(true);setError("")}catch{setError("Saved request is invalid.")}}}>{tr("Discard recovery")}</button>}
 <form onSubmit={event=>{event.preventDefault();start()}}><fieldset disabled={!allowed||busy||!!pending||!ready}><legend>{tr("Configured workflows")}</legend>
 {configuration.bindings.filter(binding=>binding.enabled).map(binding=><label className="check-label" key={binding.id}><input type="checkbox" checked={selected.includes(binding.id)} onChange={event=>{setSelected(event.target.checked?[...selected,binding.id]:selected.filter(id=>id!==binding.id));setReviewed(0)}}/>{binding.id} · {binding.workflow.id} · {binding.output.prefix}</label>)}
 {ids.map(id=>{const binding=configuration.bindings.find(binding=>binding.id===id)!;const values=budgets[id]||budgetText(binding.budget);return <fieldset key={id}><legend>{id}</legend><p>{tr("Source selection")}: {(binding.source_ids||[]).join(", ")||tr("None")} · {tr("Output library")}: {binding.output.library_id}</p><div className="form-grid">{budgetFields.map(field=><label key={field}>{tr(budgetLabels[field])}<input aria-label={`${id}: ${tr(budgetLabels[field])}`} type="number" min={field==="max_cost_micros"?0:1} max={Math.min(binding.budget[field],Number.MAX_SAFE_INTEGER)} value={values[field]} onChange={event=>{setBudgets(old=>({...old,[id]:{...values,[field]:event.target.value}}));setReviewed(0)}}/></label>)}</div></fieldset>})}
 {needsEvidence&&!permissions.can_read_evidence&&<p role="alert">{tr("Source evidence permission is required for captured inputs.")}</p>}
 <label className="check-label"><input type="checkbox" checked={reviewed===configuration.revision} onChange={event=>setReviewed(event.target.checked?configuration.revision:0)}/>{tr("I reviewed the selected workflows, input scope and spending ceilings.")}</label><button className="primary" disabled={!ids.length||reviewed!==configuration.revision||needsEvidence&&!permissions.can_read_evidence}>{tr("Start manual batch")}</button></fieldset></form>
 {result&&<section className="panel" role="status"><h2>{tr("Manual batch queued")}</h2><p>{result.batch_id}</p><p>{tr("Dispatch, execution and Studio handoff are separate stages. Open the run after dispatch; a pending run may not be visible in Evolution Space yet.")}</p><ul>{Object.entries(result.runs).map(([id,run])=><li key={id}><a href={`/evolution-space/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(run)}`}>{id} · {run}</a></li>)}</ul></section>}
 </>;
}
