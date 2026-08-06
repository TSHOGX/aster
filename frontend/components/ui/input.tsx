"use client";

import { forwardRef, type InputHTMLAttributes, type ReactNode } from "react";

import { cn } from "@/lib/cn";

/**
 * Single-line text field.
 *
 * Exists because the same field was being spelled out three different ways —
 * a shared `inputClass` string in the login screen, an inline set of utilities
 * around the artifact search box, and again in the share dialog — which is how
 * focus treatment and height drifted apart between them.
 */
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...rest }, ref) => (
    <input
      ref={ref}
      className={cn(
        "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-sm text-[var(--color-foreground)]",
        "placeholder:text-[var(--color-foreground-muted)]",
        "outline-none transition-colors focus-visible:border-[var(--color-accent)] focus-visible:ring-2 focus-visible:ring-[var(--color-accent-soft)]",
        "disabled:cursor-not-allowed disabled:opacity-60",
        className
      )}
      {...rest}
    />
  )
);
Input.displayName = "Input";

/**
 * An input with a leading icon (search boxes, filters). The focus ring is drawn
 * on the wrapper rather than the field, so the icon sits inside the highlight.
 */
export function InputWithIcon({
  icon,
  className,
  inputClassName,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & {
  icon: ReactNode;
  inputClassName?: string;
}) {
  return (
    <label
      className={cn(
        "flex h-8 items-center gap-2 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-background)] px-2.5 text-xs",
        "transition-colors focus-within:border-[var(--color-accent)] focus-within:ring-2 focus-within:ring-[var(--color-accent-soft)]",
        className
      )}
    >
      <span className="shrink-0 text-[var(--color-foreground-muted)]">{icon}</span>
      <input
        className={cn(
          "min-w-0 flex-1 bg-transparent outline-none placeholder:text-[var(--color-foreground-muted)]",
          inputClassName
        )}
        {...rest}
      />
    </label>
  );
}
