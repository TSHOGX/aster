"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowUp } from "lucide-react";

import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/cn";
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
      <div className="pointer-events-auto w-full max-w-3xl">
        {topSlot}
        <div
          className={cn(
            "flex gap-2 border border-[var(--color-border)] bg-[var(--color-surface)] shadow-lg shadow-black/5 backdrop-blur transition-all duration-200 ease-out",
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
        <div className="mt-1.5 flex items-center justify-between px-3 text-[11px] text-[var(--color-foreground-muted)]">
          <span>{disabled ? "Session is busy or ended." : "Enter to send · Shift+Enter for newline"}</span>
          {error ? <span className="text-[var(--color-danger)]">{error}</span> : null}
        </div>
      </div>
    </div>
  );
}
