"use client";
import { useT } from "@/lib/i18n";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useCallback, useEffect, useRef, useState, type MouseEvent } from "react";
import { api, APIError } from "@/lib/api";
import type { Identity } from "@web/identity";
import { displayName } from "@web/brand";
import { clearAppearanceBinding } from "@web/appearance";
import { useAccountPreferences } from "@web/account-preferences";
import { useSessionRefresh } from "@web/session-refresh";
import { PolicyList } from "./policy-list";
import { PolicyEditor } from "./policy-editor";
import { StudioPermissionsEditor } from "./studio-permissions";
import { GroupEditor } from "./group-editor";
import { ProjectList, ProjectView } from "./project-views";
import { UserList } from "./user-list";
import { ResourceEditor } from "./resource-editor";
import { UserConfiguration } from "./configuration-controls";
import { ResourceCenter } from "./resource-center";
import { UserEditor } from "./user-editor";
import { AuditView, OperationView } from "./audit-view";
const evolutionRoutes: Record<string, string> = { optimization: "settings/configuration/", run: "runs/new/", corpus: "settings/sources/", "evolution-permissions": "settings/permissions/" };
export function Administration() {
  const tr = useT();
  const pathname = usePathname();
  const route = pathname.replace(/^\/admin(?=\/|$)/, "").split("/").filter(Boolean).map(decodeURIComponent);
  // Group evolution work moved to Evolution Space; keep old links working.
  const evolutionDestination = route[0] === "groups" && route.length === 3 ? evolutionRoutes[route[2]] : undefined;
  const [identity, setIdentity] = useState<Identity | null>(null), [status, setStatus] = useState("loading"), [dirty, setDirty] = useState(false), [error, setError] = useState("");
  const generation = useRef(0);
  const dirtyRef = useRef(false);
  const reloadIdentity = useCallback(() => {
    const current = ++generation.current;
    return api<Identity>("/api/auth/session").then(account => {
      if (current !== generation.current) return;
      if (account.must_change_password) { dirtyRef.current = false; setIdentity(null); clearAppearanceBinding(); location.replace(`/?return_to=${encodeURIComponent(location.pathname + location.search)}`); return; }
      setIdentity(account); setStatus(account.role === "admin" ? "ready" : "denied");
    }).catch(error => {
      if (current !== generation.current) return;
      if (error instanceof APIError && error.status === 401) { dirtyRef.current = false; setIdentity(null); clearAppearanceBinding(); location.replace(`/?return_to=${encodeURIComponent(location.pathname + location.search)}`); return; }
      setStatus("unavailable");
    });
  }, []);
  const evolutionGroup = evolutionDestination ? route[1] : "";
  useEffect(() => {
    if (evolutionDestination) location.replace(`/evolution-space/groups/${encodeURIComponent(evolutionGroup)}/${evolutionDestination}`);
  }, [evolutionDestination, evolutionGroup]);
  useEffect(() => {
    void reloadIdentity();
    const changed = () => void reloadIdentity();
    window.addEventListener("admin-auth-expired", changed); window.addEventListener("admin-authority-denied", changed);
    return () => { window.removeEventListener("admin-auth-expired", changed); window.removeEventListener("admin-authority-denied", changed); };
  }, [reloadIdentity]);
  useAccountPreferences(identity?.id);
  useSessionRefresh(() => void reloadIdentity());
  useEffect(() => { document.title = `${tr("Administration")} — ${displayName}`; }, [tr]);
  useEffect(() => {
    if (!dirty) return;
    const before = (event: BeforeUnloadEvent) => { if (dirtyRef.current) { event.preventDefault(); event.returnValue = ""; } };
    window.addEventListener("beforeunload", before); return () => window.removeEventListener("beforeunload", before);
  }, [dirty]);
  const setEditorDirty = useCallback((value: boolean) => { dirtyRef.current = value; setDirty(value); }, []);
  const mayLeave = () => {
    if (dirtyRef.current && !window.confirm(tr("Leave without saving these changes? An unconfirmed request may already have completed."))) return false;
    dirtyRef.current = false; setDirty(false); return true;
  };
  const guardNavigation = (event: MouseEvent<HTMLElement>) => {
    const link = (event.target as Element).closest("a");
    if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || link.target === "_blank" || link.hasAttribute("download")) return;
    const destination = new URL(link.href, location.href);
    if (destination.origin === location.origin && destination.pathname === location.pathname && destination.search === location.search) return;
    if (!mayLeave()) { event.preventDefault(); event.stopPropagation(); }
  };
  const logout = async () => {
    if (!mayLeave()) return;
    try { await api("/api/auth/logout", { method: "POST" }); clearAppearanceBinding(); location.assign(new URL("/", location.origin).href); }
    catch (error) { setError(String(error)); }
  };
  return <div className="admin-shell">
    <header className="admin-header"><strong>{displayName} <span>{tr("Administration")}</span></strong><nav aria-label={tr("Administration")} onClickCapture={guardNavigation}>
      {status === "ready" && <><Link href="/users" aria-current={!route.length || route[0] === "users" ? "page" : undefined}>{tr("Users")}</Link><Link href="/groups" aria-current={route[0] === "groups" ? "page" : undefined}>{tr("Groups")}</Link><Link href="/policies" aria-current={route[0] === "policies" ? "page" : undefined}>{tr("Policies")}</Link><Link href="/projects" aria-current={route[0] === "projects" ? "page" : undefined}>{tr("Projects")}</Link><Link href="/resources/network" aria-current={route[0] === "resources" ? "page" : undefined}>{tr("Resource Center")}</Link><Link href="/audit" aria-current={route[0] === "audit" ? "page" : undefined}>{tr("Audit")}</Link></>}
      <WorkbenchLink />{identity && <button onClick={() => void logout()}>{tr("Sign out")}</button>}
    </nav></header>
    <main>
      {error && <p role="alert" className="error">{tr(error)}</p>}
      {status === "loading" && <p role="status">{tr("Opening administration…")}</p>}
      {status === "denied" && <><h1>{tr("Access denied")}</h1><p>{tr("A platform administrator role is required.")}</p></>}
      {status === "unavailable" && <><p role="alert">{tr("Account access is unavailable.")}</p><button onClick={() => void reloadIdentity()}>{tr("Retry access check")}</button></>}
      {status === "ready" && identity && <div key={identity.id} onClickCapture={guardNavigation}>
        {(!route.length || route[0] === "users" && route.length === 1) ? <UserList />
          : route[0] === "users" && route.length === 2 ? <UserEditor key={route[1]} id={route[1] === "new" ? undefined : route[1]} onDirty={setEditorDirty} />
          : (route[0] === "groups" || route[0] === "policies") && route.length === 1 ? <PolicyList key={route[0]} kind={route[0]} />
          : (route[0] === "users" || route[0] === "groups") && route.length === 3 && route[2] === "studio-permissions" ? <StudioPermissionsEditor key={`${route[0]}-${route[1]}`} kind={route[0] === "users" ? "account" : "group"} subject={route[1]} actor={identity.id} onDirty={setEditorDirty} />
          : route[0] === "groups" && route.length === 2 ? <GroupEditor key={route[1]} id={route[1] === "new" ? undefined : route[1]} onDirty={setEditorDirty} />
          : route[0] === "policies" && route.length === 2 ? <PolicyEditor key={route[1]} id={route[1] === "new" ? undefined : route[1]} onDirty={setEditorDirty} />
          : route[0] === "users" && route.length === 3 && route[2] === "configuration" ? <UserConfiguration key={route[1]} id={route[1]} onDirty={setEditorDirty} />
          : (route[0] === "users" || route[0] === "groups") && route.length === 3 && route[2] === "resources" ? <ResourceEditor key={`${route[0]}-${route[1]}-resources`} kind={route[0] === "users" ? "user" : "group"} id={route[1]} onDirty={setEditorDirty} />
          : route[0] === "projects" && route.length === 1 ? <ProjectList />
          : route[0] === "projects" && route.length === 2 ? <ProjectView key={route[1]} id={route[1]} onDirty={setEditorDirty} />
          : route[0] === "resources" ? <ResourceCenter key={identity.id} accountID={identity.id} section={route[1]} onDirty={setEditorDirty} />
          : route[0] === "audit" && route.length === 1 ? <AuditView />
          : route[0] === "operations" && route.length === 2 ? <OperationView id={route[1]} />
          : <><h1>{tr("Page not found")}</h1><Link href="/users">{tr("Open users")}</Link></>}
      </div>}
    </main>
  </div>;
}
function WorkbenchLink() {
  const tr = useT();
  return (
    // The workbench is a separate build; Next Link would add /admin to this URL.
    // eslint-disable-next-line @next/next/no-html-link-for-pages
    <a href="/">{tr("Return to workbench")}</a>
  );
}
