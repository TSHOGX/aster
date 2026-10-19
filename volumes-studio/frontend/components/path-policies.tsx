"use client";

import { useEffect, useRef, useState } from "react";
import { api, APIError } from "../lib/api";
import { message, useT } from "../lib/i18n";
import type { Message } from "../../../web/i18n/locale";
import { useStudio } from "./studio-provider";

type Revision = { id: string; revision: number; digest: string };
type Release = { publisher_id: string; library_id: string; version_id: string; descriptor_digest: string };
type Policy = { mode: "disabled" | "report_only" | "required"; profile?: Revision; service?: Revision; reference_strategy: "none" | "target_main" | "generation_base" | "pinned_release"; reference_release?: Release };
type SavedPolicy = { id: string; group_id: string; path: string; revision: number; digest: string; evaluation: Policy };
type PolicyPage = { group_id: string; can_configure: boolean; items: SavedPolicy[] };
type Draft = { expected_revision: number; path: string; evaluation: Policy };
type Change = Draft & { request_id: string };
type Recovery = { draft: Draft; pending?: Change };
type PolicyHistory = { items: SavedPolicy[]; next: number };
const off = (): Policy => ({ mode: "disabled", reference_strategy: "none" });
const emptyRevision = (): Revision => ({ id: "", revision: 1, digest: "" });
const idPattern = "[A-Za-z0-9_\\-]{1,128}";
const emptyDraft = (): Draft => ({ path: "", expected_revision: 0, evaluation: off() });
const releaseLabels = { publisher_id: "Publisher ID", library_id: "Library ID", version_id: "Version ID", descriptor_digest: "Descriptor digest" };
const modeLabels = { disabled: "Off", report_only: "Informational evaluation", required: "Required evaluation" };
function readRecovery(raw: string): Recovery {
  const value = JSON.parse(raw) as Recovery;
  const valid = (draft: Draft) => {
    const policy = draft?.evaluation;
    const revision = (value?: Revision) => value && typeof value.id === "string" && Number.isSafeInteger(value.revision) && typeof value.digest === "string";
    return typeof draft?.path === "string" && Number.isSafeInteger(draft.expected_revision) && draft.expected_revision >= 0 && policy && Object.hasOwn(modeLabels, policy.mode) && ["none", "target_main", "generation_base", "pinned_release"].includes(policy.reference_strategy) &&
      (policy.mode === "disabled" || (revision(policy.profile) && revision(policy.service))) &&
      (policy.reference_strategy !== "pinned_release" || policy.reference_release && Object.keys(releaseLabels).every(key => typeof policy.reference_release![key as keyof Release] === "string"));
  };
  if (!valid(value?.draft) || value.pending && (!valid(value.pending) || !/^[A-Za-z0-9_-]{1,128}$/.test(value.pending.request_id) || value.pending.path !== value.draft.path || value.pending.expected_revision !== value.draft.expected_revision || JSON.stringify(value.pending.evaluation) !== JSON.stringify(value.draft.evaluation))) throw new Error("Invalid recovery");
  return value;
}

