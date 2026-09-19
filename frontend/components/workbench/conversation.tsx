"use client";

import { Fragment, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ArrowUp, Bot, Clock, Loader2, RefreshCw, RotateCw, Sparkles, X } from "lucide-react";
import { toast } from "sonner";

import { useActivity, useCapabilities, useConversation, useHarness, useSelectedSession, useSessionRuntime } from "@/components/harness-provider";
import { clearMessageJump, getMessageJump, subscribeMessageJump } from "@/lib/message-jump";
import { PagedTranscript } from "./paged-transcript";
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
import type { ApiTurn } from "@/lib/types";
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
  const capabilities = useCapabilities();
  const paged = capabilities?.message_pages && process.env.NEXT_PUBLIC_HARNESS_MESSAGE_PAGES !== "0";
  const session = useSelectedSession();
  const convo = useConversation(session?.id ?? null);
  const activity = useActivity(session?.id ?? null);
  const runtime = useSessionRuntime(session?.id ?? null);
  const { sendMessage, interruptSession, cancelQueuedTurn, sendQueuedTurn, refresh } = useHarness();
  const [refreshing, setRefreshing] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [queueError, setQueueError] = useState<string | null>(null);
  // Turns whose cancel request is in flight. The X used to give no feedback until
  // the server answered, and a cancel competes for the orchestrator's single
  // writer, so a slow one looked like a missed click and got clicked again.
  const [cancelling, setCancelling] = useState<ReadonlySet<number>>(() => new Set());
  // Same for ↑: a steer waits on a file write the sandbox has to pick up, so the
  // button has to show it is working or it reads as a dead click.
  const [sending, setSending] = useState<ReadonlySet<number>>(() => new Set());
  const scrollRef = useRef<HTMLDivElement>(null);
  const scrollRafRef = useRef<number | null>(null);

  // The queue is server state, rendered as-is. There is no local mirror to
  // reconcile, which is what previously let the same prompt appear in the queue
  // and the transcript at once, and left drained entries behind.
  const queuedTurns = runtime?.queued_turns ?? [];

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

  const activityLive = activity.phase !== "idle";
  // "Busy" is a property of the turn queue, so the projection answers it. Until
  // the first snapshot arrives, fall back to what the session list knows.
  //
  // A queued turn counts as busy: work is pending either way, and the placeholder
  // should invite a follow-up rather than pretend the agent is free.
  const busy = runtime
    ? runtime.active_turn != null || runtime.queued_turns.length > 0
    : activityLive || session.status === "running_active";

  // Whether Stop has anything to act on, which is narrower than busy. Stop ends the
  // turn in flight; it is not a queue-clearing button, and offering it when only
  // queued turns remain would promise an action with no effect. A queued turn is
  // withdrawn with its own X instead — one prompt at a time, each the user's call.
  //
  // Any turn the agent is actually executing is leased or running, and the
  // projection reports both as active_turn, so this never hides Stop during real
  // work.
  //
  // Before the first snapshot there is no queue/active split to read, so the
  // fallback keeps the old coarse answer: running_active offers Stop. It can be
  // briefly wrong in the new queue-only case, and that is the right way round — a
  // Stop with nothing running is idempotent and leaves the queue intact, whereas
  // hiding Stop from a live turn would leave the user unable to interrupt at all.
  const stoppable = runtime ? runtime.active_turn != null : activityLive || session.status === "running_active";

  // A busy session accepts input now — the turn is queued behind the current one
  // — so the placeholder invites it instead of reporting the agent as unavailable.
  const placeholder = readOnly
    ? `Read-only — shared by ${session.shared_by ?? "another user"}`
    : terminal
    ? t("composer.sessionEnded")
    : busy
    ? t("composer.queuePlaceholder")
    : t("composer.placeholder");

  const stop = async () => {
    if (stopping || !session || !stoppable) return;
    setStopping(true);
    setQueueError(null);
    const result = await interruptSession(session.id);
    setStopping(false);
    if (!result.ok) {
      // The composer owns the normal inline error surface; a failed stop must
      // still be visible when the agent composer is disabled by a busy session.
      toast.error(result.error ?? t("composer.stopFailed"), { duration: 6000 });
    }
  };

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
      ? paged ? convo.interruption || undefined : interruptionKind(convo.messages)
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
          <StatusPill tone={busy ? "running" : statusTone(session.status, session.sandbox)}>
            {busy ? t("composer.thinking") : statusLabel(session.status, session.sandbox)}
          </StatusPill>
        </div>
      </header>

      {/* Bottom padding clears the floating composer, which starts at two lines
          tall (composer.tsx) — the last message has to scroll clear of it. */}
      {paged && (convo.messages.length > 0 || convo.loading) ? <PagedTranscript key={session.id} sessionId={session.id} /> : <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto pb-40">
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
      </div>}
      {paged && interrupted && <InterruptedNotice kind={interrupted} onContinue={() => sendMessage(session.id, t(interrupted === "sandbox" ? "conversation.continuePromptAfterSandboxDeath" : "conversation.continuePrompt"))} />}

      <Composer
        disabled={readOnly || terminal}
        busy={!readOnly && !terminal && stoppable}
        stopping={stopping}
        onStop={() => void stop()}
        placeholder={placeholder}
        topSlot={<ActivityIndicator activity={activity} />}
        queueSlot={
          queuedTurns.length > 0 ? (
            <QueuedTurns
              turns={queuedTurns}
              error={queueError}
              cancelling={cancelling}
              sending={sending}
              // Whether ↑ steers into the running turn or releases the row to run on
              // its own. The server decides for real; this only picks the label, so a
              // stale projection mislabels a button rather than misrouting a prompt.
              steers={runtime?.active_turn != null}
              onSend={async (turnId) => {
                if (sending.has(turnId)) return;
                setQueueError(null);
                setSending((prev) => new Set(prev).add(turnId));
                try {
                  const result = await sendQueuedTurn(session.id, turnId);
                  if (!result.ok) setQueueError(result.error ?? t("composer.queueSendFailed"));
                } finally {
                  setSending((prev) => {
                    const next = new Set(prev);
                    next.delete(turnId);
                    return next;
                  });
                }
              }}
              onCancel={async (turnId) => {
                if (cancelling.has(turnId)) return;
                setQueueError(null);
                setCancelling((prev) => new Set(prev).add(turnId));
                try {
                  const result = await cancelQueuedTurn(session.id, turnId);
                  if (!result.ok) setQueueError(result.error ?? t("composer.queueCancelFailed"));
                } finally {
                  setCancelling((prev) => {
                    const next = new Set(prev);
                    next.delete(turnId);
                    return next;
                  });
                }
              }}
            />
          ) : null
        }
        // No sandbox behind the session: the send blocks for the cold start.
        sendingHint={session.sandbox === "live" ? undefined : t("composer.wakingSandbox")}
        onSend={async (content) => {
          setQueueError(null);
          return sendMessage(session.id, content);
        }}
      />
    </main>
  );
}

