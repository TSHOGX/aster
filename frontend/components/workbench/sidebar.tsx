"use client";

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ChevronRight,
  Clock3,
  Loader2,
  LogOut,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  Pencil,
  Search,
  Share2,
  SquarePen,
  Trash2
} from "lucide-react";

import {
  useAuth,
  useCapabilities,
  useConnection,
  useHarness,
  useReady,
  useSelectedId,
  useSessions
} from "@/components/harness-provider";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { Wordmark } from "@/components/ui/wordmark";
import { SessionListSkeleton } from "@/components/ui/skeleton";
import { NotificationBell } from "@/components/notifications/notification-bell";
import { ShareDialog } from "./share-dialog";
import { StatusDot, statusTone } from "@/components/ui/badge";
import { formatRelative, isTerminal, statusLabel } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n";
import { useDismissOnOutside } from "@/lib/use-popover";
import type { ApiSession } from "@/lib/types";

type SessionRowProps = {
  session: ApiSession;
  selected: boolean;
  closing: boolean;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onRename: (id: string, title: string) => Promise<{ ok: boolean; error?: string }>;
};

const SessionRow = memo(function SessionRow({
  session,
  selected,
  closing,
  onSelect,
  onClose,
  onRename
}: SessionRowProps) {
  const t = useT();
  const displayTitle = session.title?.trim() || session.id;
  // Sessions shared with the caller are read-only: no rename, no delete, no
  // re-share. The server enforces this (every write route is owner-gated and
  // 404s for a reader); hiding the entry points keeps the UI from offering
  // actions that can only fail.
  const readOnly = session.access === "reader";
  const [menuOpen, setMenuOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(displayTitle);
  const [saving, setSaving] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Close the "..." menu on any outside click or Escape.
  useDismissOnOutside(menuOpen, [menuRef], () => setMenuOpen(false), { escape: true });

  const startEditing = () => {
    setMenuOpen(false);
    setDraft(session.title?.trim() || "");
    setEditing(true);
    // Focus + select after the input mounts.
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.select();
    });
  };

  const commit = async () => {
    if (saving) return;
    const next = draft.trim();
    // No-op or empty: just leave edit mode without a request.
    if (!next || next === (session.title?.trim() || "")) {
      setEditing(false);
      return;
    }
    setSaving(true);
    const res = await onRename(session.id, next);
    setSaving(false);
    if (res.ok) setEditing(false);
  };

  const onInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      void commit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      setEditing(false);
    }
  };

  if (editing) {
    return (
      <li>
        <div className="flex items-center gap-2 rounded-[var(--radius)] px-2.5 py-1.5">
          <input
            ref={inputRef}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onInputKeyDown}
            onBlur={() => void commit()}
            disabled={saving}
            maxLength={200}
            aria-label={t("sidebar.sessionTitle")}
            className="w-full rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-2 py-1 text-2xs leading-tight outline-none transition-colors focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent-soft)]"
          />
        </div>
      </li>
    );
  }

  return (
    <li>
      <div
        className={cn(
          "group relative flex h-9 w-full items-center gap-2.5 rounded-[var(--radius)] px-2.5 text-left transition-colors",
          selected
            ? "bg-[var(--color-accent-soft)] font-medium text-[var(--color-foreground)]"
            : "text-[var(--color-foreground)] hover:bg-[var(--color-surface-muted)]"
        )}
      >
        {/* Active accent bar — a thin, precise indicator instead of a heavy border. */}
        {selected ? (
          <span className="absolute left-0 top-1.5 bottom-1.5 w-[3px] rounded-full bg-[var(--color-accent)]" />
        ) : null}
        {/* Status dot alone — the only glyph the row needs. */}
        <span className="flex h-4 w-3 shrink-0 items-center justify-center">
          <StatusDot tone={statusTone(session.status)} />
        </span>
        <button
          className="min-w-0 flex-1 truncate text-left text-2xs leading-tight"
          onClick={() => onSelect(session.id)}
          onDoubleClick={readOnly ? undefined : startEditing}
          title={
            readOnly
              ? t("sidebar.sharedReadOnly", {
                  title: displayTitle,
                  owner: session.shared_by ?? t("sidebar.someoneElse")
                })
              : t("sidebar.rowTitle", {
                  title: displayTitle,
                  status: statusLabel(session.status),
                  when: formatRelative(session.updated_at)
                })
          }
        >
          {displayTitle}
        </button>
        {/* No actions menu on a shared row. Every item in it is owner-only, so a
            reader's menu held nothing but a "read-only" label -- a button that
            opens a menu with no actions reads as broken rather than as a
            restriction. The row's tooltip already says it is shared and
            read-only. */}
        {readOnly ? null : (
          <div className="relative shrink-0" ref={menuRef}>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 opacity-0 group-hover:opacity-70 hover:opacity-100 focus:opacity-100 aria-expanded:opacity-100"
              onClick={() => setMenuOpen((open) => !open)}
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              aria-label={t("sidebar.sessionActions", { title: displayTitle })}
              title={t("sidebar.sessionActions", { title: displayTitle })}
            >
              <MoreHorizontal className="h-4 w-4" />
            </Button>
            {menuOpen ? (
              <div
                role="menu"
                className="absolute right-0 top-8 z-30 min-w-[9rem] overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] py-1 shadow-[var(--shadow-lg)]"
              >
                <button
                  role="menuitem"
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-2xs hover:bg-[var(--color-surface-muted)]"
                  onClick={startEditing}
                >
                  <Pencil className="h-3.5 w-3.5" />
                  {t("sidebar.rename")}
                </button>
                <button
                  role="menuitem"
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-2xs hover:bg-[var(--color-surface-muted)]"
                  onClick={() => {
                    setMenuOpen(false);
                    setShareOpen(true);
                  }}
                >
                  <Share2 className="h-3.5 w-3.5" />
                  {t("sidebar.share")}
                </button>
                <button
                  role="menuitem"
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-2xs text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)] disabled:opacity-50"
                  onClick={() => {
                    setMenuOpen(false);
                    onClose(session.id);
                  }}
                  disabled={closing || session.status === "destroyed"}
                >
                  {closing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                  {t("sidebar.delete")}
                </button>
              </div>
            ) : null}
          </div>
        )}
      </div>
      {shareOpen ? (
        <ShareDialog
          sessionId={session.id}
          sessionTitle={displayTitle}
          isShell={session.mode === "shell"}
          onClose={() => setShareOpen(false)}
        />
      ) : null}
    </li>
  );
});


