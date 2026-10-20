"use client";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useSessionRefresh } from "../../../web/session-refresh";
import { useAccountPreferences } from "../../../web/account-preferences";
import { clearAppearanceBinding } from "../../../web/appearance";
import { evolutionSectionPath } from "../../../web/application-paths";
import { useLocale } from "../../../web/i18n";
import { base, type Session, type GroupPage } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";
import { SessionContext, lastGroupKey, useGroup, useSession } from "./group-context";
import { GroupList } from "./group-list";
import { PageHeading } from "./group-layout";

export function SpaceShell({ children }: { children: ReactNode }) {
  const tr = useT();
  const session = useRead<Session>("/session", true, true);
  useSessionRefresh(session.refresh);
  const [lastSession, setLastSession] = useState<Session>();
  useEffect(() => {
    // Keep only the logout credential available if current admission is revoked.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (session.data) setLastSession(session.data);
  }, [session.data]);
  if (!session.data) return <main id="main-content" className="session-gate">
    <p className="brand">{tr("Evolution Space")}</p>
    <h1>{tr(session.loading ? "Checking your session…" : "Sign in to Evolution Space")}</h1>
    {!session.loading && <><p role="status">{tr(session.status === 401 || session.status === 403 ? "Use your central account to continue." : "Identity is unavailable. Retry when the service is restored.")}</p>
      <a className="button primary" href={`${base}/auth/login?return=${encodeURIComponent(window.location.pathname + window.location.search)}`}>{tr("Sign in")}</a>
      <button onClick={session.refresh}>{tr("Retry")}</button>
      {lastSession && <SignOut session={lastSession} />}</>}
  </main>;
  return <SessionContext.Provider value={session.data}><Workspace key={session.data.subject} session={session.data}>
    {session.status && <div role="alert"><p>{tr("Identity is unavailable. Retry when the service is restored.")}</p><button onClick={session.refresh}>{tr("Retry")}</button></div>}
    {children}
  </Workspace></SessionContext.Provider>;
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
  return <><button disabled={pending} onClick={() => void logout()}>{tr(pending ? "Signing out…" : "Sign out of this central session")}</button>{error && <p role="alert">{tr("Sign out is unavailable. Please retry.")}</p>}</>;
}
function AccountMenu({ session }: { session: Session }) {
  const tr = useT(); const menu = useRef<HTMLDetailsElement>(null);
  useEffect(() => {
    const close = (event: MouseEvent | KeyboardEvent) => {
      if (!menu.current?.open) return;
      if (event instanceof KeyboardEvent ? event.key === "Escape" : !menu.current.contains(event.target as Node)) menu.current.open = false;
    };
    document.addEventListener("click", close); document.addEventListener("keydown", close);
    return () => { document.removeEventListener("click", close); document.removeEventListener("keydown", close); };
  }, []);
  // The workbench is a separate build; a document navigation crosses the edge.
  return <details ref={menu} className="account-menu"><summary>{tr("Account")}</summary><div className="account-menu-items">
    {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
    <a href="/">{tr("Back to Workbench")}</a>
    <SignOut session={session} />
  </div></details>;
}
function Workspace({ session, children }: { session: Session; children: ReactNode }) {
  const tr = useT();
  // Preferences are edited only in Workbench settings; Space applies them.
  useAccountPreferences(session.subject, true);
  return <>
    <a className="skip-link" href="#main-content">{tr("Skip to main content")}</a>
    <header className="header"><Link prefetch={false} className="brand" href="/?choose=1">{tr("Evolution Space")}</Link>
      <AccountMenu session={session} />
    </header>
    <div id="main-content" tabIndex={-1}>{children}</div>
  </>;
}

// Opening the application continues in the last workspace, or the only one,
// and falls back to an explicit choice. A stale preference is verified first.
export function Landing() {
  const tr = useT(); const router = useRouter(); const session = useSession(); const params = useSearchParams();
  const choose = params.get("choose") === "1" || params.has("groups_search") || params.has("groups_after");
  const [remembered] = useState(() => { try { return choose ? "" : localStorage.getItem(lastGroupKey(session.subject)) ?? ""; } catch { return ""; } });
  const last = useRead<GroupPage>(remembered ? `/groups/${encodeURIComponent(remembered)}` : "/groups?limit=2");
  const target = last.data && (remembered ? last.data.items[0]?.id : !choose && last.data.items.length === 1 && !last.data.has_more ? last.data.items[0].id : "");
  useEffect(() => { if (target) router.replace(`/groups/${encodeURIComponent(target)}/`); }, [target, router]);
  if (target || last.loading && !choose) return <main className="group-message"><p role="status">{tr("Opening your workspace…")}</p></main>;
  return <main className="landing"><h1>{tr("Choose a group")}</h1><p>{tr("Each group keeps its own sources, workflows, runs and permissions. You can switch groups at any time.")}</p><GroupList persist /></main>;
}

const states: Record<string, string> = { unconfigured: "Not configured", draft: "Draft", activating: "Activating", active: "Active", failed: "Activation failed", discarding: "Discarding", discarded: "Discarded" };
type RecentRun = { id: string; state: string; handoff: string; evaluation_state: string; created_at: string; pins: { template: { id: string; revision: number } } };
export function GroupOverview() {
  const tr = useT(); const { group, refresh } = useGroup(); const search = useSearchParams(); const locale = useLocale();
  const root = `/groups/${encodeURIComponent(group.id)}/`;
  const runs = useRead<{ items: RecentRun[]; has_more: boolean }>(`/groups/${encodeURIComponent(group.id)}/runs?limit=5`);
  const active = runs.data?.items.filter(run => ["queued", "running", "pause_requested", "paused", "cancelling"].includes(run.state)) ?? [];
  const review = runs.data?.items.filter(run => run.state === "completed" && run.handoff === "submitted") ?? [];
  const failed = runs.data?.items.filter(run => run.state === "failed" || run.handoff === "failed") ?? [];
  return <section className="overview" aria-label={tr("Group overview")}>
    <PageHeading title={group.name} description={group.description || undefined} actions={<>
      <button onClick={refresh}>{tr("Refresh")}</button>
      {group.can_run && <Link prefetch={false} className="button primary" href={`${root}runs/new/`}>{tr("New run")}</Link>}
    </>} />
    <div className="overview-cards">
      <section className="card" id="configuration"><h2>{tr("Configuration")}</h2><p className="status">{tr(states[group.state] || group.state)}</p>
        <dl><div><dt>{tr("Active revision")}</dt><dd>{group.active_revision || "—"}</dd></div><div><dt>{tr("Desired revision")}</dt><dd>{group.desired_revision || "—"}</dd></div><div><dt>{tr("Automatic optimization")}</dt><dd>{tr(group.auto_enabled ? "On" : "Off")}</dd></div></dl>
        {group.desired_revision !== group.active_revision && <p>{tr("A configuration change is pending. The active revision remains separate.")}</p>}
        <Link prefetch={false} href={`${root}settings/configuration/`}>{tr(group.can_manage ? "Edit configuration" : "View configuration")}</Link>
      </section>
      <section className="card"><h2>{tr("Activity")}</h2>
        {runs.loading && <p role="status">{tr("Loading runs…")}</p>}
        {runs.status && <p role="alert">{tr("Runs are unavailable. Retry when the service is restored.")}</p>}
        {runs.data && <dl><div><dt>{tr("Active runs")}</dt><dd>{active.length}</dd></div><div><dt>{tr("Ready for expert review")}</dt><dd>{review.length}</dd></div><div><dt>{tr("Needs attention")}</dt><dd>{failed.length}</dd></div></dl>}
        {runs.data?.has_more && <p><small>{tr("Counts cover the latest runs. Open run history for the full list.")}</small></p>}
        <Link prefetch={false} href={evolutionSectionPath(group.id, "runs", search)}>{tr("Open run history")}</Link>
      </section>
    </div>
    <section className="card concept-flow" aria-labelledby="flow-heading"><h2 id="flow-heading">{tr("How the pieces fit together")}</h2>
      <ol className="flow-steps">
        <li><Link prefetch={false} href={`${root}workflows/`}><span className="flow-index">1</span><strong>{tr("Workflow")}</strong><small>{tr("How the work runs: a graph of agents and checks with typed inputs and outputs. Start from a built-in example.")}</small></Link></li>
        <li><Link prefetch={false} href={`${root}templates/`}><span className="flow-index">2</span><strong>{tr("Goal template")}</strong><small>{tr("What you want to produce: the goal, compatible workflows, output format and a suggested directory.")}</small></Link></li>
        <li><Link prefetch={false} href={`${root}settings/configuration/#bindings`}><span className="flow-index">3</span><strong>{tr("Configuration binding")}</strong><small>{tr("Where it applies: sources to read, the Volume and directory to write, trigger, budget and evaluation mode.")}</small></Link></li>
        <li><Link prefetch={false} href={`${root}runs/`}><span className="flow-index">4</span><strong>{tr("Run and review")}</strong><small>{tr("Each run writes a branch in Volume Studio. An expert reviews and merges it; publishing is a separate step.")}</small></Link></li>
      </ol>
      <p className="flow-optional"><Link prefetch={false} href={`${root}profiles/`}>{tr("Evaluation profile")}</Link> · {tr("Optional. Measures a candidate on a held-out dataset before review. A binding sets it to Off, Informational or Required for merge.")}</p>
      {!group.active_revision && <p className="hint">{tr(group.can_manage ? "This group has no active configuration yet. Also approve sources under Settings, then review and activate the configuration." : "No active configuration. Ask a configuration manager to complete setup.")}</p>}
    </section>
    <section className="card"><div className="section-heading"><h2>{tr("Recent runs")}</h2><Link prefetch={false} href={evolutionSectionPath(group.id, "runs", search)}>{tr("View all")}</Link></div>
      {runs.data && (!runs.data.items.length ? <p>{tr("No runs yet. Start a manual run, or test a workflow draft.")}</p> : <table tabIndex={0} aria-label={tr("Recent runs")}><thead><tr>{["Run", "Execution state", "Handoff", "Evaluation", "Template", "Created at"].map(label => <th key={label} scope="col">{tr(label)}</th>)}</tr></thead>
        <tbody>{runs.data.items.map(run => <tr key={run.id}><th scope="row"><Link prefetch={false} href={`${root}runs/${encodeURIComponent(run.id)}/`}>{run.id}</Link></th><td>{tr(run.state === "pause_requested" ? "Pausing" : run.state)}</td><td>{tr(run.handoff)}</td><td>{tr(run.evaluation_state)}</td><td>{run.pins.template.id} · {run.pins.template.revision}</td><td><time dateTime={run.created_at}>{new Date(run.created_at).toLocaleString(locale)}</time></td></tr>)}</tbody></table>)}
    </section>
  </section>;
}
