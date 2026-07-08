"use client";

import { User, Bot } from "lucide-react";

import { MarkdownView } from "./markdown-view";
import type { ApiMessage } from "@/lib/types";
import { cn } from "@/lib/cn";
import { formatFullTimestamp, formatTime } from "@/lib/format";

export function MessageBubble({ message }: { message: ApiMessage }) {
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
            <MarkdownView content={message.content} />
          )}
        </div>
      </div>
    </div>
  );
}

export function StreamingBubble({ text }: { text: string }) {
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
          <MarkdownView content={text || "…"} />
        </div>
      </div>
    </div>
  );
}

export function DayDivider({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3 px-6 py-2.5 bg-[var(--color-background)]">
      <div className="h-px flex-1 bg-[var(--color-border)]" />
      <span className="text-[11px] font-medium text-[var(--color-foreground-muted)]">{label}</span>
      <div className="h-px flex-1 bg-[var(--color-border)]" />
    </div>
  );
}