/**
 * A labelled, collapsible group of session rows. The header is the toggle: a
 * disclosure caret, the label, and the row count, which is what stays visible
 * once the group is closed.
 */
function SessionSection({
  label,
  sessions,
  open,
  onToggle,
  selectedId,
  closingId,
  onSelect,
  onClose,
  onRename
}: {
  label: string;
  sessions: ApiSession[];
  open: boolean;
  onToggle: () => void;
  selectedId: string | null;
  closingId: string | null;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onRename: (id: string, title: string) => Promise<{ ok: boolean; error?: string }>;
}) {
  const t = useT();
  return (
    <section>
      <button
        onClick={onToggle}
        aria-expanded={open}
        className="group/section flex w-full items-center gap-1 px-2 pb-1.5 pt-2 text-left"
        title={open ? t("sidebar.collapseSection", { label }) : t("sidebar.expandSection", { label })}
      >
        <ChevronRight
          className={cn(
            "h-3 w-3 shrink-0 text-[var(--color-foreground-muted)] transition-transform duration-150",
            open && "rotate-90"
          )}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate text-[12px] font-medium uppercase tracking-wider text-[var(--color-foreground-muted)] transition-colors group-hover/section:text-[var(--color-foreground)]">
          {label}
        </span>
        <span className="shrink-0 text-[12px] text-[var(--color-foreground-muted)]">{sessions.length}</span>
      </button>
      {open ? (
        <ul className="space-y-0.5 px-2 pb-3">
          {sessions.map((s) => (
            <SessionRow
              key={s.id}
              session={s}
              selected={s.id === selectedId}
              closing={closingId === s.id}
              onSelect={onSelect}
              onClose={onClose}
              onRename={onRename}
            />
          ))}
        </ul>
      ) : null}
    </section>
  );
}

