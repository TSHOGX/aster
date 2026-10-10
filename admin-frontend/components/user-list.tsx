"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { date, type UserPage } from "@/lib/models";
export function UserList() {
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
    <header className="page-heading"><div><h1 id="users-title">Users</h1><p>Manage identities, memberships and account security.</p></div><Link className="primary button" href="/users/new">Create user</Link></header>
    <form className="filters" onSubmit={event => { event.preventDefault(); setLoading(true); setFilter({ ...filter, search }); setCursors([""]); }}>
      <label>Search users<input value={search} onChange={event => setSearch(event.target.value)} placeholder="Username or display name" /></label>
      <label>Status<select value={filter.status} onChange={event => { setLoading(true); setFilter({ ...filter, status: event.target.value }); setCursors([""]); }}><option value="">All statuses</option><option value="active">Active</option><option value="disabled">Disabled</option></select></label>
      <label>Role<select value={filter.role} onChange={event => { setLoading(true); setFilter({ ...filter, role: event.target.value }); setCursors([""]); }}><option value="">All roles</option><option value="admin">Administrator</option><option value="user">User</option></select></label>
      <button type="submit">Search</button><button type="button" onClick={() => { setLoading(true); setRefresh(v => v + 1); }}>Refresh</button>
    </form>
    {error && <p className="error" role="alert">{error}</p>}
    {loading && <p role="status">Loading users…</p>}
    <div className="table-scroll" aria-busy={loading}><table><thead><tr><th>User</th><th>Status</th><th>Role</th><th>Memberships</th><th>Last sign-in</th></tr></thead><tbody>
      {page.users.map(user => <tr key={user.id}><td><Link href={`/users/${encodeURIComponent(user.id)}`}>{user.display_name}</Link><small>{user.username}</small></td><td><span className={`status ${user.status}`}>{user.status}</span>{user.must_change_password && <small>Password setup required</small>}</td><td>{user.role === "admin" ? "Administrator" : "User"}</td><td>{user.groups.length ? user.groups.map(group => group.name).join(", ") : "No groups"}</td><td>{date(user.last_login_at)}</td></tr>)}
    </tbody></table></div>
    {!loading && !error && !page.users.length && <p className="empty">No matching users.</p>}
    <nav className="pagination" aria-label="User pages"><button disabled={loading || cursors.length === 1} onClick={() => { setLoading(true); setCursors(values => values.slice(0, -1)); }}>Previous</button><span>Page {cursors.length}</span><button disabled={loading || !page.next_cursor} onClick={() => { setLoading(true); setCursors(values => [...values, page.next_cursor!]); }}>Next</button></nav>
  </section>;
}
