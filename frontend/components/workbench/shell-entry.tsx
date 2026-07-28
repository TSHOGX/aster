"use client";

import type { ApiMessage } from "@/lib/types";

/**
 * One line of a shell transcript: the command the user typed, or a block of
 * output.
 *
 * Kept in its own module, and deliberately free of any provider or context
 * dependency, because the read-only shared-link page renders the same transcript
 * from its own fetch. Data comes in as props; nothing here reaches for the
 * workbench's auth/SSE/session state.
 */
export function ShellEntry({ message }: { message: ApiMessage }) {
  const isUser = message.role === "user";
  return isUser ? (
    <div className="flex gap-2 px-6 py-3">
      <span className="shrink-0 font-mono text-[11px] leading-6 text-[var(--color-foreground-muted)]">$</span>
      <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-sm leading-6 text-[var(--color-foreground)]">
        {message.content}
      </pre>
    </div>
  ) : (
    <div className="px-6 py-2">
      <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-[13px] leading-6 text-[var(--color-foreground)]">
        {message.content}
      </pre>
    </div>
  );
}