type SidebarProps = {
  collapsed?: boolean;
  onToggleCollapse?: () => void;
  onOpenSearch?: () => void;
  onOpenSchedules?: () => void;
  onOpenConversation?: () => void;
  schedulesActive?: boolean;
  // Set when the sidebar is a narrow-width drawer: selecting or creating a
  // session dismisses the overlay so the conversation is visible underneath.
  onNavigate?: () => void;
};

export function Sidebar({ collapsed = false, onToggleCollapse, onOpenSearch, onOpenSchedules, onOpenConversation, schedulesActive = false, onNavigate }: SidebarProps) {
  const [peek, setPeek] = useState(false);
  const closeTimer = useRef<number | null>(null);

  // Hover-triggered flyout: open on enter, close on a short delay after leave so
  // brief pointer excursions (e.g. across the scrollbar) don't dismiss it.
  const openPeek = () => {
    if (closeTimer.current) window.clearTimeout(closeTimer.current);
    setPeek(true);
  };
  const scheduleClose = () => {
    if (closeTimer.current) window.clearTimeout(closeTimer.current);
    closeTimer.current = window.setTimeout(() => setPeek(false), 160);
  };
  useEffect(() => () => {
    if (closeTimer.current) window.clearTimeout(closeTimer.current);
  }, []);

  if (!collapsed) {
    return (
      <SidebarPanel
        variant="docked"
        onOpenSearch={onOpenSearch}
        onOpenSchedules={onOpenSchedules}
        onOpenConversation={onOpenConversation}
        schedulesActive={schedulesActive}
        onToggleCollapse={onToggleCollapse}
        onNavigate={onNavigate}
      />
    );
  }

  return (
    <div
      className="relative h-full"
      onMouseEnter={openPeek}
      onMouseLeave={scheduleClose}
    >
      <CollapsedRail onExpand={onToggleCollapse} onOpenSearch={onOpenSearch} onOpenSchedules={onOpenSchedules} onOpenConversation={onOpenConversation} schedulesActive={schedulesActive} onPeek={openPeek} />
      {/* Floating flyout: a full sidebar that hovers above the conversation. */}
      <div
        className={cn(
          // Matches the docked rail's ceiling so expanding does not jump width.
          "absolute left-2 top-2 bottom-2 z-40 w-[264px] origin-left overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)] transition-all duration-200 ease-out",
          peek ? "pointer-events-auto scale-100 opacity-100" : "pointer-events-none -translate-x-1 scale-[0.98] opacity-0"
        )}
      >
        <SidebarPanel
          variant="floating"
          onOpenSearch={onOpenSearch}
          onOpenSchedules={onOpenSchedules}
          onOpenConversation={onOpenConversation}
          schedulesActive={schedulesActive}
          onToggleCollapse={onToggleCollapse}
          onNavigate={() => setPeek(false)}
        />
      </div>
    </div>
  );
}

