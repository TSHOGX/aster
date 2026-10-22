"use client";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useState } from "react";
import { type GroupPage } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";

// The landing picker keeps its search in the URL so reload and Back restore it;
// the switcher dialog keeps it locally so it never rewrites the current page.
export function GroupList({ selected = "", onNavigate, persist = false }: { selected?: string; onNavigate?: () => void; persist?: boolean }) {
  const tr = useT(); const params = useSearchParams();
  const [local, setLocal] = useState({ search: "", after: "" });
  const search = persist ? params.get("groups_search") ?? "" : local.search;
  const after = persist ? params.get("groups_after") ?? "" : local.after;
  const navigate = (search: string, after: string) => {
    if (!persist) { setLocal({ search, after }); return; }
    const url = new URL(window.location.href);
    for (const [key, value] of [["groups_search", search], ["groups_after", after]]) {
      if (value) url.searchParams.set(key, value); else url.searchParams.delete(key);
    }
    window.history.pushState(null, "", url.pathname + url.search + url.hash);
  };
  const read = useRead<GroupPage>(`/groups?${new URLSearchParams({ search, after, limit: "25" })}`);
  return <div className="groups">
    <form key={search} onSubmit={event => { event.preventDefault(); navigate(String(new FormData(event.currentTarget).get("search") ?? "").trim(), ""); }}>
      <label htmlFor={persist ? "group-search" : "group-switch-search"}>{tr("Search groups")}</label>
      <div className="search"><input id={persist ? "group-search" : "group-switch-search"} name="search" defaultValue={search} maxLength={256} /><button type="submit">{tr("Search")}</button><button type="button" onClick={read.refresh}>{tr("Refresh")}</button></div>
    </form>
    {read.loading && <p role="status">{tr("Loading groups…")}</p>}
    {read.status && <div role="alert"><p>{tr(read.status === 401 || read.status === 403 ? "Group access was revoked. Sign in again or contact an administrator." : "Group access is unavailable. Please retry.")}</p><button onClick={read.refresh}>{tr("Retry")}</button></div>}
    {read.data && <><ul className="group-choices">{read.data.items.map(group => <li key={group.id}>
      <Link prefetch={false} href={`/groups/${encodeURIComponent(group.id)}/`} aria-current={selected === group.id ? "page" : undefined} onClick={onNavigate}>
        <strong>{group.name}</strong>{group.description && <small>{group.description}</small>}
        <span className="status">{tr(group.auto_enabled ? "Automatic optimization on" : group.active_revision ? "Manual only" : "Not configured")}</span>
      </Link></li>)}</ul>
      {!read.data.items.length && <div><h3>{tr(search ? "No matching groups" : "No readable groups")}</h3>{!search && <p>{tr("Ask an administrator for group membership and configuration read access.")}</p>}</div>}
      <div className="pagination">{after && <button onClick={() => navigate(search, "")}>{tr("First page")}</button>}{read.data.has_more && <button onClick={() => navigate(search, read.data!.next_after)}>{tr("Next page")}</button>}</div>
    </>}
  </div>;
}
