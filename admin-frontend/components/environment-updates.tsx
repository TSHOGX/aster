"use client";
import { formatQuantity, intlLocale } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";


import Link from "next/link";
import { useEffect, useState } from "react";
import { Select } from "@web/select";
import { useEnvironmentQuery } from "@/lib/use-environment-query";
import { useCatalogMutation } from "./catalog-mutation";
import type { Mount } from "@/lib/runtime-models";

export type PublicationLabels = Record<
  string,
  { library_name: string; version_label: string; version_note: string }
>;
export type EnvironmentRelease = {
  labels?: PublicationLabels;
  id: string;
  sequence: number;
  revision: number;
  configuration_revision: number;
  state: string;
  note: string;
  created_at: string;
  mounts: Mount[];
};
export type EnvironmentProject = {
  labels?: PublicationLabels;
  project_id: string;
  title: string;
  environment_id: string;
  policy: string;
  revision: number;
  target_id: string;
  applied_id: string;
  state: string;
  reason: string;
  attempts: number;
  updated_at: string;
  current: Mount[];
  target: Mount[];
};
type Page = {
  releases: EnvironmentRelease[];
  projects: EnvironmentProject[];
  counts: Record<string, number>;
  next_cursor?: string;
  release_next_cursor?: number;
};
type Review = {
  can_publish?: boolean;
  release?: EnvironmentRelease;
  counts?: Record<string, number>;
  new_projects_only?: string;
};
export const environmentUpdateStates: Record<string, string> = {
  current: "Up to date",
  pending: "Waiting for next start or resume",
  applying: "Applying",
  unknown: "Verifying previous attempt",
  deferred: "Using previous version · will retry",
  failed: "Using previous version · review needed",
  fixed: "Fixed by administrator",
  incompatible: "Incompatible configuration",
  unauthorized: "Access unavailable",
};
export function MountVersions({
  mounts,
  labels,
}: {
  mounts: Mount[];
  labels?: PublicationLabels;
}) {
  const tr = useT();
  return (
    <ul className="environment-version-list">
      {mounts.map((m) => (
        <li key={m.key}>
          <span>{m.destination}</span>
          <strong>
            {labels?.[`${m.source.artifact_id}@${m.source.version}`]
              ?.version_label || m.source.version}
          </strong>
          {labels?.[`${m.source.artifact_id}@${m.source.version}`]
            ?.library_name && (
            <small>
              {
                labels[`${m.source.artifact_id}@${m.source.version}`]
                  .library_name
              }
            </small>
          )}
          <details>
            <summary>{tr("Source details")}</summary>
            {labels?.[`${m.source.artifact_id}@${m.source.version}`]
              ?.version_note && (
              <p>
                {
                  labels[`${m.source.artifact_id}@${m.source.version}`]
                    .version_note
                }
              </p>
            )}
            <small className="identifier">
              {m.source.artifact_id}
              <br />
              {m.source.digest}
            </small>
          </details>
        </li>
      ))}
    </ul>
  );
}
export function EnvironmentUpdates({
  environment,
  revision,
  tab,
  hasPublications,
  onDirty,
}: {
  environment: string;
  revision: number;
  tab: "updates" | "projects";
  hasPublications: boolean;
  onDirty: (value: boolean) => void;
}) {
  const tr = useT();
  const [note, setNote] = useState(""),
    [cursor, setCursor] = useState(""),
    [filter, setFilter] = useState(""),
    [selected, setSelected] = useState<string[]>([]),
    [releaseBefore, setReleaseBefore] = useState(0);
  const mutation = useCatalogMutation();
  const path = `/api/admin/environments/${encodeURIComponent(environment)}/updates`;
  const {
    data: page,
    error,
    refresh,
  } = useEnvironmentQuery<Page>(
    `${path}?limit=30&cursor=${encodeURIComponent(cursor)}&state=${encodeURIComponent(filter)}`,
    !mutation.blocked && tab === "projects",
  );
  const history = useEnvironmentQuery<{
    releases: EnvironmentRelease[];
    next_cursor?: number;
  }>(releaseBefore ? `${path}/history?before=${releaseBefore}` : null);
  const releases = releaseBefore ? history.data?.releases : page?.releases;
  const nextRelease = releaseBefore
    ? history.data?.next_cursor
    : page?.release_next_cursor;
  const load = async (required = false) => {
    setSelected([]);
    setReleaseBefore(0);
    await refresh(required);
  };
  useEffect(() => {
    onDirty(mutation.blocked || note !== "");
    return () => onDirty(false);
  }, [mutation.blocked, note, onDirty]);
  const review = (
    action: string,
    release?: EnvironmentRelease,
    projects?: EnvironmentProject[],
  ) =>
    void mutation.preview(
      path,
      {
        action,
        expected_revision:
          release?.revision ??
          (projects?.length === 1 ? projects[0].revision : revision),
        release_id: release?.id,
        note,
        project_ids: projects?.map((p) => p.project_id),
      },
      "POST",
    );
  const proposed = mutation.review?.impact.proposed as Review | undefined;
  const saved = async () => {
    setNote("");
    await load(true);
  };
  return (
    <section
      className="environment-updates"
      aria-label={
        tab === "updates" ? tr("Environment updates") : tr("Environment projects")
      }
    >
      <header className="catalog-toolbar">
        <div>
          <h3>
            {tab === "updates" ? tr("Updates for existing projects") : tr("Projects using this environment")}
          </h3>
          <p>
             {tr("Updates apply on the next start or resume. Running projects keep their current content.")} </p>
        </div>
        <button disabled={mutation.blocked} onClick={() => void load()}>
           {tr("Refresh")} </button>
      </header>
      {error && (
        <p role="alert" className="error">
          {tr(error)}
        </p>
      )}
      {!page && !error && <p role="status">{tr("Loading environment updates…")}</p>}
      {page && (
        <>
          <div className="environment-counts">
            {Object.entries(page.counts).map(([state, count]) => (
              <span key={state} className="environment-count">
                <strong>{formatQuantity(count)}</strong>{" "}
                {tr(environmentUpdateStates[state] || state)}
              </span>
            ))}
          </div>
          {tab === "updates" ? (
            <>
              <section className="panel">
                <h3>{tr("Publish saved data versions")}</h3>
                {!hasPublications && (
                  <p>
                     {tr("Add an immutable data publication in Configuration before publishing an update.")} </p>
                )}
                <p>
                   {tr("Configuration revision")} {formatQuantity(revision)}  {tr("is used for new projects. Publishing separately updates compatible existing projects. Only read-only data publications are included.")} </p>
                <label>
                   {tr("Release note")} <textarea
                    value={note}
                    maxLength={4096}
                    disabled={mutation.blocked}
                    onChange={(e) => setNote(e.target.value)}
                    placeholder={tr("What changed in this data release?")}
                  />
                </label>
                <button
                  className="primary"
                  disabled={mutation.blocked || !hasPublications}
                  onClick={() => review("publish")}
                >
                   {tr("Preview update")} </button>
              </section>
              <h3>{tr("Release history")}</h3>
              {releases?.length === 0 && (
                <p>
                   {tr("No updates published. Saving configuration alone does not update existing projects.")} </p>
              )}
              {releases?.map((release) => (
                <article key={release.id} className="panel environment-release">
                  <header>
                    <h4>{tr("Update")} {formatQuantity(release.sequence)}</h4>
                    <span>{tr(release.state)}</span>
                  </header>
                  <p>{release.note || tr("Data publication update")}</p>
                  <small>
                    {new Date(release.created_at).toLocaleString(intlLocale())}  {tr("· Configuration revision")} {formatQuantity(release.configuration_revision)}
                  </small>
                  <MountVersions
                    mounts={release.mounts}
                    labels={release.labels}
                  />
                  <ReleaseMembers path={path} release={release.id} />
                  <div className="form-actions">
                    <button
                      disabled={
                        mutation.blocked ||
                        !["active", "stopped"].includes(release.state)
                      }
                      onClick={() =>
                        review(
                          release.state === "active" ? "stop" : "continue",
                          release,
                        )
                      }
                    >
                      {release.state === "active" ? tr("Stop rollout") : tr("Continue rollout")}
                    </button>
                    <button
                      disabled={mutation.blocked || release.state === "retired"}
                      onClick={() => review("retry", release)}
                    >
                       {tr("Retry deferred projects")} </button>
                    {["superseded", "retired"].includes(release.state) && (
                      <>
                        <button
                          disabled={
                            mutation.blocked || release.state === "retired"
                          }
                          onClick={() =>
                            void mutation.preview(
                              path,
                              {
                                action: "rollback",
                                expected_revision: revision,
                                release_id: release.id,
                                note,
                              },
                              "POST",
                            )
                          }
                        >
                           {tr("Preview rollback to these versions")} </button>
                        <button
                          disabled={
                            mutation.blocked || release.state === "retired"
                          }
                          onClick={() => review("retire", release)}
                        >
                           {tr("Retire rollback target")} </button>
                      </>
                    )}
                  </div>
                  {release.state === "retired" && (
                    <p>{tr("This release can no longer be used for rollback.")}</p>
                  )}
                </article>
              ))}
              {history.error && <p role="alert">{tr(history.error)}</p>}
              {releaseBefore !== 0 && !history.data && !history.error && (
                <p role="status">{tr("Loading release history…")}</p>
              )}
              <div className="form-actions">
                {releaseBefore !== 0 && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => setReleaseBefore(0)}
                  >
                     {tr("Latest releases")} </button>
                )}
                {nextRelease && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => setReleaseBefore(nextRelease)}
                  >
                     {tr("Older releases")} </button>
                )}
                {history.error && (
                  <button onClick={() => void history.refresh()}>
                     {tr("Retry history")} </button>
                )}
              </div>
            </>
          ) : (
            <>
              <div className="form-actions">
                <label>
                   {tr("Status")} <Select
                    aria-label={tr("Status")}
                    value={filter}
                    disabled={mutation.blocked}
                    onChange={(e) => {
                      setSelected([]);
                      setFilter(e.target.value);
                      setCursor("");
                    }}
                  >
                    <option value="">{tr("All projects")}</option>
                    {Object.entries(environmentUpdateStates).map(
                      ([value, label]) => (
                        <option key={value} value={value}>
                          {tr(label)}
                        </option>
                      ),
                    )}
                  </Select>
                </label>
                <button
                  disabled={
                    mutation.blocked ||
                    !page.projects.some((p) => selected.includes(p.project_id))
                  }
                  onClick={() =>
                    review(
                      "fix",
                      undefined,
                      page.projects.filter((p) =>
                        selected.includes(p.project_id),
                      ),
                    )
                  }
                >
                   {tr("Fix selected projects")} </button>
                <button
                  disabled={
                    mutation.blocked ||
                    !page.projects.some((p) => selected.includes(p.project_id))
                  }
                  onClick={() =>
                    review(
                      "follow",
                      undefined,
                      page.projects.filter((p) =>
                        selected.includes(p.project_id),
                      ),
                    )
                  }
                >
                   {tr("Resume following")} </button>
              </div>
              {!page.projects.length && <p>{tr("No projects match this view.")}</p>}
              {page.projects.map((p) => (
                <article
                  key={p.project_id}
                  className="panel environment-project"
                >
                  <header>
                    <label>
                      <input
                        type="checkbox"
                        checked={selected.includes(p.project_id)}
                        disabled={mutation.blocked}
                        onChange={(e) =>
                          setSelected((ids) =>
                            e.target.checked
                              ? [...ids, p.project_id]
                              : ids.filter((id) => id !== p.project_id),
                          )
                        }
                      />
                      <Link
                        href={`/projects/${encodeURIComponent(p.project_id)}`}
                      >
                        {p.title}
                      </Link>
                    </label>
                    <span>{tr(environmentUpdateStates[p.state] || p.state)}</span>
                  </header>
                  <div className="environment-version-columns">
                    <section>
                      <h4>{tr("Current content")}</h4>
                      <MountVersions mounts={p.current} labels={p.labels} />
                    </section>
                    <section>
                      <h4>{tr("Target content")}</h4>
                      {p.target.length ? (
                        <MountVersions mounts={p.target} labels={p.labels} />
                      ) : (
                        <p>{tr("No published target")}</p>
                      )}
                    </section>
                  </div>
                  {p.reason && (
                    <details>
                      <summary>
                         {tr("Why this project is retaining its version")} </summary>
                      <p>{tr(p.reason)}</p>
                    </details>
                  )}
                  <div className="form-actions">
                    <button
                      disabled={mutation.blocked}
                      onClick={() =>
                        review(
                          p.policy === "fixed" ? "follow" : "fix",
                          undefined,
                          [p],
                        )
                      }
                    >
                      {p.policy === "fixed" ? tr("Resume following") : tr("Keep current versions")}
                    </button>
                    {["deferred", "failed"].includes(p.state) && (
                      <button
                        disabled={mutation.blocked}
                        onClick={() => review("retry_project", undefined, [p])}
                      >
                         {tr("Retry on next resume")} </button>
                    )}
                  </div>
                </article>
              ))}
              <div className="form-actions">
                {cursor && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => {
                      setSelected([]);
                      setCursor("");
                    }}
                  >
                     {tr("First page")} </button>
                )}
                {page.next_cursor && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => {
                      setSelected([]);
                      setCursor(page.next_cursor!);
                    }}
                  >
                     {tr("Next page")} </button>
                )}
              </div>
            </>
          )}
        </>
      )}
      {mutation.error && (
        <p role="alert" className="error">
          {tr(mutation.error)}
        </p>
      )}
      {mutation.busy && <p role="status">{tr("Checking update…")}</p>}
      {mutation.review && (
        <section
          className="panel environment-review"
          aria-label={tr("Review environment update")}
        >
          <h3>{tr("Review environment update")}</h3>
          {proposed?.can_publish === false && (
            <p>
               {tr("No projects can adopt a change from this publication. Review the project reasons below; no empty release will be created.")} </p>
          )}
          {proposed?.counts && (
            <ul>
              {Object.entries(proposed.counts).map(([state, count]) => (
                <li key={state}>
                  {formatQuantity(count)} · {tr(environmentUpdateStates[state] || state)}
                </li>
              ))}
            </ul>
          )}
          {proposed?.release && (
            <MountVersions
              mounts={proposed.release.mounts}
              labels={proposed.release.labels}
            />
          )}
          {proposed?.release && (
            <details>
              <summary>
                 {tr("Review affected projects and version differences")} </summary>
              <UpdateImpactProjects
                path={path}
                release={
                  mutation.review.request.body.action === "rollback" ? String(mutation.review.request.body.release_id) : undefined
                }
              />
            </details>
          )}
          <p>
            {proposed?.new_projects_only || tr("This action controls future update attempts. Already issued operations are settled safely before another change can apply.")}
          </p>
          <p>
             {tr("Running projects will not be interrupted. New-project configuration is not changed by this action.")} </p>
          <div className="form-actions">
            <button
              className="primary"
              disabled={mutation.busy || proposed?.can_publish === false}
              onClick={() => void mutation.save(saved)}
            >
               {tr("Confirm")} </button>
            <button disabled={mutation.busy} onClick={mutation.cancel}>
               {tr("Cancel")} </button>
          </div>
        </section>
      )}
      {mutation.pending && !mutation.review && (
        <button
          disabled={mutation.busy}
          onClick={() => void mutation.save(saved)}
        >
           {tr("Check original request")} </button>
      )}
      {mutation.refreshRequired && (
        <button onClick={() => void mutation.refresh(saved)}>
           {tr("Reload saved update")} </button>
      )}
    </section>
  );
}

