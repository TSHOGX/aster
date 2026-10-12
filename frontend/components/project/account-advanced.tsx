"use client";
import { Select } from "@web/select";
import { useEffect, useState } from "react";
import { api, APIError, command } from "@/lib/project-api";
import type {
  ConfigurationPreferences,
  ConfigurationSettings,
} from "@web/configuration";
import { useDialog } from "./ui";
import { SharedDirectoryBrowser } from "./shared-directory-browser";
import { useT } from "@/lib/i18n";

type ProjectConfiguration = {
  project_id: string;
  title: string;
  state: string;
  suspended: boolean;
  revision: number;
  network_template_id: string;
  network_name: string;
  environment_name: string;
};
export function AccountAdvanced({
  onDirty,
}: {
  onDirty: (dirty: boolean) => void;
}) {
  const tr = useT();
  const [current, setCurrent] = useState<ConfigurationSettings | null>(null),
    [draft, setDraft] = useState<ConfigurationPreferences | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [attempt, setAttempt] = useState(0);
  const [pending, setPending] = useState<Record<string, unknown> | null>(null);
  const [projectDirty, setProjectDirty] = useState(false);
  const dirty =
    !!draft &&
    !!current &&
    JSON.stringify(draft) !== JSON.stringify(current.preferences);
  useEffect(() => {
    onDirty(dirty || !!pending || projectDirty);
    return () => onDirty(false);
  }, [dirty, pending, projectDirty, onDirty]);
  useEffect(() => {
    const controller = new AbortController();
    void api<ConfigurationSettings>("/api/me/configuration", {
      signal: controller.signal,
    })
      .then((value) => {
        if (!controller.signal.aborted) {
          setCurrent(value);
          setDraft(value.preferences);
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [attempt]);
  const save = async () => {
    if (!draft || !current) return;
    const request = pending || {
      request_id: crypto.randomUUID(),
      expected_revision: current.preferences.revision,
      network_template_id: draft.network_template_id,
      environment_template_id: draft.environment_template_id,
      show_shared_volumes: draft.show_shared_volumes,
    };
    setPending(request);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const result = await command<ConfigurationSettings>(
        "/api/me/configuration",
        request,
        "PATCH",
      );
      const latest = result;
      setCurrent(latest);
      setDraft(latest.preferences);
      setPending(null);
      setNotice(tr("Saved"));
    } catch (e) {
      if (
        e instanceof APIError &&
        e.status >= 400 &&
        e.status < 500 &&
        e.outcome?.code !== "outcome_unknown"
      )
        setPending(null);
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="advanced-settings">
      <p>
        {tr(
          "Optional settings for new projects. Existing projects keep their configuration.",
        )}
      </p>
      {error && (
        <p className="settings-error" role="alert">
          {error}
          {current && !pending && (
            <button
              disabled={busy || projectDirty}
              onClick={() => {
                setError("");
                setAttempt((value) => value + 1);
              }}
            >
              {tr("Use saved settings")}
            </button>
          )}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {!current || !draft ? (
        !error ? (
          <p role="status">{tr("Loading settings…")}</p>
        ) : (
          <button
            onClick={() => {
              setError("");
              setAttempt((v) => v + 1);
            }}
          >
            {tr("Retry")}
          </button>
        )
      ) : (
        <>
          {current.needs_admin_resolution && (
            <p role="status">
              {tr(
                "An administrator needs to finish your project configuration.",
              )}
            </p>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            <fieldset disabled={busy || !!pending || projectDirty}>
              {[
                {
                  key: "network_template_id" as const,
                  label: tr("Network for new projects"),
                  options: current.networks,
                  inherited: current.default_network,
                },
                {
                  key: "environment_template_id" as const,
                  label: tr("Environment for new projects"),
                  options: current.environments,
                  inherited: current.default_environment,
                },
              ].map((field) => (
                <label key={field.key}>
                  {field.label}
                  {
                    <Select aria-label={field.label}
                      value={draft[field.key]}
                      onChange={(e) => {
                        setDraft({ ...draft, [field.key]: e.target.value });
                        setNotice("");
                      }}
                    >
                      <option value="">
                        {tr("Use administrator default")}
                        {field.inherited ? ` · ${field.inherited.name}` : ""}
                      </option>
                      {draft[field.key] &&
                        !field.options.some(
                          (o) => o.id === draft[field.key],
                        ) && (
                          <option value={draft[field.key]} disabled>
                            {tr("Saved template is unavailable")}
                          </option>
                        )}
                      {field.options.map((o) => (
                        <option key={o.id} value={o.id}>
                          {o.name}
                        </option>
                      ))}
                    </Select>
                  }
                </label>
              ))}
              <label className="advanced-check">
                <input
                  type="checkbox"
                  checked={draft.show_shared_volumes}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      show_shared_volumes: e.target.checked,
                    })
                  }
                />
                {tr("Show shared directories in advanced settings")}
              </label>
              <p className="settings-status">
                {tr(
                  "Visibility does not change the directories available to your agent.",
                )}
              </p>
              <div className="settings-actions">
                <button className="primary" disabled={!dirty}>
                  {tr("Save settings")}
                </button>
                <button
                  type="button"
                  disabled={!dirty}
                  onClick={() => {
                    setDraft(current.preferences);
                    setNotice("");
                  }}
                >
                  {tr("Cancel")}
                </button>
                <button
                  type="button"
                  onClick={() =>
                    setDraft({
                      ...current.preferences,
                      network_template_id: "",
                      environment_template_id: "",
                      show_shared_volumes: false,
                    })
                  }
                >
                  {tr("Restore defaults")}
                </button>
              </div>
            </fieldset>
          </form>
          {pending && !busy && (
            <button onClick={() => void save()}>
              {tr("Check original request")}
            </button>
          )}
          <AdvancedProjects settings={current} onDirty={setProjectDirty} />
          <details>
            <summary>{tr("Configuration details")}</summary>
            <p>
              {tr("Network")}:{" "}
              {current.selected_network?.name || tr("Not configured")}
            </p>
            <p>
              {tr("Environment")}:{" "}
              {current.selected_environment?.name || tr("Not configured")}
            </p>
            {current.shared_volumes.map((g) => (
              <p key={g.volume.id}>
                {g.volume.name} ·{" "}
                {g.access === "read_write"
                  ? tr("Read and write")
                  : tr("Read only")}
              </p>
            ))}
            <details>
              <summary>{tr("Permission sources")}</summary>
              <ul>
                {[
                  ...current.network_default_source,
                  ...current.environment_sources,
                ].map((s, i) => (
                  <li key={`${s}-${i}`}>{s}</li>
                ))}
              </ul>
            </details>
          </details>
        </>
      )}
    </div>
  );
}
function AdvancedProjects({
  settings,
  onDirty,
}: {
  settings: ConfigurationSettings;
  onDirty: (v: boolean) => void;
}) {
  const tr = useT(),
    ask = useDialog();
  const [loaded, setLoaded] = useState(false),
    [attempt, setAttempt] = useState(0),
    [notice, setNotice] = useState("");
  const [open, setOpen] = useState(false),
    [projects, setProjects] = useState<ProjectConfiguration[]>([]),
    [selected, setSelected] = useState(""),
    [network, setNetwork] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [pending, setPending] = useState<{
      id: string;
      body: Record<string, unknown>;
    } | null>(null);
  const [fileDirty, setFileDirty] = useState(false);
  const current = projects.find((p) => p.project_id === selected);
  const dirty = !!network && network !== current?.network_template_id;
  useEffect(() => {
    onDirty(dirty || !!pending || fileDirty || busy);
    return () => onDirty(false);
  }, [dirty, pending, fileDirty, busy, onDirty]);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    void api<{ projects: ProjectConfiguration[] }>(
      "/api/me/project-configurations",
      { signal: controller.signal },
    )
      .then((r) => {
        if (!controller.signal.aborted) {
          setProjects(r.projects);
          setLoaded(true);
          setError("");
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [open, attempt]);
  const change = async () => {
    if (!current) return;
    const request = pending || {
      id: current.project_id,
      body: {
        request_id: crypto.randomUUID(),
        expected_revision: current.revision,
        network_template_id: network,
      },
    };
    setPending(request);
    setBusy(true);
    setError("");
    try {
      const result = await command<ProjectConfiguration>(
        `/api/me/project-configurations/${encodeURIComponent(request.id)}`,
        request.body,
        "PATCH",
      );
      setPending(null);
      setNetwork("");
      setProjects((items) =>
        items.map((item) =>
          item.project_id === result.project_id ? result : item,
        ),
      );
      setNotice(tr("Saved"));
    } catch (e) {
      if (
        e instanceof APIError &&
        e.status >= 400 &&
        e.status < 500 &&
        e.outcome?.code !== "outcome_unknown"
      )
        setPending(null);
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <details
      onToggle={(e) => {
        if (e.currentTarget.open) setOpen(true);
      }}
    >
      <summary>{tr("Existing projects")}</summary>
      {open && (
        <>
          {!loaded && !error && <p role="status">{tr("Loading projects…")}</p>}
          {loaded && projects.length === 0 && <p>{tr("No projects yet.")}</p>}
          {notice && <p role="status">{notice}</p>}
          <label>
            {tr("Project")}
            <Select aria-label={tr("Project")}
              disabled={busy || !!pending || fileDirty}
              value={selected}
              onChange={(e) => {
                const id = e.target.value;
                void (async () => {
                  if (
                    dirty &&
                    (await ask({
                      title: tr("Discard unsaved account changes?"),
                      choices: [
                        {
                          label: tr("Discard changes"),
                          value: "discard",
                          danger: true,
                        },
                      ],
                    })) !== "discard"
                  )
                    return;
                  setSelected(id);
                  setNetwork("");
                  setNotice("");
                })();
              }}
            >
              <option value="">{tr("Choose a project")}</option>
              {projects.map((p) => (
                <option key={p.project_id} value={p.project_id}>
                  {p.title}
                </option>
              ))}
            </Select>
          </label>
          {error && (
            <p role="alert" className="settings-error">
              {error}
              {!pending && (
                <button
                  disabled={busy || fileDirty}
                  onClick={() => {
                    setError("");
                    setAttempt((v) => v + 1);
                  }}
                >
                  {tr("Refresh")}
                </button>
              )}
            </p>
          )}
          {current && (
            <>
              <p>
                {current.environment_name} · {current.network_name}
              </p>
              <p>
                {current.suspended
                  ? tr(
                      "Paused or pausing after a configuration change. Resume when ready.",
                    )
                  : current.state}
              </p>
              {settings.networks.length > 0 && (
                <>
                  <label>
                    {tr("Network")}
                    <Select aria-label={tr("Network")}
                      disabled={busy || !!pending || fileDirty}
                      value={network || current.network_template_id}
                      onChange={(e) => setNetwork(e.target.value)}
                    >
                      {!settings.networks.some(
                        (n) => n.id === current.network_template_id,
                      ) && (
                        <option value={current.network_template_id} disabled>
                          {current.network_name} · {tr("Unavailable")}
                        </option>
                      )}
                      {settings.networks.map((n) => (
                        <option key={n.id} value={n.id}>
                          {n.name}
                        </option>
                      ))}
                    </Select>
                  </label>
                  {network && network !== current.network_template_id && (
                    <p>
                      {tr(
                        "Applying this change pauses a running project. You can resume it afterward.",
                      )}
                    </p>
                  )}
                  <button
                    disabled={
                      busy ||
                      fileDirty ||
                      (!pending &&
                        (!network || network === current.network_template_id))
                    }
                    onClick={() => void change()}
                  >
                    {pending
                      ? tr("Check original request")
                      : tr("Apply network change")}
                  </button>
                </>
              )}
              {settings.preferences.show_shared_volumes && (
                <SharedDirectoryBrowser
                  key={current.project_id}
                  project={current.project_id}
                  onDirty={setFileDirty}
                />
              )}
            </>
          )}
        </>
      )}
    </details>
  );
}
