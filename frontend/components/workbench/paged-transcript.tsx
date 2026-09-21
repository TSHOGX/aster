"use client";
import { useT } from "@/lib/i18n";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import {
  useConversation,
  useTranscriptState,
  useSelectedSession,
  useHarness,
} from "@/components/harness-provider";
import {
  clearMessageJump,
  getMessageJump,
  subscribeMessageJump,
} from "@/lib/message-jump";
import { dayKey, formatDayDivider } from "@/lib/format";
import { isFinalReply } from "@/lib/message-grouping";
import { DayDivider, MessageBubble, StreamingBubble } from "./message-bubble";
import { VirtualTranscript } from "./virtual-transcript";
import type { ApiMessage } from "@/lib/types";

export function PagedTranscript({ sessionId }: { sessionId: string }) {
  const t = useT();
  const c = useTranscriptState(sessionId);
  const readOnly = useSelectedSession()?.access === "reader";
  const { loadOlderMessages, loadNewerMessages, loadMessageWindow } =
    useHarness();
  const jump = useSyncExternalStore(
    subscribeMessageJump,
    getMessageJump,
    () => null,
  );
  const target = jump?.sessionId === sessionId ? jump.messageId : undefined;
  useEffect(() => {
    if (target && !c.messages.some((m) => m.id === target))
      void loadMessageWindow(sessionId, target);
  }, [target, c.messages, loadMessageWindow, sessionId]);
  const render = useCallback(
    (m: ApiMessage, index: number) => (
      <>
        {(index === 0 ||
          dayKey(c.messages[index - 1]?.created_at) !==
            dayKey(m.created_at)) && (
          <DayDivider label={formatDayDivider(m.created_at)} />
        )}
        <MessageBubble
          message={m}
          sessionId={sessionId}
          showActions={!readOnly && (m.is_final_reply ?? isFinalReply(m, c.messages[index + 1]))}
        />
      </>
    ),
    [c.messages, sessionId, readOnly],
  );
  if (!c.historyLoaded || !c.messages.length)
    return (
      <div className="p-4 text-sm">
        {c.error ?? (c.loading ? t("perf.loading") : "")}
        {c.error && <button onClick={() => void loadMessageWindow(sessionId)}>{t("perf.retry")}</button>}
      </div>
    );
  return (
    <VirtualTranscript
      key={sessionId}
      sessionId={sessionId}
      rows={c.messages}
      render={render}
      hasOlder={c.hasOlder}
      hasNewer={c.hasNewer}
      loadOlder={() => void loadOlderMessages(sessionId)}
      loadNewer={() => void loadNewerMessages(sessionId)}
      jumpId={target}
      onJumpDone={clearMessageJump}
      onLatest={() => void loadMessageWindow(sessionId)}
      footer={<LiveTail sessionId={sessionId} />}
    />
  );
}

function LiveTail({ sessionId }: { sessionId: string }) {
  const c = useConversation(sessionId);
  const { loadOlderMessages } = useHarness();
  return (
    <>
      {c.error && (
        <button onClick={() => void loadOlderMessages(sessionId)}>
          {c.error} · Retry
        </button>
      )}
      {c.streaming.map((stream, i) => (
        <StreamingBubble
          key={stream.id}
          text={stream.text}
          liveTrace={i === 0 ? c.liveTrace : undefined}
        />
      ))}
      {!c.streaming.length &&
        c.liveTrace.length > 0 &&
        c.activity.phase !== "idle" && (
          <StreamingBubble text="" liveTrace={c.liveTrace} />
        )}
    </>
  );
}
