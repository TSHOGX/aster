"use client";
import { useCallback, useEffect, useState } from "react";
import { api, APIError } from "./api";
type ReadState<T> = { path: string; data?: T; loading: boolean; status?: number };
export function useRead<T>(path: string, retainWhileLoading = false) {
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
      if (!abort.signal.aborted) setState({ path, loading: false, status: error instanceof APIError ? error.status : 503 });
    });
    return () => abort.abort();
  }, [path, nonce, retainWhileLoading]);
  useEffect(() => {
    window.addEventListener("focus", refresh);
    return () => window.removeEventListener("focus", refresh);
  }, [refresh]);
  return { ...(state.path === path ? state : { path, loading: true }), refresh };
}
