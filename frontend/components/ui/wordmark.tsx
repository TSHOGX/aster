import { cn } from "@/lib/cn";

/**
 * The product mark: a battery cell whose interior is a charge/discharge trace.
 * Drawn with `currentColor` and no fixed size, so it inherits the surrounding
 * text color and scales with whatever box it is placed in.
 */
export function LogoMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden
      className={cn("h-5 w-5", className)}
    >
      {/* Cell body + terminal cap. */}
      <rect
        x="2.5"
        y="6"
        width="16"
        height="12"
        rx="3"
        stroke="currentColor"
        strokeWidth="1.6"
      />
      <path
        d="M21 10.2v3.6"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
      {/* Charge trace across the cell interior. */}
      <path
        d="M5.6 14.4 8 10.4l2.4 3 2.2-4.4 2 5.4 1.4-2.2"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

/**
 * Logo + product name, shared by the sidebar header, the narrow-width top bar,
 * and the login screen so the three never drift apart.
 */
export function Wordmark({
  className,
  size = "sm"
}: {
  className?: string;
  size?: "sm" | "lg";
}) {
  return (
    <span className={cn("flex items-center gap-2", className)}>
      <LogoMark
        className={cn("shrink-0 text-[var(--color-accent)]", size === "lg" ? "h-7 w-7" : "h-5 w-5")}
      />
      <span
        className={cn(
          "truncate font-semibold tracking-tight",
          size === "lg" ? "text-xl" : "text-base"
        )}
      >
        BatteryGPT
      </span>
    </span>
  );
}
