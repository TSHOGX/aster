"use client";

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
            <summary>Source details</summary>
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
        tab === "updates" ? "Environment updates" : "Environment projects"
      }
    >
      <header className="catalog-toolbar">
        <div>
          <h3>
            {tab === "updates"
              ? "Updates for existing projects"
              : "Projects using this environment"}
          </h3>
          <p>
            Updates apply on the next start or resume. Running projects keep
            their current content.
          </p>
        </div>
        <button disabled={mutation.blocked} onClick={() => void load()}>
          Refresh
        </button>
      </header>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!page && !error && <p role="status">Loading environment updates…</p>}
      {page && (
        <>
          <div className="environment-counts">
            {Object.entries(page.counts).map(([state, count]) => (
              <span key={state} className="environment-count">
                <strong>{count}</strong>{" "}
                {environmentUpdateStates[state] || state}
              </span>
            ))}
          </div>
          {tab === "updates" ? (
            <>
              <section className="panel">
                <h3>Publish saved data versions</h3>
                {!hasPublications && (
                  <p>
                    Add an immutable data publication in Configuration before
                    publishing an update.
                  </p>
                )}
                <p>
                  Configuration revision {revision} is used for new projects.
                  Publishing separately updates compatible existing projects.
                  Only read-only data publications are included.
                </p>
                <label>
                  Release note
                  <textarea
                    value={note}
                    maxLength={4096}
                    disabled={mutation.blocked}
                    onChange={(e) => setNote(e.target.value)}
                    placeholder="What changed in this data release?"
                  />
                </label>
                <button
                  className="primary"
                  disabled={mutation.blocked || !hasPublications}
                  onClick={() => review("publish")}
                >
                  Preview update
                </button>
              </section>
              <h3>Release history</h3>
              {releases?.length === 0 && (
                <p>
                  No updates published. Saving configuration alone does not
                  update existing projects.
                </p>
              )}
              {releases?.map((release) => (
                <article key={release.id} className="panel environment-release">
                  <header>
                    <h4>Update {release.sequence}</h4>
                    <span>{release.state}</span>
                  </header>
                  <p>{release.note || "Data publication update"}</p>
                  <small>
                    {new Date(release.created_at).toLocaleString()} ·
                    Configuration revision {release.configuration_revision}
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
                      {release.state === "active"
                        ? "Stop rollout"
                        : "Continue rollout"}
                    </button>
                    <button
                      disabled={mutation.blocked || release.state === "retired"}
                      onClick={() => review("retry", release)}
                    >
                      Retry deferred projects
                    </button>
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
                          Preview rollback to these versions
                        </button>
                        <button
                          disabled={
                            mutation.blocked || release.state === "retired"
                          }
                          onClick={() => review("retire", release)}
                        >
                          Retire rollback target
                        </button>
                      </>
                    )}
                  </div>
                  {release.state === "retired" && (
                    <p>This release can no longer be used for rollback.</p>
                  )}
                </article>
              ))}
              {history.error && <p role="alert">{history.error}</p>}
              {releaseBefore !== 0 && !history.data && !history.error && (
                <p role="status">Loading release history…</p>
              )}
              <div className="form-actions">
                {releaseBefore !== 0 && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => setReleaseBefore(0)}
                  >
                    Latest releases
                  </button>
                )}
                {nextRelease && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => setReleaseBefore(nextRelease)}
                  >
                    Older releases
                  </button>
                )}
                {history.error && (
                  <button onClick={() => void history.refresh()}>
                    Retry history
                  </button>
                )}
              </div>
            </>
          ) : (
            <>
              <div className="form-actions">
                <label>
                  Status
                  <Select
                    aria-label="Status"
                    value={filter}
                    disabled={mutation.blocked}
                    onChange={(e) => {
                      setSelected([]);
                      setFilter(e.target.value);
                      setCursor("");
                    }}
                  >
                    <option value="">All projects</option>
                    {Object.entries(environmentUpdateStates).map(
                      ([value, label]) => (
                        <option key={value} value={value}>
                          {label}
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
                  Fix selected projects
                </button>
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
                  Resume following
                </button>
              </div>
              {!page.projects.length && <p>No projects match this view.</p>}
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
                    <span>{environmentUpdateStates[p.state] || p.state}</span>
                  </header>
                  <div className="environment-version-columns">
                    <section>
                      <h4>Current content</h4>
                      <MountVersions mounts={p.current} labels={p.labels} />
                    </section>
                    <section>
                      <h4>Target content</h4>
                      {p.target.length ? (
                        <MountVersions mounts={p.target} labels={p.labels} />
                      ) : (
                        <p>No published target</p>
                      )}
                    </section>
                  </div>
                  {p.reason && (
                    <details>
                      <summary>
                        Why this project is retaining its version
                      </summary>
                      <p>{p.reason}</p>
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
                      {p.policy === "fixed"
                        ? "Resume following"
                        : "Keep current versions"}
                    </button>
                    {["deferred", "failed"].includes(p.state) && (
                      <button
                        disabled={mutation.blocked}
                        onClick={() => review("retry_project", undefined, [p])}
                      >
                        Retry on next resume
                      </button>
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
                    First page
                  </button>
                )}
                {page.next_cursor && (
                  <button
                    disabled={mutation.blocked}
                    onClick={() => {
                      setSelected([]);
                      setCursor(page.next_cursor!);
                    }}
                  >
                    Next page
                  </button>
                )}
              </div>
            </>
          )}
        </>
      )}
      {mutation.error && (
        <p role="alert" className="error">
          {mutation.error}
        </p>
      )}
      {mutation.busy && <p role="status">Checking update…</p>}
      {mutation.review && (
        <section
          className="panel environment-review"
          aria-label="Review environment update"
        >
          <h3>Review environment update</h3>
          {proposed?.can_publish === false && (
            <p>
              No projects can adopt a change from this publication. Review the
              project reasons below; no empty release will be created.
            </p>
          )}
          {proposed?.counts && (
            <ul>
              {Object.entries(proposed.counts).map(([state, count]) => (
                <li key={state}>
                  {count} · {environmentUpdateStates[state] || state}
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
                Review affected projects and version differences
              </summary>
              <UpdateImpactProjects
                path={path}
                release={
                  mutation.review.request.body.action === "rollback"
                    ? String(mutation.review.request.body.release_id)
                    : undefined
                }
              />
            </details>
          )}
          <p>
            {proposed?.new_projects_only ||
              "This action controls future update attempts. Already issued operations are settled safely before another change can apply."}
          </p>
          <p>
            Running projects will not be interrupted. New-project configuration
            is not changed by this action.
          </p>
          <div className="form-actions">
            <button
              className="primary"
              disabled={mutation.busy || proposed?.can_publish === false}
              onClick={() => void mutation.save(saved)}
            >
              Confirm
            </button>
            <button disabled={mutation.busy} onClick={mutation.cancel}>
              Cancel
            </button>
          </div>
        </section>
      )}
      {mutation.pending && !mutation.review && (
        <button
          disabled={mutation.busy}
          onClick={() => void mutation.save(saved)}
        >
          Check original request
        </button>
      )}
      {mutation.refreshRequired && (
        <button onClick={() => void mutation.refresh(saved)}>
          Reload saved update
        </button>
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
  const [cursor, setCursor] = useState("");
  const {
    data: page,
    error,
    refresh,
  } = useEnvironmentQuery<Page>(
    `${path}/preview?limit=20&cursor=${encodeURIComponent(cursor)}&release_id=${encodeURIComponent(release || "")}`,
  );
  return (
    <section aria-label="Project update impact">
      {error && (
        <p role="alert">
          {error} <button onClick={() => void refresh()}>Retry</button>
        </p>
      )}
      {!page && !error && <p role="status">Loading projects…</p>}
      {page?.projects.map((p) => (
        <article key={p.project_id}>
          <h4>
            {p.title} · {environmentUpdateStates[p.state] || p.state}
          </h4>
          <div className="environment-version-columns">
            <div>
              <strong>Current</strong>
              <MountVersions mounts={p.current} labels={p.labels} />
            </div>
            <div>
              <strong>Proposed</strong>
              <MountVersions mounts={p.target} labels={p.labels} />
            </div>
          </div>
          {p.reason && <p>{p.reason}</p>}
        </article>
      ))}
      {cursor && (
        <button onClick={() => setCursor("")}>First impact page</button>
      )}
      {page?.next_cursor && (
        <button onClick={() => setCursor(page.next_cursor!)}>
          Next impact page
        </button>
      )}
    </section>
  );
}

function ReleaseMembers({ path, release }: { path: string; release: string }) {
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
      <summary>Deployment scope</summary>
      {error && (
        <p role="alert">
          {error} <button onClick={() => void refresh()}>Retry</button>
        </p>
      )}
      {open && !page && !error && (
        <p role="status">Loading deployment scope…</p>
      )}
      {page?.members.map((m) => (
        <p key={m.project_id}>
          <Link href={`/projects/${encodeURIComponent(m.project_id)}`}>
            {m.title}
          </Link>{" "}
          · {environmentUpdateStates[m.state] || m.state}
          {m.reason && <small>{m.reason}</small>}
        </p>
      ))}
      {page?.next_cursor && (
        <button onClick={() => setCursor(page.next_cursor!)}>
          More projects in this release
        </button>
      )}
      {cursor && (
        <button onClick={() => setCursor("")}>First scope page</button>
      )}
    </details>
  );
}
