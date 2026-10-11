"use client";
import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/api";
import { validResourceInteger } from "@web/resources";
import { artifactKey, bodyOf, mergeEnvironment, same, type Environment, type EnvironmentBody, type EnvironmentPreview, type Mount, type OptionsPage, type RuntimeOption } from "@/lib/runtime-models";
import { FitIssues, SavedPlanIssues } from "./resource-usage";
const defaultPath = "/api/admin/runtime/default";
const emptyOptions: OptionsPage = { options: [], max_mounts: "0" };
const selectable = (option: RuntimeOption) => option.enabled && ["available", "unchecked", "unknown"].includes(option.availability);
const settingError = (error: unknown) => {
  if (!(error instanceof APIError)) return String(error);
  const mount = /^mounts\.(\d+)\.(.+)$/.exec(error.field || "");
  return mount ? `Mount ${Number(mount[1]) + 1} ${mount[2]}: ${error.message}` : error.message;
};
const label = (option: RuntimeOption) => `${option.reference.id} @ ${option.reference.version} (${option.availability})`;

export function RuntimeEditor({ onDirty }: { onDirty: (dirty: boolean) => void }) {
  const [current, setCurrent] = useState<Environment | null>(null), [draft, setDraft] = useState<EnvironmentBody | null>(null);
  const [releases, setReleases] = useState<OptionsPage>(emptyOptions), [volumes, setVolumes] = useState<OptionsPage>(emptyOptions), [chosen, setChosen] = useState<RuntimeOption | null>(null);
  const [releasePages, setReleasePages] = useState([""]), [volumePages, setVolumePages] = useState([""]), [previewPages, setPreviewPages] = useState([""]);
  const [preview, setPreview] = useState<EnvironmentPreview | null>(null), [reviewed, setReviewed] = useState(false), [busy, setBusy] = useState(false), [pending, setPending] = useState<Record<string, unknown> | null>(null);
  const [error, setError] = useState(""), [notice, setNotice] = useState(""), [attempt, setAttempt] = useState(0);
  const alive = useRef(true);
  const dirty = !!pending || !!current && !!draft && !same(bodyOf(current), draft);
  useEffect(() => { onDirty(dirty); return () => onDirty(false); }, [dirty, onDirty]);
  useEffect(() => {
    alive.current = true;
    const abort = new AbortController();
    void Promise.all([api<Environment>(defaultPath, { signal: abort.signal }), api<OptionsPage>("/api/admin/runtime/options?kind=sandbox&limit=20", { signal: abort.signal }), api<OptionsPage>("/api/admin/runtime/options?kind=volume&limit=20", { signal: abort.signal })]).then(([value, releases, volumes]) => {
      if (abort.signal.aborted) return;
      setCurrent(value); setDraft(bodyOf(value)); setReleases(releases); setVolumes(volumes); setChosen(releases.options.find(option => same(option.reference, value.sandbox_release)) || null);
    }).catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    return () => { alive.current = false; abort.abort(); };
  }, [attempt]);
  const invalidate = () => { setPreview(null); setPreviewPages([""]); setReviewed(false); setNotice(""); };
  const update = (value: EnvironmentBody) => { setDraft(value); invalidate(); };
  const mount = (key: string, changes: Partial<Mount>) => { if (draft) update({ ...draft, mounts: draft.mounts.map(value => value.key === key ? { ...value, ...changes } : value) }); };
  const selectedRelease = releases.options.find(option => draft && same(option.reference, draft.sandbox_release)) || (chosen && draft && same(chosen.reference, draft.sandbox_release) ? chosen : undefined);
  const required = selectedRelease?.required_publications || (current && draft && same(current.sandbox_release, draft.sandbox_release) ? current.required_publications : []);
  const releaseOptions = [...releases.options];
  if (chosen && !releaseOptions.some(option => same(option.reference, chosen.reference))) releaseOptions.unshift(chosen);
  const chooseRelease = (key: string) => {
    const option = releaseOptions.find(option => artifactKey(option.reference) === key); if (!option || !draft) return;
    setChosen(option);
    const mounts = [...draft.mounts];
    for (const role of option.required_publications || []) {
      const index = mounts.findIndex(mount => mount.role === role.role), existing = mounts[index];
      const value: Mount = { key: existing?.key || `${role.role}-${crypto.randomUUID().slice(0, 8)}`, role: role.role, destination: role.destination, storage: "shared_readonly", access: "read_only", source: existing?.source.artifact_id === role.artifact_id ? existing.source : { kind: "builtin", artifact_id: role.artifact_id }, capacity: {} };
      if (index >= 0) mounts[index] = value; else mounts.push(value);
    }
    update({ sandbox_release: option.reference, mounts });
  };
  const loadOptions = async (kind: "sandbox" | "volume", pages: string[]) => {
    setBusy(true); setError("");
    try { const value = await api<OptionsPage>(`/api/admin/runtime/options?kind=${kind}&limit=20&cursor=${encodeURIComponent(pages.at(-1)!)}`); if (!alive.current) return; if (kind === "sandbox") { setReleases(value); setReleasePages(pages); } else { setVolumes(value); setVolumePages(pages); } }
    catch (error) { if (alive.current) setError(String(error)); }
    finally { if (alive.current) setBusy(false); }
  };
  const reload = async (clear: boolean) => {
    const latest = await api<Environment>(defaultPath);
    if (!alive.current) return;
    setDraft(!clear && current && draft ? mergeEnvironment(bodyOf(current), draft, bodyOf(latest)) : bodyOf(latest)); setCurrent(latest); invalidate();
    if (clear) setChosen(releases.options.find(option => same(option.reference, latest.sandbox_release)) || null);
  };
  const inspect = async (pages = [""]) => {
    if (!current || !draft) return;
    setBusy(true); setError(""); setReviewed(false);
    try { const value = await mutate<EnvironmentPreview>(`${defaultPath}/preview?limit=10&cursor=${encodeURIComponent(pages.at(-1)!)}`, { ...draft, expected_revision: current.revision }); if (alive.current) { setPreview(value); setPreviewPages(pages); } }
    catch (error) { if (alive.current) { setPreview(null); setError(settingError(error)); } }
    finally { if (alive.current) setBusy(false); }
  };
  const save = async (body: Record<string, unknown>) => {
    setBusy(true); setPending(body); setError(""); setNotice("");
    let committed = false;
    try { const value = await mutate<Environment & { replayed: boolean }>(defaultPath, body, "PATCH"); committed = true; if (!alive.current) return; await reload(true); if (!alive.current) return; setPending(null); setNotice(value.replayed ? "Original environment change confirmed. Current default loaded." : "Default environment saved. Bound Projects retain their saved release and mounts."); }
    catch (error) { if (!alive.current) return; if (!committed && error instanceof APIError && ((error.status >= 400 && error.status < 500 && error.code !== "outcome_unknown") || error.code === "artifact_unavailable")) { setPending(null); setPreview(null); setReviewed(false); } setError(error instanceof APIError && error.status === 409 ? "The environment or its dependencies changed. Reload, review your retained edits and preview again." : String(error)); }
    finally { if (alive.current) setBusy(false); }
  };
  const blocked = busy || !!pending;
  const invalidCapacity = draft?.mounts.some(mount => mount.storage === "owned" && mount.role === "data" && (!validResourceInteger(mount.capacity.bytes || "") || mount.capacity.bytes === "0"));
  return <section><header className="page-heading"><div><h1>Default runtime environment</h1><p>Unbound Projects select this environment at their first upload or compute allocation. Bound Projects retain their exact release, mounts and capacities.</p></div></header>
    {error && <p role="alert" className="error">{error}</p>}{notice && <p role="status">{notice}</p>}
    {!current && (!error ? <p role="status">Loading runtime inventory…</p> : <button onClick={() => { setError(""); setAttempt(value => value + 1); }}>Retry loading</button>)}
    {pending && !busy && <div className="notice"><p>The outcome is unconfirmed. Resolve the original request before editing again.</p><button onClick={() => void save(pending)}>Check original request</button></div>}
    {current && draft && <><form className="account-form runtime-form" onSubmit={event => { event.preventDefault(); void inspect(); }}><fieldset disabled={blocked}>
      <label>Sandbox release<select value={artifactKey(draft.sandbox_release)} onChange={event => chooseRelease(event.target.value)}>{!releaseOptions.some(option => same(option.reference, draft.sandbox_release)) && <option value={artifactKey(draft.sandbox_release)}>{draft.sandbox_release.id} @ {draft.sandbox_release.version} (saved selection)</option>}{releaseOptions.map(option => <option key={artifactKey(option.reference)} value={artifactKey(option.reference)} disabled={!selectable(option)}>{label(option)}</option>)}</select></label><p className="identifier">{draft.sandbox_release.digest}</p>{selectedRelease && <p className="hint">{selectedRelease.checked_at ? `Availability checked at ${selectedRelease.checked_at}` : "Availability has not been confirmed."}</p>}
      <div className="form-actions"><button type="button" disabled={releasePages.length === 1} onClick={() => void loadOptions("sandbox", releasePages.slice(0, -1))}>Previous releases</button><button type="button" disabled={!releases.next_cursor} onClick={() => void loadOptions("sandbox", [...releasePages, releases.next_cursor!])}>Next releases</button><button type="button" onClick={() => void loadOptions("sandbox", releasePages)}>Refresh release availability</button></div>
      <p className="hint">Only operator-registered versions appear. Availability is an observation, not a capacity reservation. Unknown or unchecked availability must be verified at allocation.</p>
      {selectedRelease && <details><summary>Image path rules</summary><p>Protected roots: {selectedRelease.protected_roots?.join(", ")}</p><p>{selectedRelease.blocked_path_count || 0} additional image file or symlink paths are excluded. Preview validates each destination against the selected image.</p></details>}
      <h2>Mounts ({draft.mounts.length}/{current.max_mounts})</h2>
      {draft.mounts.map((value, index) => {
        const system = required.find(role => role.role === value.role), core = value.role === "workspace" || value.role === "runtime", prefix = `Mount ${index + 1}`;
        const options = volumes.options.filter(option => option.roles?.includes(value.role) && (!system || option.reference.id === system.artifact_id) && (value.role !== "runtime" || option.clean_runtime));
        const selected = value.source.kind === "builtin" ? JSON.stringify([value.source.artifact_id, value.source.version, value.source.digest]) : "empty";
        return <section className="runtime-mount" key={value.key}><header><h3>{value.role}</h3><span className="identifier">{value.key}</span></header>{system && <p className="hint">Required by this release. Destination, storage and access are fixed; select an approved version of {system.artifact_id}.</p>}
          {!system && !core && value.role !== "data" && <p className="error">This role is not declared by the selected release. Remove this mount or select a compatible release.</p>}
          <label>{prefix} destination<input required maxLength={4096} value={value.destination} readOnly={!!system} onChange={event => mount(value.key, { destination: event.target.value })} /></label>
          <div className="runtime-fields"><label>{prefix} storage<select aria-label={`${prefix} storage`} disabled={!!system || core} value={value.storage} onChange={event => mount(value.key, event.target.value === "shared_readonly" ? { storage: "shared_readonly", access: "read_only", capacity: {}, source: value.source.kind === "builtin" ? value.source : { kind: "builtin" } } : { storage: "owned", capacity: { bytes: "1048576" }, source: value.source.version ? value.source : { kind: "empty" } })}><option value="owned">Owned Project Volume</option><option value="shared_readonly">Shared immutable publication</option></select></label>
          <label>{prefix} access<select aria-label={`${prefix} access`} value={value.access} disabled={core || value.storage === "shared_readonly"} onChange={event => mount(value.key, { access: event.target.value as Mount["access"] })}><option value="read_write">Read and write</option><option value="read_only">Read only</option></select></label></div>
          <label>{prefix} source<select aria-label={`${prefix} source`} value={selected} onChange={event => { const option = options.find(option => artifactKey(option.reference) === event.target.value); mount(value.key, { source: option ? { kind: "builtin", artifact_id: option.reference.id, version: option.reference.version, digest: option.reference.digest } : { kind: "empty" } }); }}>
            {value.storage === "owned" && <option value="empty">Empty storage</option>}{value.storage === "shared_readonly" && !value.source.version && <option value={selected}>Choose a registered source</option>}
            {value.source.kind === "builtin" && value.source.version && !options.some(option => artifactKey(option.reference) === selected) && <option value={selected}>{value.source.artifact_id} @ {value.source.version} (saved selection)</option>}
            {options.map(option => <option key={artifactKey(option.reference)} value={artifactKey(option.reference)} disabled={!selectable(option)}>{label(option)} · minimum copy {option.minimum_bytes} bytes · source {option.source_capacity_bytes || "unavailable"} bytes</option>)}</select></label>
          {value.source.digest && <p className="identifier">{value.source.digest}</p>}
          {core ? <p>Capacity inherits each user’s {value.role} resource setting. <Link href="/groups">Manage group entitlements</Link></p> : value.storage === "owned" ? <label>{prefix} capacity in bytes<input required inputMode="numeric" maxLength={19} value={value.capacity.bytes || ""} onChange={event => mount(value.key, { capacity: { bytes: event.target.value } })} /></label> : <p>Shared capacity comes from the immutable publication and is charged once to platform storage.</p>}
          {!system && !core && <button type="button" onClick={() => update({ ...draft, mounts: draft.mounts.filter(mount => mount.key !== value.key) })}>Remove {value.key}</button>}
        </section>;
      })}
      <button type="button" disabled={BigInt(draft.mounts.length) >= BigInt(current.max_mounts)} onClick={() => update({ ...draft, mounts: [...draft.mounts, { key: `data-${crypto.randomUUID().slice(0, 8)}`, role: "data", destination: "/data/new", storage: "owned", access: "read_write", source: { kind: "empty" }, capacity: { bytes: "1048576" } }] })}>Add data mount</button>
      <div className="form-actions"><button type="button" disabled={volumePages.length === 1} onClick={() => void loadOptions("volume", volumePages.slice(0, -1))}>Previous source versions</button><button type="button" disabled={!volumes.next_cursor} onClick={() => void loadOptions("volume", [...volumePages, volumes.next_cursor!])}>Next source versions</button><button type="button" onClick={() => void loadOptions("volume", volumePages)}>Refresh source availability</button></div>
      {invalidCapacity && <p role="alert" className="error">Owned data capacity must be a positive whole number of bytes.</p>}<button disabled={!dirty || !!invalidCapacity}>Preview environment changes</button>
    </fieldset></form>
    <div className="form-actions"><button disabled={blocked} onClick={() => { setBusy(true); void reload(false).then(() => { if (alive.current) { setError(""); setNotice("Current environment loaded. Changed fields are retained for review."); } }).catch(error => { if (alive.current) setError(String(error)); }).finally(() => { if (alive.current) setBusy(false); }); }}>Reload current environment</button><button disabled={blocked} onClick={() => { setDraft(bodyOf(current)); setChosen(null); invalidate(); }}>Discard edits</button></div>
    {preview && <section className="panel"><h2>Environment preview</h2><p>{preview.affected_accounts} accounts; {preview.accounts_with_plan_conflicts} cannot fit the proposed plan. Saving does not raise quotas or modify bound Projects.</p>
      {preview.users.map(user => <article className="impact-card" key={user.account_id}><h3>{user.username}</h3><FitIssues issues={user.issues} /><p>Owned plan: {user.plan.owned_bytes} bytes. Current persistent usage: {user.usage.persistent_bytes?.used || "0"} allocated / {user.usage.persistent_bytes?.reserved || "0"} reserved.</p>
        <div className="table-scroll"><table><thead><tr><th>Slot</th><th>Destination</th><th>Storage / access</th><th>Capacity</th><th>Source</th></tr></thead><tbody>{user.plan.mount_plan?.map(mount => <tr key={mount.key}><td>{mount.key}<small>{mount.role}</small></td><td>{mount.destination}</td><td>{mount.storage} / {mount.access}</td><td>{mount.capacity_bytes} bytes{mount.capacity.setting && <small>{mount.capacity.setting}: {user.effective[mount.capacity.setting].override === null ? "inherited" : "user override"}</small>}</td><td>{mount.source.kind === "empty" ? "Empty" : `${mount.source.artifact_id} @ ${mount.source.version}`}<small>{mount.source.digest}</small></td></tr>)}</tbody></table></div>
        <details><summary>Current overages</summary><FitIssues issues={user.overages} /></details><details><summary>Existing saved-plan conflicts (unchanged by this edit)</summary><SavedPlanIssues value={user.saved_plan_conflicts} /></details>
      </article>)}
      <nav className="pagination" aria-label="Environment impact pages"><button disabled={blocked || previewPages.length === 1} onClick={() => void inspect(previewPages.slice(0, -1))}>Previous accounts</button><button disabled={blocked || !preview.next_cursor} onClick={() => void inspect([...previewPages, preview.next_cursor!])}>Next accounts</button></nav>
      <label className="check-label"><input type="checkbox" checked={reviewed} disabled={blocked} onChange={event => setReviewed(event.target.checked)} />I reviewed the impact. Bound Projects retain their environment; users that do not fit will be denied new allocation.</label><button className="primary" disabled={blocked || !reviewed} onClick={() => void save({ ...draft, expected_revision: current.revision, expected_dependencies: preview.expected_dependencies, request_id: crypto.randomUUID() })}>Save default environment</button>
    </section>}</>}
  </section>;
}
