"use client";
import { useT } from "@/lib/i18n";

import { Select } from "@web/select";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { ConfigurationSettings } from "@web/configuration";
import type { Template, SharedVolume } from "@/lib/catalog";
import type { Policy, GroupDetail } from "@/lib/policy-models";
import { CatalogReview, useCatalogMutation } from "./catalog-mutation";

export type Grant = {
  kind: string;
  id: string;
  access: "read_only" | "read_write";
};
export function PolicyResourceGrants({
  policy,
  saved,
  onDirty,
  disabled,
  onDraft,
}: {
  policy?: Policy;
  saved: (value: Policy) => void;
  onDirty: (v: boolean) => void;
  disabled: boolean;
  onDraft?: (grants: Grant[]) => void;
}) {
  const tr = useT();
  const [grants, setGrants] = useState<Grant[]>([]),
    [baseline, setBaseline] = useState<Grant[]>([]),
    [networks, setNetworks] = useState<Template[]>([]),
    [environments, setEnvironments] = useState<Template[]>([]),
    [volumes, setVolumes] = useState<SharedVolume[]>([]),
    [loaded, setLoaded] = useState(false),
    [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const mutation = useCatalogMutation(),
    dirty = JSON.stringify(grants) !== JSON.stringify(baseline);
  useEffect(() => {
    onDirty(dirty || mutation.blocked);
    return () => onDirty(false);
  }, [dirty, mutation.blocked, onDirty]);
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      policy
        ? api<{ resources: Grant[] }>(
            `/api/admin/policies/${encodeURIComponent(policy.id)}/resources`,
            { signal: controller.signal },
          )
        : Promise.resolve({ resources: [] as Grant[] }),
      api<{ templates: Template[] }>("/api/admin/resource-templates/network", {
        signal: controller.signal,
      }),
      api<{ templates: Template[] }>(
        "/api/admin/resource-templates/environment",
        { signal: controller.signal },
      ),
      api<{ volumes: SharedVolume[] }>("/api/admin/shared-volumes", {
        signal: controller.signal,
      }),
    ])
      .then(([g, n, e, v]) => {
        if (controller.signal.aborted) return;
        setGrants(g.resources);
        setBaseline(g.resources);
        setNetworks(n.templates);
        setEnvironments(e.templates);
        setVolumes(v.volumes);
        setLoaded(true);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [policy, attempt]);
  useEffect(() => {
    if (!policy) onDraft?.(grants);
  }, [policy, grants, onDraft]);
  const missing = environments
    .filter((e) =>
      grants.some((g) => g.kind === "environment_template" && g.id === e.id),
    )
    .flatMap((e) =>
      (e.environment?.mounts || [])
        .filter(
          (m) =>
            m.source.kind === "shared" &&
            !grants.some(
              (g) =>
                g.kind === "shared_volume" &&
                g.id === m.source.shared_volume_id,
            ),
        )
        .map((m) => ({
          id: m.source.shared_volume_id!,
          name:
            volumes.find((v) => v.id === m.source.shared_volume_id)?.name ||
            m.source.shared_volume_id,
          environment: e.name,
        })),
    );
  const selectTemplate = (kind: string, id: string) =>
    setGrants((current) => [
      ...current.filter((g) => g.kind !== kind),
      ...(id ? [{ kind, id, access: "read_only" as const }] : []),
    ]);
  return (
    <section className="panel configuration-panel">
      <h2>{tr("Resource access")}</h2>
      <p>
         {tr("Choose one network template and one environment template for this policy. Shared directories can be granted separately.")} </p>
      {error && (
        <p className="error" role="alert">
          {tr(error)}{" "}
          <button
            type="button"
            disabled={disabled || mutation.blocked}
            onClick={() => {
              setError("");
              setAttempt((value) => value + 1);
            }}
          >
             {tr("Retry loading")} </button>
        </p>
      )}
      {!loaded && !error && (
        <p role="status">{tr("Loading resource configuration…")}</p>
      )}
      <fieldset disabled={!loaded || disabled || mutation.blocked}>
        {[
          {
            kind: "network_template",
            label: "Network template",
            options: networks,
          },
          {
            kind: "environment_template",
            label: "Environment template",
            options: environments,
          },
        ].map((field) => {
          const selected = grants.filter((g) => g.kind === field.kind),
            available = field.options.filter((o) => o.enabled);
          const valid =
            selected.length === 1 &&
            available.some((o) => o.id === selected[0].id);
          return (
            <div key={field.kind}>
              <label>
                {tr(field.label)}
                <Select aria-label={field.label}
                  value={valid ? selected[0].id : ""}
                  onChange={(e) => selectTemplate(field.kind, e.target.value)}
                >
                  <option value="">{tr("No template granted")}</option>
                  {available.map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.name}
                    </option>
                  ))}
                </Select>
              </label>
              {loaded && selected.length > 0 && !valid && (
                <p role="alert">
                   {tr("The saved selection is unavailable or contains multiple templates. Choose one available template or")}{" "}
                  <button
                    type="button"
                    onClick={() => selectTemplate(field.kind, "")}
                  >
                     {tr("clear the selection")} </button>
                  .
                </p>
              )}
              {loaded && !available.length && (
                <p className="hint">
                   {tr("No enabled templates. Configure an available template in the Resource Center.")} </p>
              )}
              {valid && (
                <p className="hint">
                  {available.find((o) => o.id === selected[0].id)?.description}
                </p>
              )}
            </div>
          );
        })}
        <h3>{tr("Shared directories")}</h3>
        {volumes
          .filter((v) => v.enabled)
          .map((v) => {
            const grant = grants.find(
              (g) => g.kind === "shared_volume" && g.id === v.id,
            );
            return (
              <div key={v.id}>
                <label className="check-label">
                  <input
                    type="checkbox"
                    checked={!!grant}
                    onChange={(e) =>
                      setGrants((current) =>
                        e.target.checked
                          ? [
                              ...current,
                              {
                                kind: "shared_volume",
                                id: v.id,
                                access: "read_only",
                              },
                            ]
                          : current.filter(
                              (g) =>
                                !(g.kind === "shared_volume" && g.id === v.id),
                            ),
                      )
                    }
                  />
                  {v.name}
                </label>
                {grant && (
                  <label>
                     {tr("Access for")} {v.name}
                    <Select aria-label={tr("Access for {value0}", { value0: v.name })}
                      value={grant.access}
                      onChange={(e) =>
                        setGrants((current) =>
                          current.map((g) =>
                            g.kind === "shared_volume" && g.id === v.id
                              ? {
                                  ...g,
                                  access: e.target.value as Grant["access"],
                                }
                              : g,
                          ),
                        )
                      }
                    >
                      <option value="read_only">{tr("Read only")}</option>
                      <option value="read_write">{tr("Read and write")}</option>
                    </Select>
                  </label>
                )}
              </div>
            );
          })}
        {grants
          .filter(
            (g) =>
              g.kind === "shared_volume" &&
              !volumes.some((v) => v.id === g.id && v.enabled),
          )
          .map((g) => (
            <p role="alert" key={g.id}>
               {tr("A saved shared directory is unavailable.")}{" "}
              <button
                type="button"
                onClick={() =>
                  setGrants((current) => current.filter((v) => v !== g))
                }
              >
                 {tr("Remove unavailable grant")} </button>
            </p>
          ))}
        {missing.map((v, i) => (
          <p key={`${v.id}-${i}`} role="status">
            {v.environment}  {tr("requires access to")} {v.name}.{" "}
            <button
              type="button"
              disabled={
                !volumes.some((volume) => volume.id === v.id && volume.enabled)
              }
              onClick={() =>
                setGrants((current) =>
                  current.some(
                    (g) => g.kind === "shared_volume" && g.id === v.id,
                  )
                    ? current
                    : [
                        ...current,
                        {
                          kind: "shared_volume",
                          id: v.id,
                          access: "read_only",
                        },
                      ],
                )
              }
            >
               {tr("Add read access")} </button>
          </p>
        ))}
        {policy ? (
          <div className="form-actions">
            <button
              className="primary"
              disabled={!dirty}
              onClick={() =>
                void mutation.preview(
                  `/api/admin/policies/${encodeURIComponent(policy.id)}/resources`,
                  { expected_revision: policy.revision, resources: grants },
                  "PUT",
                )
              }
            >
               {tr("Review resource access")} </button>
            <button disabled={!dirty} onClick={() => setGrants(baseline)}>
               {tr("Discard changes")} </button>
          </div>
        ) : (
          <p className="hint">
             {tr("These selections will be saved together with the new policy.")} </p>
        )}
      </fieldset>
      {policy && (
        <CatalogReview
          state={mutation}
          saved={async () => {
            const current = await api<Policy>(
              `/api/admin/policies/${encodeURIComponent(policy.id)}`,
            );
            const g = await api<{ resources: Grant[] }>(
              `/api/admin/policies/${encodeURIComponent(policy.id)}/resources`,
            );
            setBaseline(g.resources);
            setGrants(g.resources);
            saved(current);
          }}
        />
      )}
    </section>
  );
}
export function GroupConfigurationDefaults({
  group,
  saved,
  onDirty,
  disabled,
}: {
  group: GroupDetail;
  saved: (g: GroupDetail) => void;
  onDirty: (v: boolean) => void;
  disabled: boolean;
}) {
  const tr = useT();
  const [networks, setNetworks] = useState<Template[]>([]),
    [environments, setEnvironments] = useState<Template[]>([]),
    [allowed, setAllowed] = useState<Grant[]>([]),
    [loaded, setLoaded] = useState(false),
    [network, setNetwork] = useState(group.network_template_id || ""),
    [environment, setEnvironment] = useState(
      group.environment_template_id || "",
    ),
    [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const mutation = useCatalogMutation(),
    dirty =
      network !== (group.network_template_id || "") ||
      environment !== (group.environment_template_id || "");
  useEffect(() => {
    onDirty(dirty || mutation.blocked);
    return () => onDirty(false);
  }, [dirty, mutation.blocked, onDirty]);
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      api<{ templates: Template[] }>("/api/admin/resource-templates/network", {
        signal: controller.signal,
      }),
      api<{ templates: Template[] }>(
        "/api/admin/resource-templates/environment",
        { signal: controller.signal },
      ),
      Promise.all(
        group.policy_ids.map((id) =>
          api<{ resources: Grant[] }>(
            `/api/admin/policies/${encodeURIComponent(id)}/resources`,
            { signal: controller.signal },
          ),
        ),
      ),
    ])
      .then(([n, e, g]) => {
        if (controller.signal.aborted) return;
        setNetworks(n.templates);
        setEnvironments(e.templates);
        setAllowed(g.flatMap((v) => v.resources));
        setLoaded(true);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [group.policy_ids, attempt]);
  return (
    <section className="panel configuration-panel">
      <h2>{tr("Default configuration")}</h2>
      <p>
         {tr("Members inherit these defaults. Conflicting defaults across groups require an administrator selection on the user account.")} </p>
      {error && (
        <p className="error" role="alert">
          {tr(error)}{" "}
          <button
            type="button"
            disabled={disabled || mutation.blocked}
            onClick={() => {
              setError("");
              setAttempt((value) => value + 1);
            }}
          >
             {tr("Retry loading")} </button>
        </p>
      )}
      {!loaded && !error && (
        <p role="status">{tr("Loading resource configuration…")}</p>
      )}
      <fieldset disabled={!loaded || disabled || mutation.blocked}>
        {[
          {
            label: "Network",
            kind: "network_template",
            value: network,
            set: setNetwork,
            options: networks,
          },
          {
            label: "Environment",
            kind: "environment_template",
            value: environment,
            set: setEnvironment,
            options: environments,
          },
        ].map((field) => (
          <label key={field.kind}>
            {tr(field.label)}
            <Select aria-label={field.label}
              value={field.value}
              onChange={(e) => field.set(e.target.value)}
            >
              <option value="">{tr("No group default")}</option>
              {field.options
                .filter(
                  (o) =>
                    allowed.some(
                      (g) => g.kind === field.kind && g.id === o.id,
                    ) || o.id === field.value,
                )
                .map((o) => (
                  <option
                    value={o.id}
                    key={o.id}
                    disabled={
                      !o.enabled ||
                      !allowed.some(
                        (g) => g.kind === field.kind && g.id === o.id,
                      )
                    }
                  >
                    {o.name}
                    {!o.enabled ? tr(" (disabled)") : ""}
                  </option>
                ))}
            </Select>
          </label>
        ))}
        <div className="form-actions">
          <button
            disabled={!dirty}
            className="primary"
            onClick={() =>
              void mutation.preview(
                `/api/admin/groups/${encodeURIComponent(group.id)}/configuration-defaults`,
                {
                  expected_revision: group.revision,
                  network_template_id: network,
                  environment_template_id: environment,
                },
                "PUT",
              )
            }
          >
             {tr("Review defaults")} </button>
          <button
            disabled={!dirty}
            onClick={() => {
              setNetwork(group.network_template_id || "");
              setEnvironment(group.environment_template_id || "");
            }}
          >
             {tr("Discard changes")} </button>
        </div>
      </fieldset>
      <CatalogReview
        state={mutation}
        saved={async () =>
          saved(
            await api<GroupDetail>(
              `/api/admin/groups/${encodeURIComponent(group.id)}`,
            ),
          )
        }
      />
    </section>
  );
}
export function UserConfiguration({
  id,
  onDirty,
}: {
  id: string;
  onDirty: (v: boolean) => void;
}) {
  const tr = useT();
  const [value, setValue] = useState<ConfigurationSettings | null>(null),
    [network, setNetwork] = useState(""),
    [environment, setEnvironment] = useState(""),
    [error, setError] = useState("");
  const mutation = useCatalogMutation();
  const dirty =
    !!value &&
    (network !== value.administrator_defaults.network_template_id ||
      environment !== value.administrator_defaults.environment_template_id);
  const accept = (next: ConfigurationSettings) => {
    setValue(next);
    setNetwork(next.administrator_defaults.network_template_id);
    setEnvironment(next.administrator_defaults.environment_template_id);
    setError("");
  };
  useEffect(() => {
    onDirty(dirty || mutation.blocked);
    return () => onDirty(false);
  }, [dirty, mutation.blocked, onDirty]);
  const load = async () => {
    accept(
      await api<ConfigurationSettings>(
        `/api/admin/users/${encodeURIComponent(id)}/configuration`,
      ),
    );
  };
  useEffect(() => {
    const controller = new AbortController();
    void api<ConfigurationSettings>(
      `/api/admin/users/${encodeURIComponent(id)}/configuration`,
      { signal: controller.signal },
    )
      .then((next) => {
        if (!controller.signal.aborted) accept(next);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [id]);
  return (
    <section>
      <header className="page-heading">
        <div>
          <h1>{tr("Effective configuration")}</h1>
          <p>
             {tr("Review defaults, resolve group conflicts, and see where access comes from.")} </p>
        </div>
      </header>
      {error && (
        <p className="error" role="alert">
          {tr(error)}
          <button
            type="button"
            disabled={mutation.blocked}
            onClick={() => void load().catch((e) => setError(String(e)))}
          >
             {tr("Reload configuration")} </button>
        </p>
      )}
      {value ? (
        <>
          <section className="panel configuration-panel">
            <h2>
              {value.needs_admin_resolution ? tr("Administrator selection required") : tr("Ready for new projects")}
            </h2>
            <p>{tr("Network:")} {value.default_network?.name || tr("Not resolved")}</p>
            <p>
               {tr("Environment:")} {value.default_environment?.name || tr("Not resolved")}
            </p>
            <p>{tr("These defaults do not migrate existing projects.")}</p>
          </section>
          <form
            className="account-form"
            onSubmit={(e) => {
              e.preventDefault();
              void mutation.preview(
                `/api/admin/users/${encodeURIComponent(id)}/configuration-defaults`,
                {
                  expected_revision: value.default_revision,
                  network_template_id: network,
                  environment_template_id: environment,
                },
                "PUT",
              );
            }}
          >
            <fieldset disabled={mutation.blocked}>
              {[
                {
                  name: "Network default",
                  value: network,
                  set: setNetwork,
                  options: value.networks,
                },
                {
                  name: "Environment default",
                  value: environment,
                  set: setEnvironment,
                  options: value.environments,
                },
              ].map((f) => (
                <label key={f.name}>
                  {f.name}
                  <Select aria-label={f.name}
                    value={f.value}
                    onChange={(e) => f.set(e.target.value)}
                  >
                    <option value="">{tr("Inherit group defaults")}</option>
                    {f.value && !f.options.some((o) => o.id === f.value) && (
                      <option value={f.value} disabled>
                         {tr("Saved template is unavailable")} </option>
                    )}
                    {f.options.map((o) => (
                      <option key={o.id} value={o.id}>
                        {o.name}
                      </option>
                    ))}
                  </Select>
                </label>
              ))}
              <div className="form-actions">
                <button className="primary" disabled={!dirty}>
                   {tr("Review administrator defaults")} </button>
                <button
                  type="button"
                  disabled={!dirty}
                  onClick={() => accept(value)}
                >
                   {tr("Discard changes")} </button>
              </div>
            </fieldset>
          </form>
          <section className="panel configuration-panel">
            <h2>{tr("Authorization sources")}</h2>
            {[...value.networks, ...value.environments].map((v) => (
              <details key={v.id}>
                <summary>{v.name}</summary>
                <p>{v.description}</p>
                <ul>
                  {v.sources.map((s) => (
                    <li key={s}>{s}</li>
                  ))}
                </ul>
              </details>
            ))}
            <h3>{tr("Shared directories")}</h3>
            {value.shared_volumes.map((g) => (
              <p key={g.volume.id}>
                {g.volume.name} ·{" "}
                {g.access === "read_write" ? tr("Read and write") : tr("Read only")}
              </p>
            ))}
          </section>
          <CatalogReview state={mutation} saved={load} />
        </>
      ) : (
        !error && <p role="status">{tr("Loading effective configuration…")}</p>
      )}
    </section>
  );
}
