"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { base } from "../lib/api";
import { ArtifactPreview } from "./artifact-preview";
import { RunExperiment } from "./run-experiment";
import { useT } from "../lib/i18n";

type Run = { draft_test?: { kind: string; node_id?: string; source: { run_id: string }; draft: { id: string; revision: number } }; id: string; state: string; fence: number; handoff: string; evaluation_state: string; experiment_of?: string; retry_of?: string; pins: { workflow?: { id?: string; revision?: number }; service?: { id?: string; revision?: number }; configuration_revision?: number }; budget: { max_tokens: number; max_cost_micros: number; max_attempts: number; timeout_seconds: number; max_artifact_bytes: number }; created_at: string };
type Event = { id: string; sequence: number; kind: string; attempt_id?: string; timestamp: string; artifacts?: { id: string }[] };
type Attempt = { reused_from?: { run_id: string; checkpoint_id: string; attempt_id: string }; call: { id: string; instance: string; parent?: string; number?: number; node: { id?: string; kind?: string; model_ref?: { id?: string; revision?: number }; tools?: { id?: string; revision?: number }[] }; inputs?: Record<string, unknown>; prompt?: string }; state: string; error_code?: string; outputs?: Record<string, unknown>; inactive?: string[] };
type AttemptPage = { items: Attempt[]; next_after: string; has_more: boolean };
type ArtifactPage = { items: Artifact[]; next_after: string; has_more: boolean };
type Artifact = { id: string; digest: string; size: number; media_type: string; schema_digest: string; policy_revision: number };

type Debugger = { budget: { spent: { tokens: number; cost_micros: number }; held: { tokens: number; cost_micros: number }; available: { tokens: number; cost_micros: number } }; counts: { attempts: number; events: number; provider_observations: number }; next_after: string; has_more: boolean; provider_observations: { call: { id: string; idempotency_key: string; request: { id: string; digest: string; size: number } }; outcome: { state: string; provider_request_id: string; usage: { tokens: number; cost_micros: number }; output?: { id: string }; evidence?: { id: string }[] } }[]; opaque: string[] };
function attemptContext(instance: string) {
  const segments = instance.split("/").filter(Boolean);
  const iteration = instance.match(/(?:^|\/)iteration-(\d+)(?:\/|$)/)?.[1];
  const item = instance.match(/(?:^|\/)item-(\d+)(?:\/|$)/)?.[1];
  return { depth: Math.max(0, segments.length - 1), iteration, item };
}

export function RunInspector({ group, run }: { group: string; run: string }) {
  return <Inspector key={`${group}:${run}`} group={group} run={run} />;
}

