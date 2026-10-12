"use client";
import Link from "next/link";
import { useState } from "react";
import { api } from "@/lib/api";
import { productActions } from "@web/capabilities";
import type { DeletionPreview, GroupDetail } from "@/lib/policy-models";
export function GroupDeletion({group,disabled,onDelete}:{group:GroupDetail;disabled:boolean;onDelete:(preview:DeletionPreview)=>void}) {
  const [preview,setPreview]=useState<DeletionPreview|null>(null),[error,setError]=useState(""),[loading,setLoading]=useState(false),[confirmed,setConfirmed]=useState(false);
  const load=async(cursor="")=>{
    setLoading(true);setError("");setConfirmed(false);
    try{const value=await api<DeletionPreview>(`/api/admin/groups/${encodeURIComponent(group.id)}/deletion-preview?limit=25&cursor=${encodeURIComponent(cursor)}`);setPreview(value);}
    catch(error){setError(String(error));setPreview(null);}finally{setLoading(false);}
  };
  const current=preview?.group.revision===group.revision;
  return <section className="panel"><h2>Delete group</h2><p>Deletion removes memberships and policy bindings. No replacement group is assigned, and management roles remain unchanged.</p><button disabled={disabled||loading} onClick={()=>void load()}>Preview deletion</button>{error&&<p role="alert" className="error">{error}</p>}
    {preview&&<><p>{preview.group.member_count} affected accounts. This page shows {preview.users.length} accounts.</p>{!current&&<p role="alert">This group changed. Load a fresh preview before deleting.</p>}
      {preview.users.map(user=><article className="impact-card" key={user.account_id}><h3><Link href={`/users/${encodeURIComponent(user.account_id)}`}>{user.username}</Link></h3><p>{user.account_status} · {user.management_role}</p><h4>Grants lost</h4><ul>{productActions.filter(action=>user.before_access[action]?.allowed&&!user.after_access[action]?.allowed).map(action=><li key={action}>{action}</li>)}</ul>{!productActions.some(action=>user.before_access[action]?.allowed&&!user.after_access[action]?.allowed)&&<p>No effective grants lost.</p>}
        <details><summary>Resource entitlement changes</summary><div className="table-scroll"><table><thead><tr><th>Resource</th><th>Default before → after</th><th>Ceiling before → after</th><th>Effective before → after</th></tr></thead><tbody>{Object.entries(user.before_resources).map(([dimension,before])=>{const after=user.after_resources[dimension];return <tr key={dimension}><td>{dimension}</td><td>{before.default} → {after?.default??0}</td><td>{before.ceiling} → {after?.ceiling??0}</td><td>{before.effective} → {after?.effective??0}</td></tr>;})}</tbody></table></div></details>
      </article>)}
      {preview.next_cursor&&<button disabled={disabled||loading} onClick={()=>void load(preview.next_cursor)}>Next affected accounts</button>}
      <label className="check-label"><input type="checkbox" checked={confirmed} disabled={disabled||loading||!current} onChange={e=>setConfirmed(e.target.checked)}/>I reviewed the impact and understand that affected accounts receive no replacement membership.</label>
      <button disabled={disabled||loading||!current||!confirmed} onClick={()=>onDelete(preview)}>Delete group</button>
    </>}
  </section>;
}
