"use client";

import Link from "next/link";
import { useEffect, useRef } from "react";
import { useT } from "../lib/i18n";

export type Attempt = { reused_from?: { run_id: string; checkpoint_id: string; attempt_id: string }; call: { id: string; instance: string; parent?: string; number?: number; node: { id?: string; kind?: string; model_ref?: { id?: string; revision?: number }; tools?: { id?: string; revision?: number }[] }; inputs?: Record<string, unknown>; prompt?: string }; state: string; error_code?: string; outputs?: Record<string, unknown>; inactive?: string[] };

export function attemptHref(group: string, run: string, attempt: string) {
  return `/groups/${encodeURIComponent(group)}/runs/${encodeURIComponent(run)}?attempt=${encodeURIComponent(attempt)}`;
}

function attemptContext(instance: string) {
  const depth = [...instance.matchAll(/\/attempt-\d+\/(?:body|iteration-\d+|item-\d+)(?=\/|$)/g)].length;
  const iteration = [...instance.matchAll(/(?:^|\/)iteration-(\d+)(?=\/|$)/g)].at(-1)?.[1];
  const item = [...instance.matchAll(/(?:^|\/)item-(\d+)(?=\/|$)/g)].at(-1)?.[1];
  return { depth, iteration, item };
}

export function RunAttempt({ attempt, group, run, selected, onSelect }: { attempt: Attempt; group: string; run: string; selected: boolean; onSelect: (id: string) => void }) {
  const tr = useT(); const summary = useRef<HTMLElement>(null);
  useEffect(() => { if (selected) summary.current?.focus(); }, [selected]);
  return <details id={`attempt-${attempt.call.id}`} open={selected}><summary ref={summary} onClick={event => { event.preventDefault(); onSelect(selected ? "" : attempt.call.id); }}><strong>{attempt.call.instance}</strong> · {tr(attempt.state)} {attempt.error_code && <span>({attempt.error_code})</span>}</summary>
    <p><Link prefetch={false} href={attemptHref(group, run, attempt.call.id)}>{tr("Link to this attempt")}</Link></p>
    <dl className="attempt-details">{attempt.reused_from && <div><dt>{tr("Reused from checkpoint")}</dt><dd><Link prefetch={false} href={attemptHref(group, attempt.reused_from.run_id, attempt.reused_from.attempt_id)}>{attempt.reused_from.run_id}</Link> · {attempt.reused_from.checkpoint_id} · {attempt.reused_from.attempt_id}</dd></div>}<div><dt>{tr("Node")}</dt><dd>{attempt.call.node.id ?? "—"} · {tr(attempt.call.node.kind ?? "unknown")}</dd></div>
      <div><dt>{tr("Attempt")}</dt><dd>{attempt.call.number ?? "—"}</dd></div>
      <div><dt>{tr("Parent attempt")}</dt><dd>{attempt.call.parent ? <button onClick={() => onSelect(attempt.call.parent!)}>{attempt.call.parent}</button> : "—"}</dd></div>
      <div><dt>{tr("Execution depth")}</dt><dd>{attemptContext(attempt.call.instance).depth}</dd></div>
      <div><dt>{tr("Loop iteration")}</dt><dd>{attemptContext(attempt.call.instance).iteration ?? "—"}</dd></div>
      <div><dt>{tr("Map item")}</dt><dd>{attemptContext(attempt.call.instance).item ?? "—"}</dd></div>
      <div><dt>{tr("Model")}</dt><dd>{attempt.call.node.model_ref?.id ? `${attempt.call.node.model_ref.id} @${attempt.call.node.model_ref.revision ?? "—"}` : "—"}</dd></div>
      <div><dt>{tr("Tools")}</dt><dd>{attempt.call.node.tools?.length ? attempt.call.node.tools.map(tool => `${tool.id} @${tool.revision}`).join(", ") : "—"}</dd></div>
      <div><dt>{tr("Effective prompt")}</dt><dd><pre>{attempt.call.node.kind !== "agent" ? tr("Not applicable") : attempt.call.prompt == null ? tr("Prompt was not retained.") : attempt.call.prompt || tr("Empty prompt.")}</pre></dd></div>
      <div><dt>{tr("Inputs")}</dt><dd><pre>{attempt.call.inputs == null ? tr("Inputs were not retained.") : JSON.stringify(attempt.call.inputs, null, 2)}</pre></dd></div>
      <div><dt>{tr("Outputs")}</dt><dd><pre>{attempt.outputs == null ? tr(attempt.state === "running" ? "No outputs yet." : "Outputs were not retained.") : JSON.stringify(attempt.outputs, null, 2)}</pre></dd></div>
      <div><dt>{tr("Inactive outputs")}</dt><dd>{attempt.inactive?.length ? attempt.inactive.join(", ") : "—"}</dd></div>
    </dl></details>;
}
