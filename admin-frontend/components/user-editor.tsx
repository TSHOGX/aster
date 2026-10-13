"use client";
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

import { Select } from "@web/select";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/api";
import { date, type Group, type Mutation, type User } from "@/lib/models";
import { ContentInspection } from "./content-inspection";
import { EffectiveAccessView } from "./effective-access";
import { CredentialDialog } from "./credential-dialog";

type Pending = { path: string; body: Record<string, unknown>; method: string; kind: "create" | "edit" | "reset" | "status" };
export function UserEditor({ id, onDirty }: { id?: string; onDirty: (dirty: boolean) => void }) {
  const tr = useT();
  const router = useRouter();
  const [user, setUser] = useState<User | null>(null), [username, setUsername] = useState(""), [name, setName] = useState(""), [role, setRole] = useState("user");
  const [membership, setMembership] = useState("default"), [selected, setSelected] = useState<string[]>([]), [groups, setGroups] = useState<Group[]>([]), [groupCursor, setGroupCursor] = useState("");
  const [loading, setLoading] = useState(!!id), [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [pending, setPending] = useState<Pending | null>(null), [credential, setCredential] = useState<{ username: string; password: string } | null>(null);
  const [operationID,setOperationID]=useState("");
  const alive = useRef(true);
  const changedName = user ? name !== user.display_name : !!name;
  const changedRole = user ? role !== user.role : role !== "user";
  const dirty = !!pending || (!credential && (changedName || changedRole || (!id && !user && (!!username || membership !== "default"))));
  useEffect(() => { onDirty(dirty); return () => onDirty(false); }, [dirty, name, role, username, membership, selected, pending, onDirty]);
  useEffect(() => {
    alive.current = true;
    const abort = new AbortController();
    if (id) {
      void api<User>(`/api/admin/users/${encodeURIComponent(id)}`, { signal: abort.signal }).then(value => { if (alive.current) { setUser(value); setName(value.display_name); setRole(value.role); } })
        .catch(error => { if (!abort.signal.aborted) setError(String(error)); }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    } else {
      void api<{ groups: Group[]; next_cursor?: string }>("/api/admin/groups?limit=100", { signal: abort.signal }).then(value => { if (alive.current) { setGroups(value.groups); setGroupCursor(value.next_cursor || ""); } }).catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    }
    return () => { alive.current = false; abort.abort(); };
  }, [id]);
  const submit = async (request: Pending) => {
    setBusy(true); setError(""); setNotice(""); setPending(request);
    const keepName = changedName, keepRole = changedRole;
    try {
      const result = await mutate<Mutation>(request.path, request.body, request.method);
      if (!alive.current) return;
      let currentUser = result.user;
      if (result.replayed) {
        try { currentUser = await api<User>(`/api/admin/users/${encodeURIComponent(result.user.id)}`); } catch {}
        if (!alive.current) return;
      }
      setUser(currentUser); setName(currentUser.display_name); setRole(currentUser.role); setPending(null);
      if (result.temporary_password) setCredential({ username: result.user.username, password: result.temporary_password });
      else if(request.kind==="status") {setOperationID(result.operation?.id || "");setNotice(result.user.status==="disabled" ? "Suspension recorded. Follow the operation for observed stop/pause progress." : "Account enabled. Projects, schedules and sharing remain suspended until explicitly resumed or reactivated.");}
      else setNotice(result.replayed && request.kind !== "edit" ? "The original change is confirmed. Its password cannot be shown again; issue a new reset if needed." : "Saved.");
    } catch (error) {
      if (!alive.current) return;
      const uncertain = error instanceof APIError && error.code === "outcome_unknown";
      if (!uncertain) setPending(null);
      if (error instanceof APIError && error.code === "revision_conflict" && user) {
        try {
          const latest = await api<User>(`/api/admin/users/${encodeURIComponent(user.id)}`);
          if (!alive.current) return;
          setUser(latest); if (!keepName) setName(latest.display_name); if (!keepRole) setRole(latest.role);
        } catch {}
        if (alive.current) setError("This account changed elsewhere. Current data was reloaded where available; review and reapply your changed fields.");
      } else if (error instanceof APIError && error.code === "last_admin") setError("The last active administrator cannot be demoted or disabled.");
      else if (error instanceof APIError && error.code === "group_unavailable") setError("A selected group is no longer available. Choose current memberships and try again.");
      else if (error instanceof APIError && error.code === "username_taken") setError("That username is already in use. Open the existing account to manage it.");
      else setError(String(error));
    } finally { if (alive.current) setBusy(false); }
  };
  const save = () => {
    if (user) {
      const body: Record<string, unknown> = { request_id: crypto.randomUUID(), expected_revision: user.revision };
      if (changedName) body.display_name = name; if (changedRole) body.role = role;
      void submit({ path: `/api/admin/users/${encodeURIComponent(user.id)}`, body, method: "PATCH", kind: "edit" });
    } else {
      const body: Record<string, unknown> = { request_id: crypto.randomUUID(), username, display_name: name, role };
      if (membership !== "default") body.group_ids = membership === "none" ? [] : selected;
      void submit({ path: "/api/admin/users", body, method: "POST", kind: "create" });
    }
  };
  return <section aria-labelledby="user-title">
    <header className="page-heading"><div><h1 id="user-title">{user ? user.display_name : id ? tr("User details") : tr("Create user")}</h1><p>{user ? user.username : tr("Issue a one-time password for account setup.")}</p></div><Link href="/users">{tr("Back to users")}</Link></header>
    {loading && <p role="status">{tr("Loading account…")}</p>}
    {error && <p className="error" role="alert">{tr(error)}</p>}{notice && <p role="status">{tr(notice)}</p>}
    {pending && !busy && <div className="notice"><p>{tr("The outcome is unconfirmed. Retry checks the original request without repeating its effects.")}</p><button onClick={() => void submit(pending)}>{tr("Check original request")}</button></div>}
    {(!id || user) && <form className="account-form" onSubmit={event => { event.preventDefault(); save(); }}>
      <fieldset disabled={loading || busy || !!pending}>
        {!user && <label>{tr("Username")}<input required maxLength={64} autoComplete="off" pattern="[A-Za-z0-9_-]{1,64}" value={username} onChange={event => setUsername(event.target.value)} /></label>}
        <label>{tr("Display name")}<input required maxLength={256} value={name} onChange={event => setName(event.target.value)} /></label>
        <label>{tr("Management role")}<Select aria-label={tr("Management role")} value={role} onChange={event => setRole(event.target.value)}><option value="user">{tr("User")}</option><option value="admin">{tr("Platform administrator")}</option></Select></label>
        <p className="hint">{tr("Management authority does not grant private-content access. Changing the role signs this account out.")}</p>
        {!user && <>
          <label>{tr("Membership")}<Select aria-label={tr("Membership")} value={membership} onChange={event => setMembership(event.target.value)}><option value="default">{tr("Default group")}</option><option value="custom">{tr("Choose groups")}</option><option value="none">{tr("No groups")}</option></Select></label>
          {membership === "custom" && <fieldset className="group-options"><legend>{tr("Groups")}</legend>{groups.map(group => <label key={group.id}><input type="checkbox" checked={selected.includes(group.id)} onChange={event => setSelected(values => event.target.checked ? [...values, group.id] : values.filter(value => value !== group.id))} />{group.name}</label>)}
            {groupCursor && <button type="button" onClick={() => void api<{groups: Group[]; next_cursor?: string}>(`/api/admin/groups?limit=100&cursor=${encodeURIComponent(groupCursor)}`).then(value => { setGroups(values => [...values, ...value.groups]); setGroupCursor(value.next_cursor || ""); }).catch(error => setError(String(error)))}>{tr("More groups")}</button>}
          </fieldset>}
        </>}
        <div className="form-actions"><button className="primary" type="submit" disabled={!!user && !changedName && !changedRole}>{busy ? tr("Saving…") : user ? tr("Save changes") : tr("Create account")}</button>
        {user && (changedName || changedRole) && <button type="button" onClick={() => { setName(user.display_name); setRole(user.role); setError(""); }}>{tr("Discard edits")}</button>}</div>
      </fieldset>
    </form>}
    {user && !id && <p><Link href={`/users/${encodeURIComponent(user.id)}`}>{tr("Open this account")}</Link></p>}
    {user && <section className="panel" aria-labelledby="security-title"><h2 id="security-title">{tr("Account and security")}</h2>
      <dl><dt>{tr("Status")}</dt><dd>{tr(user.status)}</dd><dt>{tr("Memberships")}</dt><dd>{user.groups.map(group => group.name).join(", ") || tr("No groups")}</dd><dt>{tr("Last sign-in")}</dt><dd>{date(user.last_login_at)}</dd><dt>{tr("Created")}</dt><dd>{date(user.created_at)}</dd><dt>{tr("Updated")}</dt><dd>{date(user.updated_at)}</dd><dt>{tr("Revision")}</dt><dd>{formatQuantity(user.revision)}</dd></dl>
      {user.must_change_password && <p>{tr("Password setup is required.")}</p>}
      <p>{tr("Resetting issues a new one-time password and revokes this account’s sessions. Existing work is not stopped.")}</p>
      <button disabled={busy || !!pending || changedName || changedRole} onClick={() => void submit({ path: `/api/admin/users/${encodeURIComponent(user.id)}/password-reset`, body: { request_id: crypto.randomUUID(), expected_revision: user.revision }, method: "POST", kind: "reset" })}>{tr("Reset password")}</button>
      <button disabled={busy || !!pending || changedName || changedRole} onClick={() => {
        const suspend=user.status==="active";
        if(suspend && !window.confirm(tr("Suspend {name}? Sessions and sharing access will close, schedules and queues will be held, and running work will be stopped and Projects paused. Files and history are preserved.", { name: user.username }))) return;
        void submit({path:`/api/admin/users/${encodeURIComponent(user.id)}/${suspend?"suspend":"enable"}`,body:{request_id:crypto.randomUUID(),expected_revision:user.revision},method:"POST",kind:"status"});
      }}>{user.status==="active" ? tr("Suspend account") : tr("Enable account")}</button>
      {operationID && <Link className="inline-link" href={`/operations/${encodeURIComponent(operationID)}`}>{tr("View suspension operation")}</Link>}

      <Link className="inline-link" href={`/projects?owner_id=${encodeURIComponent(user.id)}`}>{tr("Projects and Volumes")}</Link>
      <Link className="inline-link" href={`/users/${encodeURIComponent(user.id)}/resources`}>{tr("Resources and usage")}</Link>
      <Link className="inline-link" href={`/users/${encodeURIComponent(user.id)}/configuration`}>{tr("Effective configuration")}</Link>
      <Link className="inline-link" href={`/audit?target=${encodeURIComponent(user.id)}`}>{tr("View account audit")}</Link>
    </section>}
    {user && <><EffectiveAccessView accountID={user.id} /><ContentInspection accountID={user.id}/></>}
    {credential && <CredentialDialog username={credential.username} password={credential.password} onClose={() => {
      setCredential(null); setNotice("Credential dialog closed. Issue a new reset if another password is needed.");
      if (!id && user) router.replace(`/users/${encodeURIComponent(user.id)}`);
    }} />}
  </section>;
}
