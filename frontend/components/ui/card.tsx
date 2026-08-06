import type { HTMLAttributes } from "react";

import { cn } from "@/lib/cn";

/**
 * A panel: hairline border, surface fill, optional lift.
 *
 * `elevation` maps to the --shadow-* scale rather than taking a raw utility, so
 * dialogs and inline panels cannot drift onto different depths. "none" is the
 * default because most cards in the workbench are docked, not floating — the
 * border alone is enough to separate them.
 */
export function Card({
  className,
  elevation = "none",
  interactive = false,
  ...rest
}: HTMLAttributes<HTMLDivElement> & {
  elevation?: "none" | "sm" | "md" | "lg" | "xl";
  // Adds hover affordances. Only for cards that are themselves a control; a card
  // that merely contains buttons should leave this off.
  interactive?: boolean;
}) {
  return (
    <div
      className={cn(
        "rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]",
        elevation === "sm" && "shadow-[var(--shadow-sm)]",
        elevation === "md" && "shadow-[var(--shadow-md)]",
        elevation === "lg" && "shadow-[var(--shadow-lg)]",
        elevation === "xl" && "shadow-[var(--shadow-xl)]",
        interactive &&
          "cursor-pointer text-left transition-colors hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-muted)]",
        className
      )}
      {...rest}
    />
  );
}