export function PathPolicies({ library }: { library: string }) {
  const tr = useT(); const studio = useStudio();
  const [page, setPage] = useState<PolicyPage>();
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [dirty, setDirty] = useState(false); const [busy, setBusy] = useState(false);
  const [error, setError] = useState(""); const [localError, setLocalError] = useState("");
  const [malformed, setMalformed] = useState(false); const [pending, setPending] = useState<Change>();
  const [saved, setSaved] = useState<SavedPolicy>(); const [history, setHistory] = useState<PolicyHistory>();
  const active = useRef<AbortController | null>(null);
  const recovery = useRef<Recovery | undefined>(undefined);
  const controller = useRef(studio);
  useEffect(() => { controller.current = studio; });
  const root = `/libraries/${encodeURIComponent(library)}/evaluation-policies`;
  const key = `studio-path-policy:${JSON.stringify([studio.session?.issuer, studio.session?.subject, library])}`;
  const { path, evaluation: policy } = draft;
  const current = page?.items.find(item => item.path === path);
  const conflict = !!page && (current?.revision ?? 0) !== draft.expected_revision;

  function persist(next?: Recovery) {
    recovery.current = next;
    try {
      if (next) sessionStorage.setItem(key, JSON.stringify(next)); else sessionStorage.removeItem(key);
      setLocalError("");
    } catch { setLocalError("Local policy recovery is unavailable. Keep this tab open until the save is confirmed."); }
  }
  function clearVisible() {
    setPage(undefined); setDraft(emptyDraft()); setDirty(false); setPending(undefined); setSaved(undefined); setHistory(undefined);
  }
  async function reload() {
    active.current?.abort(); const abort = new AbortController(); active.current = abort; setBusy(true); setError(""); setSaved(undefined); setHistory(undefined);
    try {
      const result = await api<PolicyPage>(root, { signal: abort.signal });
      if (abort.signal.aborted) return;
      setPage(result);
      let restored = recovery.current;
      try {
        const raw = sessionStorage.getItem(key);
        if (raw) restored = readRecovery(raw);
        setMalformed(false);
      } catch { setMalformed(true); }
      recovery.current = restored;
      const selected = result.items.find(item => item.path === draft.path) ?? result.items[0];
      setDraft(restored?.draft ?? (selected ? { path: selected.path, expected_revision: selected.revision, evaluation: selected.evaluation } : emptyDraft()));
      setDirty(!!restored); setPending(restored?.pending);
    } catch {
      if (abort.signal.aborted) return;
      clearVisible(); setError("Evaluation policies are unavailable. Retry or check Volume access.");
    } finally { if (!abort.signal.aborted) setBusy(false); }
  }
  useEffect(() => {
    recovery.current = undefined;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Hydrate authenticated, Volume-scoped recovery only after a successful authorized read.
    clearVisible(); void reload();
    return () => active.current?.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [root, key]);
  function edit(next: Draft) {
    setDraft(next); setDirty(true); setSaved(undefined); setHistory(undefined);
    persist({ draft: next });
  }
  const update = (next: Policy) => edit({ ...draft, evaluation: next });
  async function choose(value: string) {
    if (dirty && await controller.current.ask("Choose policy", "Discard unsaved policy changes?", ["Keep editing", "Choose policy"]) !== "Choose policy") return;
    const next = page?.items.find(item => item.id === value);
    setDraft(next ? { path: next.path, expected_revision: next.revision, evaluation: next.evaluation } : emptyDraft());
    setDirty(false); setSaved(undefined); setHistory(undefined); setError(""); persist();
  }
  async function discard() {
    if (await controller.current.ask("Discard policy recovery", "An uncertain save may already have succeeded. Discard the local policy request and edits?", ["Keep editing", "Discard"]) !== "Discard") return;
    persist(); setPending(undefined); setMalformed(false); setDirty(false); setDraft(emptyDraft()); await reload();
  }
  async function save() {
    if (!studio.session || !page || busy) return;
    const request = pending ?? { ...draft, request_id: crypto.randomUUID() };
    const abort = new AbortController(); active.current?.abort(); active.current = abort;
    setBusy(true); setError(""); setSaved(undefined); setHistory(undefined); setPending(request);
    persist({ draft, pending: request });
    try {
      const result = await api<SavedPolicy>(root, { method: "POST", body: JSON.stringify(request), signal: abort.signal }, studio.session.csrf);
      if (abort.signal.aborted) return;
      persist(); setPending(undefined); setSaved(result); setDirty(false);
      setDraft({ path: result.path, expected_revision: result.revision, evaluation: result.evaluation });
      const latest = page.items.find(item => item.path === result.path);
      setPage({ ...page, items: [...page.items.filter(item => item.path !== result.path), latest && latest.revision > result.revision ? latest : result].sort((a, b) => a.path.localeCompare(b.path)) });
      controller.current.changed();
    } catch (cause) {
      if (abort.signal.aborted) return;
      if (cause instanceof APIError && [401, 403, 404].includes(cause.status)) {
        clearVisible(); setError("Evaluation policies are unavailable. Retry or check Volume access.");
      } else if (cause instanceof APIError && cause.status < 500) {
        persist({ draft }); setPending(undefined); setDirty(true);
        setError(cause.status === 409 ? "Policy changed. Reload policies, review the current revision, then explicitly use it before saving your edits." : "Policy save was rejected. Check the path and exact published references.");
      } else setError("Policy save is unconfirmed. Recover the same request before making another change.");
    } finally { if (!abort.signal.aborted) setBusy(false); }
  }
  async function loadHistory() {
    const abort = new AbortController(); active.current?.abort(); active.current = abort; setBusy(true); setError("");
    try {
      const result = await api<PolicyHistory>(`${root}/history?${new URLSearchParams({ path, before: String(history?.next ?? 0) })}`, { signal: abort.signal });
      if (!abort.signal.aborted) setHistory({ ...result, items: [...history?.items ?? [], ...result.items] });
    } catch (cause) {
      if (abort.signal.aborted) return;
      if (cause instanceof APIError && [401, 403, 404].includes(cause.status)) clearVisible();
      setHistory(undefined); setError("Policy history is unavailable. Reload policies or check Volume access.");
    } finally { if (!abort.signal.aborted) setBusy(false); }
  }
  return <section className="path-policy-editor" aria-label={tr("Evaluation policies")}>
    <h2>{tr("Evaluation policies")}</h2>
    <p>{tr("Path policies apply to manual and generated changes. Overlapping checks combine; disabling one policy does not remove other requirements.")}</p>
    <button disabled={busy} onClick={() => void reload()}>{tr("Reload policies")}</button>
    {error && <p role="alert">{tr(error)}</p>}
    {localError && <p role="alert">{tr(localError)}</p>}
    {page && <>
      <p>{tr("Owner group")}: {page.group_id || tr("No owner group")}</p>
      {!page.group_id && <p role="status">{tr("Activate a group output for this Volume before configuring evaluation policies.")}</p>}
      {!page.can_configure && <p role="status">{tr("Volume configure access is required to save policy revisions.")}</p>}
      <p>{tr("Evaluation requests still require an active group budget, exact published profile and evaluator, and a dataset use grant.")}</p>
      {malformed && <p role="alert">{tr("Local policy recovery is invalid. Discard it to continue.")}</p>}
      {dirty && !pending && <p role="status">{tr("Policy edits are retained in this tab until saved or discarded.")}</p>}
      {pending && <p role="status">{tr("A policy request is pending. Retry recovers the same revision.")}</p>}
      {(pending || dirty || malformed) && <button disabled={busy} onClick={() => void discard()}>{tr("Discard policy recovery")}</button>}
      <fieldset disabled={busy || !!pending || malformed}>
        <label>{tr("Existing policy")}<select aria-label={tr("Existing policy")} value={current?.id ?? ""} onChange={event => void choose(event.target.value)}><option value="">{tr("New policy")}</option>{page.items.map(item => <option key={item.id} value={item.id}>{item.path || tr("Whole Volume")} · {tr(modeLabels[item.evaluation.mode])} · {item.revision}</option>)}</select></label>
        <fieldset disabled={!page.can_configure || !page.group_id}><form onSubmit={event => { event.preventDefault(); void save(); }}>
          <label>{tr("Policy path")}<input aria-label={tr("Policy path")} value={path} onChange={event => edit({ ...draft, path: event.target.value, expected_revision: 0 })} /></label>
          <p>{tr("Leave the path empty to cover the whole Volume. A path includes its descendants.")}</p>
          <p>{tr("Expected policy revision")}: {draft.expected_revision} · {tr("Current policy revision")}: {current?.revision ?? 0}</p>
          {conflict && <p role="alert">{tr("Review the current policy before replacing its revision. Your edits are retained.")}</p>}
          {conflict && <button type="button" onClick={() => edit({ ...draft, expected_revision: current?.revision ?? 0 })}>{tr("Use current policy revision")}</button>}
          {conflict && current && <details><summary>{tr("Current policy")}</summary><PolicyDetails value={current.evaluation} /><code>{current.digest}</code></details>}
          <label>{tr("Evaluation mode")}<select aria-label={tr("Evaluation mode")} value={policy.mode} onChange={event => { const mode = event.target.value as Policy["mode"]; update(mode === "disabled" ? off() : { ...policy, mode, profile: policy.profile ?? emptyRevision(), service: policy.service ?? emptyRevision() }); }}>{Object.entries(modeLabels).map(([value, label]) => <option key={value} value={value}>{tr(label)}</option>)}</select></label>
          {policy.mode !== "disabled" && <>
            <RevisionFields title={message("Evaluation profile")} value={policy.profile!} onChange={value => update({ ...policy, profile: value })} />
            <RevisionFields title={message("Evaluation service")} value={policy.service!} onChange={value => update({ ...policy, service: value })} />
            <label>{tr("Reference strategy")}<select aria-label={tr("Reference strategy")} value={policy.reference_strategy} onChange={event => { const strategy = event.target.value as Policy["reference_strategy"]; update({ ...policy, reference_strategy: strategy, reference_release: strategy === "pinned_release" ? { publisher_id: "", library_id: "", version_id: "", descriptor_digest: "" } : undefined }); }}><option value="none">{tr("No comparison reference")}</option><option value="target_main">{tr("Accepted main")}</option><option value="generation_base">{tr("Branch initial base")}</option><option value="pinned_release">{tr("Pinned release")}</option></select></label>
            {policy.reference_release && <ReleaseFields value={policy.reference_release} onChange={value => update({ ...policy, reference_release: value })} />}
          </>}
          <button disabled={conflict} type="submit">{tr("Save policy revision")}</button>
        </form></fieldset>
      </fieldset>
      {pending && <button disabled={busy || malformed || !page.can_configure} onClick={() => void save()}>{tr("Recover policy save")}</button>}
      {saved && <p role="status">{tr("Saved policy revision")}: {saved.revision} · <code>{saved.digest}</code></p>}
      {current && <button disabled={busy || !!pending || !!history && !history.next} onClick={() => void loadHistory()}>{tr(history ? "Load earlier policy revisions" : "Policy revision history")}</button>}
      {history && <ol aria-label={tr("Policy revision history")}>{history.items.map(item => <li key={item.revision}><p>{tr("Revision")}: {item.revision} · {tr(modeLabels[item.evaluation.mode])}</p><code>{item.digest}</code><details><summary>{tr("Policy")}</summary><PolicyDetails value={item.evaluation} /></details></li>)}</ol>}
      <p>{tr("Open integrations retain their previous requirements. Use an explicit policy rebind and a new approval to relax them.")}</p>
    </>}
  </section>;
}

function PolicyDetails({value}: {value: Policy}) {
  const tr = useT();
  return <dl>
    <dt>{tr("Evaluation mode")}</dt><dd>{tr(modeLabels[value.mode])}</dd>
    {value.profile && <><dt>{tr("Evaluation profile")}</dt><dd>{value.profile.id} · {value.profile.revision}<br/><code>{value.profile.digest}</code></dd></>}
    {value.service && <><dt>{tr("Evaluation service")}</dt><dd>{value.service.id} · {value.service.revision}<br/><code>{value.service.digest}</code></dd></>}
    <dt>{tr("Reference strategy")}</dt><dd>{tr(value.reference_strategy)}</dd>
    {value.reference_release && <><dt>{tr("Pinned release")}</dt><dd>{value.reference_release.publisher_id} / {value.reference_release.library_id} / {value.reference_release.version_id}<br/><code>{value.reference_release.descriptor_digest}</code></dd></>}
  </dl>;
}

function RevisionFields({title, value, onChange}: {title: Message; value: Revision; onChange: (value: Revision) => void}) {
  const tr = useT();
  return <fieldset><legend>{tr(title)}</legend>
    <label>{tr("ID")}<input aria-label={`${tr(title)} ${tr("ID")}`} required pattern={idPattern} value={value.id} onChange={event => onChange({...value, id:event.target.value})} /></label>
    <label>{tr("Revision")}<input aria-label={`${tr(title)} ${tr("Revision")}`} required type="number" min={1} max={Number.MAX_SAFE_INTEGER} step={1} value={value.revision} onChange={event => onChange({...value, revision:Number(event.target.value)})} /></label>
    <label>{tr("Digest")}<input aria-label={`${tr(title)} ${tr("Digest")}`} required pattern="sha256:[a-f0-9]{64}" value={value.digest} onChange={event => onChange({...value, digest:event.target.value})} /></label>
  </fieldset>;
}

function ReleaseFields({value, onChange}: {value: Release; onChange: (value: Release) => void}) {
  const tr = useT();
  return <fieldset><legend>{tr("Pinned release")}</legend>
    <label>{tr("Publisher ID")}<input aria-label={tr("Publisher ID")} required pattern={idPattern} value={value.publisher_id} onChange={event => onChange({...value, publisher_id:event.target.value})} /></label>
    <label>{tr("Library ID")}<input aria-label={tr("Library ID")} required pattern={idPattern} value={value.library_id} onChange={event => onChange({...value, library_id:event.target.value})} /></label>
    <label>{tr("Version ID")}<input aria-label={tr("Version ID")} required pattern={idPattern} value={value.version_id} onChange={event => onChange({...value, version_id:event.target.value})} /></label>
    <label>{tr("Descriptor digest")}<input aria-label={tr("Descriptor digest")} required pattern="sha256:[a-f0-9]{64}" value={value.descriptor_digest} onChange={event => onChange({...value, descriptor_digest:event.target.value})} /></label>
  </fieldset>;
}
