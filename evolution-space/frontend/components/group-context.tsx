"use client";
import Link from "next/link";
import { createContext, useContext, useEffect, type ReactNode } from "react";
import { type Group, type GroupPage, type Session } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";

export const SessionContext = createContext<Session | null>(null);
export function useSession() {
  const session = useContext(SessionContext);
  if (!session) throw new Error("Evolution Space session is unavailable");
  return session;
}

type GroupState = { group: Group; refresh: () => void };
const GroupContext = createContext<GroupState | null>(null);
export function useGroup() {
  const value = useContext(GroupContext);
  if (!value) throw new Error("Group context is unavailable");
  return value;
}
export const lastGroupKey = (subject: string) => `evolution-space:last-group:${subject}`;

// One authoritative group read serves the sidebar and every section page.
// A denied read removes the workspace instead of leaving stale controls.
export function GroupProvider({ id, children, fallback }: { id: string; children: ReactNode; fallback?: ReactNode }) {
  const tr = useT(); const session = useSession();
  const read = useRead<GroupPage>(`/groups/${encodeURIComponent(id)}`, true);
  const group = read.data?.items[0];
  const denied = read.status === 401 || read.status === 403 || read.status === 404;
  useEffect(() => {
    try {
      if (group) localStorage.setItem(lastGroupKey(session.subject), group.id);
      else if (denied && localStorage.getItem(lastGroupKey(session.subject)) === id) localStorage.removeItem(lastGroupKey(session.subject));
    } catch { /* Storage-disabled tabs simply choose a group each time. */ }
  }, [group, denied, id, session.subject]);
  if (read.loading && !group) return <main className="group-message"><p role="status">{tr("Loading group…")}</p></main>;
  if (read.status || !group) return <main className="group-message"><div role="alert"><h1>{tr(denied ? "You no longer have access to this group." : "Group access is unavailable. Please retry.")}</h1>
    <div className="editor-actions"><button onClick={read.refresh}>{tr("Retry")}</button><Link prefetch={false} href="/?choose=1">{tr("Choose another group")}</Link></div></div>{fallback}</main>;
  return <GroupContext.Provider value={{ group, refresh: read.refresh }}>{children}</GroupContext.Provider>;
}
