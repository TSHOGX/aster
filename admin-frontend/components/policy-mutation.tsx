"use client";
import { useT } from "@/lib/i18n";

import { useEffect, useRef, useState } from "react";
import { APIError, mutate } from "@/lib/api";
import type { PolicyResult } from "@/lib/policy-models";
export type PolicyRequest = { path: string; method: string; body: Record<string, unknown> };

// Keep the exact original request after an uncertain response. The server's
// durable receipt resolves it; refreshing data never invents a replacement ID.
export function usePolicyMutation() {
  const alive = useRef(true);
  const [pending, setPending] = useState<PolicyRequest | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const submit = async (request: PolicyRequest, saved: (result: PolicyResult) => Promise<void> | void) => {
    setBusy(true); setPending(request); setError(""); setNotice("");
    try {
      const result = await mutate<PolicyResult>(request.path, request.body, request.method);
      if (!alive.current) return;
      setPending(null); setNotice(result.replayed ? "Original change confirmed." : "Saved.");
      await saved(result);
    } catch (error) {
      if (!alive.current) return;
      if (!(error instanceof APIError && error.code === "outcome_unknown")) setPending(null);
      setError(error instanceof APIError && error.code === "preview_stale" ? "The deletion impact changed. Preview deletion again before confirming."
        : error instanceof APIError && error.status === 409
        ? "The data or its dependencies changed. Reload current data, review your edits and submit again."
        : String(error));
    } finally { if (alive.current) setBusy(false); }
  };
  return { pending, busy, error, notice, submit };
}
export function MutationStatus({ state, saved }: { state: ReturnType<typeof usePolicyMutation>; saved: (result: PolicyResult) => Promise<void> | void }) {
  const tr = useT();
  return <>{state.error && <p role="alert" className="error">{tr(state.error)}</p>}{state.notice && <p role="status">{tr(state.notice)}</p>}
    {state.pending && !state.busy && <div className="notice"><p>{tr("The outcome is unconfirmed. Check the original request before making another change.")}</p><button onClick={() => void state.submit(state.pending!, saved)}>{tr("Check original request")}</button></div>}</>;
}
