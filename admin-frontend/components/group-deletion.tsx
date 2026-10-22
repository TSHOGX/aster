"use client";
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

import Link from "next/link";
import { useState } from "react";
import { api } from "@/lib/api";
import { productActions } from "@web/capabilities";
import type { DeletionPreview, GroupDetail } from "@/lib/policy-models";
export function GroupDeletion({group,disabled,onDelete}:{group:GroupDetail;disabled:boolean;onDelete:(preview:DeletionPreview)=>void}) {
  const tr = useT();
  const [preview,setPreview]=useState<DeletionPreview|null>(null),[error,setError]=useState(""),[loading,setLoading]=useState(false),[confirmed,setConfirmed]=useState(false);
  const load=async(cursor="")=>{
    setLoading(true);setError("");setConfirmed(false);
    try{const value=await api<DeletionPreview>(`/api/admin/groups/${encodeURIComponent(group.id)}/deletion-preview?limit=25&cursor=${encodeURIComponent(cursor)}`);setPreview(value);}
    catch(error){setError(String(error));setPreview(null);}finally{setLoading(false);}
  };
  const current=preview?.group.revision===group.revision;
  return <section className="panel"><h2>{tr("Delete group")}</h2><p>{tr("Deletion removes memberships and policy bindings. No replacement group is assigned, and management roles remain unchanged.")}</p><button disabled={disabled||loading} onClick={()=>void load()}>{tr("Preview deletion")}</button>{error&&<p role="alert" className="error">{tr(error)}</p>}
    {preview&&<><p>{formatQuantity(preview.group.member_count)}  {tr("affected accounts. This page shows")} {preview.users.length}  {tr("accounts.")}</p>{!current&&<p role="alert">{tr("This group changed. Load a fresh preview before deleting.")}</p>}
      {preview.users.map(user=><article className="impact-card" key={user.account_id}><h3><Link href={`/users/${encodeURIComponent(user.account_id)}`}>{user.username}</Link></h3><p>{tr(user.account_status)} · {tr(user.management_role)}</p><h4>{tr("Grants lost")}</h4><ul>{productActions.filter(action=>user.before_access[action]?.allowed&&!user.after_access[action]?.allowed).map(action=><li key={action}>{tr(action)}</li>)}</ul>{!productActions.some(action=>user.before_access[action]?.allowed&&!user.after_access[action]?.allowed)&&<p>{tr("No effective grants lost.")}</p>}
        <details><summary>{tr("Resource entitlement changes")}</summary><div className="table-scroll"><table><thead><tr><th>{tr("Resource")}</th><th>{tr("Default before → after")}</th><th>{tr("Ceiling before → after")}</th><th>{tr("Effective before → after")}</th></tr></thead><tbody>{Object.entries(user.before_resources).map(([dimension,before])=>{const after=user.after_resources[dimension];return <tr key={dimension}><td>{tr(dimension)}</td><td>{formatQuantity(before.default)} → {after?.default??0}</td><td>{formatQuantity(before.ceiling)} → {after?.ceiling??0}</td><td>{formatQuantity(before.effective)} → {after?.effective??0}</td></tr>;})}</tbody></table></div></details>
      </article>)}
      {preview.next_cursor&&<button disabled={disabled||loading} onClick={()=>void load(preview.next_cursor)}>{tr("Next affected accounts")}</button>}
      <label className="check-label"><input type="checkbox" checked={confirmed} disabled={disabled||loading||!current} onChange={e=>setConfirmed(e.target.checked)}/>{tr("I reviewed the impact and understand that affected accounts receive no replacement membership.")}</label>
      <button className="danger solid" disabled={disabled||loading||!current||!confirmed} onClick={()=>onDelete(preview)}>{tr("Delete group")}</button>
    </>}
  </section>;
}
