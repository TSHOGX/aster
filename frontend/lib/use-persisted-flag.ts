"use client";

import { useCallback, useSyncExternalStore } from "react";

/**
 * A boolean UI preference that survives reloads.
 *
 * Same external-store shape as lib/use-pane-width.ts, and for the same reason:
 * the value has to be readable during the client's *first* render. Holding it in
 * component state and restoring it in an effect would render the default for one
 * frame and then flip — a visible flash of a section expanding itself shut.
 *
 * Only a value that differs from the default is written, so an untouched
 * preference leaves no key behind and a later change of default still reaches
 * users who never expressed a choice.
 */

type Store = {
  value: boolean;
  listeners: Set<() => void>;
  hydrated: boolean;
};

const stores = new Map<string, Store>();

function storeFor(key: string, fallback: boolean): Store {
  let store = stores.get(key);
  if (!store) {
    store = { value: fallback, listeners: new Set(), hydrated: false };
    stores.set(key, store);
  }
  // Read storage once per key. getSnapshot runs on every render and is compared
  // by identity, so it must not re-parse (or, for a boolean, re-decide) per call.
  if (!store.hydrated) {
    store.hydrated = true;
    try {
      const raw = window.localStorage.getItem(key);
      store.value = raw === null ? fallback : raw === "1";
    } catch {
      // Private mode / blocked storage: the default drives this session.
      store.value = fallback;
    }
  }
  return store;
}

function writeStore(key: string, fallback: boolean, value: boolean) {
  const store = storeFor(key, fallback);
  if (store.value === value) return;
  store.value = value;
  try {
    if (value === fallback) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value ? "1" : "0");
  } catch {
    // Persistence is best-effort; the in-memory value still drives this session.
  }
  for (const listener of store.listeners) listener();
}

/**
 * `[value, setValue]` for a persisted boolean.
 *
 * The server snapshot is `fallback`, which is safe only for content that renders
 * after mount (this workbench holds a blank screen until /api/me resolves).
 */
export function usePersistedFlag(
  key: string,
  fallback: boolean
): [boolean, (next: boolean) => void] {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const store = storeFor(key, fallback);
      store.listeners.add(onChange);
      return () => store.listeners.delete(onChange);
    },
    [key, fallback]
  );

  const value = useSyncExternalStore(
    subscribe,
    () => storeFor(key, fallback).value,
    () => fallback
  );

  const setValue = useCallback(
    (next: boolean) => writeStore(key, fallback, next),
    [key, fallback]
  );

  return [value, setValue];
}
