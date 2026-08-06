import { cn } from "@/lib/cn";

/**
 * A placeholder block for content that is still loading.
 *
 * Shimmer comes from `.skeleton-pulse` in globals.css, which is disabled under
 * prefers-reduced-motion.
 */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      className={cn("skeleton-pulse rounded-[var(--radius-sm)] bg-[var(--color-surface-muted)]", className)}
      aria-hidden
    />
  );
}

/**
 * Loading state for the conversation: alternating user/assistant shapes at the
 * widths real turns tend to have, so the transition to content does not jump.
 */
export function ConversationSkeleton() {
  return (
    <div className="mx-auto w-full max-w-[var(--measure)] px-4 sm:px-6" role="status" aria-label="Loading conversation">
      {[0, 1].map((turn) => (
        <div key={turn}>
          <div className="flex justify-end py-4">
            <Skeleton className="h-10 w-[45%] rounded-[var(--radius-xl)]" />
          </div>
          <div className="flex gap-3 py-4">
            <Skeleton className="h-7 w-7 shrink-0 rounded-full" />
            <div className="flex-1 space-y-2">
              <Skeleton className="h-3 w-24" />
              <Skeleton className="h-3 w-full" />
              <Skeleton className="h-3 w-[92%]" />
              <Skeleton className="h-3 w-[70%]" />
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

/** Loading state for the session list. */
export function SessionListSkeleton() {
  return (
    <div className="space-y-1 px-2 py-1" role="status" aria-label="Loading sessions">
      {[0, 1, 2, 3, 4].map((row) => (
        <Skeleton key={row} className="h-9 w-full rounded-[var(--radius)]" />
      ))}
    </div>
  );
}