function UpdateImpactProjects({
  path,
  release,
}: {
  path: string;
  release?: string;
}) {
  const tr = useT();
  const [cursor, setCursor] = useState("");
  const {
    data: page,
    error,
    refresh,
  } = useEnvironmentQuery<Page>(
    `${path}/preview?limit=20&cursor=${encodeURIComponent(cursor)}&release_id=${encodeURIComponent(release || "")}`,
  );
  return (
    <section aria-label={tr("Project update impact")}>
      {error && (
        <p role="alert">
          {tr(error)} <button onClick={() => void refresh()}>{tr("Retry")}</button>
        </p>
      )}
      {!page && !error && <p role="status">{tr("Loading projects…")}</p>}
      {page?.projects.map((p) => (
        <article key={p.project_id}>
          <h4>
            {p.title} · {tr(environmentUpdateStates[p.state] || p.state)}
          </h4>
          <div className="environment-version-columns">
            <div>
              <strong>{tr("Current")}</strong>
              <MountVersions mounts={p.current} labels={p.labels} />
            </div>
            <div>
              <strong>{tr("Proposed")}</strong>
              <MountVersions mounts={p.target} labels={p.labels} />
            </div>
          </div>
          {p.reason && <p>{tr(p.reason)}</p>}
        </article>
      ))}
      {cursor && (
        <button onClick={() => setCursor("")}>{tr("First impact page")}</button>
      )}
      {page?.next_cursor && (
        <button onClick={() => setCursor(page.next_cursor!)}>
           {tr("Next impact page")} </button>
      )}
    </section>
  );
}

