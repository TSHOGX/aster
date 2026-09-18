"use client";

import { MessagePreview, messageBase } from "./message-detail";
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
export function ShellEntry({ message, resourceBase }: { message: ApiMessage; resourceBase?: string }) {
  if (message.content_truncated) return <MessagePreview message={message} base={messageBase(message.session_id, message.id, resourceBase)} />;
  const isUser = message.role === "user";
  // Horizontal padding belongs to the centering wrapper in the parent, so the
  // entry itself only owns its vertical rhythm.
  return isUser ? (
    <div className="flex gap-2 py-3">
      <span className="shrink-0 font-mono text-2xs leading-6 text-[var(--color-accent)]">$</span>
      <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-sm leading-6 text-[var(--color-foreground)]">
        {message.content}
      </pre>
    </div>
  ) : (
    <div className="py-2">
      <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-xs leading-6 text-[var(--color-foreground-muted)]">
        {message.content}
      </pre>
    </div>
  );
}
