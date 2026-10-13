"use client";
import { intlLocale } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";


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
  const tr = useT();
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
        <h2>{tr("Environment updates")}</h2>
        <button disabled={mutation.blocked} onClick={() => void load()}>
           {tr("Refresh updates")} </button>
      </header>
      {error && (
        <p role="alert" className="error">
          {tr(error)}
        </p>
      )}
      {!detail && !error && <p role="status">{tr("Loading environment details…")}</p>}
      {detail && (
        <>
          <p>
            {detail.policy === "fixed" ? tr("Current versions are fixed by an administrator.") : tr("Follows explicitly published updates on the next start or resume.")}
          </p>
          <p>
             {tr("Status:")}{" "}
            {tr(environmentUpdateStates[detail.state] ||
              detail.state.replaceAll("_", " "))}
          </p>
          <div className="environment-version-columns">
            <section>
              <h3>{tr("Current content")}</h3>
              <MountVersions mounts={detail.current} labels={detail.labels} />
            </section>
            <section>
              <h3>{tr("Target content")}</h3>
              {detail.target.length ? (
                <MountVersions mounts={detail.target} labels={detail.labels} />
              ) : (
                <p>{tr("No published target")}</p>
              )}
            </section>
          </div>
          {detail.reason && (
            <details>
              <summary>{tr("Update diagnostic")}</summary>
              <p>{tr(detail.reason)}</p>
            </details>
          )}
          <div className="form-actions">
            <button
              disabled={mutation.blocked}
              onClick={() =>
                review(detail.policy === "fixed" ? "follow" : "fix")
              }
            >
              {detail.policy === "fixed" ? tr("Resume following") : tr("Keep current versions")}
            </button>
            {["failed", "deferred"].includes(detail.state) && (
              <button
                disabled={mutation.blocked}
                onClick={() => review("retry_project")}
              >
                 {tr("Retry on next resume")} </button>
            )}
          </div>
          <details>
            <summary>{tr("Execution history")}</summary>
            {!detail.executions.length && <p>{tr("No update attempts yet.")}</p>}
            {detail.executions.map((e) => (
              <article key={e.id}>
                <h4>
                  {tr(e.state)} · {new Date(e.updated_at).toLocaleString(intlLocale())}
                </h4>
                <MountVersions mounts={e.after} />
                {e.reason && <p>{tr(e.reason)}</p>}
              </article>
            ))}
            {cursor && (
              <button disabled={mutation.blocked} onClick={() => setCursor("")}>
                 {tr("Latest attempts")} </button>
            )}
            {detail.next_cursor && (
              <button
                disabled={mutation.blocked}
                onClick={() => setCursor(detail.next_cursor!)}
              >
                 {tr("Older attempts")} </button>
            )}
          </details>
        </>
      )}
      {mutation.error && (
        <p role="alert" className="error">
          {tr(mutation.error)}
        </p>
      )}
      {mutation.review && (
        <section aria-label={tr("Review project environment policy")}>
          <h3>{tr("Review project update policy")}</h3>
          <p>
             {tr("This changes future update attempts. Running work is not interrupted; an already issued update will finish safely.")} </p>
          <button
            disabled={mutation.busy}
            onClick={() => void mutation.save(() => load(true))}
          >
             {tr("Confirm policy")} </button>
          <button disabled={mutation.busy} onClick={mutation.cancel}>
             {tr("Cancel")} </button>
        </section>
      )}
      {mutation.pending && !mutation.review && (
        <button
          disabled={mutation.busy}
          onClick={() => void mutation.save(() => load(true))}
        >
           {tr("Check original request")} </button>
      )}
      {mutation.refreshRequired && (
        <button onClick={() => void mutation.refresh(() => load(true))}>
           {tr("Reload saved policy")} </button>
      )}
    </section>
  );
}
