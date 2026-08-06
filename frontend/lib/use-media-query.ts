"use client";

import { useCallback, useSyncExternalStore } from "react";

/**
 * Subscribes to a CSS media query and returns whether it currently matches.
 *
 * Used for layout decisions that CSS alone cannot express — below the
 * three-column threshold the side panels become overlay drawers, which changes
 * which elements render, not just how they are styled.
 *
 * The server snapshot is `false`: there is no viewport during SSR, so the
 * desktop layout is assumed and corrected on hydration. Anything gated on this
 * must therefore be safe to mount one frame late.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const list = window.matchMedia(query);
      list.addEventListener("change", onChange);
      return () => list.removeEventListener("change", onChange);
    },
    [query]
  );

  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(query).matches,
    () => false
  );
}

/** Below this width the shell drops to one column with overlay side panels. */
export const NARROW_QUERY = "(max-width: 1023px)";
