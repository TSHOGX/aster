"use client";
import { useT } from "@/lib/i18n";

import { useRef, useState } from "react";
import { APIError, mutate } from "@/lib/api";
import type { Impact } from "@/lib/catalog";

type Pending = { path: string; method: string; body: Record<string, unknown> };
export function useCatalogMutation() {
  const inFlight = useRef(false);
  const [refreshRequired, setRefreshRequired] = useState(false);
  const [review, setReview] = useState<{
    request: Pending;
    impact: Impact;
  } | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const preview = async (
    path: string,
    body: Record<string, unknown>,
    method = "PATCH",
  ) => {
    if (inFlight.current || pending || refreshRequired) return;
    inFlight.current = true;
    setBusy(true);
    setError("");
    setReview(null);
    try {
      const request = {
        path,
        method,
        body: { ...body, request_id: crypto.randomUUID() },
      };
      const impact = await mutate<Impact>(
        path,
        { ...request.body, preview: true },
        method,
      );
      setReview({ request, impact });
    } catch (error) {
      setError(String(error));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };
  const refresh = async (saved: () => Promise<void>) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError("");
    try {
      await saved();
      setRefreshRequired(false);
    } catch (error) {
      setError(String(error));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };
  const save = async (saved: () => Promise<void>) => {
    if (inFlight.current || refreshRequired) return;
    const request =
      pending ||
      (review
        ? {
            ...review.request,
            body: {
              ...review.request.body,
              expected_dependencies: review.impact.expected_dependencies,
            },
          }
        : null);
    if (!request) return;
    inFlight.current = true;
    setPending(request);
    setBusy(true);
    setError("");
    let committed = false;
    try {
      await mutate(request.path, request.body, request.method);
      committed = true;
      setPending(null);
      setReview(null);
      await saved();
    } catch (error) {
      if (
        committed ||
        (error instanceof APIError &&
          error.status >= 400 &&
          error.status < 500 &&
          error.code !== "outcome_unknown")
      ) {
        setPending(null);
        setReview(null);
      }
      if (committed) setRefreshRequired(true);
      setError(String(error));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };
  return {
    review,
    refreshRequired,
    refresh,
    pending,
    busy,
    error,
    preview,
    save,
    cancel: () => {
      if (!pending) setReview(null);
    },
    blocked: busy || !!pending || !!review || refreshRequired,
  };
}
export function CatalogReview({
  state,
  saved,
}: {
  state: ReturnType<typeof useCatalogMutation>;
  saved: () => Promise<void>;
}) {
  const tr = useT();
  return (
    <>
      {state.error && (
        <p className="error" role="alert">
          {tr(state.error)}
        </p>
      )}
      {state.busy && (
        <p role="status">
          {state.review || state.pending ? tr("Saving changes…") : tr("Checking configuration…")}
        </p>
      )}
      {state.refreshRequired && (
        <div className="notice" role="status">
          <p>
             {tr("Changes were saved. Reload the current data before editing again.")} </p>
          <button
            disabled={state.busy}
            onClick={() => void state.refresh(saved)}
          >
             {tr("Reload saved data")} </button>
        </div>
      )}
      {state.review && (
        <section className="panel catalog-review" aria-label={tr("Review changes")}>
          <h2>
            {state.review.request.method === "DELETE" ? tr("Review deletion") : tr("Review changes")}
          </h2>
          {state.review.request.method === "DELETE" && (
            <p role="alert">{tr("Deleting this resource cannot be undone.")}</p>
          )}
          <p>
             {tr("Existing project environments and data remain in place. Projects that lose access will be paused. Counts include existing configuration issues.")} </p>
          <p>
            {state.review.impact.projects.length}  {tr("projects require attention.")}{" "}
            {
              state.review.impact.users.filter((u) => u.needs_admin_resolution)
                .length
            }{" "}
             {tr("users need default configuration.")} </p>
          {!!state.review.impact.projects.length && (
            <ul>
              {state.review.impact.projects.map((p) => (
                <li key={p.id}>
                  {p.title} · {tr(p.state)} · {p.reason.replaceAll("_", " ")}
                </li>
              ))}
            </ul>
          )}
          <details>
            <summary>{tr("Account configuration checks")}</summary>
            <ul>
              {state.review.impact.users.map((u) => (
                <li key={u.id}>
                  {u.name}:{" "}
                  {u.needs_admin_resolution ? tr("Administrator configuration needed") : tr("Ready")}
                </li>
              ))}
            </ul>
          </details>
          <div className="form-actions">
            <button
              className="primary"
              disabled={state.busy}
              onClick={() => void state.save(saved)}
            >
              {state.review.request.method === "DELETE" ? tr("Confirm deletion") : tr("Confirm and save")}
            </button>
            <button
              disabled={state.busy || !!state.pending}
              onClick={state.cancel}
            >
               {tr("Back to editing")} </button>
          </div>
        </section>
      )}
      {state.pending && !state.busy && (
        <p role="alert">
           {tr("The save outcome is unknown.")}{" "}
          <button onClick={() => void state.save(saved)}>
             {tr("Check original request")} </button>
        </p>
      )}
    </>
  );
}
