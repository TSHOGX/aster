"use client";

import { useSyncExternalStore } from "react";
import type { Theme } from "@/lib/theme";

/**
 * Read the active theme straight from <html data-theme>, which the inline head
 * script sets before first paint. useSyncExternalStore keeps SSR (no attribute
 * yet → null) and the client in sync without a setState-in-effect, and the
 * MutationObserver makes consumers reactive to any theme change.
 *
 * Returns null until the attribute is known (SSR / first paint) so callers can
 * avoid flashing the wrong state.
 */
function subscribe(onChange: () => void) {
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"]
  });
  return () => observer.disconnect();
}

function getSnapshot(): Theme | null {
  const value = document.documentElement.getAttribute("data-theme");
  return value === "dark" ? "dark" : value === "light" ? "light" : null;
}

export function useTheme(): Theme | null {
  return useSyncExternalStore(subscribe, getSnapshot, () => null);
}
