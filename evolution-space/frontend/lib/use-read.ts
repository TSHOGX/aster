"use client";
import { useCallback, useEffect, useState } from "react";
import { api, APIError } from "./api";
type ReadState<T> = { path: string; data?: T; loading: boolean; status?: number };
export function useRead<T>(path: string, retainWhileLoading = false, retainOnUnavailable = false) {
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<ReadState<T>>({ path, loading: true });
  const refresh = useCallback(() => setNonce(value => value + 1), []);
  useEffect(() => {
    const abort = new AbortController();
    // Synchronize the current remote resource; old group data must disappear before revalidation.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setState(previous => ({ path, loading: true, data: retainWhileLoading && previous.path === path ? previous.data : undefined }));
    void api<T>(path, abort.signal).then(data => {
      if (!abort.signal.aborted) setState({ path, loading: false, data });
    }).catch((error: unknown) => {
      if (abort.signal.aborted) return;
      const status = error instanceof APIError ? error.status : 503;
      setState(previous => ({ path, loading: false, status,
        // Session refresh may preserve local editors through a transient outage.
        // Denied sessions and ordinary resource reads always discard old data.
        data: retainOnUnavailable && status >= 500 && previous.path === path ? previous.data : undefined,
      }));
    });
    return () => abort.abort();
  }, [path, nonce, retainWhileLoading, retainOnUnavailable]);
  useEffect(() => {
    window.addEventListener("focus", refresh);
    return () => window.removeEventListener("focus", refresh);
  }, [refresh]);
  return { ...(state.path === path ? state : { path, loading: true }), refresh };
}
