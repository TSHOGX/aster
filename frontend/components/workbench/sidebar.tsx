"use client";

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Loader2,
  LogOut,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  Pencil,
  Plus,
  Search,
  Share2,
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
import { NotificationBell } from "@/components/notifications/notification-bell";
import { ShareDialog } from "./share-dialog";
import { StatusDot, statusTone } from "@/components/ui/badge";
import { formatRelative, isTerminal, statusLabel } from "@/lib/format";
import { cn } from "@/lib/cn";
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
            aria-label="Session title"
            className="w-full rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-2 py-1 text-sm leading-tight outline-none focus:border-[var(--color-accent)]"
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
            ? "bg-[var(--color-surface-muted)] text-[var(--color-foreground)]"
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
          className="min-w-0 flex-1 truncate text-left text-sm leading-tight"
          onClick={() => onSelect(session.id)}
          onDoubleClick={readOnly ? undefined : startEditing}
          title={
            readOnly
              ? `${displayTitle} · shared by ${session.shared_by ?? "another user"} · read-only`
              : `${displayTitle} · ${statusLabel(session.status)} · ${formatRelative(session.updated_at)}`
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
              aria-label={`Session actions for ${displayTitle}`}
              title="Session actions"
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
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-[var(--color-surface-muted)]"
                  onClick={startEditing}
                >
                  <Pencil className="h-3.5 w-3.5" />
                  Rename
                </button>
                <button
                  role="menuitem"
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-[var(--color-surface-muted)]"
                  onClick={() => {
                    setMenuOpen(false);
                    setShareOpen(true);
                  }}
                >
                  <Share2 className="h-3.5 w-3.5" />
                  Share
                </button>
                <button
                  role="menuitem"
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)] disabled:opacity-50"
                  onClick={() => {
                    setMenuOpen(false);
                    onClose(session.id);
                  }}
                  disabled={closing || session.status === "destroyed"}
                >
                  {closing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                  Delete
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


type SidebarProps = {
  collapsed?: boolean;
  onToggleCollapse?: () => void;
  onOpenSearch?: () => void;
  // Set when the sidebar is a narrow-width drawer: selecting or creating a
  // session dismisses the overlay so the conversation is visible underneath.
  onNavigate?: () => void;
};

export function Sidebar({ collapsed = false, onToggleCollapse, onOpenSearch, onNavigate }: SidebarProps) {
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
      <CollapsedRail onExpand={onToggleCollapse} onOpenSearch={onOpenSearch} onPeek={openPeek} />
      {/* Floating flyout: a full sidebar that hovers above the conversation. */}
      <div
        className={cn(
          "absolute left-2 top-2 bottom-2 z-40 w-[288px] origin-left overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)] transition-all duration-200 ease-out",
          peek ? "pointer-events-auto scale-100 opacity-100" : "pointer-events-none -translate-x-1 scale-[0.98] opacity-0"
        )}
      >
        <SidebarPanel
          variant="floating"
          onOpenSearch={onOpenSearch}
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
  onPeek
}: {
  onExpand?: () => void;
  onOpenSearch?: () => void;
  onPeek?: () => void;
}) {
  const { createSession } = useHarness();
  const sessions = useSessions();
  const [creating, setCreating] = useState(false);
  const activeCount = useMemo(
    () => sessions.filter((s) => !isTerminal(s.status)).length,
    [sessions]
  );

  const handleCreate = async () => {
    setCreating(true);
    await createSession("agent");
    setCreating(false);
  };

  return (
    <aside className="flex h-full min-h-0 w-16 flex-col items-center gap-1 overflow-hidden border-r border-[var(--color-border)] bg-[var(--color-surface)] py-4">
      <button
        onClick={onExpand}
        onMouseEnter={onPeek}
        aria-label="Expand sidebar"
        title="Expand sidebar"
        className="flex h-9 w-9 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
      >
        <PanelLeftOpen className="h-[18px] w-[18px]" />
      </button>
      <Button
        variant="primary"
        size="icon"
        className="mt-1 h-9 w-9"
        onClick={() => void handleCreate()}
        disabled={creating}
        aria-label="New session"
        title="New session"
      >
        {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
      </Button>
      {onOpenSearch ? (
        <button
          onClick={onOpenSearch}
          aria-label="Search"
          title="Search (⌘P / Ctrl+P)"
          className="flex h-9 w-9 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] transition-colors hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
        >
          <Search className="h-[18px] w-[18px]" />
        </button>
      ) : null}
      <div className="mt-auto flex flex-col items-center gap-1">
        <span
          className="flex h-6 min-w-6 items-center justify-center rounded-full bg-[var(--color-surface-muted)] px-1.5 text-2xs font-medium text-[var(--color-foreground-muted)]"
          title={`${activeCount} active session${activeCount === 1 ? "" : "s"}`}
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
  onToggleCollapse,
  onNavigate
}: {
  variant: "docked" | "floating";
  onOpenSearch?: () => void;
  onToggleCollapse?: () => void;
  onNavigate?: () => void;
}) {
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
    if (!res.ok) setCreateError(res.error ?? "Failed to create session");
    else onNavigate?.();
  };

  // Stable callbacks so the memoized SessionRow only re-renders when its own
  // props (session/selected/closing) change, not on every parent render.
  const handleClose = useCallback(
    async (id: string) => {
      if (closingId) return;
      if (!window.confirm(`Close session ${id}? History and workspace files are retained.`)) return;
      setClosingId(id);
      setCloseError(null);
      const res = await destroySession(id);
      setClosingId(null);
      if (!res.ok) setCloseError(res.error ?? "Failed to close session");
    },
    [closingId, destroySession]
  );

  const handleSelect = useCallback(
    (id: string) => {
      selectSession(id);
      onNavigate?.();
    },
    [selectSession, onNavigate]
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
              aria-label="Collapse sidebar"
              title="Collapse sidebar"
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
              aria-label="Search sessions and messages"
              title="Search (⌘P / Ctrl+P)"
            >
              <Search className="h-4 w-4" />
            </Button>
          ) : null}
          <ThemeToggle />
          <NotificationBell />
        </div>
      </div>

      {auth.required && auth.userId ? (
        <div className="flex shrink-0 items-center justify-between px-4 pb-2 pt-1">
          <span className="truncate text-xs text-[var(--color-foreground-muted)]">
            <span className="text-[var(--color-foreground)]">{auth.userId}</span>
          </span>
          <button
            onClick={() => void logout()}
            aria-label="Sign out"
            title="Sign out"
            className="flex h-7 w-7 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
          >
            <LogOut className="h-4 w-4" />
          </button>
        </div>
      ) : null}

      <div className="shrink-0 px-3 pb-2 pt-2">
        <Button
          variant="primary"
          size="lg"
          className="w-full justify-start gap-2 rounded-[var(--radius-lg)]"
          onClick={() => void handleCreate()}
          disabled={creating || agentCreateDisabled}
          title={agentCapability?.disabled_reason ?? undefined}
        >
          {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          New session
        </Button>
        {createError ? <p className="mt-2 text-xs text-[var(--color-danger)]">{createError}</p> : null}
        {closeError ? <p className="mt-2 text-xs text-[var(--color-danger)]">{closeError}</p> : null}
      </div>

      <div className="flex items-center justify-between px-4 pb-1.5 pt-2">
        <span className="text-2xs font-medium uppercase tracking-wider text-[var(--color-foreground-muted)]">
          History
        </span>
        <span className="text-2xs text-[var(--color-foreground-muted)]">{ownedSessions.length}</span>
      </div>

      {/* The scroll container is this element itself, not a nested child: it is a
          direct flex child of the <aside>, so flex-1 + min-h-0 give it a bounded
          height to scroll within. A wrapper with overflow-hidden around an inner
          flex-1 child does not work -- the wrapper is not a flex container, so
          the child's flex-1 is inert and its height grows with the list, which
          silently clips rows instead of scrolling them. */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {activeSessions.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-[var(--color-foreground-muted)]">
            {ready || connection === "live" ? "No sessions yet. Create one to get started." : "Loading…"}
          </p>
        ) : (
          <>
            <ul className="space-y-0.5 px-2 pb-3">
              {ownedSessions.map((s) => (
                <SessionRow
                  key={s.id}
                  session={s}
                  selected={s.id === selectedId}
                  closing={closingId === s.id}
                  onSelect={handleSelect}
                  onClose={handleClose}
                  onRename={renameSession}
                />
              ))}
            </ul>
            {sharedSessions.length > 0 ? (
              <>
                <div className="flex items-center justify-between px-4 pb-1.5 pt-2">
                  <span className="text-2xs font-medium uppercase tracking-wider text-[var(--color-foreground-muted)]">
                    Shared with me
                  </span>
                  <span className="text-2xs text-[var(--color-foreground-muted)]">
                    {sharedSessions.length}
                  </span>
                </div>
                <ul className="space-y-0.5 px-2 pb-3">
                  {sharedSessions.map((s) => (
                    <SessionRow
                      key={s.id}
                      session={s}
                      selected={s.id === selectedId}
                      closing={closingId === s.id}
                      onSelect={handleSelect}
                      onClose={handleClose}
                      onRename={renameSession}
                    />
                  ))}
                </ul>
              </>
            ) : null}
          </>
        )}
      </div>

      {connection !== "live" && connection !== "idle" ? (
        <div className="shrink-0 border-t border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 text-2xs text-[var(--color-foreground-muted)]">
          {connection === "connecting"
            ? "Connecting to event stream…"
            : connection === "reconnecting"
            ? "Reconnecting…"
            : "Disconnected. Retrying."}
        </div>
      ) : null}
    </aside>
  );
}
