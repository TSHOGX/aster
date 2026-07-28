"use client";

import { Fragment, useEffect, useRef, useSyncExternalStore } from "react";
import { Bot } from "lucide-react";

import { useActivity, useConversation, useHarness, useSelectedSession } from "@/components/harness-provider";
import { clearMessageJump, getMessageJump, subscribeMessageJump } from "@/lib/message-jump";
import { ShellConversation } from "./shell-conversation";
import { DayDivider, MessageBubble, StreamingBubble } from "./message-bubble";
import { ActivityIndicator } from "./activity-indicator";
import { Composer } from "./composer";
import { StatusDot, statusTone } from "@/components/ui/badge";
import { isFinalReply } from "@/lib/message-grouping";
import { sessionModeLabel } from "@/lib/agents";
import { dayKey, formatDayDivider, isAcceptingInput, isTerminal, statusLabel } from "@/lib/format";

export function Conversation() {
  const session = useSelectedSession();
  if (session?.mode === "shell") {
    return <ShellConversation />;
  }
  return <ChatConversation />;
}

function ChatConversation() {
  const session = useSelectedSession();
  const convo = useConversation(session?.id ?? null);
  const activity = useActivity(session?.id ?? null);
  const { sendMessage } = useHarness();
  const scrollRef = useRef<HTMLDivElement>(null);
  const scrollRafRef = useRef<number | null>(null);

  // A pending jump target (set by the search palette) for THIS session means we
  // scroll to a specific message instead of auto-scrolling to the bottom.
  const jump = useSyncExternalStore(subscribeMessageJump, getMessageJump, () => null);
  const pendingJumpHere = jump !== null && session != null && jump.sessionId === session.id;

  useEffect(() => {
    // Don't fight a pending jump-to-message with the bottom auto-scroll.
    if (pendingJumpHere) return;
    if (!scrollRef.current) return;
    // Reading scrollHeight forces a layout reflow. During a live turn deltas
    // fire many times per second, so doing it per token thrashed layout and
    // froze the UI. Coalesce all deltas within a frame into a single scroll:
    // the rAF guard ensures at most one pending scroll, and it always reads the
    // latest scrollHeight when it runs, so the view still follows the stream.
    if (scrollRafRef.current !== null) return;
    scrollRafRef.current = requestAnimationFrame(() => {
      scrollRafRef.current = null;
      const node = scrollRef.current;
      if (node) node.scrollTop = node.scrollHeight;
    });
  }, [convo.messages.length, convo.streaming, activity.phase, pendingJumpHere]);

  // Cancel any pending scroll on unmount so the rAF callback never touches a
  // detached node.
  useEffect(
    () => () => {
      if (scrollRafRef.current !== null) cancelAnimationFrame(scrollRafRef.current);
    },
    []
  );

  // Once the target message has rendered, scroll it into view and flash a
  // highlight ring, then consume the signal.
  useEffect(() => {
    if (!pendingJumpHere || !jump) return;
    const el = document.getElementById(`msg-${jump.messageId}`);
    if (!el) return; // messages may still be loading; a later render will retry.
    el.scrollIntoView({ block: "center" });
    el.classList.add("msg-jump-flash");
    const timer = window.setTimeout(() => {
      el.classList.remove("msg-jump-flash");
    }, 1500);
    clearMessageJump();
    return () => window.clearTimeout(timer);
  }, [pendingJumpHere, jump, convo.messages.length, convo.loading]);

  if (!session) {
    return (
      <main className="flex flex-col items-center justify-center text-center bg-[var(--color-background)]">
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-surface-muted)]">
          <Bot className="h-5 w-5 text-[var(--color-foreground-muted)]" />
        </div>
        <h2 className="mt-3 text-sm font-medium">No session selected</h2>
        <p className="mt-1 max-w-xs text-xs text-[var(--color-foreground-muted)]">
          Pick a session from the left, or create a new one to start a conversation.
        </p>
      </main>
    );
  }

  // A session shared with the caller is read-only: sendMessage would 404 (every
  // write route is owner-gated), so the composer is disabled and says why rather
  // than accepting text that cannot be delivered.
  const readOnly = session.access === "reader";
  const accepting = !readOnly && isAcceptingInput(session.status);
  const terminal = isTerminal(session.status);
  const placeholder = readOnly
    ? `Read-only — shared by ${session.shared_by ?? "another user"}`
    : terminal
    ? "Session ended. Start a new one to continue."
    : accepting
    ? "Send a message…"
    : "Agent is thinking…";

  const activityLive = activity.phase !== "idle";

  return (
    <main className="relative flex h-full min-h-0 min-w-0 flex-col bg-[var(--color-background)]">
      <header className="flex items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-surface)]/80 px-6 py-3 backdrop-blur">
        <div className="flex min-w-0 items-center gap-2">
          <StatusDot tone={statusTone(session.status)} />
          <span className="truncate text-sm font-medium" title={session.id}>
            {session.title?.trim() || session.id}
          </span>
          <span className="text-[11px] text-[var(--color-foreground-muted)]">
            · {sessionModeLabel(session.mode, session.mode_label)}
          </span>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-[11px] text-[var(--color-foreground-muted)]">{statusLabel(session.status)}</span>
        </div>
      </header>

      <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto pb-36">
        {convo.loading && convo.messages.length === 0 ? (
          <p className="px-6 py-8 text-xs text-[var(--color-foreground-muted)]">Loading conversation…</p>
        ) : convo.messages.length === 0 && convo.streaming.length === 0 && !activityLive ? (
          <div className="flex h-full items-center justify-center px-6 text-center">
            <div className="max-w-md">
              <h3 className="text-base font-semibold">Ready when you are.</h3>
              <p className="mt-1.5 text-sm text-[var(--color-foreground-muted)]">
                Type a prompt below. The agent will work in <code className="font-mono text-xs bg-[var(--color-surface-muted)] px-1 rounded">/workspace</code>{" "}
                inside its sandbox and stream replies back here.
              </p>
            </div>
          </div>
        ) : (
          <div>
            {convo.messages.map((m, i) => {
              const prev = convo.messages[i - 1];
              const showDivider = i === 0 || dayKey(prev?.created_at) !== dayKey(m.created_at);
              return (
                <Fragment key={m.id}>
                  {showDivider && <DayDivider label={formatDayDivider(m.created_at)} />}
                  <div id={`msg-${m.id}`} className={showDivider ? "" : "border-t border-[var(--color-border)]"}>
                    <MessageBubble
                      message={m}
                      sessionId={session.id}
                      showActions={isFinalReply(m, convo.messages[i + 1])}
                    />
                  </div>
                </Fragment>
              );
            })}
            {convo.streaming.map((stream, i) => (
              <div
                key={stream.id}
                className={i === 0 && convo.messages.length === 0 ? "" : "border-t border-[var(--color-border)]"}
              >
                <StreamingBubble text={stream.text} liveTrace={i === 0 ? convo.liveTrace : undefined} />
              </div>
            ))}
            {/* Tool activity can begin before any text streams. When the turn is
                live with trace nodes but no streaming bubble exists yet, show a
                standalone bubble so the work is visible immediately. */}
            {convo.streaming.length === 0 && activityLive && convo.liveTrace.length > 0 ? (
              <div className={convo.messages.length === 0 ? "" : "border-t border-[var(--color-border)]"}>
                <StreamingBubble text="" liveTrace={convo.liveTrace} />
              </div>
            ) : null}
          </div>
        )}
      </div>

      <Composer
        disabled={!accepting}
        placeholder={placeholder}
        topSlot={<ActivityIndicator activity={activity} />}
        onSend={(content) => sendMessage(session.id, content)}
      />
    </main>
  );
}
