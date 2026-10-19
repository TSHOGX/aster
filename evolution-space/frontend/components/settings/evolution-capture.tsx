"use client";
import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/central";
import { useT } from "@/lib/i18n";
import { Select } from "@web/select";
import { formatQuantity } from "@web/i18n/locale";
import { backfillInterval, budgetFields, budgetLabels, budgetText, dependencySelection, localInputTime, parseBackfillBudgets, type Backfill, type Binding, type BudgetText, type Collector, type Configuration, type ConfigurationState, type Permissions, type TriggerPage } from "@/lib/evolution";

type Command = { action: "preview" | "execute" | "cancel" | "grant" | "capture" | "seal"; id?: string; body: Record<string, unknown> };
const actions = new Set(["preview", "execute", "cancel", "grant", "capture", "seal"]);
const emptyPage: TriggerPage = { items: [], next: 0 };
const enc = encodeURIComponent;
function message(error: unknown) { return error instanceof Error ? error.message : String(error); }
export function EvolutionCapture({ group, onDirty }: { group: string; onDirty: (value: boolean) => void }) {
  const tr = useT();
  const [data, setData] = useState<{ name: string; permissions: Permissions; state: ConfigurationState | null; configuration: Configuration | null } | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const permissions = await api<Permissions>(`/api/admin/groups/${enc(group)}/optimization/permissions`, { signal });
      let state: ConfigurationState | null = null, configuration: Configuration | null = null;
      try { state = await api<ConfigurationState>(`/api/admin/groups/${enc(group)}/optimization`, { signal }); }
      catch (error) { if (!(error instanceof APIError && error.status === 404)) throw error; }
      if (state?.active_revision) configuration = (await api<{ configuration: Configuration }>(`/api/admin/groups/${enc(group)}/optimization/revisions/${state.active_revision}`, { signal })).configuration;
      if (!signal?.aborted) { setData({ name: permissions.group_name, permissions, state, configuration }); setError(""); }
    } catch (error) { if (!signal?.aborted) { setData(null); setError(message(error)); } }
  }, [group]);
  useEffect(() => { const abort = new AbortController(); const initial = setTimeout(() => void load(abort.signal), 0);
    const refresh = () => { if (!document.hidden) void load(abort.signal); };
    const timer = setInterval(refresh, 15000); document.addEventListener("visibilitychange", refresh);
    return () => { abort.abort(); clearTimeout(initial); clearInterval(timer); document.removeEventListener("visibilitychange", refresh); };
  }, [load]);
  return <section className="evolution-capture"><header className="page-heading"><div><h1>{tr("Sources and corpus")}</h1><p>{tr("Inspect approved sources and review exact historical evidence before running a batch.")}</p></div></header>
    {error && <div role="alert" className="error">{tr(error)} <button onClick={() => void load()}>{tr("Retry loading")}</button></div>}
    {!data && !error && <p role="status">{tr("Loading group evidence settings…")}</p>}
    {data && <CaptureWorkbench key={`${group}:${data.permissions.account_id}`} group={group} {...data} onDirty={onDirty} reload={load} />}
  </section>;
}
function CaptureWorkbench({ group, permissions, state, configuration, onDirty, reload }: { group: string; permissions: Permissions; state: ConfigurationState | null; configuration: Configuration | null; onDirty: (value: boolean) => void; reload: () => Promise<void> }) {
  const tr = useT(), base = `/api/admin/groups/${enc(group)}`;
  const storageKey = `evolution-request:${permissions.account_id}:${group}`;
  const [pending, setPending] = useState<Command | null>(null), [restored, setRestored] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const inFlight = useRef(false), alive = useRef(true);
  const [from, setFrom] = useState(""), [until, setUntil] = useState(""), [limit, setLimit] = useState("16");
  const [selected, setSelected] = useState<string[]>([]), [budgets, setBudgets] = useState<Record<string, BudgetText>>({});
  const [preview, setPreview] = useState<Backfill | null>(null), [previewID, setPreviewID] = useState(""), [reviewed, setReviewed] = useState(false);
  const [collectors, setCollectors] = useState<Collector[]>([]), [page, setPage] = useState<TriggerPage>(emptyPage), [cursors, setCursors] = useState([0]), [statusError, setStatusError] = useState("");
  const [sourceID, setSourceID] = useState(""), [execution, setExecution] = useState(""), [after, setAfter] = useState("0"), [snapshotLimit, setSnapshotLimit] = useState("32");
  const [snapshot, setSnapshot] = useState<{ id: string; through_sequence: number; members: unknown[] } | null>(null), [frontier, setFrontier] = useState<{ through_sequence: number; snapshot_id: string; run_id: string } | null>(null), [frontierBinding, setFrontierBinding] = useState("");
  const sources = configuration?.sources || [], bindings = configuration?.bindings || [];
  const cursor = cursors.at(-1)!;
  const locked = busy || !!pending || !restored;
  const active = state?.state === "active" && !!configuration;
  const policiesValid = sources.every(source => permissions.sources.some(status => status.source_id === source.id && status.valid));
  const sourceUsable = (id: string) => permissions.sources.some(status => status.source_id === id && status.valid) && !!sources.find(source => source.id === id && source.derivative_publication && source.audience_groups.includes(group));
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    const initial = setTimeout(() => {
    try {
      const raw = sessionStorage.getItem(storageKey);
      if (raw) {
        if (raw.length > 65536) throw new Error("Saved request is invalid.");
        const value = JSON.parse(raw) as Command;
        if (!actions.has(value.action) || !value.body || typeof value.body.request_id !== "string") throw new Error("Saved request is invalid.");
        setPending(value);
      }
      setRestored(true);
    } catch (error) { setError(message(error)); }
    setFrom(localInputTime(new Date(Date.now() - 86400000))); setUntil(localInputTime(new Date()));
    setPreviewID(new URLSearchParams(location.search).get("backfill") || "");
    }, 0); return () => clearTimeout(initial);
  }, [storageKey]);
  useEffect(() => { onDirty(!!pending || selected.length > 0 && !preview); return () => onDirty(false); }, [pending, selected.length, preview, onDirty]);
  const refreshStatus = useCallback(async (signal?: AbortSignal) => {
    const results = await Promise.allSettled([api<Collector[]>(`${base}/corpus/collectors`, { signal }), api<TriggerPage>(`${base}/evolution/triggers?after=${cursor}`, { signal })]);
    if (signal?.aborted) return;
    const [capture, triggers] = results;
    setCollectors(capture.status === "fulfilled" ? capture.value : []); setPage(triggers.status === "fulfilled" ? triggers.value : emptyPage);
    setStatusError(results.filter(value => value.status === "rejected").map(value => message((value as PromiseRejectedResult).reason)).join(" · "));
  }, [base, cursor]);
  useEffect(() => { const abort = new AbortController(); const initial = setTimeout(() => void refreshStatus(abort.signal), 0); const timer = setInterval(() => void refreshStatus(abort.signal), 5000); return () => { abort.abort(); clearTimeout(initial); clearInterval(timer); }; }, [refreshStatus]);
  function acceptPreview(value: Backfill) {
    setPreview(value); setPreviewID(value.id); setReviewed(false);
    const url = new URL(location.href); url.searchParams.set("backfill", value.id); history.replaceState(null, "", url);
  }
  const readPreview = useCallback(async (id: string, signal?: AbortSignal) => {
    if (!id) return;
    try { const value = await api<Backfill>(`${base}/evolution/backfills/${enc(id)}${permissions.can_read_evidence ? "" : "/status"}`, { signal }); if (!signal?.aborted) { setPreview(value); setPreviewID(value.id); setReviewed(false); setError(""); const url = new URL(location.href); url.searchParams.set("backfill", value.id); history.replaceState(null, "", url); } }
    catch (error) {
      if (signal?.aborted) return;
      setReviewed(false);
      if (error instanceof APIError && error.status === 403) {
        try { const status = await api<Backfill>(`${base}/evolution/backfills/${enc(id)}/status`, { signal }); if (!signal?.aborted) { setPreview(status); setError("Evidence is unavailable. You can still cancel your own unprepared backfill."); } return; }
        catch { if (signal?.aborted) return; }
      }
      setPreview(null); setError(message(error));
    }
  }, [base, permissions.can_read_evidence]);
  useEffect(() => {
    const abort = new AbortController(); const id = new URLSearchParams(location.search).get("backfill"); const initial = setTimeout(() => { if (id) void readPreview(id, abort.signal); }, 0);
    return () => { abort.abort(); clearTimeout(initial); };
  }, [readPreview]);
  async function submit(command: Command) {
    if (inFlight.current || !restored) return;
    inFlight.current = true; setBusy(true); setError(""); setNotice("");
    let confirmed = false;
    try {
      sessionStorage.setItem(storageKey, JSON.stringify(command)); setPending(command);
      const path = command.action === "preview" ? `${base}/evolution/backfills/preview` : command.action === "grant" ? `${base}/optimization/grants` : command.action === "capture" ? `${base}/corpus/capture` : command.action === "seal" ? `${base}/corpus/snapshots` : `${base}/evolution/backfills/${enc(command.id || "")}/${command.action}`;
      const result = await mutate<Backfill | { id: string; through_sequence: number; members: unknown[]; capture_state: string }>(path, command.body, command.action === "grant" ? "PUT" : "POST");
      confirmed = true; sessionStorage.removeItem(storageKey);
      if (!alive.current) return;
      setPending(null); setNotice("Request confirmed.");
      if (["preview", "execute"].includes(command.action)) acceptPreview(result as Backfill);
      if (command.action === "cancel") { setPreview(null); setReviewed(false); setNotice("Backfill cancelled."); }
      if (command.action === "seal") setSnapshot(result as { id: string; through_sequence: number; members: unknown[] });
      if (command.action === "capture") setNotice(`Capture state: ${(result as { capture_state: string }).capture_state}`);
      if (command.action === "grant") await reload();
      await refreshStatus();
    } catch (error) {
      if (!alive.current) return;
      if (confirmed || error instanceof APIError && error.status >= 400 && error.status < 500 && error.code !== "outcome_unknown") { sessionStorage.removeItem(storageKey); setPending(null); }
      setError(error instanceof APIError && error.status === 409 ? "The configuration or preview changed. Reload current data before trying again." : message(error));
    } finally { inFlight.current = false; if (alive.current) setBusy(false); }
  }
  let chosen: string[] = [], selectionError = "";
  try { chosen = dependencySelection(bindings, selected); } catch (error) { selectionError = message(error); }
  function previewBackfill() {
    try {
      const interval = backfillInterval(from, until), parsed = parseBackfillBudgets(bindings, selected, budgets), maximum = Number(limit);
      if (!/^\d+$/.test(limit) || !Number.isInteger(maximum) || maximum < 1 || maximum > 32) throw new Error("Choose an episode limit from 1 to 32.");
      void submit({ action: "preview", body: { request_id: crypto.randomUUID(), configuration_revision: configuration!.revision, ...interval, max_episodes: maximum, budgets: parsed } });
    } catch (error) { setError(message(error)); }
  }
  const source = sources.find(value => value.id === sourceID);
  async function readFrontier() {
    try { setFrontier(await api(`${base}/corpus/bindings/${enc(frontierBinding)}/sources/${enc(sourceID)}/frontier`)); setError(""); }
    catch (error) { setFrontier(null); setError(message(error)); }
  }
  const ownPreview = preview?.requested_by === permissions.account_id;
  return <>
    {error && <p className="error" role="alert">{tr(error)}</p>}{notice && <p role="status">{tr(notice)}</p>}
    {!restored && error && <section className="notice"><p>{tr("Recovery data could not be read. Inspect existing work before clearing the saved request.")}</p><button onClick={() => { try { sessionStorage.removeItem(storageKey); setRestored(true); setError(""); } catch (error) { setError(message(error)); } }}>{tr("Clear invalid recovery data")}</button></section>}
    {pending && <section className="notice"><p>{tr("An original request is awaiting confirmation. Reloading preserves its identity.")}</p><button disabled={busy} onClick={() => void submit(pending)}>{tr("Check original request")}</button></section>}
    <section className="panel"><h2>{tr("Current configuration")}</h2>{state ? <><p>{tr("Active revision")} {formatQuantity(state.active_revision)} · {tr("Desired revision")} {formatQuantity(state.desired_revision)} · {tr(state.state)}</p><p>{tr(state.auto_enabled ? "Automatic optimization is enabled." : "Automatic optimization is off. Authorized manual backfill remains available.")}</p></> : <p>{tr("No active optimization configuration. Configure sources and bindings before backfill.")}</p>}<button disabled={locked} onClick={() => void reload()}>{tr("Reload current data")}</button><Link href={`/groups/${enc(group)}/settings/configuration/`}>{tr("Configuration and activation")}</Link></section>
    {!permissions.can_read_evidence && <section className="panel"><h2>{tr("Evidence access")}</h2><p>{tr("Reading corpus evidence requires an explicit source-read grant, including for administrators.")}</p>{permissions.can_manage_grants && <button disabled={locked} onClick={() => void submit({ action: "grant", body: { request_id: crypto.randomUUID(), account_id: permissions.account_id, action: "source.read", effect: "allow", expected_revision: permissions.evidence_grant_revision } })}>{tr("Grant myself evidence access")}</button>}</section>}
    {(!permissions.can_read_evidence || !policiesValid || !preview?.episodes) && preview && ["preview", "queued"].includes(preview.state) && ownPreview && permissions.can_manage_grants && <button disabled={locked} onClick={() => void submit({ action: "cancel", id: preview.id, body: { request_id: crypto.randomUUID(), digest: preview.digest } })}>{tr("Cancel backfill")}</button>}
    <section className="panel"><h2>{tr("Configured sources")}</h2>{!sources.length && <p>{tr("No sources in the active configuration.")}</p>}<div className="evolution-cards">{sources.map(value => { const collector = collectors.find(item => item.source_id === value.id); return <article className="volume-card" key={value.id}><h3>{value.id}</h3><p>{tr(value.kind)} · {value.resource_id}</p>{!permissions.sources.some(status => status.source_id === value.id && status.valid) && <p role="alert">{tr("Source policy changed or is unavailable.")}</p>}<p>{tr("Policy revision")} {value.policy_revision} · {tr(sourceUsable(value.id) ? "Derivative publication allowed" : "Source reuse is unavailable")}</p><p>{tr("Exclusions")}: {value.exclusions?.join(", ") || tr("None")}</p>{collector ? <><p>{tr("Processed event")} {collector.after_event} · {tr("Sealed through")} {collector.sealed_through}</p>{collector.snapshot_id && permissions.can_read_evidence && permissions.can_manage && sourceUsable(value.id) && <a href={`${base}/corpus/snapshots/${enc(collector.snapshot_id)}/content`} download>{tr("Download latest snapshot")}</a>}</> : <p>{tr("Automatic collection is not active for this source.")}</p>}</article>; })}</div></section>
    <section className="panel"><h2>{tr("Historical backfill")}</h2><p>{tr("Preview up to 32 terminal episodes in a range of at most 90 days. Oversized selections are rejected, never truncated.")}</p>
      {!policiesValid && <p role="alert">{tr("Source permissions changed. Review the active configuration.")}</p>}
      {permissions.can_read_evidence && !permissions.can_backfill && <p>{tr("Backfill requires configuration management and run-start permission.")}</p>}
      <form onChange={() => setReviewed(false)} onSubmit={event => { event.preventDefault(); previewBackfill(); }}><fieldset disabled={locked || !permissions.can_backfill || !active || !policiesValid}>
        <div className="runtime-fields"><label>{tr("From (local time, included)")}<input type="datetime-local" required value={from} onChange={event => setFrom(event.target.value)} /></label><label>{tr("Until (local time, excluded)")}<input type="datetime-local" required value={until} onChange={event => setUntil(event.target.value)} /></label><label>{tr("Maximum episodes")}<input type="number" min={1} max={32} required value={limit} onChange={event => setLimit(event.target.value)} /></label></div>
        <h3>{tr("Bindings to run")}</h3>{bindings.filter(binding => binding.enabled).map(binding => <label className="check-label" key={binding.id}><input type="checkbox" checked={selected.includes(binding.id)} onChange={event => setSelected(values => event.target.checked ? [...values, binding.id] : values.filter(id => id !== binding.id))} />{binding.id} · {binding.output.prefix}</label>)}
        <p>{tr("Required prerequisite bindings are included automatically. Their budgets also need review.")}</p>{selectionError && <p role="alert">{tr(selectionError)}</p>}
        {chosen.map(id => { const binding = bindings.find(value => value.id === id)!; return <BindingBudget key={id} binding={binding} value={budgets[id] || budgetText(binding.budget)} setValue={value => setBudgets(values => ({ ...values, [id]: value }))} />; })}
        <button className="primary" disabled={!chosen.length || !!selectionError}>{tr("Preview historical backfill")}</button>
      </fieldset></form>
      <form className="filters" onSubmit={event => { event.preventDefault(); setReviewed(false); void readPreview(previewID); }}><label>{tr("Backfill ID")}<input value={previewID} onChange={event => setPreviewID(event.target.value)} required maxLength={128} /></label><button disabled={busy}>{tr("Load backfill")}</button></form>
      {preview && permissions.can_read_evidence && policiesValid && preview.episodes && <section className="evolution-review" aria-label={tr("Backfill review")}><h3>{tr("Frozen selection")}</h3><p>{tr("Backfill ID")}: {preview.id}</p><p>{tr("State")}: {tr(preview.state)} {preview.last_error && `· ${tr(preview.last_error)}`}</p><p>{tr("Configuration revision")}: {preview.configuration_revision}</p><p><time>{preview.from}</time> → <time>{preview.until}</time></p><p>{tr("Maximum tokens")}: {formatQuantity(preview.maximum_tokens || 0)} · {tr("Maximum cost (micros)")}: {formatQuantity(preview.maximum_cost_micros || 0)}</p>
        {configuration?.revision !== preview.configuration_revision && <p role="alert">{tr("The active configuration changed. Create a new preview to execute this selection.")}</p>}{!ownPreview && <p>{tr("Only the account that created this preview can execute or cancel it.")}</p>}
        <p>{tr("These exact input bytes are retained. Later evidence requires a new preview.")}</p><ul className="evolution-episodes">{(preview.episodes || []).map(item => <li key={`${item.source_id}/${item.execution_id}`}><div><strong>{tr("Source / execution")}</strong><p>{item.source_id}<small>{item.execution_id}</small></p></div><div><strong>{tr("Evidence family")}</strong><p>{item.family_id}<small>{tr(item.family_kind)}</small></p></div><div><strong>{tr("Capture / outcome")}</strong><p>{tr(item.capture_state)} · {tr(item.outcome)}{item.truncated && <strong> · {tr("Partial evidence")}</strong>}</p></div></li>)}</ul>
        <ul>{Object.keys(preview.budgets || {}).map(id => <li key={id}>{id}<ul>{(preview.download_inputs?.[id] || []).map(port => <li key={port}><a download href={`${base}/evolution/backfills/${enc(preview.id)}/inputs/${enc(id)}/${enc(port)}`}>{tr("Download frozen input")} · {port}</a></li>)}</ul></li>)}</ul>
        <details><summary>{tr("Exact identity and budgets")}</summary><pre>{JSON.stringify({ digest: preview.digest, budgets: preview.budgets, runs: preview.runs }, null, 2)}</pre></details>
        {preview.state === "preview" && <label className="check-label"><input type="checkbox" checked={reviewed} disabled={locked} onChange={event => setReviewed(event.target.checked)} />{tr("I reviewed the selected evidence and spending ceilings.")}</label>}
        <div className="form-actions">{preview.state === "preview" && <button className="primary" disabled={locked || !reviewed || !ownPreview || !permissions.can_backfill || configuration?.revision !== preview.configuration_revision || !active || !policiesValid} onClick={() => void submit({ action: "execute", id: preview.id, body: { request_id: crypto.randomUUID(), digest: preview.digest } })}>{tr("Execute reviewed backfill")}</button>}{["preview", "queued"].includes(preview.state) && ownPreview && <button disabled={locked} onClick={() => void submit({ action: "cancel", id: preview.id, body: { request_id: crypto.randomUUID(), digest: preview.digest } })}>{tr("Cancel backfill")}</button>}<button disabled={busy} onClick={() => void readPreview(preview.id)}>{tr("Refresh backfill")}</button></div>
      </section>}
    </section>
    <details className="panel"><summary>{tr("Capture an execution or seal a snapshot")}</summary><p>{tr("Explicit capture does not run a workflow. Snapshot sealing does not consume a binding’s frontier.")}</p><label>{tr("Source")}<Select aria-label={tr("Source")} value={sourceID} onChange={event => { setSourceID(event.target.value); setSnapshot(null); setFrontier(null); }}><option value="">{tr("Choose a source")}</option>{sources.filter(source => source.kind !== "volume").map(source => <option key={source.id} value={source.id} disabled={!sourceUsable(source.id)}>{source.id}</option>)}</Select></label>
      <form onSubmit={event => { event.preventDefault(); if (source) void submit({ action: "capture", body: { request_id: crypto.randomUUID(), source_id: source.id, source_revision: source.policy_revision, execution_id: execution } }); }}><fieldset disabled={locked || !permissions.can_manage || !permissions.can_read_evidence || !source || !sourceUsable(source.id)}><label>{tr("Execution ID")}<input required pattern="[a-zA-Z0-9_-]+" maxLength={128} value={execution} onChange={event => setExecution(event.target.value)} /></label><button>{tr("Capture execution")}</button></fieldset></form>
      <form onSubmit={event => { event.preventDefault(); if (source && Number.isSafeInteger(Number(after)) && Number(after) >= 0) void submit({ action: "seal", body: { request_id: crypto.randomUUID(), source_id: source.id, source_revision: source.policy_revision, after_sequence: Number(after), limit: Number(snapshotLimit) } }); }}><fieldset disabled={locked || !permissions.can_manage || !permissions.can_read_evidence || !source || !sourceUsable(source.id)}><div className="runtime-fields"><label>{tr("After sequence")}<input type="number" min={0} max={Number.MAX_SAFE_INTEGER} required value={after} onChange={event => setAfter(event.target.value)} /></label><label>{tr("Snapshot episode limit")}<input type="number" min={1} max={100} required value={snapshotLimit} onChange={event => setSnapshotLimit(event.target.value)} /></label></div><button>{tr("Seal snapshot")}</button></fieldset></form>
      {snapshot && permissions.can_read_evidence && permissions.can_manage && <p role="status">{tr("Sealed through")} {snapshot.through_sequence} · {snapshot.members.length} {tr("episodes")} <a download href={`${base}/corpus/snapshots/${enc(snapshot.id)}/content`}>{tr("Download snapshot")}</a></p>}
      <label>{tr("Binding frontier")}<Select aria-label={tr("Binding frontier")} value={frontierBinding} onChange={event => { setFrontierBinding(event.target.value); setFrontier(null); }}><option value="">{tr("Choose a binding")}</option>{bindings.map(binding => <option key={binding.id}>{binding.id}</option>)}</Select></label><button disabled={!sourceID || !frontierBinding || busy} onClick={() => void readFrontier()}>{tr("Inspect consumption frontier")}</button>{frontier && <p>{tr("Consumed through")} {frontier.through_sequence} · {frontier.run_id || tr("No successful consumption")}</p>}
    </details>
    <section className="panel"><h2>{tr("Queued and dispatched work")}</h2>{statusError && <p role="alert">{tr(statusError)}</p>}<button disabled={busy} onClick={() => void refreshStatus()}>{tr("Refresh work")}</button>{!page.items.length && !statusError && <p>{tr("No work on this page.")}</p>}<div className="table-scroll"><table><thead><tr><th>{tr("Binding / run")}</th><th>{tr("Trigger")}</th><th>{tr("Dispatch")}</th><th>{tr("Details")}</th></tr></thead><tbody>{page.items.map(item => <tr key={item.id}><td>{item.binding_id}<small>{item.run_id || "—"}</small></td><td>{tr(item.kind)}</td><td>{tr(item.dispatch_state || item.state)}<small>{tr(item.dispatch_error || item.pending_reason)}</small></td><td><details><summary>{tr("Selected range")}</summary><p>{tr("Configuration revision")} {item.configuration_revision}</p>{item.ranges.map(range => <p key={range.source_id}>{range.source_id}: {range.after_sequence} → {range.through_sequence}</p>)}<p>{item.first_at}</p></details></td></tr>)}</tbody></table></div><nav className="pagination" aria-label={tr("Work pages")}><button disabled={cursors.length === 1 || busy} onClick={() => setCursors(values => values.slice(0, -1))}>{tr("Previous")}</button><button disabled={page.items.length < 50 || busy} onClick={() => setCursors(values => [...values, page.next])}>{tr("Next")}</button><button disabled={busy || cursor === 0} onClick={() => setCursors([0])}>{tr("Newest status from first page")}</button></nav></section>
  </>;
}
function BindingBudget({ binding, value, setValue }: { binding: Binding; value: BudgetText; setValue: (value: BudgetText) => void }) {
  const tr = useT();
  const field = (key: typeof budgetFields[number]) => <label key={key}>{tr(budgetLabels[key])}<input aria-label={`${binding.id}: ${tr(budgetLabels[key])}`} inputMode="numeric" pattern="[0-9]+" required value={value[key]} onChange={event => setValue({ ...value, [key]: event.target.value })} /><small>{tr("Ceiling")}: {Number.isSafeInteger(binding.budget[key]) ? formatQuantity(binding.budget[key]) : tr("This ceiling exceeds the browser’s exact-integer range. Enter a lower whole-number budget.")}</small></label>;
  return <fieldset className="evolution-budget"><legend>{binding.id}</legend><p>{binding.template.id} · {tr("Workflow revision")} {binding.workflow.revision}</p><div className="evolution-budget-fields">{budgetFields.slice(0, 2).map(field)}</div><details><summary>{tr("Advanced budget limits")}</summary><div className="evolution-budget-fields">{budgetFields.slice(2).map(field)}</div></details></fieldset>;
}
