"use client";

import { Fragment, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Bot, RefreshCw, RotateCw, Sparkles } from "lucide-react";

import { useActivity, useConversation, useHarness, useSelectedSession } from "@/components/harness-provider";
import { clearMessageJump, getMessageJump, subscribeMessageJump } from "@/lib/message-jump";
import { ShellConversation } from "./shell-conversation";
import { DayDivider, MessageBubble, StreamingBubble } from "./message-bubble";
import { ActivityIndicator } from "./activity-indicator";
import { Composer } from "./composer";
import { useT } from "@/lib/i18n";
import { PromptSuggestions } from "./prompt-suggestions";
import { EmptyState } from "@/components/ui/empty-state";
import { MessageWithPath } from "@/components/ui/message-with-path";
import { ConversationSkeleton } from "@/components/ui/skeleton";
import { StatusPill, statusTone } from "@/components/ui/badge";
import { isFinalReply, interruptionKind, type InterruptionKind } from "@/lib/message-grouping";
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
  const t = useT();
  const session = useSelectedSession();
  const convo = useConversation(session?.id ?? null);
  const activity = useActivity(session?.id ?? null);
  const { sendMessage, refresh } = useHarness();
  const [refreshing, setRefreshing] = useState(false);
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
      <main className="flex h-full flex-col items-center justify-center bg-[var(--color-background)]">
        <EmptyState
          icon={<Bot className="h-5 w-5" />}
          title={t("conversation.noSessionTitle")}
          description={t("conversation.noSessionBody")}
        />
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
    ? t("composer.sessionEnded")
    : accepting
    ? t("composer.placeholder")
    : t("composer.thinking");

  const activityLive = activity.phase !== "idle";

  // Offer to resume a turn that died without answering. Gated on the session
  // being idle and writable: mid-stream, a toolUse tail is simply the turn still
  // working, and on a read-only or ended session there is nothing to resume.
  //
  // The kind matters: after a sandbox death the agent's context is gone, so the
  // notice must not imply the work can be picked up mid-thought.
  const interrupted =
    accepting &&
    !activityLive &&
    convo.streaming.length === 0 &&
    !convo.loading
      ? interruptionKind(convo.messages)
      : undefined;

  return (
    <main className="relative flex h-full min-h-0 min-w-0 flex-col bg-[var(--color-background)]">
      <header className="flex shrink-0 items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-surface)]/80 px-4 py-3 backdrop-blur sm:px-6">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="truncate text-sm font-medium" title={session.id}>
            {session.title?.trim() || session.id}
          </span>
          <span className="hidden shrink-0 text-2xs text-[var(--color-foreground-muted)] sm:inline">
            {sessionModeLabel(session.mode, session.mode_label)}
          </span>
        </div>
        <div className="flex items-center gap-3">
          {/* A shared session gets no live events: eventVisibleTo filters them to
              the owner, so without an explicit control the view would silently
              stop updating while the owner keeps working. */}
          {readOnly ? (
            <button
              type="button"
              onClick={async () => {
                setRefreshing(true);
                await refresh();
                setRefreshing(false);
              }}
              disabled={refreshing}
              className="flex items-center gap-1.5 rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1 text-2xs hover:bg-[var(--color-surface-muted)] disabled:opacity-60"
              title={t("conversation.sharedNoLive")}
            >
              <RefreshCw className={refreshing ? "h-3 w-3 animate-spin" : "h-3 w-3"} />
              Refresh
            </button>
          ) : null}
          <StatusPill tone={statusTone(session.status)}>{statusLabel(session.status)}</StatusPill>
        </div>
      </header>

      {/* Bottom padding clears the floating composer, which starts at two lines
          tall (composer.tsx) — the last message has to scroll clear of it. */}
      <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto pb-40">
        {convo.loading && convo.messages.length === 0 ? (
          <ConversationSkeleton />
        ) : convo.messages.length === 0 && convo.streaming.length === 0 && !activityLive ? (
          <div className="flex min-h-full items-center justify-center px-4 py-10 sm:px-6">
            <div className="w-full max-w-[var(--measure)]">
              <EmptyState
                icon={<Sparkles className="h-5 w-5" />}
                title={t("conversation.emptyTitle")}
                description={
                  <MessageWithPath
                    messageKey="conversation.emptyBody"
                    path="/workspace"
                    codeClassName="rounded bg-[var(--color-surface-muted)] px-1 text-xs"
                  />
                }
              >
                {accepting ? <PromptSuggestions /> : null}
              </EmptyState>
            </div>
          </div>
        ) : (
          /* One centered column at --measure, the same width the composer uses,
             so message text and the input line up in a single channel. Turns are
             separated by their own vertical padding — no rules, no row fills. */
          <div className="mx-auto w-full max-w-[var(--measure)] px-4 sm:px-6">
            {convo.messages.map((m, i) => {
              const prev = convo.messages[i - 1];
              const showDivider = i === 0 || dayKey(prev?.created_at) !== dayKey(m.created_at);
              return (
                <Fragment key={m.id}>
                  {showDivider && <DayDivider label={formatDayDivider(m.created_at)} />}
                  <div id={`msg-${m.id}`}>
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
              <StreamingBubble
                key={stream.id}
                text={stream.text}
                liveTrace={i === 0 ? convo.liveTrace : undefined}
              />
            ))}
            {/* Tool activity can begin before any text streams. When the turn is
                live with trace nodes but no streaming bubble exists yet, show a
                standalone bubble so the work is visible immediately. */}
            {convo.streaming.length === 0 && activityLive && convo.liveTrace.length > 0 ? (
              <StreamingBubble text="" liveTrace={convo.liveTrace} />
            ) : null}
            {interrupted ? (
              <InterruptedNotice
                kind={interrupted}
                onContinue={() =>
                  sendMessage(
                    session.id,
                    interrupted === "sandbox"
                      ? t("conversation.continuePromptAfterSandboxDeath")
                      : t("conversation.continuePrompt")
                  )
                }
              />
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

// Shown when the last turn ended without answering. The reason is already in the
// transcript as an assistant notice, so this adds only the recovery action.
//
// What clicking it means depends on the kind. After a model failure the sandbox
// still holds the agent's context, so "carry on" resumes the work in place. After
// a sandbox death the context is gone and only host-side files survived, so the
// prompt asks the agent to re-establish its progress first — and the label says
// so, because a bare "continue" would promise something the system cannot do.
function InterruptedNotice({
  kind,
  onContinue
}: {
  kind: InterruptionKind;
  onContinue: () => void;
}) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center gap-3 py-4">
      <span className="text-xs text-[var(--color-foreground-muted)]">
        {kind === "sandbox"
          ? t("conversation.interruptedSandbox")
          : t("conversation.interrupted")}
      </span>
      <button
        type="button"
        onClick={onContinue}
        className="inline-flex items-center gap-1.5 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-1 text-xs font-medium text-[var(--color-foreground)] transition-colors hover:bg-[var(--color-surface-muted)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-accent)]"
      >
        <RotateCw className="h-3 w-3" aria-hidden="true" />
        {kind === "sandbox"
          ? t("conversation.restartTurn")
          : t("conversation.continueTurn")}
      </button>
    </div>
  );
}
