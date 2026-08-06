import type { HTMLAttributes } from "react";

import { cn } from "@/lib/cn";

export function Badge({ className, ...rest }: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-[1px] text-2xs font-medium text-[var(--color-foreground-muted)]",
        className
      )}
      {...rest}
    />
  );
}

type Tone = "running" | "idle" | "ready" | "failed" | "muted";
type DotProps = { tone?: Tone; className?: string };

function dotColor(tone: Tone): string {
  switch (tone) {
    case "running":
      return "bg-[var(--color-accent)] animate-pulse-dot";
    case "idle":
      return "bg-[var(--color-success)]";
    case "ready":
      return "bg-[var(--color-warning)]";
    case "failed":
      return "bg-[var(--color-danger)]";
    default:
      return "bg-[var(--color-border-strong)]";
  }
}

export function StatusDot({ tone = "muted", className }: DotProps) {
  return <span className={cn("inline-block h-2 w-2 rounded-full shrink-0", dotColor(tone), className)} />;
}

/**
 * Dot plus label in one tinted chip.
 *
 * A bare dot next to free-floating grey text made status read as two unrelated
 * scraps; binding them into a chip with a tinted ground makes the state legible
 * at a glance, and gives the label a color that is not just "muted".
 */
export function StatusPill({
  tone = "muted",
  children,
  className,
  title
}: DotProps & { children: React.ReactNode; title?: string }) {
  const ink =
    tone === "running"
      ? "text-[var(--color-accent)] bg-[var(--color-accent-soft)]"
      : tone === "idle"
      ? "text-[var(--color-success)] bg-[var(--color-surface-muted)]"
      : tone === "ready"
      ? "text-[var(--color-warning)] bg-[var(--color-surface-muted)]"
      : tone === "failed"
      ? "text-[var(--color-danger)] bg-[var(--color-danger-soft)]"
      : "text-[var(--color-foreground-muted)] bg-[var(--color-surface-muted)]";
  return (
    <span
      title={title}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-full px-2 py-0.5 text-2xs font-medium",
        ink,
        className
      )}
    >
      <StatusDot tone={tone} className="h-1.5 w-1.5" />
      {children}
    </span>
  );
}

export function statusTone(status: string): DotProps["tone"] {
  switch (status) {
    case "running_active":
    case "checkpointing":
      return "running";
    case "running_idle":
    case "checkpointed":
      return "idle";
    case "created":
      return "ready";
    case "failed":
      return "failed";
    default:
      return "muted";
  }
}
