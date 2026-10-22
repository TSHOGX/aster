"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useUnsavedNavigation } from "../../../web/unsaved-navigation";
import { base, type Group } from "../lib/api";
import { useT } from "../lib/i18n";
import { GroupProvider, useGroup, useSession } from "./group-context";
import { GroupList } from "./group-list";

type Item = { href: string; label: string; allowed?: boolean; reason?: string; exact?: boolean };
function sections(group: Group, admin: boolean): { label: string; items: Item[] }[] {
  return [
    { label: "", items: [{ href: "", label: "Overview", exact: true }] },
    { label: "Build", items: [
      { href: "templates/", label: "Goal templates" },
      { href: "workflows/", label: "Workflows" },
      { href: "profiles/", label: "Evaluation profiles", allowed: group.can_manage, reason: "Requires permission to manage configuration." },
    ] },
    { label: "Operate", items: [
      { href: "runs/", label: "Runs" },
      { href: "evaluations/", label: "Evaluations" },
    ] },
    { label: "Settings", items: [
      { href: "settings/configuration/", label: "Configuration and activation" },
      { href: "settings/sources/", label: "Sources and corpus" },
      { href: "settings/permissions/", label: "Permissions" },
      { href: "settings/models/", label: "Model connections", allowed: admin, reason: "Requires platform administrator." },
    ] },
  ];
}

export function GroupLayout({ id, children }: { id: string; children: ReactNode }) {
  return <GroupProvider key={id} id={id}><Workspace>{children}</Workspace></GroupProvider>;
}
function Workspace({ children }: { children: ReactNode }) {
  const tr = useT(); const { group } = useGroup(); const pathname = usePathname();
  const admin = useSession().entitlements.includes("evolution_space.models");
  const root = `/groups/${encodeURIComponent(group.id)}/`;
  const [switching, setSwitching] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => { if (switching) dialog.current?.showModal(); else dialog.current?.close(); }, [switching]);
  return <div className="group-workspace">
    <aside className="sidebar" aria-label={tr("Group navigation")}>
      <div className="group-switcher"><span>{tr("Group")}</span><strong>{group.name}</strong><button aria-haspopup="dialog" onClick={() => setSwitching(true)}>{tr("Switch group")}</button></div>
      <nav aria-label={tr("Group sections")}>{sections(group, admin).map(section => <div key={section.label || "overview"} className="sidebar-section">
        {section.label && <h2>{tr(section.label)}</h2>}
        <ul>{section.items.map(item => {
          const href = root + item.href;
          const current = item.exact ? pathname === href || pathname === href.slice(0, -1) : pathname.startsWith(href);
          if (item.allowed === false) return <li key={item.href}><span className="sidebar-disabled" aria-disabled="true" title={tr(item.reason!)}>{tr(item.label)}<small>{tr(item.reason!)}</small></span></li>;
          return <li key={item.href}><Link prefetch={false} href={href} aria-current={current ? "page" : undefined}>{tr(item.label)}</Link></li>;
        })}</ul>
      </div>)}</nav>
    </aside>
    <main className="group-content">{children}</main>
    <dialog ref={dialog} className="group-dialog" aria-label={tr("Switch group")} onClose={() => setSwitching(false)} onClick={event => { if (event.target === dialog.current) setSwitching(false); }}>
      {switching && <><div className="section-heading"><h2>{tr("Switch group")}</h2><button onClick={() => setSwitching(false)}>{tr("Close")}</button></div><GroupList selected={group.id} onNavigate={() => setSwitching(false)} /></>}
    </dialog>
  </div>;
}

// Moved settings pages report dirty state; one page-level guard covers
// client-side section navigation and document unload.
export function useDirtyGuard() {
  const [dirty, setDirty] = useState(false);
  useUnsavedNavigation(dirty, base);
  return useCallback((value: boolean) => setDirty(value), []);
}

export function PageHeading({ title, description, actions, crumbs }: { title: ReactNode; description?: string; actions?: ReactNode; crumbs?: { label: string; href?: string }[] }) {
  const tr = useT();
  return <header className="page-header">
    {crumbs && <nav aria-label={tr("Breadcrumb")} className="breadcrumbs"><ol>{crumbs.map((crumb, index) => <li key={index}>{crumb.href ? <Link prefetch={false} href={crumb.href}>{crumb.label}</Link> : <span aria-current="page">{crumb.label}</span>}</li>)}</ol></nav>}
    <div className="section-heading"><div><h1>{title}</h1>{description && <p>{tr(description)}</p>}</div>{actions && <div className="editor-actions">{actions}</div>}</div>
  </header>;
}
