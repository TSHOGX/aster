"use client";

import { memo, useEffect, useRef, useState } from "react";
import { User, Bot } from "lucide-react";

import { MarkdownView } from "./markdown-view";
import { TraceList } from "./trace-list";
import { MessageActions } from "./message-actions";
import type { ApiMessage, TraceNode } from "@/lib/types";
import { cn } from "@/lib/cn";
import { formatFullTimestamp, formatTime } from "@/lib/format";

function MessageBubbleImpl({
  message,
  sessionId,
  showActions = true
}: {
  message: ApiMessage;
  sessionId: string;
  // Whether to show the Message Action Bar (thumbs / copy / overflow). We only
  // surface it on a turn's final assistant reply — the answer to the user's
  // message — not on the intermediate tool-use steps that precede it.
  showActions?: boolean;
}) {
  const isUser = message.role === "user";
  return (
    <div
      className={cn(
        "flex gap-3 px-6 py-4",
        isUser ? "bg-transparent" : "bg-[var(--color-surface)]"
      )}
    >
      <div
        className={cn(
          "flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-[11px] font-medium",
          isUser
            ? "bg-[var(--color-user-bubble)] text-[var(--color-foreground)]"
            : "bg-[var(--color-foreground)] text-[var(--color-accent-foreground)]"
        )}
      >
        {isUser ? <User className="h-3.5 w-3.5" /> : <Bot className="h-3.5 w-3.5" />}
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-1.5 text-[11px] uppercase tracking-wider text-[var(--color-foreground-muted)]">
          <span>{isUser ? "You" : "Assistant"}</span>
          <time
            dateTime={message.created_at}
            title={formatFullTimestamp(message.created_at)}
            className="normal-case tracking-normal font-mono tabular-nums text-[var(--color-foreground-muted)] opacity-70 cursor-default"
          >
            {formatTime(message.created_at)}
          </time>
        </div>
        <div className="mt-1">
          {isUser ? (
            <div className="whitespace-pre-wrap break-words text-sm leading-relaxed">{message.content}</div>
          ) : (
            <>
              <TraceList nodes={message.trace ?? []} />
              <MarkdownView content={message.content} />
              {showActions ? <MessageActions message={message} sessionId={sessionId} /> : null}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

export const MessageBubble = memo(MessageBubbleImpl);
MessageBubble.displayName = "MessageBubble";

// Returns `value`, but rate-limits how often the returned reference changes to
// at most once per `ms`. A trailing timeout always flushes the most recent
// value after the last change, so the final value is never dropped — critical
// for streaming, where the last token completes the message.
function useThrottledValue<T>(value: T, ms: number): T {
  const [throttled, setThrottled] = useState(value);
  // 0 means "never emitted": the first change reads as fully elapsed and lands
  // immediately (leading edge). Seeded in the effect, not during render, so no
  // impure Date.now() call happens while rendering.
  const lastRun = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const elapsed = Date.now() - lastRun.current;
    if (elapsed >= ms) {
      // Enough time has passed — update immediately (leading edge).
      lastRun.current = Date.now();
      setThrottled(value);
    } else {
      // Schedule a trailing update so the latest value still lands even if no
      // further changes arrive. Each new value reschedules to the newest one.
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        lastRun.current = Date.now();
        setThrottled(value);
      }, ms - elapsed);
    }
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [value, ms]);

  return throttled;
}

function StreamingBubbleImpl({ text, liveTrace }: { text: string; liveTrace?: TraceNode[] }) {
  // Throttle only the markdown text so the expensive parse/highlight runs at
  // ~12fps instead of once per streamed token. The live trace still updates
  // immediately below.
  const throttled = useThrottledValue(text, 80);
  return (
    <div className="flex gap-3 px-6 py-4 bg-[var(--color-surface)]">
      <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-[var(--color-foreground)] text-[var(--color-accent-foreground)]">
        <Bot className="h-3.5 w-3.5" />
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-[11px] uppercase tracking-wider text-[var(--color-foreground-muted)]">
          Assistant
          <span className="ml-1 inline-block h-1.5 w-1.5 rounded-full bg-[var(--color-accent)] animate-pulse-dot" />
        </div>
        <div className="mt-1">
          <TraceList nodes={liveTrace ?? []} defaultOpen />
          <MarkdownView content={throttled || "…"} />
        </div>
      </div>
    </div>
  );
}

export const StreamingBubble = memo(StreamingBubbleImpl);
StreamingBubble.displayName = "StreamingBubble";

function DayDividerImpl({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3 px-6 py-2.5 bg-[var(--color-background)]">
      <div className="h-px flex-1 bg-[var(--color-border)]" />
      <span className="text-[11px] font-medium text-[var(--color-foreground-muted)]">{label}</span>
      <div className="h-px flex-1 bg-[var(--color-border)]" />
    </div>
  );
}

export const DayDivider = memo(DayDividerImpl);
DayDivider.displayName = "DayDivider";