function CollapsedRail({
  onExpand,
  onOpenSearch,
  onOpenSchedules,
  onOpenConversation,
  schedulesActive,
  onPeek
}: {
  onExpand?: () => void;
  onOpenSearch?: () => void;
  onOpenSchedules?: () => void;
  onOpenConversation?: () => void;
  schedulesActive?: boolean;
  onPeek?: () => void;
}) {
  const t = useT();
  const { createSession } = useHarness();
  const sessions = useSessions();
  const [creating, setCreating] = useState(false);
  const activeCount = useMemo(
    () => sessions.filter((s) => !isTerminal(s.status)).length,
    [sessions]
  );

  const handleCreate = async () => {
    setCreating(true);
    const result = await createSession("agent");
    setCreating(false);
    if (result.ok) onOpenConversation?.();
  };

  return (
    <aside className="flex h-full min-h-0 w-16 flex-col items-center gap-1 overflow-hidden border-r border-[var(--color-border)] bg-[var(--color-surface)] py-4">
      <button
        onClick={onExpand}
        onMouseEnter={onPeek}
        aria-label={t("sidebar.expand")}
        title={t("sidebar.expand")}
        className="flex h-9 w-9 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
      >
        <PanelLeftOpen className="h-[18px] w-[18px]" />
      </button>
      <Button
        variant="ghost"
        size="icon"
        className="mt-1 h-9 w-9 border border-transparent hover:border-[var(--color-border)]"
        onClick={() => void handleCreate()}
        disabled={creating}
        aria-label={t("sidebar.newSession")}
        title={t("sidebar.newSession")}
      >
        {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <SquarePen className="h-[18px] w-[18px]" />}
      </Button>
      {onOpenSearch ? (
        <button
          onClick={onOpenSearch}
          aria-label={t("sidebar.search")}
          title={t("sidebar.searchTitle")}
          className="flex h-9 w-9 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
        >
          <Search className="h-[18px] w-[18px]" />
        </button>
      ) : null}
      <button
        onClick={onOpenSchedules}
        aria-label={t("sidebar.schedules")}
        title={t("sidebar.schedules")}
        className={cn("flex h-9 w-9 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]", schedulesActive && "bg-[var(--color-accent-soft)] text-[var(--color-accent)]")}
      >
        <Clock3 className="h-[18px] w-[18px]" />
      </button>
      <div className="mt-auto flex flex-col items-center gap-1">
        <span
          className="flex h-6 min-w-6 items-center justify-center rounded-full bg-[var(--color-surface-muted)] px-1.5 text-2xs font-medium text-[var(--color-foreground-muted)]"
          title={t(activeCount === 1 ? "sidebar.activeSession" : "sidebar.activeSessions", { count: activeCount })}
        >
          {activeCount}
        </span>
        <ThemeToggle />
        <NotificationBell />
      </div>
    </aside>
  );
}

