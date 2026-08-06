"use client";

import { forwardRef, type TextareaHTMLAttributes } from "react";

import { cn } from "@/lib/cn";

/**
 * Multi-line text field.
 *
 * Standalone it carries its own border and focus treatment. Inside the composer
 * it is passed `border-0`, because there the focus ring belongs to the rounded
 * bar wrapping the textarea and the send button — ringing both would draw two
 * concentric highlights around one control.
 */
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(
  ({ className, ...rest }, ref) => (
    <textarea
      ref={ref}
      className={cn(
        "w-full resize-none rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm leading-relaxed",
        "placeholder:text-[var(--color-foreground-muted)] transition-colors focus:border-[var(--color-accent)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent-soft)]",
        "disabled:opacity-60 disabled:cursor-not-allowed",
        className
      )}
      {...rest}
    />
  )
);
Textarea.displayName = "Textarea";
