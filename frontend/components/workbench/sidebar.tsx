"use client";

import { useEffect, useRef, useState } from "react";
import { ChevronDown, Loader2, LogOut, MoreHorizontal, Pencil, Plus, Trash2 } from "lucide-react";

import { useHarness } from "@/components/harness-provider";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { NotificationBell } from "@/components/notifications/notification-bell";
import { StatusDot, statusTone } from "@/components/ui/badge";
import { formatRelative, isTerminal, statusLabel } from "@/lib/format";
import { cn } from "@/lib/cn";
import type { ApiSession } from "@/lib/types";

type SessionRowProps = {
  session: ApiSession;
  selected: boolean;
  closing: boolean;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onRename: (id: string, title: string) => Promise<{ ok: boolean; error?: string }>;
};

function SessionRow({ session, selected, closing, onSelect, onClose, onRename }: SessionRowProps) {
  const displayTitle = session.title?.trim() || session.id;
  const [menuOpen, setMenuOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(displayTitle);
  const [saving, setSaving] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Close the "..." menu on any outside click or Escape.
  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenuOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMenuOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [menuOpen]);

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

  return (
    <li>
      <div
        className={cn(
          "group flex w-full items-start gap-2 rounded-[var(--radius)] px-2.5 py-2 text-left transition-colors",
          selected
            ? "border-l-2 border-[var(--color-accent)] bg-[var(--color-surface-muted)] pl-[calc(0.625rem-2px)]"
            : "hover:bg-[var(--color-surface-muted)]"
        )}
      >
        <StatusDot tone={statusTone(session.status)} className="mt-1.5" />
        <div className="min-w-0 flex-1">
          {editing ? (
            <input
              ref={inputRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={onInputKeyDown}
              onBlur={() => void commit()}
              disabled={saving}
              maxLength={200}
              aria-label="Session title"
              className="w-full rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-1.5 py-0.5 text-sm leading-tight outline-none focus:border-[var(--color-accent)]"
            />
          ) : (
            <button
              className="block w-full text-left"
              onClick={() => onSelect(session.id)}
              onDoubleClick={startEditing}
              title={displayTitle}
            >
              <div className="truncate text-sm leading-tight">{displayTitle}</div>
              <div className="mt-0.5 flex items-center gap-1.5 text-[11px] text-[var(--color-foreground-muted)]">
                <span>{statusLabel(session.status)}</span>
                <span aria-hidden>·</span>
                <span>{formatRelative(session.updated_at)}</span>
              </div>
            </button>
          )}
        </div>
        {!editing ? (
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
                className="absolute right-0 top-8 z-20 min-w-[9rem] overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] py-1 shadow-lg"
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
        ) : null}
      </div>
    </li>
  );
}

export function Sidebar() {
  const { state, selectSession, createSession, destroySession, renameSession, logout } = useHarness();
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [closeError, setCloseError] = useState<string | null>(null);
  const [closingId, setClosingId] = useState<string | null>(null);
  const [endedOpen, setEndedOpen] = useState(false);

  const byRecentActivity = (a: ApiSession, b: ApiSession) =>
    new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
  const activeSessions = state.sessions
    .filter((session) => !isTerminal(session.status))
    .sort(byRecentActivity);
  const endedSessions = state.sessions
    .filter((session) => isTerminal(session.status))
    .sort(byRecentActivity);
  const agentCapability = state.capabilities?.session_modes.find((mode) => mode.mode === "agent");
  const agentCreateDisabled = agentCapability ? !agentCapability.create_enabled : false;

  const handleCreate = async () => {
    setCreating(true);
    setCreateError(null);
    const res = await createSession("agent");
    setCreating(false);
    if (!res.ok) setCreateError(res.error ?? "Failed to create session");
  };

  const handleClose = async (id: string) => {
    if (closingId) return;
    if (!window.confirm(`Close session ${id}? History and workspace files are retained.`)) return;
    setClosingId(id);
    setCloseError(null);
    const res = await destroySession(id);
    setClosingId(null);
    if (!res.ok) setCloseError(res.error ?? "Failed to close session");
  };

  return (
    <aside className="flex h-full min-h-0 w-[280px] min-w-0 flex-col overflow-hidden border-r border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="flex shrink-0 items-center justify-between px-4 pt-5 pb-1">
        <span className="text-base font-semibold tracking-tight">BatteryGPT</span>
        <div className="flex items-center gap-1">
          <ThemeToggle />
          <NotificationBell />
        </div>
      </div>

      {state.auth.required && state.auth.userId ? (
        <div className="flex shrink-0 items-center justify-between px-4 pb-3">
          <span className="truncate text-xs text-[var(--color-foreground-muted)]">
            Signed in as <span className="text-[var(--color-foreground)]">{state.auth.userId}</span>
          </span>
          <Button
            variant="ghost"
            size="icon"
            onClick={() => void logout()}
            aria-label="Sign out"
            title="Sign out"
          >
            <LogOut className="h-4 w-4" />
          </Button>
        </div>
      ) : null}

      <div className="relative shrink-0 px-3 pb-2">
        <Button
          variant="primary"
          size="lg"
          className="w-full justify-start gap-2"
          onClick={() => void handleCreate()}
          disabled={creating || agentCreateDisabled}
          title={agentCapability?.disabled_reason ?? undefined}
        >
          {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          New session
        </Button>
        {createError ? (
          <p className="mt-2 text-xs text-[var(--color-danger)]">{createError}</p>
        ) : null}
        {closeError ? (
          <p className="mt-2 text-xs text-[var(--color-danger)]">{closeError}</p>
        ) : null}
      </div>

      <div className="flex shrink-0 items-center justify-between px-4 pt-3 pb-2">
        <span className="text-[11px] uppercase tracking-wider text-[var(--color-foreground-muted)]">Sessions</span>
        <span className="text-[11px] text-[var(--color-foreground-muted)]">{state.sessions.length}</span>
      </div>

      <div className="min-h-0 flex-1 overflow-hidden">
        {state.sessions.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-[var(--color-foreground-muted)]">
            {state.ready || state.connection === "live" ? "No sessions yet. Create one to get started." : "Loading…"}
          </p>
        ) : (
          <div className="flex h-full min-h-0 flex-col">
            <div className="min-h-0 flex-1 overflow-y-auto">
              {activeSessions.length === 0 ? (
                <p className="px-4 py-6 text-center text-xs text-[var(--color-foreground-muted)]">
                  No active sessions.
                </p>
              ) : (
                <ul className="space-y-0.5 px-1.5 pb-3">
                  {activeSessions.map((s) => (
                    <SessionRow
                      key={s.id}
                      session={s}
                      selected={s.id === state.selectedId}
                      closing={closingId === s.id}
                      onSelect={selectSession}
                      onClose={(id) => void handleClose(id)}
                      onRename={renameSession}
                    />
                  ))}
                </ul>
              )}
            </div>

            {endedSessions.length > 0 ? (
              <div className="shrink-0 border-t border-[var(--color-border)] bg-[var(--color-surface)]">
                <button
                  className="flex h-9 w-full items-center justify-between px-3 text-left text-[11px] uppercase tracking-wider text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)]"
                  onClick={() => setEndedOpen((open) => !open)}
                  aria-expanded={endedOpen}
                >
                  <span className="flex min-w-0 items-center gap-1.5">
                    <ChevronDown
                      className={cn("h-3.5 w-3.5 shrink-0 transition-transform", endedOpen ? "" : "-rotate-90")}
                    />
                    <span>Ended</span>
                  </span>
                  <span>{endedSessions.length}</span>
                </button>
                {endedOpen ? (
                  <div className="max-h-64 overflow-y-auto border-t border-[var(--color-border)]">
                    <ul className="space-y-0.5 px-1.5 py-1.5">
                      {endedSessions.map((s) => (
                        <SessionRow
                          key={s.id}
                          session={s}
                          selected={s.id === state.selectedId}
                          closing={closingId === s.id}
                          onSelect={selectSession}
                          onClose={(id) => void handleClose(id)}
                          onRename={renameSession}
                        />
                      ))}
                    </ul>
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
        )}
      </div>

      {state.connection !== "live" && state.connection !== "idle" ? (
        <div className="shrink-0 border-t border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 text-[11px] text-[var(--color-foreground-muted)]">
          {state.connection === "connecting"
            ? "Connecting to event stream…"
            : state.connection === "reconnecting"
            ? "Reconnecting…"
            : "Disconnected. Retrying."}
        </div>
      ) : null}
    </aside>
  );
}