function SidebarPanel({
  variant,
  onOpenSearch,
  onOpenSchedules,
  onOpenConversation,
  schedulesActive,
  onToggleCollapse,
  onNavigate
}: {
  variant: "docked" | "floating";
  onOpenSearch?: () => void;
  onOpenSchedules?: () => void;
  onOpenConversation?: () => void;
  schedulesActive?: boolean;
  onToggleCollapse?: () => void;
  onNavigate?: () => void;
}) {
  const t = useT();
  const { selectSession, createSession, destroySession, renameSession, logout } = useHarness();
  // Slice subscriptions: the sidebar reads only sessions/selection/meta, so it
  // is never re-rendered by a per-token agent.delta on the open conversation.
  const sessions = useSessions();
  const selectedId = useSelectedId();
  const capabilities = useCapabilities();
  const auth = useAuth();
  const ready = useReady();
  const connection = useConnection();
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [closeError, setCloseError] = useState<string | null>(null);
  const [closingId, setClosingId] = useState<string | null>(null);
  // Both groups start open; collapsing one is a way to park it, so the state is
  // per-mount and deliberately not persisted.
  const [sharedOpen, setSharedOpen] = useState(true);
  const [historyOpen, setHistoryOpen] = useState(true);

  // Filter + sort only when the sessions array identity changes (the reducer
  // replaces it only on a real session change), not on every render. Terminal
  // (deleted/failed) sessions are dropped outright — a deletion is a deletion.
  const activeSessions = useMemo(() => {
    const byRecentActivity = (a: ApiSession, b: ApiSession) =>
      new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
    return sessions.filter((session) => !isTerminal(session.status)).sort(byRecentActivity);
  }, [sessions]);
  // Sessions shared with the caller get their own group. They are read-only and
  // somebody else's work, so mixing them into History would make the list look
  // like the user owns work they cannot rename, delete, or continue.
  const ownedSessions = useMemo(
    () => activeSessions.filter((session) => session.access !== "reader"),
    [activeSessions]
  );
  const sharedSessions = useMemo(
    () => activeSessions.filter((session) => session.access === "reader"),
    [activeSessions]
  );
  const agentCapability = capabilities?.session_modes.find((mode) => mode.mode === "agent");
  const agentCreateDisabled = agentCapability ? !agentCapability.create_enabled : false;

  const handleCreate = async () => {
    setCreating(true);
    setCreateError(null);
    const res = await createSession("agent");
    setCreating(false);
    if (!res.ok) setCreateError(res.error ?? t("sidebar.createFailed"));
    else {
      onOpenConversation?.();
      onNavigate?.();
    }
  };

  // Stable callbacks so the memoized SessionRow only re-renders when its own
  // props (session/selected/closing) change, not on every parent render.
  const handleClose = useCallback(
    async (id: string) => {
      if (closingId) return;
      if (!window.confirm(t("sidebar.confirmClose", { id }))) return;
      setClosingId(id);
      setCloseError(null);
      const res = await destroySession(id);
      setClosingId(null);
      if (!res.ok) setCloseError(res.error ?? t("sidebar.closeFailed"));
    },
    [closingId, destroySession, t]
  );

  const handleSelect = useCallback(
    (id: string) => {
      selectSession(id);
      onOpenConversation?.();
      onNavigate?.();
    },
    [selectSession, onNavigate, onOpenConversation]
  );

  return (
    <aside
      className={cn(
        "flex h-full min-h-0 w-full min-w-0 flex-col overflow-hidden bg-[var(--color-surface)]",
        variant === "docked" ? "border-r border-[var(--color-border)]" : ""
      )}
    >
      <div className="flex shrink-0 items-center justify-between px-3 pt-4 pb-1">
        <div className="flex min-w-0 items-center gap-1.5">
          {onToggleCollapse ? (
            <button
              onClick={onToggleCollapse}
              aria-label={t("sidebar.collapse")}
              title={t("sidebar.collapse")}
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
            >
              <PanelLeftClose className="h-[18px] w-[18px]" />
            </button>
          ) : null}
          <Wordmark className="min-w-0" />
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          {onOpenSearch ? (
            <Button
              variant="ghost"
              size="icon"
              onClick={onOpenSearch}
              aria-label={t("sidebar.searchSessions")}
              title={t("sidebar.searchTitle")}
            >
              <Search className="h-4 w-4" />
            </Button>
          ) : null}
          <ThemeToggle />
          <NotificationBell />
        </div>
      </div>

      <div className="shrink-0 px-3 pb-3 pt-3">
        <nav aria-label={t("sidebar.primaryNavigation")} className="space-y-1">
          <button
          type="button"
          className="group flex h-11 w-full items-center gap-3 rounded-[var(--radius-lg)] px-3 text-left text-sm font-medium text-[var(--color-foreground)] transition-colors hover:bg-[var(--color-surface-muted)]"
          onClick={() => void handleCreate()}
          disabled={creating || agentCreateDisabled}
          title={agentCapability?.disabled_reason ?? undefined}
        >
          <span className="flex h-6 w-6 shrink-0 items-center justify-center text-[var(--color-foreground-muted)] transition-colors group-hover:text-[var(--color-foreground)]">
            {creating ? <Loader2 className="h-[19px] w-[19px] animate-spin" /> : <SquarePen className="h-[21px] w-[21px]" strokeWidth={1.7} />}
          </span>
          {t("sidebar.newSession")}
          </button>
          <button
            type="button"
            onClick={onOpenSchedules}
            aria-current={schedulesActive ? "page" : undefined}
            className={cn("group flex h-11 w-full items-center gap-3 rounded-[var(--radius-lg)] px-3 text-left text-sm font-medium transition-colors", schedulesActive ? "bg-[var(--color-accent-soft)] text-[var(--color-accent)]" : "text-[var(--color-foreground)] hover:bg-[var(--color-surface-muted)]")}
          >
            <span className={cn("flex h-6 w-6 shrink-0 items-center justify-center", schedulesActive ? "text-[var(--color-accent)]" : "text-[var(--color-foreground-muted)] group-hover:text-[var(--color-foreground)]")}>
              <Clock3 className="h-[21px] w-[21px]" strokeWidth={1.7} />
            </span>
            {t("sidebar.schedules")}
          </button>
        </nav>
        {createError ? <p className="mt-2 text-xs text-[var(--color-danger)]">{createError}</p> : null}
        {closeError ? <p className="mt-2 text-xs text-[var(--color-danger)]">{closeError}</p> : null}
      </div>

      {/* The scroll container is this element itself, not a nested child: it is a
          direct flex child of the <aside>, so flex-1 + min-h-0 give it a bounded
          height to scroll within. A wrapper with overflow-hidden around an inner
          flex-1 child does not work -- the wrapper is not a flex container, so
          the child's flex-1 is inert and its height grows with the list, which
          silently clips rows instead of scrolling them. */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {activeSessions.length === 0 ? (
          ready || connection === "live" ? (
            <p className="px-4 py-6 text-center text-2xs text-[var(--color-foreground-muted)]">
              {t("sidebar.noSessions")}
            </p>
          ) : (
            <SessionListSkeleton />
          )
        ) : (
          <>
            {/* Shared sessions lead the list. They are the smaller, more
                volatile group (somebody hands you a session and you go look at
                it now), so burying them under a long History meant scrolling
                to find them. History is the archive and sits below. */}
            {sharedSessions.length > 0 ? (
              <SessionSection
                label={t("sidebar.sharedWithMe")}
                sessions={sharedSessions}
                open={sharedOpen}
                onToggle={() => setSharedOpen((v) => !v)}
                selectedId={selectedId}
                closingId={closingId}
                onSelect={handleSelect}
                onClose={handleClose}
                onRename={renameSession}
              />
            ) : null}
            <SessionSection
              label={t("sidebar.history")}
              sessions={ownedSessions}
              open={historyOpen}
              onToggle={() => setHistoryOpen((v) => !v)}
              selectedId={selectedId}
              closingId={closingId}
              onSelect={handleSelect}
              onClose={handleClose}
              onRename={renameSession}
            />
          </>
        )}
      </div>

      {/* Above the account block, so appearing/disappearing does not shove the
          account row up and down under the pointer. */}
      {connection !== "live" && connection !== "idle" ? (
        <div className="shrink-0 border-t border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 text-2xs text-[var(--color-foreground-muted)]">
          {connection === "connecting"
            ? t("connection.connecting")
            : connection === "reconnecting"
            ? t("connection.reconnecting")
            : t("connection.disconnected")}
        </div>
      ) : null}

      {/* Account block, pinned to the floor of the rail — the conventional place
          for "who am I / get me out", and out of the way of the session list. */}
      {auth.required && auth.userId ? (
        <div className="shrink-0 border-t border-[var(--color-border)] px-2 py-2">
          <div className="flex items-center gap-2 rounded-[var(--radius)] px-1.5 py-1">
            <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-[var(--color-accent-soft)] text-2xs font-semibold uppercase text-[var(--color-accent)]">
              {auth.userId.slice(0, 1)}
            </span>
            <span className="min-w-0 flex-1 truncate text-2xs text-[var(--color-foreground)]" title={auth.userId}>
              {auth.userId}
            </span>
            <button
              onClick={() => void logout()}
              aria-label={t("sidebar.signOut")}
              title={t("sidebar.signOut")}
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
            >
              <LogOut className="h-4 w-4" />
            </button>
          </div>
        </div>
      ) : null}
    </aside>
  );
}
