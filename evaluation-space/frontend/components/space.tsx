/* eslint-disable @next/next/no-html-link-for-pages -- Root links leave this application base path for the central application. */
"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { LanguagePicker } from "../../../web/language-picker";
import { useAccountPreferences } from "../../../web/account-preferences";
import { clearAppearanceBinding } from "../../../web/appearance";
import { base, type Session, type Group, type GroupPage } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";
import { ProfileEditor } from "./profile-editor";
import { WorkflowDrafts } from "./workflow-drafts";

export function Space({ groupID = "" }: { groupID?: string }) {
  const tr = useT();
  const session = useRead<Session>("/session", true);
  const [lastSession, setLastSession] = useState<Session>();
  useEffect(() => {
    // Keep only the logout credential available if current admission is revoked.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (session.data) setLastSession(session.data);
  }, [session.data]);
  if (!session.data) return <main className="session-gate">
    <p className="brand">{tr("Evaluation Space")}</p>
    <h1>{tr(session.loading ? "Checking your session…" : "Sign in to Evaluation Space")}</h1>
    {!session.loading && <><p role="status">{tr(session.status === 401 || session.status === 403 ? "Use your central account to continue." : "Identity is unavailable. Retry when the service is restored.")}</p>
      <a className="button primary" href={`${base}/auth/login?return=${encodeURIComponent(window.location.pathname + window.location.search)}`}>{tr("Sign in")}</a>
      <button onClick={session.refresh}>{tr("Retry")}</button>
      {lastSession && <SignOut session={lastSession} />}</>}
  </main>;
  return <Workspace key={session.data.subject} session={session.data} groupID={groupID} />;
}
function SignOut({ session }: { session: Session }) {
  const tr = useT(); const [pending, setPending] = useState(false); const [error, setError] = useState(false);
  async function logout() {
    setPending(true); setError(false);
    try {
      const response = await fetch(`${base}/auth/logout`, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": session.csrf } });
      if (!response.ok) throw new Error();
      clearAppearanceBinding(); window.location.reload();
    } catch { setError(true); setPending(false); }
  }
  return <div><button disabled={pending} onClick={() => void logout()}>{tr(pending ? "Signing out…" : "Sign out of this central session")}</button>{error && <p role="alert">{tr("Sign out is unavailable. Please retry.")}</p>}</div>;
}
function Workspace({ session, groupID }: { session: Session; groupID: string }) {
  const tr = useT(); const preferences = useAccountPreferences(session.subject, true);
  return <>
    <header className="header"><Link className="brand" href="/">{tr("Evaluation Space")}</Link>
      <nav aria-label={tr("Central application")}><a href="/">{tr("Central application")}</a><a href="/volumes-studio/">{tr("Volume Studio")}</a></nav>
      <LanguagePicker preferences={preferences} /><SignOut session={session} />
    </header>
    <main className="workspace"><GroupList selected={groupID} /><section className="overview" aria-label={tr("Group overview")}>
      {groupID ? <GroupOverview key={groupID} id={groupID} /> : <><h1>{tr("Select a group")}</h1><p>{tr("Each group keeps its own configuration and permissions.")}</p></>}
    </section></main>
  </>;
}
function GroupList({ selected }: { selected: string }) {
  const tr = useT(); const [draft, setDraft] = useState(""); const [search, setSearch] = useState(""); const [after, setAfter] = useState("");
  const read = useRead<GroupPage>(`/groups?${new URLSearchParams({ search, after, limit: "25" })}`);
  return <aside className="groups" aria-label={tr("Groups")}><div className="section-heading"><h2>{tr("Groups")}</h2><button onClick={read.refresh}>{tr("Refresh")}</button></div>
    <form onSubmit={event => { event.preventDefault(); setSearch(draft.trim()); setAfter(""); }}><label htmlFor="group-search">{tr("Search groups")}</label><div className="search"><input id="group-search" value={draft} maxLength={256} onChange={e => setDraft(e.target.value)} /><button type="submit">{tr("Search")}</button></div></form>
    {read.loading && <p role="status">{tr("Loading groups…")}</p>}
    {read.status && <div role="alert"><p>{tr(read.status === 401 || read.status === 403 ? "Group access was revoked. Sign in again or contact an administrator." : "Group access is unavailable. Please retry.")}</p><button onClick={read.refresh}>{tr("Retry")}</button></div>}
    {read.data && <><ul>{read.data.items.map(group => <li key={group.id}><Link prefetch={false} href={`/groups/${encodeURIComponent(group.id)}/`} aria-current={selected === group.id ? "page" : undefined}>{group.name}</Link></li>)}</ul>
      {!read.data.items.length && <div><h3>{tr(search ? "No matching groups" : "No readable groups")}</h3>{!search && <p>{tr("Ask an administrator for group membership and configuration read access.")}</p>}</div>}
      <div className="pagination">{after && <button onClick={() => setAfter("")}>{tr("First page")}</button>}{read.data.has_more && <button onClick={() => setAfter(read.data!.next_after)}>{tr("Next page")}</button>}</div>
    </>}
  </aside>;
}
const states: Record<string, string> = { unconfigured: "Not configured", draft: "Draft", activating: "Activating", active: "Active", failed: "Activation failed", discarding: "Discarding", discarded: "Discarded" };
type RunSummary = { id: string; state: string; evaluation_state: string; pins: { workflow?: { id?: string; revision?: number } } };
type RunPage = { items: RunSummary[]; next_after: string; has_more: boolean };
type EvaluationSummary = { id: string; state: string; profile: { ref?: { id?: string; revision?: number } } };
type EvaluationPage = { items: EvaluationSummary[]; next_after: string; has_more: boolean };
function GroupOverview({ id }: { id: string }) {
  const tr = useT(); const read = useRead<GroupPage>(`/groups/${encodeURIComponent(id)}`);
  if (read.loading) return <p role="status">{tr("Loading group…")}</p>;
  if (read.status || !read.data?.items[0]) return <div role="alert"><h1>{tr(read.status === 401 || read.status === 403 ? "You no longer have access to this group." : "Group access is unavailable. Please retry.")}</h1><button onClick={read.refresh}>{tr("Retry")}</button><Link href="/">{tr("Back to groups")}</Link></div>;
  return <GroupDetails group={read.data.items[0]} refresh={read.refresh} />;
}
function GroupDetails({ group, refresh }: { group: Group; refresh: () => void }) {
  const tr = useT();
  const runs = useRead<RunPage>(`/groups/${encodeURIComponent(group.id)}/runs?limit=5`);
  const evaluations = useRead<EvaluationPage>(`/groups/${encodeURIComponent(group.id)}/evaluations?limit=5`);
  return <><div className="section-heading"><h1>{group.name}</h1><div className="editor-actions"><button onClick={refresh}>{tr("Refresh")}</button>{group.can_manage && <a className="button" href={`/admin/groups/${encodeURIComponent(group.id)}/optimization`}>{tr("Open configuration")}</a>}</div></div><p>{group.description}</p>
    <section className="card"><h2>{tr("Configuration")}</h2><p className="status">{tr(states[group.state] || group.state)}</p>
      <dl><div><dt>{tr("Desired revision")}</dt><dd>{group.desired_revision || "—"}</dd></div><div><dt>{tr("Active revision")}</dt><dd>{group.active_revision || "—"}</dd></div><div><dt>{tr("Automatic optimization")}</dt><dd>{tr(group.auto_enabled ? "On" : "Off")}</dd></div></dl>
      {!group.active_revision && <p>{tr("No active configuration. Ask a configuration manager to complete setup.")}</p>}
      {group.desired_revision !== group.active_revision && <p>{tr("A configuration change is pending. The active revision remains separate.")}</p>}
    </section>
    <div className="workbench-grid overview-grid"><section className="card"><h2>{tr("Recent runs")}</h2>{runs.status && <p role="alert">{tr("Runs are unavailable. Retry when the service is restored.")}</p>}{!runs.loading && !runs.data?.items.length && <p>{tr("No runs yet.")}</p>}<ul>{runs.data?.items.map(run => <li key={run.id}><Link href={`/groups/${encodeURIComponent(group.id)}/runs/${encodeURIComponent(run.id)}`}>{run.id}</Link> · {tr(run.state === "pause_requested" ? "Pausing" : run.state)} · {tr(run.evaluation_state)}</li>)}</ul></section><section className="card"><h2>{tr("Recent evaluations")}</h2>{evaluations.status && <p role="alert">{tr("Evaluations are unavailable. Retry when the service is restored.")}</p>}{!evaluations.loading && !evaluations.data?.items.length && <p>{tr("No evaluations yet.")}</p>}<ul>{evaluations.data?.items.map(job => <li key={job.id}><Link href={`/groups/${encodeURIComponent(group.id)}/evaluations/${encodeURIComponent(job.id)}`}>{job.id}</Link> · {tr(job.state)} · {job.profile.ref?.id ?? "—"}</li>)}</ul></section></div>
    <WorkflowDrafts group={group.id} editable={group.can_edit_workflows} evidence={group.can_read_evidence} />
    {group.can_manage && <ProfileEditor group={group.id} />}<section className="card"><h2>{tr("Your permissions")}</h2><dl>{[["Read configuration", true], ["Manage configuration", group.can_manage], ["Edit and publish workflows", group.can_edit_workflows], ["Operate runs", group.can_run], ["Read source evidence", group.can_read_evidence]].map(([label, allowed]) => <div key={String(label)}><dt>{tr(String(label))}</dt><dd>{tr(allowed ? "Allowed" : "Not granted")}</dd></div>)}</dl><p>{tr("Configuration access does not grant source evidence access.")}</p></section>
  </>;
}
