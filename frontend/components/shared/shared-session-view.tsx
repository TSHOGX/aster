"use client";
import { PagedSharedSession } from "./paged-shared-session";

import { Fragment, useCallback, useEffect, useState } from "react";
import { Bot, Link2Off, RefreshCw } from "lucide-react";

import {
  fetchSharedArtifacts,
  fetchSharedMessages,
  fetchSharedSession,
} from "@/lib/api";
import type {
  SharedLinkFile,
  SharedLinkMessage,
  SharedLinkSession,
} from "@/lib/types";
import { dayKey, formatDayDivider, formatFullTimestamp } from "@/lib/format";
import { isFinalReply } from "@/lib/message-grouping";
import {
  DayDivider,
  MessageBubble,
} from "@/components/workbench/message-bubble";
import { ShellEntry } from "@/components/workbench/shell-entry";
import { SharedFilePanel } from "./shared-file-panel";

type LoadState =
  | { phase: "loading" }
  | { phase: "gone" }
  | { phase: "error"; message: string }
  | {
      phase: "ready";
      session: SharedLinkSession;
      messages: SharedLinkMessage[];
      files: SharedLinkFile[];
    };

/**
 * Read-only rendering of a shared session, driven entirely by props/state from
 * the anonymous API. It reuses the presentational leaves (MessageBubble,
 * ShellEntry, DayDivider) but none of the workbench's data layer.
 *
 * There is no live stream by design: an anonymous connection is not admitted to
 * the SSE hub. That makes the page a snapshot, so it carries an explicit Refresh
 * control rather than looking stuck while an owner keeps working.
 */