/**
 * The user's pending turns, straight from the runtime projection.
 *
 * Deliberately a pure render of server state. The turns are durable and the
 * orchestrator drains them in order, so there is no local queue to advance and
 * nothing to reconcile — acting on an entry has to be a request, because hiding it
 * locally would leave the prompt to execute anyway.
 *
 * Each row has two actions, and neither is a queue-wide operation: ↑ sends this
 * one, X withdraws it. One prompt at a time is the point — after a Stop the queue
 * is usually stale, so the user picks what still matters rather than being handed
 * an all-or-nothing choice.
 */
function QueuedTurns({
  turns,
  error,
  cancelling,
  sending,
  steers,
  onSend,
  onCancel
}: {
  turns: ApiTurn[];
  error: string | null;
  cancelling: ReadonlySet<number>;
  sending: ReadonlySet<number>;
  steers: boolean;
  onSend: (turnId: number) => void | Promise<void>;
  onCancel: (turnId: number) => void | Promise<void>;
}) {
  const t = useT();
  return (
    <div className="mb-1 overflow-hidden rounded-[var(--radius-xl)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-sm">
      <div className="flex items-center gap-2 border-b border-[var(--color-border)] px-3 py-1.5 text-2xs text-[var(--color-foreground-muted)]">
        <Clock className="h-3 w-3" aria-hidden />
        {t("composer.queuedCount", { count: turns.length })}
      </div>
      {turns.map((turn) => (
        <div
          key={turn.id}
          className="flex items-center gap-3 border-b border-[var(--color-border)] px-3 py-2 last:border-b-0"
        >
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm text-[var(--color-foreground)]">{turn.content}</div>
            {/* Only a parked row gets the hint. An unparked one is already on its
                way to the worker, so saying it waits for the user would be wrong. */}
            {turn.steering_pending ? (
              <div className="text-2xs text-[var(--color-foreground-muted)]">{t("composer.queueSteeringPending")}</div>
            ) : turn.parked ? (
              <div className="text-2xs text-[var(--color-foreground-muted)]">{t("composer.queueParked")}</div>
            ) : null}
          </div>
          {/* The label follows what the press will actually do, which the server
              decides from the same active/idle split: with a turn executing the
              text is steered into it, otherwise the row is released to run on its
              own. Promising "send now" mid-turn would misdescribe a steer. */}
          <button
            type="button"
            onClick={() => void onSend(turn.id)}
            disabled={turn.steering_pending || sending.has(turn.id) || cancelling.has(turn.id)}
            className="shrink-0 rounded p-0.5 text-[var(--color-foreground-muted)] transition-colors hover:text-[var(--color-accent)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-accent)] disabled:cursor-wait disabled:opacity-60"
            title={steers ? t("composer.queueSteer") : t("composer.queueSend")}
            aria-label={steers ? t("composer.queueSteer") : t("composer.queueSend")}
          >
            {turn.steering_pending || sending.has(turn.id) ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
            ) : (
              <ArrowUp className="h-3.5 w-3.5" aria-hidden />
            )}
          </button>
          <button
            type="button"
            onClick={() => void onCancel(turn.id)}
            // Also inert mid-send: the row is being delivered, and withdrawing it
            // now would race a steer that has already reached the agent.
            disabled={turn.steering_pending || cancelling.has(turn.id) || sending.has(turn.id)}
            className="shrink-0 rounded p-0.5 text-[var(--color-foreground-muted)] transition-colors hover:text-[var(--color-danger)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-accent)] disabled:cursor-wait disabled:opacity-60"
            title={t("composer.queueCancel")}
            aria-label={t("composer.queueCancel")}
          >
            {cancelling.has(turn.id) ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
            ) : (
              <X className="h-3.5 w-3.5" aria-hidden />
            )}
          </button>
        </div>
      ))}
      {error ? (
        <div className="border-t border-[var(--color-border)] px-3 py-1.5 text-2xs text-[var(--color-danger)]" role="status">
          {error}
        </div>
      ) : null}
    </div>
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
