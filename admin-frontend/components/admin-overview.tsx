"use client";
import Link from "next/link";
import { useEffect, useState, type ReactNode } from "react";
import { Guide, type GuideStep } from "@web/guide";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";

type Counts = { users?: number; groups?: number; policies?: number; projects?: number };
type Page = { users?: unknown[]; groups?: unknown[]; policies?: unknown[]; projects?: unknown[]; next_cursor?: string };

// Counts come from the first page of each list. "50+" means more exist; an
// unavailable list leaves its count blank instead of blocking the overview.
function count(page: Page, key: keyof Page) {
  const items = page[key];
  return Array.isArray(items) ? items.length : undefined;
}
const nextLink = ({ href, className, children }: { href: string; className: string; children: ReactNode }) => <Link href={href} className={className}>{children}</Link>;

// Administration overview: how accounts, access, environments and projects
// relate, in the order an administrator usually sets them up.
export function AdminOverview() {
  const tr = useT();
  const [counts, setCounts] = useState<Counts>({});
  const [more, setMore] = useState<Record<string, boolean>>({});
  useEffect(() => {
    const abort = new AbortController();
    const load = (key: keyof Counts, path: string) => api<Page>(path, { signal: abort.signal }).then(page => {
      if (abort.signal.aborted) return;
      setCounts(value => ({ ...value, [key]: count(page, key) }));
      setMore(value => ({ ...value, [key]: !!page.next_cursor }));
    }).catch(() => {});
    void load("users", "/api/admin/users?limit=50");
    void load("groups", "/api/admin/groups?limit=50");
    void load("policies", "/api/admin/policies?limit=50");
    void load("projects", "/api/admin/projects?limit=50");
    return () => abort.abort();
  }, []);
  const figure = (key: keyof Counts) => counts[key] === undefined ? "" : `${counts[key]}${more[key] ? "+" : ""}`;
  const steps: GuideStep[] = [
    { id: "users", href: "/users", link: nextLink, title: tr("Users"), description: tr("Create accounts and give each person a temporary password. Only administrators open this console.") },
    { id: "groups", href: "/groups", link: nextLink, title: tr("Groups"), description: tr("Put people who share access in a group. Groups also own Evolution Space configuration and Studio access.") },
    { id: "policies", href: "/policies", link: nextLink, title: tr("Policies"), description: tr("Bind policies to groups to allow actions and environments. A denial always wins over an allowance.") },
    { id: "resources", href: "/resources/environment", link: nextLink, title: tr("Resource Center"), description: tr("Define the environments, network rules and volumes that Projects run with, and roll out content updates.") },
    { id: "projects", href: "/projects", link: nextLink, title: tr("Projects"), description: tr("Inspect running work, its saved environment and volumes, and pause or stop it when needed.") },
    { id: "audit", href: "/audit", link: nextLink, title: tr("Audit"), description: tr("Every administrative change and sensitive access is recorded here for review.") },
  ];
  const stats: [string, string, keyof Counts][] = [["Users", "/users", "users"], ["Groups", "/groups", "groups"], ["Policies", "/policies", "policies"], ["Projects", "/projects", "projects"]];
  return <section aria-labelledby="overview-heading">
    <header className="page-heading"><div><h1 id="overview-heading">{tr("Administration overview")}</h1><p>{tr("Set up who can sign in, what they may do, and which environments their Projects use. Work through the steps in order on a new installation.")}</p></div>
      <div className="button-row"><Link className="button primary" href="/users/new">{tr("Create user")}</Link><Link className="button secondary" href="/groups/new">{tr("Create group")}</Link></div></header>
    <ul className="overview-stats" aria-label={tr("Current totals")}>{stats.map(([label, href, key]) => <li key={key}><Link href={href}><span>{tr(label)}</span><strong>{figure(key) || "—"}</strong></Link></li>)}</ul>
    <Guide label={tr("How administration fits together")} title={tr("How administration fits together")} description={tr("Each step builds on the one before it.")} steps={steps} columns={3}
      // Studio and Evolution Space are separate builds; Next Link would add /admin.
      // eslint-disable-next-line @next/next/no-html-link-for-pages
      note={<>{tr("Content and evolution are managed in their own applications:")} <a href="/volumes-studio/">{tr("Volume Studio")}</a> {tr("for reviewed file libraries, and")} <a href="/evolution-space/">{tr("Evolution Space")}</a> {tr("for group knowledge workflows. Grant access to them from a user or group page.")}</>} />
  </section>;
}
