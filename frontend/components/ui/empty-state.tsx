import type { ReactNode } from "react";

import { cn } from "@/lib/cn";

/**
 * The standard "nothing here yet" block: a soft icon disc, a title, one line of
 * explanation, and optional actions underneath.
 *
 * Centralized because the workbench has half a dozen of these (no session, no
 * artifacts, no search results, backend unreachable) and they had each grown
 * their own spacing and icon treatment.
 */
export function EmptyState({
  icon,
  title,
  description,
  children,
  className
}: {
  icon?: ReactNode;
  title: string;
  description?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center px-6 text-center", className)}>
      {icon ? (
        <div className="mb-3 flex h-11 w-11 items-center justify-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
          {icon}
        </div>
      ) : null}
      <h3 className="text-base font-semibold text-[var(--color-foreground)]">{title}</h3>
      {description ? (
        <p className="mt-1.5 max-w-sm text-sm text-[var(--color-foreground-muted)]">{description}</p>
      ) : null}
      {children ? <div className="mt-5 w-full">{children}</div> : null}
    </div>
  );
}
