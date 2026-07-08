"use client";

import { useEffect, useState } from "react";
import { Brain, Wrench, PenLine, Layers, RotateCw, Loader2 } from "lucide-react";

import type { ActivityPhase, ConversationActivity } from "@/components/harness-provider";
import { cn } from "@/lib/cn";

type PhaseStyle = {
  label: string;
  Icon: typeof Brain;
  tone: string;
  spin?: boolean;
};

function phaseStyle(activity: ConversationActivity): PhaseStyle {
  switch (activity.phase) {
    case "thinking":
      return { label: "Thinking", Icon: Brain, tone: "var(--color-accent)" };
    case "tool": {
      const name = activity.toolName ?? "tool";
      const label = activity.toolDetail ? `${name} · ${activity.toolDetail}` : `Running ${name}`;
      return { label, Icon: Wrench, tone: "var(--color-warning)" };
    }
    case "streaming":
      return { label: "Responding", Icon: PenLine, tone: "var(--color-accent)" };
    case "compacting":
      return { label: "Compacting context", Icon: Layers, tone: "var(--color-foreground-muted)" };
    case "retrying":
      return { label: "Retrying", Icon: RotateCw, tone: "var(--color-danger)", spin: true };
    default:
      return { label: "Working", Icon: Loader2, tone: "var(--color-accent)", spin: true };
  }
}

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds.toFixed(0)}s`;
  const m = Math.floor(seconds / 60);
  const s = Math.floor(seconds % 60);
  return `${m}m ${s.toString().padStart(2, "0")}s`;
}

function useElapsed(startedAt: number | null): string {
  const [sample, setSample] = useState<{ startedAt: number; seconds: number } | null>(null);
  useEffect(() => {
    if (startedAt === null) return;
    const id = setInterval(
      () => setSample({ startedAt, seconds: Math.max(0, (performance.now() - startedAt) / 1000) }),
      500
    );
    return () => clearInterval(id);
  }, [startedAt]);
  if (startedAt === null) return "";
  // Ignore a sample left over from a previous turn until the interval refreshes it.
  const seconds = sample && sample.startedAt === startedAt ? sample.seconds : 0;
  return formatElapsed(seconds);
}

export function ActivityIndicator({ activity }: { activity: ConversationActivity }) {
  const elapsed = useElapsed(activity.startedAt);
  if (activity.phase === "idle") return null;

  const { label, Icon, tone, spin } = phaseStyle(activity);

  return (
    <div
      className="border-t border-[var(--color-border)] bg-[var(--color-surface-muted)] px-6 py-2"
      role="status"
      aria-live="polite"
      aria-label={activityAccessibleLabel(activity.phase)}
    >
      <div className="flex items-center gap-2.5 text-[12px]">
        <Icon
          className={cn("h-3.5 w-3.5 shrink-0", spin && "animate-spin")}
          style={{ color: tone }}
          aria-hidden
        />
        <span className="min-w-0 flex-1 truncate font-medium text-[var(--color-foreground)]">
          {label}
        </span>

        <span className="flex shrink-0 items-end gap-[3px]" aria-hidden>
          {[0, 1, 2].map((i) => (
            <span
              key={i}
              className="activity-dot inline-block h-1 w-1 rounded-full"
              style={{ backgroundColor: tone }}
            />
          ))}
        </span>

        {activity.requestCount > 0 ? (
          <span
            className="shrink-0 font-mono text-[11px] text-[var(--color-foreground-muted)]"
            title={`${activity.requestCount} model request${activity.requestCount === 1 ? "" : "s"} this turn${
              activity.inflight > 0 ? `, ${activity.inflight} in flight` : ""
            }`}
          >
            {activity.inflight > 0 ? "↑↓ " : ""}
            {activity.requestCount} req
          </span>
        ) : null}

        {elapsed ? (
          <span className="shrink-0 font-mono text-[11px] tabular-nums text-[var(--color-foreground-muted)]">
            {elapsed}
          </span>
        ) : null}
      </div>
    </div>
  );
}

function activityAccessibleLabel(phase: ActivityPhase): string {
  switch (phase) {
    case "idle":
      return "Idle";
    case "thinking":
      return "Agent is thinking";
    case "tool":
      return "Agent is running a tool";
    case "streaming":
      return "Agent is responding";
    case "compacting":
      return "Compacting context";
    case "retrying":
      return "Retrying model request";
    default:
      return "Working";
  }
}
