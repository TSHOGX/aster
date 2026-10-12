"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { productActions } from "@web/capabilities";
import type { ExplainedAccess } from "@/lib/policy-models";
export function EffectiveAccessView({accountID}:{accountID:string}) {
  const [access,setAccess]=useState<ExplainedAccess|null>(null),[error,setError]=useState(""),[revision,setRevision]=useState(0);
  useEffect(()=>{const abort=new AbortController();void api<ExplainedAccess>(`/api/admin/users/${encodeURIComponent(accountID)}/effective-access`,{signal:abort.signal}).then(value=>{if(!abort.signal.aborted){setAccess(value);setError("");}}).catch(error=>{if(!abort.signal.aborted)setError(String(error));});return()=>abort.abort();},[accountID,revision]);
  return <section className="panel" aria-labelledby="effective-access-title"><h2 id="effective-access-title">Effective access</h2><p>Groups combine allow grants; any deny wins. Account status and management role remain separate constraints.</p><button onClick={()=>setRevision(v=>v+1)}>Refresh effective access</button>{error&&<p role="alert" className="error">{error}</p>}
    {access&&<><p>Account: {access.account_status} · Role: {access.management_role} · Authorization revision {access.revision}</p><div className="table-scroll"><table><thead><tr><th>Action</th><th>Result</th><th>Sources</th></tr></thead><tbody>{productActions.map(action=>{const result=access.actions[action];return <tr key={action}><td>{action}</td><td>{result?.allowed?"Allowed":"Denied"}</td><td>{result?.sources.length?result.sources.map(source=><div key={`${source.group_id}/${source.policy_id}`}><strong>{source.effect}</strong> — <Link href={`/groups/${encodeURIComponent(source.group_id)}`}>{source.group_name}</Link> / <Link href={`/policies/${encodeURIComponent(source.policy_id)}`}>{source.policy_name}</Link></div>):"No grant"}</td></tr>;})}</tbody></table></div></>}
  </section>;
}
