"use client";

import { useEffect, useState } from "react";
import { useEnvironmentQuery } from "@/lib/use-environment-query";
import { useCatalogMutation } from "./catalog-mutation";
import {
  MountVersions,
  environmentUpdateStates,
  type EnvironmentProject,
} from "./environment-updates";
import type { Mount } from "@/lib/runtime-models";

type Detail = EnvironmentProject & {
  executions: {
    id: string;
    state: string;
    reason: string;
    updated_at: string;
    before: Mount[];
    after: Mount[];
  }[];
  next_cursor?: string;
};
export function ProjectEnvironment({
  project,
  onDirty,
}: {
  project: string;
  onDirty: (value: boolean) => void;
}) {
  const [cursor, setCursor] = useState("");
  const mutation = useCatalogMutation();
  const path = `/api/admin/projects/${encodeURIComponent(project)}/environment`;
  const {
    data: detail,
    error,
    refresh: load,
  } = useEnvironmentQuery<Detail>(
    `${path}?cursor=${encodeURIComponent(cursor)}`,
  );
  useEffect(() => {
    onDirty(mutation.blocked);
    return () => onDirty(false);
  }, [mutation.blocked, onDirty]);
  const review = (action: string) => {
    if (detail)
      void mutation.preview(
        `/api/admin/environments/${encodeURIComponent(detail.environment_id)}/updates`,
        { action, project_ids: [project], expected_revision: detail.revision },
        "POST",
      );
  };
  return (
    <section className="panel">
      <header className="catalog-toolbar">
        <h2>Environment updates</h2>
        <button disabled={mutation.blocked} onClick={() => void load()}>
          Refresh updates
        </button>
      </header>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!detail && !error && <p role="status">Loading environment details…</p>}
      {detail && (
        <>
          <p>
            {detail.policy === "fixed"
              ? "Current versions are fixed by an administrator."
              : "Follows explicitly published updates on the next start or resume."}
          </p>
          <p>
            Status:{" "}
            {environmentUpdateStates[detail.state] ||
              detail.state.replaceAll("_", " ")}
          </p>
          <div className="environment-version-columns">
            <section>
              <h3>Current content</h3>
              <MountVersions mounts={detail.current} labels={detail.labels} />
            </section>
            <section>
              <h3>Target content</h3>
              {detail.target.length ? (
                <MountVersions mounts={detail.target} labels={detail.labels} />
              ) : (
                <p>No published target</p>
              )}
            </section>
          </div>
          {detail.reason && (
            <details>
              <summary>Update diagnostic</summary>
              <p>{detail.reason}</p>
            </details>
          )}
          <div className="form-actions">
            <button
              disabled={mutation.blocked}
              onClick={() =>
                review(detail.policy === "fixed" ? "follow" : "fix")
              }
            >
              {detail.policy === "fixed"
                ? "Resume following"
                : "Keep current versions"}
            </button>
            {["failed", "deferred"].includes(detail.state) && (
              <button
                disabled={mutation.blocked}
                onClick={() => review("retry_project")}
              >
                Retry on next resume
              </button>
            )}
          </div>
          <details>
            <summary>Execution history</summary>
            {!detail.executions.length && <p>No update attempts yet.</p>}
            {detail.executions.map((e) => (
              <article key={e.id}>
                <h4>
                  {e.state} · {new Date(e.updated_at).toLocaleString()}
                </h4>
                <MountVersions mounts={e.after} />
                {e.reason && <p>{e.reason}</p>}
              </article>
            ))}
            {cursor && (
              <button disabled={mutation.blocked} onClick={() => setCursor("")}>
                Latest attempts
              </button>
            )}
            {detail.next_cursor && (
              <button
                disabled={mutation.blocked}
                onClick={() => setCursor(detail.next_cursor!)}
              >
                Older attempts
              </button>
            )}
          </details>
        </>
      )}
      {mutation.error && (
        <p role="alert" className="error">
          {mutation.error}
        </p>
      )}
      {mutation.review && (
        <section aria-label="Review project environment policy">
          <h3>Review project update policy</h3>
          <p>
            This changes future update attempts. Running work is not
            interrupted; an already issued update will finish safely.
          </p>
          <button
            disabled={mutation.busy}
            onClick={() => void mutation.save(() => load(true))}
          >
            Confirm policy
          </button>
          <button disabled={mutation.busy} onClick={mutation.cancel}>
            Cancel
          </button>
        </section>
      )}
      {mutation.pending && !mutation.review && (
        <button
          disabled={mutation.busy}
          onClick={() => void mutation.save(() => load(true))}
        >
          Check original request
        </button>
      )}
      {mutation.refreshRequired && (
        <button onClick={() => void mutation.refresh(() => load(true))}>
          Reload saved policy
        </button>
      )}
    </section>
  );
}
