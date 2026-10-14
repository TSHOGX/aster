"use client";
import { formatQuantity, intlLocale } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

import { EnvironmentUpdates } from "./environment-updates";
import { VolumeImports } from "./volume-imports";
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
  const tr = useT();
  const section = sections.find((s) => s.id === raw) || sections[0];
	const [importDirty,setImportDirty]=useState(false),[sharedDirty,setSharedDirty]=useState(false);
	useEffect(()=>{if(section.id!=="volumes")return;onDirty(importDirty||sharedDirty);return()=>onDirty(false)},[section.id,importDirty,sharedDirty,onDirty]);
  return (
    <section>
      <header className="page-heading">
        <div>
          <h1>{tr("Resource Center")}</h1>
          <p>{tr(section.description)}</p>
        </div>
      </header>
      <nav className="catalog-tabs" aria-label={tr("Resource categories")}>
        {sections.map((s) => (
          <Link
            key={s.id}
            aria-current={section.id === s.id ? "page" : undefined}
            href={`/resources/${s.id}`}
          >
            {tr(s.label)}
          </Link>
        ))}
      </nav>
      {section.id === "sandboxes" ? (
        <Inventory kind="sandbox" />
      ) : section.id === "volumes" ? (
        <>
          <VolumeImports onDirty={setImportDirty} />
          <SharedVolumes onDirty={setSharedDirty} />
          <details className="panel">
            <summary>{tr("Initialization sources")}</summary>
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
  const tr = useT();
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
          {tr(error)}
        </p>
      )}
      <button
        onClick={() => {
          setError("");
          setAttempt((v) => v + 1);
        }}
      >
         {tr("Refresh availability")} </button>
      {!items ? (
        <p role="status">{tr("Loading inventory…")}</p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>{tr("Resource")}</th>
                <th>{tr("Status")}</th>
                <th>{tr("Details")}</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr key={artifactKey(item.reference)}>
                  <td>
                    {item.name || item.library_name || item.reference.id}
                    <small>{item.reference.version}</small><small>{item.purpose}</small>
                  </td>
                  <td>{item.enabled ? tr(item.availability) : tr("Disabled")}</td>
                  <td>
                    <details>
                      <summary>{tr("View constraints")}</summary>
                      {item.roles && <p>{tr("Roles:")} {tr(item.roles.join(", "))}</p>}
                      {item.required_publications?.map((p) => (
                        <p key={p.role}>
                          {tr(p.role)}: {p.destination}
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
           {tr("No registered resources. Register a release or publish initialization content with the operator tools.")} </p>
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
  const tr = useT();
  const [items, setItems] = useState<Template[] | null>(null),
    [editing, setEditing] = useState<{
      value?: Template;
      duplicate?: boolean;
      tab?: "configuration"|"updates"|"projects";
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
        key={`${editing.value?.id||"new"}:${editing.value?.revision||0}:${!!editing.duplicate}`}
        initialTab={editing.tab}
        kind={kind}
        initial={editing.value}
        duplicate={editing.duplicate}
        onDirty={onDirty}
        close={() => setEditing(null)}
        saved={async (savedName) => {
          const result=await api<{templates:Template[]}>(`/api/admin/resource-templates/${kind}`);
          setItems(result.templates);setError("");
          const value=result.templates.find(item=>item.name===savedName);
          setEditing(kind==="environment"&&value?{value,tab:"updates"}:null);
        }}
      />
    );
  return (
    <>
      <div className="catalog-toolbar">
        <h2>
          {kind === "network" ? tr("Network templates") : tr("Environment templates")}
        </h2>
        <button className="primary" onClick={() => setEditing({})}>
           {tr("Create template")} </button>
      </div>
      {error && (
        <p className="error" role="alert">
          {tr(error)}
          <button onClick={() => void load().catch((e) => setError(String(e)))}>
             {tr("Retry")} </button>
        </p>
      )}
      {!items && !error && <p role="status">{tr("Loading templates…")}</p>}
      <div className="catalog-grid">
        {items?.map((item) => (
          <article key={item.id} className="panel catalog-card">
            <div>
              <h3>{item.name}</h3>
              <span className="catalog-status">
                {item.builtin ? tr("Built-in · ") : ""}
                {item.enabled ? tr("Enabled") : tr("Disabled")}
              </span>
            </div>
            <p>{item.description || tr("No description")}</p>
            <p className="hint">
              {kind === "network" ? tr("{value0} outbound rules", { value0: item.network?.direct.length || 0 }) : tr("{value0} mounts", { value0: item.environment?.mounts.length || 0 })}{" "}
              · {item.usage_count}  {tr("policy references")} </p>
            <small>
               {tr("Revision")} {formatQuantity(item.revision)}  {tr("· Updated")}{" "}
              {new Date(item.updated_at).toLocaleString(intlLocale())}
            </small>
            <div className="form-actions">
              <button onClick={() => setEditing({ value: item })}>
                {item.builtin ? tr("View") : tr("Edit")}
              </button>
              <button
                onClick={() => setEditing({ value: item, duplicate: true })}
              >
                 {tr(item.builtin ? "Copy and customize" : "Duplicate")} </button>
            </div>
          </article>
        ))}
      </div>
      {items?.length === 0 && (
        <section className="panel">
          <h3>{tr("No templates yet")}</h3>
          <p>
             {tr("Create a template, authorize it in a policy, and choose it as a group default.")} </p>
        </section>
      )}
    </>
  );
}
function TemplateEditor({
  initialTab = "configuration",
  kind,
  initial,
  duplicate,
  onDirty,
  close,
  saved,
}: {
  kind: "network" | "environment";
  initialTab?: "configuration"|"updates"|"projects";
  initial?: Template;
  duplicate?: boolean;
  onDirty: (value: boolean) => void;
  close: () => void;
  saved: (name?:string) => Promise<void>;
}) {
  const tr = useT();
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
  const [tab,setTab]=useState<"configuration"|"updates"|"projects">(initialTab);
  const [updatesDirty,setUpdatesDirty]=useState(false);
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
    onDirty(!!changed || mutation.blocked || updatesDirty);
    return () => onDirty(false);
  }, [changed, mutation.blocked, updatesDirty, onDirty]);
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
    if (!changed || window.confirm(tr("Discard unsaved template changes?")))
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
            {duplicate ? tr("Duplicate template") : initial ? initial.name : tr("Create template")}
          </h2>
          <p>
            {readOnly ? tr("Built-in templates are immutable. Duplicate this template to customize it.") : tr("Configure capabilities, review the impact, then save.")}
          </p>
        </div>
        <button disabled={mutation.blocked || updatesDirty} onClick={leave}>
           {tr("Back to templates")} </button>
      </header>
      {kind==="environment" && initial && !duplicate && <nav className="catalog-tabs" aria-label={tr("Environment sections")}>{(["configuration","updates","projects"] as const).map(value=><button key={value} aria-current={tab===value ? "page" : undefined} disabled={mutation.blocked||updatesDirty||(!!changed&&value!=="configuration")} onClick={()=>setTab(value)}>{value==="configuration" ? tr("Configuration") : value==="updates" ? tr("Updates") : tr("Projects")}</button>)}</nav>}
      {tab!=="configuration" && initial ? <EnvironmentUpdates environment={initial.id} revision={initial.revision} hasPublications={!!initial.environment?.mounts.some(m=>["data","shared_assets"].includes(m.role)&&m.storage==="shared_readonly")} tab={tab} onDirty={setUpdatesDirty}/> : <>
      {error && (
        <p className="error" role="alert">
          {tr(error)}{" "}
          <button
            disabled={mutation.blocked}
            onClick={() => {
              setError("");
              setAttempt((value) => value + 1);
            }}
          >
             {tr("Retry loading")} </button>
        </p>
      )}
      {inventoryLoaded && kind === "environment" && !environment && (
        <p role="status">
           {tr("No available sandbox releases. Register a release before creating an environment template.")} </p>
      )}
      {!inventoryLoaded && !error && (
        <p role="status">{tr("Loading environment resources…")}</p>
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
          <legend>{tr("Identity and purpose")}</legend>
          <label>
             {tr("Name")} <input
              required
              maxLength={256}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label>
             {tr("Description")} <textarea
              maxLength={1024}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={tr("Explain when this template should be used")}
            />
          </label>
          <label className="check-label">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
             {tr("Available for authorization")} </label>
          {kind === "network" ? (
            <>
              <h3>{tr("Outbound connections")}</h3>
              <p className="hint">
                 {tr("No rules means no additional direct connections. Platform service routes are configured separately.")} </p>
              {network.direct.map((rule, index) => (
                <div className="catalog-rule" key={index}>
                  <label>
                     {tr("IPv4 CIDR")} <input
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
                     {tr("Port")} <input
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
                     {tr("Protocol")} <Select aria-label={tr("Protocol")}
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
                      <option value="tcp">{tr("TCP")}</option>
                      <option value="udp">{tr("UDP")}</option>
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
                     {tr("Remove")} </button>
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
                 {tr("Add destination")} </button>
              <label>
                 {tr("DNS resolvers")} <textarea
                  value={network.resolvers.join("\n")}
                  placeholder={tr("One IPv4 address per line")}
                  onChange={(e) =>
                    setNetwork((n) => ({
                      ...n,
                      resolvers: e.target.value.split("\n"),
                    }))
                  }
                />
              </label>
              <p className="hint">
                 {tr("Use 0.0.0.0/0 for public IPv4 destinations on the selected port. DNS permits UDP/53; add an explicit rule if TCP DNS is required.")} </p>
            </>
          ) : (
            environment && (
              <>
                <h3>{tr("Sandbox")}</h3>
                <label>
                   {tr("Release")} <Select aria-label={tr("Release")}
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
                          {r.name || r.reference.id} · {r.reference.version} · {r.purpose}
                        </option>
                      ))}
                  </Select>
                </label>
                <h3>{tr("Storage and shared directories")}</h3>
                <p>{tr("Saved configuration is used for new projects. Publish compatible data changes separately in Updates to reach existing projects.")}</p>
                {environment.mounts.map((m) => (
                  <section className="runtime-mount" key={m.key}>
                    <h4>
                      {m.role === "data" ? tr("Data directory") : m.role === "agent_config" ? tr("Agent configuration") : m.role === "shared_assets" ? tr("Shared assets") : tr(m.role.replaceAll("_", " "))}
                    </h4>
                    {["runtime","agent_config","shared_assets"].includes(m.role)&&<p className="hint">{m.role==="runtime" ? tr("Private runtime state. Use an empty directory or an operator-reviewed clean seed.") : m.role === "agent_config" ? tr("Platform-managed Pi settings and runner configuration. Select a reviewed operator release.") : tr("AGENTS.md is appended to the Agent system prompt. Select an exact shared-assets version; skills are optional.")}</p>}
                    <label>
                       {tr("Mount path")} <input
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
                         {tr("Storage")} <Select aria-label={tr("Storage")}
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
                          <option value="owned">{tr("Project-owned directory")}</option>
                          <option value="shared">{tr("Shared directory")}</option>
                          <option value="shared_readonly">
                             {tr("Immutable publication")} </option>
                        </Select>
                      </label>
                    )}
                    {m.storage === "shared" ? (
                      <label>
                         {tr("Shared directory")} {!volumes.length&&<span>{tr("No shared directories yet.")} <Link href="/resources/volumes">{tr("Create a shared directory")}</Link></span>}
                        <Select aria-label={tr("Shared volume")}
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
                          <option value="">{tr("Select a shared directory")}</option>
                          {volumes.map((v) => (
                            <option
                              key={v.id}
                              value={v.id}
                              disabled={!v.enabled || v.state !== "ready"}
                            >
                              {v.name} · {tr(v.state)}
                            </option>
                          ))}
                        </Select>
                      </label>
                    ) : (
                      <label>
                        {m.storage==="shared_readonly" ? tr("Published version") : tr("Initial contents")}
                        {m.storage==="shared_readonly"&&<small>{tr("Mount this immutable version read-only. Project-owned directories instead receive an independent copy.")}</small>}
                        <Select aria-label={m.storage==="shared_readonly" ? tr("Published version") : tr("Initial contents")}
                          required={m.storage === "shared_readonly"}
                          value={
                            m.source.kind === "builtin" && m.source.version ? JSON.stringify([
                                  m.source.artifact_id,
                                  m.source.version,
                                  m.source.digest,
                                ]) : ""
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
                            {m.storage === "shared_readonly" ? tr("Select an initialization source") : tr("Empty directory")}
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
                                      (role.role === "shared_assets" ? s.content_profile === "shared-assets-v1" : role.artifact_id === s.reference.id),
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
                                {s.library_name || s.reference.id} · {s.version_label || s.reference.version}{m.role === "shared_assets" ? ` · ${s.reference.digest.slice(-8)} · ${tr(s.agents_status || s.availability)}` : ""}
                              </option>
                            ))}
                        </Select>
                        {sources.find(s=>s.reference.id===m.source.artifact_id&&s.reference.version===m.source.version)?.version_note&&<small>{sources.find(s=>s.reference.id===m.source.artifact_id&&s.reference.version===m.source.version)?.version_note}</small>}
                      </label>
                    )}
                    {m.role === "data" && (
                      <>
                        <label>
                           {tr("Access limit")} <Select aria-label={tr("Access limit")}
                            disabled={m.storage === "shared_readonly"}
                            value={m.access}
                            onChange={(e) =>
                              patchMount(m.key, {
                                access: e.target.value as Mount["access"],
                              })
                            }
                          >
                            <option value="read_only">{tr("Read only")}</option>
                            <option value="read_write">{tr("Read and write")}</option>
                          </Select>
                        </label>
                        {m.storage === "owned" && (
                          <label>
                             {tr("Capacity (bytes)")} <input
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
                           {tr("Remove directory")} </button>
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
                   {tr("Add data directory")} </button>
                <p className="hint">
                   {tr("Shared directories require a separate policy grant. Project storage sizes follow resource quotas. Existing projects retain their environment.")} </p>
              </>
            )
          )}
          {!readOnly && (
            <button
              className="primary"
              disabled={!changed || (kind === "environment" && !environment)}
            >
               {tr("Validate and review")} </button>
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
           {tr("Review deletion")} </button>
      )}
      <CatalogReview state={mutation} saved={()=>saved(name)} />
      </>}
    </section>
  );
}
function SharedVolumes({ onDirty }: { onDirty: (value: boolean) => void }) {
  const tr = useT();
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
        <h2>{tr("Shared directories")}</h2>
        <div className="form-actions">
          <button
            disabled={open}
            onClick={() => void load().catch((e) => setError(String(e)))}
          >
             {tr("Refresh")} </button>
          <button
            className="primary"
            disabled={open}
            onClick={() => edit(null)}
          >
             {tr("Create shared directory")} </button>
        </div>
      </div>
      {error && (
        <p role="alert" className="error">
          {tr(error)}
        </p>
      )}
      {!items && !error && <p role="status">{tr("Loading shared directories…")}</p>}
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
                 {tr("Name")} <input
                  required
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <label>
                 {tr("Description")} <textarea
                  maxLength={1024}
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </label>
              <label>
                 {tr("Capacity (bytes)")} <input
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
                 {tr("Available for authorization")} </label>
              <div className="form-actions">
                <button className="primary">{tr("Review changes")}</button>
                <button
                  type="button"
                  onClick={() => {
                    if (!dirty || window.confirm(tr("Discard unsaved changes?")))
                      setOpen(false);
                  }}
                >
                   {tr("Cancel")} </button>
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
                     {tr("Review deletion")} </button>
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
                <th>{tr("Directory")}</th>
                <th>{tr("Capacity")}</th>
                <th>{tr("Status")}</th>
                <th>{tr("Actions")}</th>
              </tr>
            </thead>
            <tbody>
              {items?.map((v) => (
                <tr key={v.id}>
                  <td>
                    {v.name}
                    <small>{v.description}</small>
                  </td>
                  <td>{v.capacity}  {tr("bytes")}</td>
                  <td>
                    {v.enabled ? tr(v.state) : tr("Disabled")}
                    {v.error && <small role="alert">{tr(v.error)}</small>}
                  </td>
                  <td>
                    <button
                      disabled={v.state === "deleting" || v.state === "deleted"}
                      onClick={() => edit(v)}
                    >
                       {tr("Configure")} </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {items?.length === 0 && (
            <p>
               {tr("No shared directories. Create one, grant group access through a policy, and add it to an environment template.")} </p>
          )}
        </div>
      )}
    </section>
  );
}
