"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { GroupSummary, Policy } from "@/lib/policy-models";
export function PolicyList({ kind }: { kind: "groups" | "policies" }) {
  const [items, setItems] = useState<(GroupSummary | Policy)[]>([]), [cursors, setCursors] = useState([""]), [next, setNext] = useState(""), [loading, setLoading] = useState(true), [error, setError] = useState(""), [refresh, setRefresh] = useState(0);
  const cursor = cursors.at(-1)!;
  useEffect(() => {
    const abort = new AbortController();
    void api<{groups?:GroupSummary[]; policies?:Policy[]; next_cursor?:string}>(`/api/admin/${kind}?limit=50&cursor=${encodeURIComponent(cursor)}`, {signal:abort.signal})
      .then(page => { if (!abort.signal.aborted) { setItems(page.groups || page.policies || []); setNext(page.next_cursor || ""); setError(""); } })
      .catch(error => { if (!abort.signal.aborted) setError(String(error)); }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [kind, cursor, refresh]);
  const title = kind === "groups" ? "Groups" : "Policies", singular = kind === "groups" ? "group" : "policy";
  return <section><header className="page-heading"><div><h1>{title}</h1><p>{kind === "groups" ? "Membership combines grants and resource entitlements; it does not share ownership." : "Named allow/deny policies. Deny takes precedence; missing grants deny access."}</p></div><Link className="primary button" href={`/${kind}/new`}>Create {singular}</Link></header>
    <button onClick={() => { setLoading(true); setRefresh(v=>v+1); }}>Refresh</button>{error && <p className="error" role="alert">{error}</p>}{loading && <p role="status">Loading {kind}…</p>}
    <div className="table-scroll"><table><thead><tr><th>Name</th><th>Description</th><th>{kind === "groups" ? "Members / policies" : "Declared actions"}</th><th>Revision</th></tr></thead><tbody>{items.map(item=><tr key={item.id}><td><Link href={`/${kind}/${encodeURIComponent(item.id)}`}>{item.name}</Link></td><td>{item.description}</td><td>{"member_count" in item ? `${item.member_count} / ${item.policy_count}` : Object.values(item.statements).filter(value=>value!=="unset").length}</td><td>{item.revision}</td></tr>)}</tbody></table></div>
    {!loading && !items.length && <p>No {kind}.</p>}<nav className="pagination" aria-label={`${title} pages`}><button disabled={loading || cursors.length===1} onClick={()=>{setLoading(true);setCursors(v=>v.slice(0,-1));}}>Previous</button><span>Page {cursors.length}</span><button disabled={loading || !next} onClick={()=>{setLoading(true);setCursors(v=>[...v,next]);}}>Next</button></nav>
  </section>;
}