export function SharedSessionView({ token }: { token: string }) {
  return process.env.NEXT_PUBLIC_HARNESS_MESSAGE_PAGES === "0" ? <LegacySharedSessionView token={token} /> : <PagedSharedSession token={token} />;
}
export function LegacySharedSessionView({ token }: { token: string }) {
  const [state, setState] = useState<LoadState>({ phase: "loading" });
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async (): Promise<LoadState> => {
    const sessionRes = await fetchSharedSession(token);
    if (!sessionRes.ok) {
      // Expired, revoked, rotated and never-existed are all 404 on purpose:
      // a public URL must not reveal that it used to work.
      return sessionRes.status === 404
        ? { phase: "gone" }
        : { phase: "error", message: sessionRes.error };
    }
    const messagesRes = await fetchSharedMessages(token);
    if (!messagesRes.ok) {
      return messagesRes.status === 404
        ? { phase: "gone" }
        : { phase: "error", message: messagesRes.error };
    }
    // The whitelist is not load-bearing for the page: a failure here leaves the
    // conversation readable with no file panel rather than blanking the page.
    const artifactsRes = await fetchSharedArtifacts(token);
    return {
      phase: "ready",
      session: sessionRes.data,
      messages: messagesRes.data.messages ?? [],
      files: artifactsRes.ok ? (artifactsRes.data.artifacts ?? []) : [],
    };
  }, [token]);

  useEffect(() => {
    let cancelled = false;
    void load().then((next) => {
      if (cancelled) return;
      setState(next);
    });
    return () => {
      cancelled = true;
    };
  }, [load]);

  const refresh = async () => {
    setRefreshing(true);
    setState(await load());
    setRefreshing(false);
  };

  if (state.phase === "loading") {
    return (
      <SharedShell>
        {
          <p className="text-sm text-[var(--color-foreground-muted)]">
            Loading…
          </p>
        }
      </SharedShell>
    );
  }
  if (state.phase === "gone") {
    return (
      <SharedShell>
        <div className="flex flex-col items-center gap-3 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-surface-muted)]">
            <Link2Off className="h-5 w-5 text-[var(--color-foreground-muted)]" />
          </div>
          <h1 className="text-sm font-medium">This link is not available</h1>
          <p className="max-w-sm text-xs text-[var(--color-foreground-muted)]">
            It may have expired, been revoked, or never existed. Ask whoever
            shared it for a new one.
          </p>
        </div>
      </SharedShell>
    );
  }
  if (state.phase === "error") {
    return (
      <SharedShell>
        <div className="flex flex-col items-center gap-3 text-center">
          <h1 className="text-sm font-medium">Could not load this session</h1>
          <p className="max-w-sm text-xs text-[var(--color-foreground-muted)]">
            {state.message}
          </p>
          <button
            type="button"
            onClick={refresh}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:bg-[var(--color-surface-muted)]"
          >
            Try again
          </button>
        </div>
      </SharedShell>
    );
  }

  const { session, messages, files } = state;
  const isShell = session.mode === "shell";

  return (
    <main className="flex h-dvh min-h-0 flex-col bg-[var(--color-background)]">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--color-border)] bg-[var(--color-surface)]/80 px-6 py-3 backdrop-blur">
        <div className="flex min-w-0 flex-col">
          <h1 className="truncate text-sm font-medium">
            {session.title?.trim() || "Shared session"}
          </h1>
          <p className="mt-0.5 text-2xs text-[var(--color-foreground-muted)]">
            {session.mode_label} · read-only
            {session.frozen ? " · snapshot" : null}
            {" · shared "}
            <time
              dateTime={session.shared_at}
              title={formatFullTimestamp(session.shared_at)}
            >
              {formatDayDivider(session.shared_at)}
            </time>
          </p>
        </div>
        <button
          type="button"
          onClick={refresh}
          disabled={refreshing}
          className="flex items-center gap-1.5 rounded-md border border-[var(--color-border)] px-2.5 py-1.5 text-xs hover:bg-[var(--color-surface-muted)] disabled:opacity-60"
        >
          <RefreshCw
            className={refreshing ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"}
          />
          Refresh
        </button>
      </header>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <div className="min-h-0 flex-1 overflow-y-auto">
          {messages.length === 0 ? (
            <div className="flex h-full items-center justify-center px-6 text-center">
              <div className="flex flex-col items-center gap-3">
                <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-surface-muted)]">
                  <Bot className="h-5 w-5 text-[var(--color-foreground-muted)]" />
                </div>
                <p className="text-xs text-[var(--color-foreground-muted)]">
                  This session has no messages to show.
                </p>
              </div>
            </div>
          ) : isShell ? (
            /* MessageBubble and ShellEntry own only their vertical rhythm, so the
               centering + horizontal padding wrapper has to be repeated here —
               the workbench conversation does the same. */
            <div className="mx-auto w-full max-w-[var(--measure)] divide-y divide-[var(--color-border)] px-4 sm:px-6">
              {messages.map((message) => (
                <ShellEntry
                  key={message.id}
                  message={sharedToApiMessage(message)}
                />
              ))}
            </div>
          ) : (
            <div className="mx-auto w-full max-w-[var(--measure)] px-4 sm:px-6">
              {messages.map((message, index) => {
                const previous = messages[index - 1];
                const showDivider =
                  index === 0 ||
                  dayKey(previous?.created_at) !== dayKey(message.created_at);
                return (
                  <Fragment key={message.id}>
                    {showDivider && (
                      <DayDivider
                        label={formatDayDivider(message.created_at)}
                      />
                    )}
                    {/* showActions=false is load-bearing here: the action bar
                        reaches into the workbench provider for feedback, which an
                        anonymous viewer has neither identity nor permission for. */}
                    <MessageBubble
                      message={sharedToApiMessage(message)}
                      sessionId={session.id}
                      showActions={false}
                    />
                  </Fragment>
                );
              })}
            </div>
          )}
        </div>
        {/* A shell transcript has no "reply" — its entries are command output, so
            there is no message whose prose could mention a file. */}
        <SharedFilePanel
          token={token}
          files={files}
          lastReplyContent={isShell ? "" : lastReplyContent(messages)}
        />
      </div>

      <footer className="border-t border-[var(--color-border)] px-6 py-2.5 text-2xs text-[var(--color-foreground-muted)]">
        {session.frozen
          ? "A snapshot of the conversation as it stood when the link was created. Later messages are not included."
          : "Read-only view. Refresh to pick up new messages."}
      </footer>
    </main>
  );
}

/**
 * Adapt the public message shape to the one the shared presentational
 * components take. The two differ deliberately — the public DTO is built field
 * by field on the server so a new authed field is not published by accident —
 * so the mapping is explicit here rather than a cast.
 */
/**
 * The last turn-final assistant reply's text, for the mentioned-files section.
 *
 * Reuses the same `isFinalReply` rule as the workbench so a link and its owner's
 * view agree on which message is "the reply". At summary visibility the server
 * has already dropped the tool-use steps, so the last assistant message is
 * normally the answer anyway — but a `full` link keeps them, and picking the last
 * assistant message blindly would read paths out of an intermediate step.
 */
function lastReplyContent(messages: SharedLinkMessage[]): string {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (message.role !== "assistant") continue;
    const next = messages[i + 1];
    if (isFinalReply(sharedToApiMessage(message), next && sharedToApiMessage(next))) {
      return message.content;
    }
  }
  return "";
}

function sharedToApiMessage(message: SharedLinkMessage) {
  return {
    id: message.id,
    session_id: "",
    role: message.role,
    content: message.content,
    created_at: message.created_at,
    stop_reason: message.stop_reason,
    trace: message.trace,
  };
}

function SharedShell({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex h-dvh items-center justify-center bg-[var(--color-background)] px-6">
      {children}
    </main>
  );
}
