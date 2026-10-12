"use client";
import { Select } from "@web/select";
import Link from "next/link";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Network, SharedVolume, Template } from "@/lib/catalog";
import {
  artifactKey,
  environmentForRelease,
  type EnvironmentBody,
  type Mount,
  type OptionsPage,
  type RuntimeOption,
} from "@/lib/runtime-models";
import { CatalogReview, useCatalogMutation } from "./catalog-mutation";

type Section = "network" | "environment" | "sandboxes" | "volumes";
const sections: { id: Section; label: string; description: string }[] = [
  {
    id: "network",
    label: "Network templates",
    description:
      "Reusable outbound rules. Assign access through a policy, then choose group defaults.",
  },
  {
    id: "environment",
    label: "Environment templates",
    description:
      "Combine a sandbox with project storage and shared directories.",
  },
  {
    id: "sandboxes",
    label: "Sandboxes",
    description: "Registered runtime releases and their availability.",
  },
  {
    id: "volumes",
    label: "Volumes",
    description:
      "Shared directories and versioned initialization sources have separate lifecycles.",
  },
];
async function inventory(kind: "sandbox" | "volume", signal?: AbortSignal) {
  const options: RuntimeOption[] = [];
  let cursor = "",
    maxMounts = "0";
  do {
    const page = await api<OptionsPage>(
      `/api/admin/runtime/options?kind=${kind}&limit=100&cursor=${encodeURIComponent(cursor)}`,
      { signal },
    );
    options.push(...page.options);
    maxMounts = page.max_mounts;
    cursor = page.next_cursor || "";
  } while (cursor);
  return { options, max_mounts: maxMounts };
}
export function ResourceCenter({
  section: raw = "network",
  onDirty,
}: {
  section?: string;
  onDirty: (value: boolean) => void;
}) {
  const section = sections.find((s) => s.id === raw) || sections[0];
  return (
    <section>
      <header className="page-heading">
        <div>
          <h1>Resource Center</h1>
          <p>{section.description}</p>
        </div>
      </header>
      <nav className="catalog-tabs" aria-label="Resource categories">
        {sections.map((s) => (
          <Link
            key={s.id}
            aria-current={section.id === s.id ? "page" : undefined}
            href={`/resources/${s.id}`}
          >
            {s.label}
          </Link>
        ))}
      </nav>
      {section.id === "sandboxes" ? (
        <Inventory kind="sandbox" />
      ) : section.id === "volumes" ? (
        <>
          <SharedVolumes onDirty={onDirty} />
          <details className="panel">
            <summary>Initialization sources</summary>
            <Inventory kind="volume" />
          </details>
        </>
      ) : (
        <TemplateCatalog key={section.id} kind={section.id} onDirty={onDirty} />
      )}
    </section>
  );
}
function Inventory({ kind }: { kind: "sandbox" | "volume" }) {
  const [items, setItems] = useState<RuntimeOption[] | null>(null),
    [error, setError] = useState(""),
    [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void inventory(kind, controller.signal)
      .then((page) => setItems(page.options))
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [kind, attempt]);
  return (
    <section className="panel">
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <button
        onClick={() => {
          setError("");
          setAttempt((v) => v + 1);
        }}
      >
        Refresh availability
      </button>
      {!items ? (
        <p role="status">Loading inventory…</p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Resource</th>
                <th>Status</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr key={artifactKey(item.reference)}>
                  <td>
                    {item.reference.id}
                    <small>{item.reference.version}</small>
                  </td>
                  <td>{item.enabled ? item.availability : "Disabled"}</td>
                  <td>
                    <details>
                      <summary>View constraints</summary>
                      {item.roles && <p>Roles: {item.roles.join(", ")}</p>}
                      {item.required_publications?.map((p) => (
                        <p key={p.role}>
                          {p.role}: {p.destination}
                        </p>
                      ))}
                      <code>{item.reference.digest}</code>
                    </details>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {items?.length === 0 && (
        <p>
          No registered resources. Register a release or publish initialization
          content with the operator tools.
        </p>
      )}
    </section>
  );
}
function TemplateCatalog({
  kind,
  onDirty,
}: {
  kind: "network" | "environment";
  onDirty: (value: boolean) => void;
}) {
  const [items, setItems] = useState<Template[] | null>(null),
    [editing, setEditing] = useState<{
      value?: Template;
      duplicate?: boolean;
    } | null>(null),
    [error, setError] = useState("");
  const load = async () => {
    const result = await api<{ templates: Template[] }>(
      `/api/admin/resource-templates/${kind}`,
    );
    setItems(result.templates);
    setError("");
  };
  useEffect(() => {
    const controller = new AbortController();
    void api<{ templates: Template[] }>(
      `/api/admin/resource-templates/${kind}`,
      { signal: controller.signal },
    )
      .then((r) => setItems(r.templates))
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [kind]);
  if (editing)
    return (
      <TemplateEditor
        kind={kind}
        initial={editing.value}
        duplicate={editing.duplicate}
        onDirty={onDirty}
        close={() => setEditing(null)}
        saved={async () => {
          await load();
          setEditing(null);
        }}
      />
    );
  return (
    <>
      <div className="catalog-toolbar">
        <h2>
          {kind === "network" ? "Network templates" : "Environment templates"}
        </h2>
        <button className="primary" onClick={() => setEditing({})}>
          Create template
        </button>
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
          <button onClick={() => void load().catch((e) => setError(String(e)))}>
            Retry
          </button>
        </p>
      )}
      {!items && !error && <p role="status">Loading templates…</p>}
      <div className="catalog-grid">
        {items?.map((item) => (
          <article key={item.id} className="panel catalog-card">
            <div>
              <h3>{item.name}</h3>
              <span className="catalog-status">
                {item.builtin ? "Built-in · " : ""}
                {item.enabled ? "Enabled" : "Disabled"}
              </span>
            </div>
            <p>{item.description || "No description"}</p>
            <p className="hint">
              {kind === "network"
                ? `${item.network?.direct.length || 0} outbound rules`
                : `${item.environment?.mounts.length || 0} mounts`}{" "}
              · {item.usage_count} policy references
            </p>
            <small>
              Revision {item.revision} · Updated{" "}
              {new Date(item.updated_at).toLocaleString()}
            </small>
            <div className="form-actions">
              <button onClick={() => setEditing({ value: item })}>
                {item.builtin ? "View" : "Edit"}
              </button>
              <button
                onClick={() => setEditing({ value: item, duplicate: true })}
              >
                Duplicate
              </button>
            </div>
          </article>
        ))}
      </div>
      {items?.length === 0 && (
        <section className="panel">
          <h3>No templates yet</h3>
          <p>
            Create a template, authorize it in a policy, and choose it as a
            group default.
          </p>
        </section>
      )}
    </>
  );
}
function TemplateEditor({
  kind,
  initial,
  duplicate,
  onDirty,
  close,
  saved,
}: {
  kind: "network" | "environment";
  initial?: Template;
  duplicate?: boolean;
  onDirty: (value: boolean) => void;
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [name, setName] = useState(
      initial ? initial.name + (duplicate ? " copy" : "") : "",
    ),
    [description, setDescription] = useState(initial?.description || ""),
    [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [network, setNetwork] = useState<Network>(
    initial?.network || { direct: [], resolvers: [] },
  );
  const [environment, setEnvironment] = useState<EnvironmentBody | null>(
    initial?.environment || null,
  );
  const [releases, setReleases] = useState<RuntimeOption[]>([]),
    [sources, setSources] = useState<RuntimeOption[]>([]),
    [volumes, setVolumes] = useState<SharedVolume[]>([]),
    [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [maxMounts, setMaxMounts] = useState("0");
  const [inventoryLoaded, setInventoryLoaded] = useState(
    kind !== "environment",
  );
  const mutation = useCatalogMutation(),
    readOnly = !!initial?.builtin && !duplicate;
  const changed =
    !readOnly &&
    (duplicate ||
      !initial ||
      name !== initial.name ||
      description !== initial.description ||
      enabled !== initial.enabled ||
      JSON.stringify(network) !==
        JSON.stringify(initial.network || { direct: [], resolvers: [] }) ||
      (kind === "environment" &&
        JSON.stringify(environment) !== JSON.stringify(initial.environment)));
  useEffect(() => {
    onDirty(!!changed || mutation.blocked);
    return () => onDirty(false);
  }, [changed, mutation.blocked, onDirty]);
  useEffect(() => {
    if (kind !== "environment") return;
    const controller = new AbortController();
    void Promise.all([
      inventory("sandbox", controller.signal),
      inventory("volume", controller.signal),
      api<{ volumes: SharedVolume[] }>("/api/admin/shared-volumes", {
        signal: controller.signal,
      }),
    ])
      .then(([r, s, v]) => {
        if (controller.signal.aborted) return;
        setReleases(r.options);
        setMaxMounts(r.max_mounts);
        setSources(s.options);
        setVolumes(v.volumes);
        const release = r.options.find(
          (value) =>
            value.enabled &&
            !["unavailable", "retired", "other_store", "incompatible"].includes(
              value.availability,
            ),
        );
        setEnvironment(
          initial?.environment ||
            (release ? environmentForRelease(release, s.options) : null),
        );
        setInventoryLoaded(true);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [kind, initial, attempt]);
  const path = `/api/admin/resource-templates/${kind}${initial && !duplicate ? `/${encodeURIComponent(initial.id)}` : ""}`;
  const leave = () => {
    if (!changed || window.confirm("Discard unsaved template changes?"))
      close();
  };
  const required =
    releases.find(
      (release) =>
        environment &&
        artifactKey(release.reference) ===
          artifactKey(environment.sandbox_release),
    )?.required_publications || [];
  const patchMount = (key: string, patch: Partial<Mount>) =>
    setEnvironment((e) =>
      e
        ? {
            ...e,
            mounts: e.mounts.map((m) =>
              m.key === key ? { ...m, ...patch } : m,
            ),
          }
        : e,
    );
  return (
    <section>
      <header className="page-heading">
        <div>
          <h2>
            {duplicate
              ? "Duplicate template"
              : initial
                ? initial.name
                : "Create template"}
          </h2>
          <p>
            {readOnly
              ? "Built-in templates are immutable. Duplicate this template to customize it."
              : "Configure capabilities, review the impact, then save."}
          </p>
        </div>
        <button disabled={mutation.blocked} onClick={leave}>
          Back to templates
        </button>
      </header>
      {error && (
        <p className="error" role="alert">
          {error}{" "}
          <button
            disabled={mutation.blocked}
            onClick={() => {
              setError("");
              setAttempt((value) => value + 1);
            }}
          >
            Retry loading
          </button>
        </p>
      )}
      {inventoryLoaded && kind === "environment" && !environment && (
        <p role="status">
          No available sandbox releases. Register a release before creating an
          environment template.
        </p>
      )}
      {!inventoryLoaded && !error && (
        <p role="status">Loading environment resources…</p>
      )}
      <form
        className="account-form catalog-form"
        onSubmit={(e) => {
          e.preventDefault();
          void mutation.preview(
            path,
            {
              expected_revision: initial && !duplicate ? initial.revision : 0,
              name,
              description,
              enabled,
              ...(kind === "network"
                ? {
                    network: {
                      ...network,
                      resolvers: network.resolvers
                        .map((value) => value.trim())
                        .filter(Boolean),
                    },
                  }
                : { environment }),
            },
            initial && !duplicate ? "PATCH" : "POST",
          );
        }}
      >
        <fieldset disabled={readOnly || mutation.blocked || !inventoryLoaded}>
          <legend>Identity and purpose</legend>
          <label>
            Name
            <input
              required
              maxLength={256}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label>
            Description
            <textarea
              maxLength={1024}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Explain when this template should be used"
            />
          </label>
          <label className="check-label">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            Available for authorization
          </label>
          {kind === "network" ? (
            <>
              <h3>Outbound connections</h3>
              <p className="hint">
                No rules means no additional direct connections. Platform
                service routes are configured separately.
              </p>
              {network.direct.map((rule, index) => (
                <div className="catalog-rule" key={index}>
                  <label>
                    IPv4 CIDR
                    <input
                      required
                      value={rule.cidr}
                      placeholder="192.0.2.0/24"
                      onChange={(e) =>
                        setNetwork((n) => ({
                          ...n,
                          direct: n.direct.map((r, i) =>
                            i === index ? { ...r, cidr: e.target.value } : r,
                          ),
                        }))
                      }
                    />
                  </label>
                  <label>
                    Port
                    <input
                      type="number"
                      min={1}
                      max={65535}
                      required
                      value={rule.port}
                      onChange={(e) =>
                        setNetwork((n) => ({
                          ...n,
                          direct: n.direct.map((r, i) =>
                            i === index
                              ? { ...r, port: Number(e.target.value) }
                              : r,
                          ),
                        }))
                      }
                    />
                  </label>
                  <label>
                    Protocol
                    <Select aria-label="Protocol"
                      value={rule.protocol}
                      onChange={(e) =>
                        setNetwork((n) => ({
                          ...n,
                          direct: n.direct.map((r, i) =>
                            i === index
                              ? {
                                  ...r,
                                  protocol: e.target.value as "tcp" | "udp",
                                }
                              : r,
                          ),
                        }))
                      }
                    >
                      <option value="tcp">TCP</option>
                      <option value="udp">UDP</option>
                    </Select>
                  </label>
                  <button
                    type="button"
                    onClick={() =>
                      setNetwork((n) => ({
                        ...n,
                        direct: n.direct.filter((_, i) => i !== index),
                      }))
                    }
                  >
                    Remove
                  </button>
                </div>
              ))}
              <button
                type="button"
                disabled={network.direct.length >= 256}
                onClick={() =>
                  setNetwork((n) => ({
                    ...n,
                    direct: [
                      ...n.direct,
                      { cidr: "", port: 443, protocol: "tcp" },
                    ],
                  }))
                }
              >
                Add destination
              </button>
              <label>
                DNS resolvers
                <textarea
                  value={network.resolvers.join("\n")}
                  placeholder="One IPv4 address per line"
                  onChange={(e) =>
                    setNetwork((n) => ({
                      ...n,
                      resolvers: e.target.value.split("\n"),
                    }))
                  }
                />
              </label>
              <p className="hint">
                Use 0.0.0.0/0 for public IPv4 destinations on the selected port.
                DNS permits UDP/53; add an explicit rule if TCP DNS is required.
              </p>
            </>
          ) : (
            environment && (
              <>
                <h3>Sandbox</h3>
                <label>
                  Release
                  <Select aria-label="Release"
                    value={artifactKey(environment.sandbox_release)}
                    onChange={(e) => {
                      const release = releases.find(
                        (r) => artifactKey(r.reference) === e.target.value,
                      );
                      if (!release) return;
                      setEnvironment((current) =>
                        environmentForRelease(
                          release,
                          sources,
                          current || undefined,
                        ),
                      );
                    }}
                  >
                    <option value={artifactKey(environment.sandbox_release)}>
                      {environment.sandbox_release.id} ·{" "}
                      {environment.sandbox_release.version}
                    </option>
                    {releases
                      .filter(
                        (r) =>
                          artifactKey(r.reference) !==
                          artifactKey(environment.sandbox_release),
                      )
                      .map((r) => (
                        <option
                          key={artifactKey(r.reference)}
                          value={artifactKey(r.reference)}
                          disabled={
                            !r.enabled || r.availability === "unavailable"
                          }
                        >
                          {r.reference.id} · {r.reference.version}
                        </option>
                      ))}
                  </Select>
                </label>
                <h3>Storage and shared directories</h3>
                {environment.mounts.map((m) => (
                  <section className="runtime-mount" key={m.key}>
                    <h4>
                      {m.role === "data"
                        ? "Data directory"
                        : m.role.replaceAll("_", " ")}
                    </h4>
                    <label>
                      Mount path
                      <input
                        required
                        readOnly={required.some((role) => role.role === m.role)}
                        value={m.destination}
                        onChange={(e) =>
                          patchMount(m.key, { destination: e.target.value })
                        }
                      />
                    </label>
                    {m.role === "data" && (
                      <label>
                        Storage
                        <Select aria-label="Storage"
                          value={m.storage}
                          onChange={(e) =>
                            patchMount(
                              m.key,
                              e.target.value === "shared_readonly"
                                ? {
                                    storage: "shared_readonly",
                                    access: "read_only",
                                    source: { kind: "builtin" },
                                    capacity: {},
                                  }
                                : e.target.value === "shared"
                                  ? {
                                      storage: "shared",
                                      source: {
                                        kind: "shared",
                                        shared_volume_id: "",
                                      },
                                      capacity: {},
                                    }
                                  : {
                                      storage: "owned",
                                      source: { kind: "empty" },
                                      capacity: { bytes: "1073741824" },
                                    },
                            )
                          }
                        >
                          <option value="owned">Project-owned directory</option>
                          <option value="shared">Shared directory</option>
                          <option value="shared_readonly">
                            Immutable publication
                          </option>
                        </Select>
                      </label>
                    )}
                    {m.storage === "shared" ? (
                      <label>
                        Shared volume
                        <Select aria-label="Shared volume"
                          required
                          value={m.source.shared_volume_id || ""}
                          onChange={(e) =>
                            patchMount(m.key, {
                              source: {
                                kind: "shared",
                                shared_volume_id: e.target.value,
                              },
                            })
                          }
                        >
                          <option value="">Select a shared directory</option>
                          {volumes.map((v) => (
                            <option
                              key={v.id}
                              value={v.id}
                              disabled={!v.enabled || v.state !== "ready"}
                            >
                              {v.name} · {v.state}
                            </option>
                          ))}
                        </Select>
                      </label>
                    ) : (
                      <label>
                        Initial contents
                        <Select aria-label="Initial contents"
                          required={m.storage === "shared_readonly"}
                          value={
                            m.source.kind === "builtin" && m.source.version
                              ? JSON.stringify([
                                  m.source.artifact_id,
                                  m.source.version,
                                  m.source.digest,
                                ])
                              : ""
                          }
                          onChange={(e) => {
                            const s = sources.find(
                              (v) =>
                                artifactKey(v.reference) === e.target.value,
                            );
                            patchMount(m.key, {
                              source: s
                                ? {
                                    kind: "builtin",
                                    artifact_id: s.reference.id,
                                    version: s.reference.version,
                                    digest: s.reference.digest,
                                  }
                                : { kind: "empty" },
                            });
                          }}
                        >
                          <option
                            value=""
                            disabled={m.storage === "shared_readonly"}
                          >
                            {m.storage === "shared_readonly"
                              ? "Select an initialization source"
                              : "Empty directory"}
                          </option>
                          {sources
                            .filter(
                              (s) =>
                                s.roles?.includes(m.role) &&
                                (m.role !== "runtime" || s.clean_runtime) &&
                                (!required.some(
                                  (role) => role.role === m.role,
                                ) ||
                                  required.some(
                                    (role) =>
                                      role.role === m.role &&
                                      role.artifact_id === s.reference.id,
                                  )),
                            )
                            .map((s) => (
                              <option
                                key={artifactKey(s.reference)}
                                value={artifactKey(s.reference)}
                                disabled={
                                  !s.enabled ||
                                  [
                                    "unavailable",
                                    "retired",
                                    "other_store",
                                    "incompatible",
                                  ].includes(s.availability)
                                }
                              >
                                {s.reference.id} · {s.reference.version}
                              </option>
                            ))}
                        </Select>
                      </label>
                    )}
                    {m.role === "data" && (
                      <>
                        <label>
                          Access limit
                          <Select aria-label="Access limit"
                            disabled={m.storage === "shared_readonly"}
                            value={m.access}
                            onChange={(e) =>
                              patchMount(m.key, {
                                access: e.target.value as Mount["access"],
                              })
                            }
                          >
                            <option value="read_only">Read only</option>
                            <option value="read_write">Read and write</option>
                          </Select>
                        </label>
                        {m.storage === "owned" && (
                          <label>
                            Capacity (bytes)
                            <input
                              required
                              inputMode="numeric"
                              pattern="[1-9][0-9]*"
                              value={m.capacity.bytes || ""}
                              onChange={(e) =>
                                patchMount(m.key, {
                                  capacity: { bytes: e.target.value },
                                })
                              }
                            />
                          </label>
                        )}
                        <button
                          type="button"
                          onClick={() =>
                            setEnvironment((current) =>
                              current
                                ? {
                                    ...current,
                                    mounts: current.mounts.filter(
                                      (v) => v.key !== m.key,
                                    ),
                                  }
                                : current,
                            )
                          }
                        >
                          Remove directory
                        </button>
                      </>
                    )}
                  </section>
                ))}
                <button
                  type="button"
                  onClick={() =>
                    setEnvironment((current) =>
                      current
                        ? {
                            ...current,
                            mounts: [
                              ...current.mounts,
                              {
                                key: crypto.randomUUID(),
                                role: "data",
                                destination: "/data",
                                storage: "shared",
                                access: "read_only",
                                source: {
                                  kind: "shared",
                                  shared_volume_id: "",
                                },
                                capacity: {},
                              },
                            ],
                          }
                        : current,
                    )
                  }
                  disabled={
                    BigInt(environment.mounts.length) >= BigInt(maxMounts)
                  }
                >
                  Add data directory
                </button>
                <p className="hint">
                  Shared directories require a separate policy grant. Project
                  storage sizes follow resource quotas. Existing projects retain
                  their environment.
                </p>
              </>
            )
          )}
          {!readOnly && (
            <button
              className="primary"
              disabled={!changed || (kind === "environment" && !environment)}
            >
              Validate and review
            </button>
          )}
        </fieldset>
      </form>
      {initial && !duplicate && !readOnly && (
        <button
          disabled={mutation.blocked || !!changed}
          onClick={() =>
            void mutation.preview(
              path,
              { expected_revision: initial.revision },
              "DELETE",
            )
          }
        >
          Review deletion
        </button>
      )}
      <CatalogReview state={mutation} saved={saved} />
    </section>
  );
}
function SharedVolumes({ onDirty }: { onDirty: (value: boolean) => void }) {
  const [items, setItems] = useState<SharedVolume[] | null>(null),
    [selected, setSelected] = useState<SharedVolume | null>(null),
    [open, setOpen] = useState(false),
    [name, setName] = useState(""),
    [description, setDescription] = useState(""),
    [capacity, setCapacity] = useState("1073741824"),
    [enabled, setEnabled] = useState(true),
    [error, setError] = useState("");
  const mutation = useCatalogMutation();
  const dirty =
    open &&
    (name !== (selected?.name || "") ||
      description !== (selected?.description || "") ||
      capacity !== (selected?.capacity || "1073741824") ||
      enabled !== (selected?.enabled ?? true));
  useEffect(() => {
    onDirty(dirty || mutation.blocked);
    return () => onDirty(false);
  }, [dirty, mutation.blocked, onDirty]);
  const load = async () => {
    const r = await api<{ volumes: SharedVolume[] }>(
      "/api/admin/shared-volumes",
    );
    setItems(r.volumes);
    setError("");
  };
  useEffect(() => {
    const controller = new AbortController();
    void api<{ volumes: SharedVolume[] }>("/api/admin/shared-volumes", {
      signal: controller.signal,
    })
      .then((r) => setItems(r.volumes))
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, []);
  const edit = (v: SharedVolume | null) => {
    setSelected(v);
    setName(v?.name || "");
    setDescription(v?.description || "");
    setCapacity(v?.capacity || "1073741824");
    setEnabled(v?.enabled ?? true);
    setOpen(true);
  };
  const path = `/api/admin/shared-volumes${selected ? `/${encodeURIComponent(selected.id)}` : ""}`;
  return (
    <section>
      <div className="catalog-toolbar">
        <h2>Shared directories</h2>
        <div className="form-actions">
          <button
            disabled={open}
            onClick={() => void load().catch((e) => setError(String(e)))}
          >
            Refresh
          </button>
          <button
            className="primary"
            disabled={open}
            onClick={() => edit(null)}
          >
            Create shared directory
          </button>
        </div>
      </div>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!items && !error && <p role="status">Loading shared directories…</p>}
      {open ? (
        <>
          <form
            className="account-form"
            onSubmit={(e) => {
              e.preventDefault();
              void mutation.preview(
                path,
                {
                  expected_revision: selected?.revision || 0,
                  name,
                  description,
                  capacity,
                  enabled,
                },
                selected ? "PATCH" : "POST",
              );
            }}
          >
            <fieldset disabled={mutation.blocked}>
              <label>
                Name
                <input
                  required
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <label>
                Description
                <textarea
                  maxLength={1024}
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </label>
              <label>
                Capacity (bytes)
                <input
                  required
                  readOnly={!!selected}
                  inputMode="numeric"
                  pattern="[1-9][0-9]*"
                  value={capacity}
                  onChange={(e) => setCapacity(e.target.value)}
                />
              </label>
              <label className="check-label">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                />
                Available for authorization
              </label>
              <div className="form-actions">
                <button className="primary">Review changes</button>
                <button
                  type="button"
                  onClick={() => {
                    if (!dirty || window.confirm("Discard unsaved changes?"))
                      setOpen(false);
                  }}
                >
                  Cancel
                </button>
                {selected && (
                  <button
                    type="button"
                    disabled={dirty}
                    onClick={() =>
                      void mutation.preview(
                        path,
                        { expected_revision: selected.revision },
                        "DELETE",
                      )
                    }
                  >
                    Review deletion
                  </button>
                )}
              </div>
            </fieldset>
          </form>
          <CatalogReview
            state={mutation}
            saved={async () => {
              await load();
              setOpen(false);
            }}
          />
        </>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Directory</th>
                <th>Capacity</th>
                <th>Status</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {items?.map((v) => (
                <tr key={v.id}>
                  <td>
                    {v.name}
                    <small>{v.description}</small>
                  </td>
                  <td>{v.capacity} bytes</td>
                  <td>
                    {v.enabled ? v.state : "Disabled"}
                    {v.error && <small role="alert">{v.error}</small>}
                  </td>
                  <td>
                    <button
                      disabled={v.state === "deleting" || v.state === "deleted"}
                      onClick={() => edit(v)}
                    >
                      Configure
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {items?.length === 0 && (
            <p>
              No shared directories. Create one, grant group access through a
              policy, and add it to an environment template.
            </p>
          )}
        </div>
      )}
    </section>
  );
}
