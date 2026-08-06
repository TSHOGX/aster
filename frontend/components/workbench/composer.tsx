"use client";

import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ArrowUp } from "lucide-react";

import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/cn";
import { clearComposerDraft, getComposerDraft, subscribeComposerDraft } from "@/lib/composer-draft";
import { useComposerState } from "@/lib/use-composer-state";

type ComposerProps = {
  disabled?: boolean;
  placeholder?: string;
  topSlot?: ReactNode;
  onSend: (content: string) => Promise<{ ok: boolean; error?: string }>;
};

export function Composer({ disabled, placeholder, topSlot, onSend }: ComposerProps) {
  const { value, setValue, sending, error, submit, onKey } = useComposerState(onSend, disabled);
  const [multiline, setMultiline] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);

  // A suggested-prompt card fills the box and focuses it, leaving the send to the
  // user — the prompts are starting points meant to be edited, not one-click
  // sends. Keyed on `seq` so picking the same card twice still lands.
  const draft = useSyncExternalStore(subscribeComposerDraft, getComposerDraft, () => null);
  const appliedSeq = useRef(0);
  useEffect(() => {
    if (draft === null || draft.seq === appliedSeq.current) return;
    appliedSeq.current = draft.seq;
    setValue(draft.text);
    clearComposerDraft();
    ref.current?.focus();
  }, [draft, setValue]);

  // Auto-grow the textarea and flip to the expanded "card" form once the content
  // spills past a single line (either a wrap or an explicit newline).
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    const next = Math.min(el.scrollHeight, 220);
    el.style.height = `${next}px`;
    setMultiline(next > 40 || value.includes("\n"));
  }, [value]);

  const canSend = !disabled && !sending && !!value.trim();

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 z-20 flex flex-col items-center px-4 pb-4">
      {/* Soft fade so scrolled content dissolves under the floating bar. */}
      <div className="pointer-events-none absolute inset-x-0 bottom-0 -z-10 h-32 bg-gradient-to-t from-[var(--color-background)] via-[var(--color-background)] to-transparent" />
      {/* Same --measure as the conversation column, so the input sits directly
          under the text it answers instead of in its own narrower channel. */}
      <div className="pointer-events-auto w-full max-w-[var(--measure)]">
        {topSlot}
        <div
          className={cn(
            "flex gap-2 border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-lg)] backdrop-blur transition-all duration-200 ease-out focus-within:border-[var(--color-accent)]",
            multiline
              ? "flex-col rounded-[var(--radius-xl)] px-3 pb-2.5 pt-2.5"
              : "flex-row items-center rounded-full py-1.5 pl-4 pr-1.5"
          )}
        >
          <Textarea
            ref={ref}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={onKey}
            placeholder={placeholder ?? "Send a message…"}
            rows={1}
            disabled={disabled || sending}
            className={cn(
              "min-h-[24px] resize-none border-0 bg-transparent p-0 leading-6 focus:border-0 focus:outline-none focus-visible:outline-none",
              multiline ? "max-h-[220px] px-1" : "h-6"
            )}
          />
          <div className={cn("flex items-center", multiline ? "justify-end" : "shrink-0")}>
            <button
              onClick={() => void submit()}
              disabled={!canSend}
              aria-label="Send"
              className={cn(
                "flex h-9 w-9 shrink-0 items-center justify-center rounded-full transition-colors",
                canSend
                  ? "bg-[var(--color-button-primary)] text-[var(--color-button-primary-foreground)] hover:bg-[var(--color-button-primary-hover)]"
                  : "bg-[var(--color-surface-muted)] text-[var(--color-foreground-muted)]"
              )}
            >
              <ArrowUp className="h-4 w-4" />
            </button>
          </div>
        </div>
        <div className="mt-1.5 flex items-center justify-between gap-3 px-3 text-2xs text-[var(--color-foreground-muted)]">
          <span>{disabled ? "Session is busy or ended." : "Enter to send · Shift+Enter for newline"}</span>
          {error ? <span className="text-[var(--color-danger)]">{error}</span> : null}
        </div>
      </div>
    </div>
  );
}
