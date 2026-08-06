import { cn } from "@/lib/cn";

/**
 * The product name, shared by the sidebar header, the narrow-width top bar, and
 * the login screen so the three never drift apart.
 *
 * Text only, by choice. An in-app glyph beside the name is redundant — the name
 * already identifies the product — and any glyph with its own brand color would
 * compete with the indigo accent for "the one saturated thing on screen", which
 * is what makes a UI look assembled rather than designed. Brand recognition is
 * the favicon's job (public/favicon.svg), where nothing shares the frame.
 */
export function Wordmark({
  className,
  size = "sm"
}: {
  className?: string;
  size?: "sm" | "lg";
}) {
  return (
    <span
      className={cn(
        "truncate font-semibold tracking-tight",
        size === "lg" ? "text-xl" : "text-base",
        className
      )}
    >
      BatteryGPT
    </span>
  );
}
