"use client";
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

import { Select } from "@web/select";
import Link from "next/link";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { date, type UserPage } from "@/lib/models";
export function UserList() {
  const tr = useT();
  const [search, setSearch] = useState(""), [filter, setFilter] = useState({ search: "", status: "", role: "" });
  const [cursors, setCursors] = useState([""]), [page, setPage] = useState<UserPage>({ users: [] });
  const [loading, setLoading] = useState(true), [error, setError] = useState(""), [refresh, setRefresh] = useState(0);
  const cursor = cursors[cursors.length - 1];
  useEffect(() => {
    const abort = new AbortController();
    const query = new URLSearchParams({ ...filter, cursor, limit: "50" });
    void api<UserPage>(`/api/admin/users?${query}`, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) { setPage(value); setError(""); } })
      .catch(error => { if (!abort.signal.aborted) setError(String(error)); }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [filter, cursor, refresh]);
  return <section aria-labelledby="users-title">
    <header className="page-heading"><div><h1 id="users-title">{tr("Users")}</h1><p>{tr("Manage identities, memberships and account security.")}</p></div><Link className="primary button" href="/users/new">{tr("Create user")}</Link></header>
    <form className="filters" onSubmit={event => { event.preventDefault(); setLoading(true); setFilter({ ...filter, search }); setCursors([""]); }}>
      <label>{tr("Search users")}<input value={search} onChange={event => setSearch(event.target.value)} placeholder={tr("Username or display name")} /></label>
      <label>{tr("Status")}<Select aria-label={tr("Status")} value={filter.status} onChange={event => { setLoading(true); setFilter({ ...filter, status: event.target.value }); setCursors([""]); }}><option value="">{tr("All statuses")}</option><option value="active">{tr("Active")}</option><option value="disabled">{tr("Disabled")}</option></Select></label>
      <label>{tr("Role")}<Select aria-label={tr("Role")} value={filter.role} onChange={event => { setLoading(true); setFilter({ ...filter, role: event.target.value }); setCursors([""]); }}><option value="">{tr("All roles")}</option><option value="admin">{tr("Administrator")}</option><option value="user">{tr("User")}</option></Select></label>
      <button type="submit">{tr("Search")}</button><button type="button" onClick={() => { setLoading(true); setRefresh(v => v + 1); }}>{tr("Refresh")}</button>
    </form>
    {error && <p className="error" role="alert">{tr(error)}</p>}
    {loading && <p role="status">{tr("Loading users…")}</p>}
    <div className="table-scroll" aria-busy={loading}><table><thead><tr><th>{tr("User")}</th><th>{tr("Status")}</th><th>{tr("Role")}</th><th>{tr("Memberships")}</th><th>{tr("Last sign-in")}</th></tr></thead><tbody>
      {page.users.map(user => <tr key={user.id}><td><Link href={`/users/${encodeURIComponent(user.id)}`}>{user.display_name}</Link><small>{user.username}</small></td><td><span className={`status ${user.status}`}>{tr(user.status)}</span>{user.must_change_password && <small>{tr("Password setup required")}</small>}</td><td>{user.role === "admin" ? tr("Administrator") : tr("User")}</td><td>{user.groups.length ? user.groups.map(group => group.name).join(", ") : tr("No groups")}</td><td>{date(user.last_login_at)}</td></tr>)}
    </tbody></table></div>
    {!loading && !error && !page.users.length && <p className="empty">{tr("No matching users.")}</p>}
    <nav className="pagination" aria-label={tr("User pages")}><button disabled={loading || cursors.length === 1} onClick={() => { setLoading(true); setCursors(values => values.slice(0, -1)); }}>{tr("Previous")}</button><span>{tr("Page")} {formatQuantity(cursors.length)}</span><button disabled={loading || !page.next_cursor} onClick={() => { setLoading(true); setCursors(values => [...values, page.next_cursor!]); }}>{tr("Next")}</button></nav>
  </section>;
}
