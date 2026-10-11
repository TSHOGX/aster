"use client";
import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError } from "@/lib/api";
import type { Identity } from "@web/identity";
import { displayName } from "@web/brand";
import { ACTIVE_APPEARANCE_KEY, appearanceKey, applyAppearance, bindAppearance, cacheAppearance, cachedAppearance, clearAppearanceBinding, DEFAULT_APPEARANCE, type Preferences } from "@web/appearance";
import { PolicyList } from "./policy-list";
import { PolicyEditor } from "./policy-editor";
import { GroupEditor } from "./group-editor";
import { UserList } from "./user-list";
import { ResourceEditor } from "./resource-editor";
import { RuntimeEditor } from "./runtime-editor";
import { NetworkEditor } from "./network-editor";
import { UserEditor } from "./user-editor";
import { AuditView, OperationView } from "./audit-view";
export function Administration({ route }: { route: string[] }) {
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
  useEffect(() => {
    void reloadIdentity();
    const changed = () => void reloadIdentity();
    const visible = () => { if (!document.hidden) changed(); };
    const storage = (event: StorageEvent) => { if (event.key === ACTIVE_APPEARANCE_KEY || event.key === null) changed(); };
    window.addEventListener("admin-auth-expired", changed); window.addEventListener("admin-authority-denied", changed);
    window.addEventListener("storage", storage); document.addEventListener("visibilitychange", visible);
    return () => { window.removeEventListener("admin-auth-expired", changed); window.removeEventListener("admin-authority-denied", changed); window.removeEventListener("storage", storage); document.removeEventListener("visibilitychange", visible); };
  }, [reloadIdentity]);
  const accountID = identity?.id;
  useEffect(() => {
    if (!accountID) return;
    let active = true;
    bindAppearance(accountID);
    let preferences = cachedAppearance(accountID) || DEFAULT_APPEARANCE;
    const refresh = () => void api<Preferences>("/api/me/preferences").then(value => {
      if (!active) return;
      if (value.revision > 0) { preferences = value; cacheAppearance(accountID, value); }
      applyAppearance(preferences);
    }).catch(() => {});
    refresh();
    const system = window.matchMedia("(prefers-color-scheme: dark)");
    const changed = () => applyAppearance(preferences);
    const storage = (event: StorageEvent) => { if (event.key === appearanceKey(accountID)) refresh(); };
    system.addEventListener("change", changed); window.addEventListener("storage", storage);
    return () => { active = false; system.removeEventListener("change", changed); window.removeEventListener("storage", storage); };
  }, [accountID]);
  useEffect(() => {
    if (!dirty) return;
    const before = (event: BeforeUnloadEvent) => { if (dirtyRef.current) { event.preventDefault(); event.returnValue = ""; } };
    window.addEventListener("beforeunload", before); return () => window.removeEventListener("beforeunload", before);
  }, [dirty]);
  const setEditorDirty = useCallback((value: boolean) => { dirtyRef.current = value; setDirty(value); }, []);
  const mayLeave = () => {
    if (dirtyRef.current && !window.confirm("Leave without saving these changes? An unconfirmed request may already have completed.")) return false;
    dirtyRef.current = false; setDirty(false); return true;
  };
  const logout = async () => {
    if (!mayLeave()) return;
    try { await api("/api/auth/logout", { method: "POST" }); clearAppearanceBinding(); location.assign(new URL("/", location.origin).href); }
    catch (error) { setError(String(error)); }
  };
  return <div className="admin-shell">
    <header className="admin-header"><strong>{displayName} <span>Administration</span></strong><nav aria-label="Administration" onClickCapture={event => { if ((event.target as Element).closest("a") && !mayLeave()) { event.preventDefault(); event.stopPropagation(); } }}>
      {status === "ready" && <><Link href="/users" aria-current={!route.length || route[0] === "users" ? "page" : undefined}>Users</Link><Link href="/groups" aria-current={route[0] === "groups" ? "page" : undefined}>Groups</Link><Link href="/policies" aria-current={route[0] === "policies" ? "page" : undefined}>Policies</Link><Link href="/runtime" aria-current={route[0] === "runtime" ? "page" : undefined}>Runtime</Link><Link href="/network" aria-current={route[0] === "network" ? "page" : undefined}>Network</Link><Link href="/audit" aria-current={route[0] === "audit" ? "page" : undefined}>Audit</Link></>}
      <WorkbenchLink />{identity && <button onClick={() => void logout()}>Sign out</button>}
    </nav></header>
    <main>
      {error && <p role="alert" className="error">{error}</p>}
      {status === "loading" && <p role="status">Opening administration…</p>}
      {status === "denied" && <><h1>Access denied</h1><p>A platform administrator role is required.</p></>}
      {status === "unavailable" && <><p role="alert">Account access is unavailable.</p><button onClick={() => void reloadIdentity()}>Retry access check</button></>}
      {status === "ready" && identity && <div key={identity.id} onClickCapture={event => {
        const link = (event.target as Element).closest("a");
        if (link && !mayLeave()) { event.preventDefault(); event.stopPropagation(); }
      }}>
        {(!route.length || route[0] === "users" && route.length === 1) ? <UserList />
          : route[0] === "users" && route.length === 2 ? <UserEditor key={route[1]} id={route[1] === "new" ? undefined : route[1]} onDirty={setEditorDirty} />
          : (route[0] === "groups" || route[0] === "policies") && route.length === 1 ? <PolicyList key={route[0]} kind={route[0]} />
          : route[0] === "groups" && route.length === 2 ? <GroupEditor key={route[1]} id={route[1] === "new" ? undefined : route[1]} onDirty={setEditorDirty} />
          : route[0] === "policies" && route.length === 2 ? <PolicyEditor key={route[1]} id={route[1] === "new" ? undefined : route[1]} onDirty={setEditorDirty} />
          : (route[0] === "users" || route[0] === "groups") && route.length === 3 && route[2] === "resources" ? <ResourceEditor key={`${route[0]}-${route[1]}-resources`} kind={route[0] === "users" ? "user" : "group"} id={route[1]} onDirty={setEditorDirty} />
          : route[0] === "runtime" && route.length === 1 ? <RuntimeEditor onDirty={setEditorDirty} />
          : route[0] === "network" && route.length === 1 ? <NetworkEditor key="default-network" onDirty={setEditorDirty} />
          : route[0] === "users" && route.length === 3 && route[2] === "network" ? <NetworkEditor key={`network-${route[1]}`} id={route[1]} onDirty={setEditorDirty} />
          : route[0] === "audit" && route.length === 1 ? <AuditView />
          : route[0] === "operations" && route.length === 2 ? <OperationView id={route[1]} />
          : <><h1>Page not found</h1><Link href="/users">Open users</Link></>}
      </div>}
    </main>
  </div>;
}
function WorkbenchLink() {
  return (
    // The workbench is a separate build; Next Link would add /admin to this URL.
    // eslint-disable-next-line @next/next/no-html-link-for-pages
    <a href="/">Return to workbench</a>
  );
}
