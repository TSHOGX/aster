"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { preferenceAPI as api, PreferenceAPIError as APIError, preferenceCommand as command } from "./preference-api";
import {
  applyAppearance, appearanceKey, bindAppearance, cacheAppearance, cachedAppearance,
  DEFAULT_APPEARANCE, type PreferenceValues, type Preferences,
} from "./appearance";

type State = {
  saved: Preferences;
  preview: PreferenceValues;
  changes: Partial<PreferenceValues>;
  pending: boolean;
  loaded: boolean;
  error: string;
  conflict: boolean;
};
export function useAccountPreferences(id: string | undefined, verifyAccount = false) {
  const [state, setState] = useState<State>(() => ({
    saved: cachedAppearance(id || "") || { ...DEFAULT_APPEARANCE, revision: 0 },
    preview: cachedAppearance(id || "") || DEFAULT_APPEARANCE,
    changes: {}, pending: false, loaded: false, error: "", conflict: false,
  }));
  const current = useRef(state);
  const generation = useRef(0);
  const alive = useRef(false);
  const publish = useCallback((value: State) => {
    if (!alive.current) return;
    current.current = value;
    applyAppearance(value.preview);
    setState(value);
  }, []);
  const accept = useCallback((saved: Preferences) => {
    cacheAppearance(id!, saved);
    const previous = current.current;
    const dirty = Object.keys(previous.changes).length > 0;
    publish({ ...previous, saved, preview: { ...saved, ...previous.changes }, loaded: true,
      error: dirty ? previous.error : "", conflict: dirty && previous.conflict });
  }, [id, publish]);
  const verify = useCallback(async () => {
    if (verifyAccount) {
      const identity = await api<{ id: string }>("/api/auth/session");
      if (identity.id !== id) throw new APIError(401, "Sign in with the same account to save settings.");
    }
  }, [id, verifyAccount]);
  const refresh = useCallback(async () => {
    if (!id || current.current.pending) return;
    const turn = ++generation.current;
    try {
      await verify();
      if (!alive.current || turn !== generation.current) return;
      let saved = await api<Preferences>("/api/me/preferences");
      if (!alive.current || turn !== generation.current) return;
      if (saved.revision === 0) {
        const cached = cachedAppearance(id || "") || DEFAULT_APPEARANCE;
        try {
          saved = await command<Preferences>("/api/me/preferences", {
            expected_revision: 0, theme_mode: cached.theme_mode, accent: cached.accent, language: cached.language,
          }, "PATCH");
        } catch (error) {
          if (!(error instanceof APIError && error.status === 409)) throw error;
          saved = await api<Preferences>("/api/me/preferences");
        }
      }
      if (alive.current && turn === generation.current) accept(saved);
    } catch (error) {
      if (alive.current && turn === generation.current)
        publish({ ...current.current, error: String(error), loaded: true });
    }
  }, [id, accept, publish, verify]);
  const save = useCallback(async (change: Partial<PreferenceValues> = {}) => {
    if (!id || current.current.pending || !current.current.loaded) return;
    const changes = { ...current.current.changes, ...change };
    if (!Object.keys(changes).length) return;
    const turn = ++generation.current;
    publish({ ...current.current, changes, preview: { ...current.current.saved, ...changes }, pending: true, error: "", conflict: false });
    try {
      await verify();
      if (!alive.current || turn !== generation.current) return;
      const saved = await command<Preferences>("/api/me/preferences", { expected_revision: current.current.saved.revision, ...changes }, "PATCH");
      if (!alive.current || turn !== generation.current) return;
      cacheAppearance(id!, saved);
      publish({ saved, preview: saved, changes: {}, loaded: true, pending: false, error: "", conflict: false });
    } catch (error) {
      if (!alive.current || turn !== generation.current) return;
      const conflict = error instanceof APIError && error.status === 409;
      // Resolve an uncertain reply or stale revision by reading, never by silently
      // replaying a write. The user's changed fields remain a deliberate preview.
      let saved = current.current.saved;
      try { await verify(); saved = await api<Preferences>("/api/me/preferences"); } catch {}
      if (!alive.current || turn !== generation.current) return;
      cacheAppearance(id!, saved);
      publish({ ...current.current, saved, preview: { ...saved, ...changes }, pending: false, error: String(error), conflict });
    }
  }, [id, publish, verify]);
  const revert = useCallback(() => {
    if (current.current.pending) return;
    publish({ ...current.current, changes: {}, preview: current.current.saved, error: "", conflict: false });
  }, [publish]);
  const invalidate = useCallback(() => { generation.current++; }, []);
  useEffect(() => {
    if (!id) return;
    alive.current = true;
    const saved = cachedAppearance(id) || { ...DEFAULT_APPEARANCE, revision: 0 };
    const initial = { saved, preview: saved, changes: {}, pending: false, loaded: false, error: "", conflict: false };
    current.current = initial;
    setState(initial);
    bindAppearance(id);
    void refresh();
    const system = window.matchMedia("(prefers-color-scheme: dark)");
    const changed = () => applyAppearance(current.current.preview);
    const storage = (event: StorageEvent) => { if (event.key === appearanceKey(id)) void refresh(); };
    const visible = () => { if (!document.hidden) void refresh(); };
    system.addEventListener("change", changed);
    window.addEventListener("storage", storage);
    document.addEventListener("visibilitychange", visible);
    return () => {
      alive.current = false;
      invalidate();
      system.removeEventListener("change", changed);
      window.removeEventListener("storage", storage);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [id, refresh, invalidate]);
  return { ...state, save, revert, refresh };
}
export type AccountPreferencesController = ReturnType<typeof useAccountPreferences>;
