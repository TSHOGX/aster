"use client";

import { useEffect, useRef, useSyncExternalStore, type ReactNode } from "react";
import { ArrowUp, Loader2, Square } from "lucide-react";

import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/cn";
import { clearComposerDraft, getComposerDraft, subscribeComposerDraft } from "@/lib/composer-draft";
import { useComposerState } from "@/lib/use-composer-state";
import { useT } from "@/lib/i18n";

type ComposerProps = {
  disabled?: boolean;
  placeholder?: string;
  topSlot?: ReactNode;
  onSend: (content: string) => Promise<{ ok: boolean; error?: string }>;
  /** The agent has work in flight (a running turn, or turns queued behind it). */
  busy?: boolean;
  stopping?: boolean;
  onStop?: () => void;
  /** The pending-turn list, rendered above the input. */
  queueSlot?: ReactNode;
  /**
   * Replaces the helper line while a send is in flight. The send request blocks
   * for the whole cold start when the session has no sandbox, so this is where
   * the user is told that the wait is the sandbox waking, not the model stalling.
   */
  sendingHint?: string;
};

// Two lines at the composer's 24px line-height, plus a cap of ~8 lines before the
// field scrolls internally. Two is the compromise the proportions want: one line
// made every wrapped message scroll, three left an empty band under short ones.
const COMPOSER_MIN_H = 52;
const COMPOSER_MAX_H = 200;

export function Composer({
  disabled,
  placeholder,
  topSlot,
  queueSlot,
  onSend,
  busy = false,
  stopping = false,
  onStop,
  sendingHint
}: ComposerProps) {
  const t = useT();
  const { value, setValue, sending, error, submit, onKey } = useComposerState(onSend, disabled);
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

  // Auto-grow the textarea from a two-line floor up to the cap. The box is not a
  // single-line pill: most messages here are a paragraph or a pasted block, and a
  // one-line field made every one of them scroll from the first wrap.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(Math.max(el.scrollHeight, COMPOSER_MIN_H), COMPOSER_MAX_H)}px`;
  }, [value]);

  const canSend = !disabled && !sending && !!value.trim();
  // The single button is Stop only while the composer is empty. With text typed,
  // it always sends — that keystroke is a queue request, and a busy agent is the
  // normal reason to queue, so letting "busy" win would make the button refuse the
  // very action the user is in the middle of.
  const showStop = busy && !value.trim();

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 z-20 flex flex-col items-center px-4 pb-4">
      {/* Soft fade so scrolled content dissolves under the floating bar. */}
      <div className="pointer-events-none absolute inset-x-0 bottom-0 -z-10 h-40 bg-gradient-to-t from-[var(--color-background)] via-[var(--color-background)] to-transparent" />
      {/* Narrower than the conversation's --measure: a full-measure input is a
          very long line to type into, and pulling the box in centers it under
          the reading column instead of spanning the whole pane. */}
      <div className="pointer-events-auto w-full max-w-[min(var(--measure),46rem)]">
        {queueSlot}
        {topSlot}
        <div
          className={cn(
            "flex flex-col gap-1 rounded-[var(--radius-xl)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 pb-2 pt-2.5 shadow-[var(--shadow-lg)] backdrop-blur transition-colors duration-200 ease-out focus-within:border-[var(--color-accent)]"
          )}
        >
          <Textarea
            ref={ref}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={onKey}
            placeholder={placeholder ?? t("composer.placeholder")}
            rows={2}
            disabled={disabled}
            className={cn(
              // The wrapping bar owns the focus ring here (see ui/textarea.tsx).
              "resize-none border-0 bg-transparent p-0 px-1 leading-6 focus:border-0 focus:outline-none focus:ring-0 focus-visible:outline-none",
              // Keep in step with COMPOSER_MIN_H / COMPOSER_MAX_H above.
              "min-h-[52px] max-h-[200px]"
            )}
          />
          <div className="flex items-center justify-end">
            <button
              onClick={() => (showStop ? onStop?.() : void submit())}
              disabled={showStop ? stopping : !canSend}
              aria-label={showStop ? t(stopping ? "composer.stopping" : "composer.stop") : t("composer.send")}
              title={showStop ? t(stopping ? "composer.stopping" : "composer.stop") : undefined}
              className={cn(
                "flex h-9 w-9 shrink-0 items-center justify-center rounded-full transition-colors",
                showStop || canSend
                  ? "bg-[var(--color-button-primary)] text-[var(--color-button-primary-foreground)] hover:bg-[var(--color-button-primary-hover)]"
                  : "bg-[var(--color-surface-muted)] text-[var(--color-foreground-muted)]"
              )}
            >
              {showStop ? (
                stopping ? <Loader2 className="h-4 w-4 animate-spin" /> : <Square className="h-4 w-4" />
              ) : (
                <ArrowUp className="h-4 w-4" />
              )}
            </button>
          </div>
        </div>
        <div className="mt-1.5 flex items-center justify-between gap-3 px-3 text-2xs text-[var(--color-foreground-muted)]">
          {sending && sendingHint ? (
            <span className="flex items-center gap-1.5" role="status" aria-live="polite">
              <Loader2 className="h-3 w-3 animate-spin" aria-hidden />
              {sendingHint}
            </span>
          ) : (
            <span>{disabled ? t("composer.unavailable") : t("composer.hint")}</span>
          )}
          {error ? <span className="text-[var(--color-danger)]">{error}</span> : null}
        </div>
      </div>
    </div>
  );
}