function Inspector({ group, run }: { group: string; run: string }) {
  const tr = useT(); const router = useRouter(); const [value, setValue] = useState<Run>(); const [comparison, setComparison] = useState<Run>(); const [comparisonDebugger, setComparisonDebugger] = useState<Debugger>(); const [events, setEvents] = useState<Event[]>([]); const [attempts, setAttempts] = useState<Attempt[]>([]); const [artifacts, setArtifacts] = useState<Artifact[]>([]); const [debuggerValue, setDebuggerValue] = useState<Debugger>(); const [error, setError] = useState(""); const [stale, setStale] = useState(false); const [busy, setBusy] = useState(false); const [evidenceReady, setEvidenceReady] = useState(false);
  const attemptPages = useRef(1); const providerPages = useRef(1);
  const [moreAttempts, setMoreAttempts] = useState(false); const [sourceAttempts, setSourceAttempts] = useState<Attempt[]>([]);
  const [moreProviders, setMoreProviders] = useState(false);
  const artifactPages = useRef(1); const [moreArtifacts, setMoreArtifacts] = useState(false);
  const activeRead = useRef<AbortController | null>(null);
  const retainedEvents = useRef<Event[]>([]); const cursor = useRef(0);
  const root = `${base}/api/v1/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(run)}`;
  const clearEvidence = useCallback(() => {
    retainedEvents.current = []; cursor.current = 0;
    setEvents([]); setAttempts([]); setArtifacts([]); setMoreArtifacts(false); setDebuggerValue(undefined);
    setComparison(undefined); setComparisonDebugger(undefined); setSourceAttempts([]); setMoreAttempts(false); setMoreProviders(false); setEvidenceReady(false);
  }, []);
  const read = useCallback(async (refresh = true) => {
    // Polls never overlap. Explicit refresh supersedes and aborts an older read.
    if (!refresh && activeRead.current) return;
    activeRead.current?.abort();
    const controller = new AbortController(); activeRead.current = controller;
    const options = { credentials: "same-origin", cache: "no-store", signal: controller.signal } as const;
    if (refresh) clearEvidence();
    let current: Run | undefined;
    try {
      const response = await fetch(root, options);
      if (!response.ok) throw new Error();
      const next = await response.json() as Run;
      if (controller.signal.aborted) return;
      current = next;
      const evidence = async (path: string) => {
        const response = await fetch(path, options);
        if (!response.ok) throw new Error("evidence unavailable");
        return response.json();
      };
      const readArtifacts = async () => {
        const items = new Map<string, Artifact>(); let path = `${root}/artifacts`; let more = false;
        for (let index = 0; index < artifactPages.current; index++) {
          const page = await evidence(path) as ArtifactPage;
          for (const item of page.items) items.set(item.id, item);
          more = page.has_more;
          if (!more) break;
          path = `${root}/artifacts?after=${encodeURIComponent(page.next_after)}&limit=100`;
        }
        return { items: [...items.values()], has_more: more };
      };
      const readAttempts = async (runRoot: string) => {
        const items = new Map<string, Attempt>(); let path = `${runRoot}/attempts?limit=100`; let more = false;
        for (let index = 0; index < attemptPages.current; index++) {
          const page = await evidence(path) as AttemptPage;
          for (const item of page.items) items.set(item.call.id, item);
          more = page.has_more;
          if (!more) break;
          path = `${runRoot}/attempts?after=${encodeURIComponent(page.next_after)}&limit=100`;
        }
        return { items: [...items.values()], has_more: more };
      };
      const readDebugger = async () => {
        const observations = new Map<string, Debugger["provider_observations"][number]>();
        let path = `${root}/debugger`; let result: Debugger | undefined;
        for (let index = 0; index < providerPages.current; index++) {
          result = await evidence(path) as Debugger;
          for (const item of result.provider_observations) observations.set(item.call.id, item);
          if (!result.has_more) break;
          path = `${root}/debugger?after=${encodeURIComponent(result.next_after)}&limit=100`;
        }
        return { ...result!, provider_observations: [...observations.values()] };
      };
      const [page, attemptPage, artifactPage, debug] = await Promise.all([
        evidence(`${root}/events?after=${cursor.current}&limit=100`),
        readAttempts(root), readArtifacts(), readDebugger(),
      ]) as [{ events: Event[]; next_after: number }, { items: Attempt[]; has_more: boolean }, { items: Artifact[]; has_more: boolean }, Debugger];
      let source: Run | undefined, sourceDebugger: Debugger | undefined;
      let sourcePage: { items: Attempt[]; has_more: boolean } = { items: [], has_more: false };
      let comparisonUnavailable = false;
      if (next.experiment_of) {
        const sourceRoot = `${base}/api/v1/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(next.experiment_of)}`;
        const [sourceResponse, debuggerResponse] = await Promise.all([fetch(sourceRoot, options), fetch(`${sourceRoot}/debugger`, options)]);
        if (sourceResponse.ok && debuggerResponse.ok) {
          [source, sourceDebugger] = await Promise.all([sourceResponse.json(), debuggerResponse.json()]) as [Run, Debugger];
          sourcePage = await readAttempts(sourceRoot);
        } else comparisonUnavailable = true;
      }
      if (controller.signal.aborted) return;
      setValue(next);
      const merged = new Map(retainedEvents.current.map(event => [event.id, event]));
      for (const event of page.events) merged.set(event.id, event);
      retainedEvents.current = [...merged.values()].sort((a, b) => a.sequence - b.sequence);
      cursor.current = Math.max(cursor.current, page.next_after);
      setEvents(retainedEvents.current); setAttempts(attemptPage.items); setArtifacts(artifactPage.items); setMoreArtifacts(artifactPage.has_more); setDebuggerValue(debug);
      setComparison(source); setComparisonDebugger(sourceDebugger); setSourceAttempts(sourcePage.items); setMoreAttempts(attemptPage.has_more || sourcePage.has_more); setMoreProviders(debug.has_more); setEvidenceReady(true);
      setError(comparisonUnavailable ? "Source comparison is unavailable. Retry or check source access." : ""); setStale(comparisonUnavailable);
    } catch {
      if (controller.signal.aborted) return;
      controller.abort(); // Stop sibling reads as soon as any evidence read fails.
      setValue(current); clearEvidence(); setError(current ? "Run evidence is unavailable. Retry or check source access." : "Run detail is unavailable. Retry or check group access."); setStale(true);
    } finally { if (activeRead.current === controller) activeRead.current = null; }
  }, [root, group, clearEvidence]);
  useEffect(() => {
    // The effect synchronizes external projections with this keyed run view.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void read();
    const timer = window.setInterval(() => void read(false), 3000);
    return () => { window.clearInterval(timer); activeRead.current?.abort(); };
  }, [read]);
  async function control(action: string) { if (!value) return; setBusy(true); try { const response = await fetch(`${root}/control`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Origin: location.origin, "X-CSRF-Token": (await (await fetch(`${base}/api/v1/session`, { credentials: "same-origin" })).json() as { csrf: string }).csrf }, body: JSON.stringify({ request_id: crypto.randomUUID(), run_id: run, expected_fence: value.fence, action }) }); if (!response.ok) throw new Error(); await read(); } catch { setError("Run control is unavailable or the run changed. Reload before retrying."); } finally { setBusy(false); } }
  async function retryRun() { if (!value || busy || !["completed", "failed", "cancelled"].includes(value.state)) return; setBusy(true); setError(""); try {
    // Keep this operation stable after a lost reply, including a page reload.
    const sessionResponse = await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" });
    if (!sessionResponse.ok) throw new Error();
    const session = await sessionResponse.json() as { subject: string; csrf: string };
    const storageKey = `evaluation-retry:${session.subject}:${group}:${run}`;
    let retryRequest: { request_id: string; source_run_id: string; new_run_id: string } | undefined;
    const saved = sessionStorage.getItem(storageKey);
    if (saved) {
      try {
        const parsed = JSON.parse(saved) as typeof retryRequest;
        if (parsed?.source_run_id === run && typeof parsed.request_id === "string" && typeof parsed.new_run_id === "string") retryRequest = parsed;
      } catch { sessionStorage.removeItem(storageKey); }
    }
    retryRequest ??= { request_id: crypto.randomUUID(), source_run_id: run, new_run_id: crypto.randomUUID() };
    sessionStorage.setItem(storageKey, JSON.stringify(retryRequest));
    const response = await fetch(`${root}/retry`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(retryRequest) }); if (!response.ok) throw new Error(); const created = await response.json() as { id: string }; sessionStorage.removeItem(storageKey); router.push(`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(created.id)}`); } catch { setError("Run retry is unavailable. Retry to recover the same request, or check run permissions."); } finally { setBusy(false); } }
  const semanticRows = (() => {
    if (!comparisonDebugger || !debuggerValue || moreAttempts) return [] as { instance: string; source?: Attempt; experiment?: Attempt; status: string }[];
    const latest = (items: Attempt[]) => new Map([...items].sort((a, b) => (a.call.number ?? 0) - (b.call.number ?? 0)).map(attempt => [attempt.call.instance, attempt]));
    const source = latest(sourceAttempts);
    const current = latest(attempts);
    const instances = [...new Set([...source.keys(), ...current.keys()])].sort();
    return instances.map(instance => {
      const before = source.get(instance); const after = current.get(instance);
      const same = before && after && before.state === after.state && JSON.stringify(before.outputs ?? {}) === JSON.stringify(after.outputs ?? {}) && JSON.stringify(before.inactive ?? []) === JSON.stringify(after.inactive ?? []);
      return { instance, source: before, experiment: after, status: same ? "unchanged" : before && after ? "changed" : after ? "added" : "removed" };
    });
  })();
  if (!value && error) return <main className="session-gate"><h1>{tr("Run inspector")}</h1><p role="alert">{tr(error)}</p><button onClick={() => void read()}>{tr("Retry")}</button><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/`}>{tr("Back to group")}</Link></main>;
  return <main className="editor-page"><header className="section-heading"><div><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/`}>{tr("Back to group")}</Link><h1>{tr("Run inspector")}: {run}</h1><p>{tr("The workflow, provider and input references are frozen for this run.")}</p></div><button onClick={() => void read()}>{tr("Refresh")}</button></header>{stale && <p role="status">{tr("Connection is stale. Reconnecting from the last event cursor.")}</p>}{error && <p role="alert">{tr(error)}</p>}
    {value && <><section className="card run-summary"><h2>{tr("Run status")}: {tr(value.state === "pause_requested" ? "Pausing" : value.state)}</h2><dl><div><dt>{tr("Workflow revision")}</dt><dd>{value.pins.workflow?.id ?? "—"} @{value.pins.workflow?.revision ?? "—"}</dd></div><div><dt>{tr("Service revision")}</dt><dd>{value.pins.service?.id ?? "—"} @{value.pins.service?.revision ?? "—"}</dd></div><div><dt>{tr("Configuration revision")}</dt><dd>{value.pins.configuration_revision ?? "—"}</dd></div><div><dt>{tr("Handoff")}</dt><dd>{tr(value.handoff)}</dd></div><div><dt>{tr("Evaluation")}</dt><dd>{tr(value.evaluation_state)}</dd></div>{value.draft_test && <><div><dt>{tr("Draft test")}</dt><dd>{tr(value.draft_test.kind)} {value.draft_test.node_id ?? ""}</dd></div><div><dt>{tr("Source run")}</dt><dd><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(value.draft_test.source.run_id)}`}>{value.draft_test.source.run_id}</Link></dd></div></>}{value.retry_of && <div><dt>{tr("Retry source")}</dt><dd><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(value.retry_of)}`}>{value.retry_of}</Link></dd></div>}</dl>{value.state === "pause_requested" && <p role="status">{tr("Pause requested. Current work is finishing before the run pauses.")}</p>}<div className="editor-actions"><button disabled={busy || !["queued", "running"].includes(value.state)} onClick={() => void control("pause")}>{tr("Pause")}</button><button disabled={busy || value.state !== "paused"} onClick={() => void control("resume")}>{tr("Resume")}</button><button disabled={busy || !["queued", "running", "pause_requested", "paused"].includes(value.state)} onClick={() => void control("cancel")}>{tr("Cancel")}</button><button disabled={busy || !["completed", "failed", "cancelled"].includes(value.state)} onClick={() => void retryRun()}>{tr("Retry as new run")}</button></div>{!value.draft_test && evidenceReady && value.pins.workflow?.id && value.budget && <RunExperiment group={group} run={run} budget={value.budget} enabled={!busy && value.state === "paused"} /> }</section>{comparison && <section className="card"><h2>{tr("Experiment comparison")}</h2><p>{tr("This experiment keeps the source run immutable.")}</p><dl><div><dt>{tr("Source run")}</dt><dd><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(comparison.id)}`}>{comparison.id}</Link></dd></div><div><dt>{tr("Source state")}</dt><dd>{tr(comparison.state)}</dd></div><div><dt>{tr("Experiment workflow")}</dt><dd>{value.pins.workflow?.id ?? "—"} @{value.pins.workflow?.revision ?? "—"}</dd></div><div><dt>{tr("Source workflow")}</dt><dd>{comparison.pins.workflow?.id ?? "—"} @{comparison.pins.workflow?.revision ?? "—"}</dd></div><div><dt>{tr("Source evaluation")}</dt><dd>{tr(comparison.evaluation_state)}</dd></div><div><dt>{tr("Experiment evaluation")}</dt><dd>{tr(value.evaluation_state)}</dd></div><div><dt>{tr("Source attempts")}</dt><dd>{comparisonDebugger?.counts.attempts ?? "—"}</dd></div><div><dt>{tr("Experiment attempts")}</dt><dd>{debuggerValue?.counts.attempts ?? "—"}</dd></div><div><dt>{tr("Source events")}</dt><dd>{comparisonDebugger?.counts.events ?? "—"}</dd></div><div><dt>{tr("Experiment events")}</dt><dd>{debuggerValue?.counts.events ?? "—"}</dd></div></dl>{moreAttempts && <p role="status">{tr("Load all attempt pages to compare execution paths.")}</p>}{semanticRows.length > 0 && <><h3>{tr("Semantic attempt comparison")}</h3><table><thead><tr><th>{tr("Execution path")}</th><th>{tr("Source")}</th><th>{tr("Experiment")}</th><th>{tr("Difference")}</th></tr></thead><tbody>{semanticRows.map(row => <tr key={row.instance}><th scope="row"><code>{row.instance}</code></th><td>{row.source ? `${tr(row.source.state)} · ${row.source.call.node.kind ?? "unknown"}` : "—"}</td><td>{row.experiment ? `${tr(row.experiment.state)} · ${row.experiment.call.node.kind ?? "unknown"}` : "—"}</td><td>{tr(row.status)}</td></tr>)}</tbody></table></>}</section>}{debuggerValue && <section className="card"><h2>{tr("Execution debugger")}</h2><dl><div><dt>{tr("Tokens spent")}</dt><dd>{debuggerValue.budget.spent.tokens}</dd></div><div><dt>{tr("Cost")}</dt><dd>{debuggerValue.budget.spent.cost_micros}</dd></div><div><dt>{tr("Held budget")}</dt><dd>{debuggerValue.budget.held.tokens} / {debuggerValue.budget.held.cost_micros}</dd></div><div><dt>{tr("Durable attempts")}</dt><dd>{debuggerValue.counts.attempts}</dd></div><div><dt>{tr("Events")}</dt><dd>{debuggerValue.counts.events}</dd></div></dl>{debuggerValue.opaque.map(item => <p role="status" key={item}>{tr("Opaque provider evidence")}: {item}</p>)}{moreProviders && <><p role="status">{tr("More provider observations are available.")}</p><button onClick={() => { providerPages.current++; void read(false); }}>{tr("Load more provider observations")}</button></>}{debuggerValue.provider_observations.length ? <ul>{debuggerValue.provider_observations.map(observation => <li key={observation.call.id}><code>{observation.call.id}</code> · {tr(observation.outcome.state)} · {observation.outcome.usage.tokens} {tr("tokens")} · {observation.outcome.usage.cost_micros} {tr("cost units")}{observation.outcome.evidence?.length ? <small> · {tr("Provider evidence")}: {observation.outcome.evidence.map(item => <ArtifactPreview key={item.id} root={root} id={item.id} label={item.id} />)}</small> : null}</li>)}</ul> : <p>{tr("No provider observations are visible yet.")}</p>}</section>}<div className="workbench-grid run-grid"><section className="card"><h2>{tr("Node attempts")}</h2>{moreAttempts && <><p role="status">{tr("More attempts are available.")}</p><button onClick={() => { attemptPages.current++; void read(false); }}>{tr("Load more attempts")}</button></>}{!attempts.length && <p>{tr("No node attempts are visible yet.")}</p>}<ol>{attempts.map(attempt => <li key={attempt.call.id}><details><summary><strong>{attempt.call.instance}</strong> · {tr(attempt.state)} {attempt.error_code && <span>({attempt.error_code})</span>}</summary><dl className="attempt-details">{attempt.reused_from && <div><dt>{tr("Reused from checkpoint")}</dt><dd><Link prefetch={false} href={`/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(attempt.reused_from.run_id)}`}>{attempt.reused_from.run_id}</Link> · {attempt.reused_from.checkpoint_id} · {attempt.reused_from.attempt_id}</dd></div>}<div><dt>{tr("Node")}</dt><dd>{attempt.call.node.id ?? "—"} · {tr(attempt.call.node.kind ?? "unknown")}</dd></div><div><dt>{tr("Attempt")}</dt><dd>{attempt.call.number ?? "—"}</dd></div><div><dt>{tr("Parent attempt")}</dt><dd>{attempt.call.parent ?? "—"}</dd></div><div><dt>{tr("Execution depth")}</dt><dd>{attemptContext(attempt.call.instance).depth}</dd></div><div><dt>{tr("Loop iteration")}</dt><dd>{attemptContext(attempt.call.instance).iteration ?? "—"}</dd></div><div><dt>{tr("Map item")}</dt><dd>{attemptContext(attempt.call.instance).item ?? "—"}</dd></div><div><dt>{tr("Model")}</dt><dd>{attempt.call.node.model_ref?.id ? `${attempt.call.node.model_ref.id} @${attempt.call.node.model_ref.revision ?? "—"}` : "—"}</dd></div><div><dt>{tr("Tools")}</dt><dd>{attempt.call.node.tools?.length ? attempt.call.node.tools.map(tool => `${tool.id} @${tool.revision}`).join(", ") : "—"}</dd></div><div><dt>{tr("Effective prompt")}</dt><dd><pre>{attempt.call.node.kind !== "agent" ? tr("Not applicable") : attempt.call.prompt == null ? tr("Prompt was not retained.") : attempt.call.prompt || tr("Empty prompt.")}</pre></dd></div><div><dt>{tr("Inputs")}</dt><dd><pre>{attempt.call.inputs == null ? tr("Inputs were not retained.") : JSON.stringify(attempt.call.inputs, null, 2)}</pre></dd></div><div><dt>{tr("Outputs")}</dt><dd><pre>{attempt.outputs == null ? tr(attempt.state === "running" ? "No outputs yet." : "Outputs were not retained.") : JSON.stringify(attempt.outputs, null, 2)}</pre></dd></div><div><dt>{tr("Inactive outputs")}</dt><dd>{attempt.inactive?.length ? attempt.inactive.join(", ") : "—"}</dd></div></dl></details></li>)}</ol></section><section className="card"><h2>{tr("Event timeline")}</h2>{!events.length && <p>{tr("Waiting for execution events…")}</p>}<ol>{events.map(event => <li key={event.id}><time dateTime={event.timestamp}>{new Date(event.timestamp).toLocaleTimeString()}</time> · {tr(event.kind)} {event.attempt_id && <span>({event.attempt_id})</span>}{event.artifacts?.length ? <small> · {tr("Artifacts")}: {event.artifacts.map(artifact => artifact.id).join(", ")}</small> : null}</li>)}</ol><h3>{tr("Artifact metadata")}</h3>{moreArtifacts && <><p role="status">{tr("More artifact metadata is available.")}</p><button onClick={() => { artifactPages.current++; void read(false); }}>{tr("Load more artifacts")}</button></>}{artifacts.length ? <ul>{artifacts.map(artifact => <li key={artifact.id}><code>{artifact.id}</code> · {artifact.size} bytes · {artifact.media_type}<small> · {artifact.digest}</small> <ArtifactPreview root={root} id={artifact.id} /></li>)}</ul> : <p>{tr("No artifacts are visible yet.")}</p>}</section></div></>}
  </main>;
}
