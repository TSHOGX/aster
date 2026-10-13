"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

// Environment views share one request lane for navigation, refresh and polling.
// A late response cannot replace a newer page or revive an unmounted view.
export function useEnvironmentQuery<T>(url: string | null, poll = false) {
  const [result, setResult] = useState<{
    url: string;
    data?: T;
    error?: string;
  }>();
  const request = useRef<AbortController | null>(null);
  const refresh = useCallback(
    async (required = false) => {
      request.current?.abort();
      if (!url) return;
      const controller = new AbortController();
      request.current = controller;
      try {
        const data = await api<T>(url, { signal: controller.signal });
        if (!controller.signal.aborted) setResult({ url, data });
      } catch (error) {
        if (!controller.signal.aborted) {
          setResult((previous) => ({
            url,
            data: previous?.url === url ? previous.data : undefined,
            error: String(error),
          }));
          if (required) throw error;
        }
      }
    },
    [url],
  );
  useEffect(() => {
    void refresh();
    return () => request.current?.abort();
  }, [refresh]);
  useEffect(() => {
    if (!poll) return;
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, 10000);
    return () => clearInterval(timer);
  }, [poll, refresh]);
  const current = result?.url === url ? result : undefined;
  return { data: current?.data, error: current?.error, refresh };
}
