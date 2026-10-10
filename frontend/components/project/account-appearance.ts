"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError, command } from "@/lib/project-api";
import {
  applyAppearance, appearanceKey, bindAppearance, cacheAppearance, cachedAppearance,
  DEFAULT_APPEARANCE, type Appearance, type Preferences,
} from "@/lib/theme";

type State = {
  saved: Preferences;
  preview: Appearance;
  changes: Partial<Appearance>;
  pending: boolean;
  loaded: boolean;
  error: string;
  conflict: boolean;
};
export function useAccountAppearance(id: string) {
  const [state, setState] = useState<State>(() => ({
    saved: cachedAppearance(id) || { ...DEFAULT_APPEARANCE, revision: 0 },
    preview: cachedAppearance(id) || DEFAULT_APPEARANCE,
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
    cacheAppearance(id, saved);
    const previous = current.current;
    const dirty = Object.keys(previous.changes).length > 0;
    publish({ ...previous, saved, preview: { ...saved, ...previous.changes }, loaded: true,
      error: dirty ? previous.error : "", conflict: dirty && previous.conflict });
  }, [id, publish]);
  const refresh = useCallback(async () => {
    if (current.current.pending) return;
    const turn = ++generation.current;
    try {
      let saved = await api<Preferences>("/api/me/preferences");
      if (!alive.current || turn !== generation.current) return;
      if (saved.revision === 0) {
        const cached = cachedAppearance(id) || DEFAULT_APPEARANCE;
        try {
          saved = await command<Preferences>("/api/me/preferences", {
            expected_revision: 0, theme_mode: cached.theme_mode, accent: cached.accent,
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
  }, [id, accept, publish]);
  const save = useCallback(async (change: Partial<Appearance> = {}) => {
    if (current.current.pending || !current.current.loaded) return;
    const changes = { ...current.current.changes, ...change };
    if (!Object.keys(changes).length) return;
    const turn = ++generation.current;
    publish({ ...current.current, changes, preview: { ...current.current.saved, ...changes }, pending: true, error: "", conflict: false });
    try {
      const saved = await command<Preferences>("/api/me/preferences", { expected_revision: current.current.saved.revision, ...changes }, "PATCH");
      if (!alive.current || turn !== generation.current) return;
      cacheAppearance(id, saved);
      publish({ saved, preview: saved, changes: {}, loaded: true, pending: false, error: "", conflict: false });
    } catch (error) {
      if (!alive.current || turn !== generation.current) return;
      const conflict = error instanceof APIError && error.status === 409;
      // Resolve an uncertain reply or stale revision by reading, never by silently
      // replaying a write. The user's changed fields remain a deliberate preview.
      let saved = current.current.saved;
      try { saved = await api<Preferences>("/api/me/preferences"); } catch {}
      if (!alive.current || turn !== generation.current) return;
      cacheAppearance(id, saved);
      publish({ ...current.current, saved, preview: { ...saved, ...changes }, pending: false, error: String(error), conflict });
    }
  }, [id, publish]);
  const revert = useCallback(() => {
    if (current.current.pending) return;
    publish({ ...current.current, changes: {}, preview: current.current.saved, error: "", conflict: false });
  }, [publish]);
  const invalidate = useCallback(() => { generation.current++; }, []);
  useEffect(() => {
    alive.current = true;
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
export type AccountAppearance = ReturnType<typeof useAccountAppearance>;