function ReleaseMembers({ path, release }: { path: string; release: string }) {
  const tr = useT();
  const [open, setOpen] = useState(false),
    [cursor, setCursor] = useState("");
  const {
    data: page,
    error,
    refresh,
  } = useEnvironmentQuery<{
    members: {
      project_id: string;
      title: string;
      initial_state: string;
      state: string;
      reason: string;
    }[];
    next_cursor?: string;
  }>(
    open
      ? `${path}/${encodeURIComponent(release)}?cursor=${encodeURIComponent(cursor)}`
      : null,
  );
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>{tr("Deployment scope")}</summary>
      {error && (
        <p role="alert">
          {tr(error)} <button onClick={() => void refresh()}>{tr("Retry")}</button>
        </p>
      )}
      {open && !page && !error && (
        <p role="status">{tr("Loading deployment scope…")}</p>
      )}
      {page?.members.map((m) => (
        <p key={m.project_id}>
          <Link href={`/projects/${encodeURIComponent(m.project_id)}`}>
            {m.title}
          </Link>{" "}
          · {tr(environmentUpdateStates[m.state] || m.state)}
          {m.reason && <small>{tr(m.reason)}</small>}
        </p>
      ))}
      {page?.next_cursor && (
        <button onClick={() => setCursor(page.next_cursor!)}>
           {tr("More projects in this release")} </button>
      )}
      {cursor && (
        <button onClick={() => setCursor("")}>{tr("First scope page")}</button>
      )}
    </details>
  );
}
