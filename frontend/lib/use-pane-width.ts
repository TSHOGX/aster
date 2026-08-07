"use client";

import { useCallback, useSyncExternalStore } from "react";

/**
 * Persisted width for a resizable panel.
 *
 * The stored width lives in a tiny external store rather than component state.
 * That is what lets the value be read straight from localStorage on the client's
 * first render while the server renders `null` (the responsive default) — the
 * same trick lib/use-media-query.ts uses for the viewport. Holding it in state
 * instead would mean restoring it in an effect, i.e. a setState-in-effect and a
 * visible width jump on every load.
 *
 * Clamping is deliberately *not* stored: the bounds depend on the viewport, so
 * the raw preference is kept and clamped at read time. A window that is
 * temporarily too narrow therefore does not overwrite the user's chosen width.
 */

/** Null means "no stored preference": follow the layout's responsive default. */
type Store = {
  value: number | null;
  listeners: Set<() => void>;
  hydrated: boolean;
};

const stores = new Map<string, Store>();

function storeFor(key: string): Store {
  let store = stores.get(key);
  if (!store) {
    store = { value: null, listeners: new Set(), hydrated: false };
    stores.set(key, store);
  }
  // Read localStorage once per key, lazily. getSnapshot must return a cached
  // value (React calls it on every render and compares by identity), so the
  // parsed number is memoized here rather than re-read each call.
  if (!store.hydrated) {
    store.hydrated = true;
    try {
      const raw = Number(window.localStorage.getItem(key));
      store.value = Number.isFinite(raw) && raw > 0 ? raw : null;
    } catch {
      // Private-mode / blocked storage: fall back to the default width.
      store.value = null;
    }
  }
  return store;
}

function writeStore(key: string, value: number | null) {
  const store = storeFor(key);
  if (store.value === value) return;
  store.value = value;
  try {
    if (value === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, String(Math.round(value)));
  } catch {
    // Persistence is best-effort; the in-memory value still drives this session.
  }
  for (const listener of store.listeners) listener();
}

/** Current viewport width, tracked so the clamp follows window resizes. */
function useViewportWidth(): number {
  return useSyncExternalStore(
    (onChange) => {
      window.addEventListener("resize", onChange);
      return () => window.removeEventListener("resize", onChange);
    },
    () => window.innerWidth,
    // SSR has no viewport. Any width derived from this is corrected on hydration,
    // so it only has to be a sane desktop guess.
    () => 1440
  );
}

export type PaneWidth = {
  /** The width to render, already clamped. Null → use the CSS default. */
  width: number | null;
  /** Upper bound at the current viewport, for the resizer's aria-valuemax. */
  max: number;
  /** Sets an absolute width (clamped on read, so pass the raw value). */
  setWidth: (width: number) => void;
  /** Drops the preference and returns the panel to its responsive default. */
  reset: () => void;
};

export function usePaneWidth({
  key,
  min,
  maxFraction
}: {
  key: string;
  min: number;
  /** Ceiling as a share of the viewport width. */
  maxFraction: number;
}): PaneWidth {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const store = storeFor(key);
      store.listeners.add(onChange);
      return () => store.listeners.delete(onChange);
    },
    [key]
  );

  const stored = useSyncExternalStore(
    subscribe,
    () => storeFor(key).value,
    () => null
  );
  const viewport = useViewportWidth();
  const max = Math.max(min, Math.round(viewport * maxFraction));

  return {
    width: stored === null ? null : Math.min(Math.max(stored, min), max),
    max,
    setWidth: useCallback((width: number) => writeStore(key, width), [key]),
    reset: useCallback(() => writeStore(key, null), [key])
  };
}
