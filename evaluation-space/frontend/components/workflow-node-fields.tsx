"use client";

import { useT } from "../lib/i18n";

type Node = Record<string, unknown>;
export type NodeJSONField = "tools" | "input_ports" | "output_ports" | "implementation_ref" | "body" | "item_inputs" | "item_outputs" | "routes";
export function parseNodeField(field: NodeJSONField, raw: string): unknown | undefined {
 try {
  const value: unknown = JSON.parse(raw);
  if (field === "tools" || field === "routes") return Array.isArray(value) ? value : undefined;
  return value && typeof value === "object" && !Array.isArray(value) ? value : undefined;
 } catch { return undefined; }
}

export function WorkflowNodeFields({ node, drafts, update, updateJSON, describedBy }: { node: Node; drafts: Partial<Record<NodeJSONField, string>>; update: (field: string, value: unknown) => void; updateJSON: (field: NodeJSONField, raw: string) => void; describedBy?: string }) {
 const tr = useT(); const kind = String(node.kind);
 function number(field: string, label: string, max?: number) {
  return <label key={field}>{tr(label)}<input data-field={field} aria-describedby={describedBy} aria-label={tr(label)} type="number" min={1} max={max} step={1} value={Number(node[field] ?? 0)} onChange={event => update(field, Number(event.target.value))} /></label>;
 }
 function choice(field: string, label: string, options: [string, string][]) {
  const value = String(node[field] ?? "");
  return <label key={field}>{tr(label)}<select data-field={field} aria-describedby={describedBy} aria-label={tr(label)} value={value} onChange={event => update(field, event.target.value)}>
   {!options.some(([id]) => id === value) && <option value={value}>{value || tr("Choose a value")}</option>}
   {options.map(([id, label]) => <option key={id} value={id}>{tr(label)}</option>)}
  </select></label>;
 }
 function json(field: NodeJSONField, label: string) {
  const raw = drafts[field] ?? JSON.stringify(node[field] ?? (field === "routes" ? [] : {}), null, 2);
  return <label key={field}>{tr(label)}<textarea data-field={field} aria-describedby={describedBy} aria-label={tr(label)} value={raw} onChange={event => updateJSON(field, event.target.value)} aria-invalid={parseNodeField(field, raw) === undefined} spellCheck={false} /></label>;
 }
 return <>
  {number("timeout_seconds", "Timeout (seconds)")}
  {(kind === "transform" || kind === "validator") && json("implementation_ref", "Implementation revision JSON")}
  {["map", "loop", "subgraph"].includes(kind) && json("body", "Body workflow revision JSON")}
  {(kind === "router" || kind === "loop") && choice("predicate", "Pure predicate", [["truthy", "Truthy"], ["all_valid", "All valid"], ["has_items", "Has items"]])}
  {kind === "router" && <>{json("routes", "Routes JSON")}<label>{tr("Default route")}<input data-field="default_route" aria-describedby={describedBy} aria-label={tr("Default route")} value={String(node.default_route ?? "")} onChange={event => update("default_route", event.target.value)} /></label></>}
  {kind === "join" && choice("inactive_inputs", "Inactive input handling", [["skip", "Skip inactive inputs"], ["fail", "Fail on inactive inputs"]])}
  {kind === "subgraph" && number("max_depth", "Maximum depth", 16)}
  {kind === "loop" && <>{number("max_iterations", "Maximum iterations")}{choice("on_limit", "At iteration limit", [["stop", "Stop with current outputs"], ["fail", "Fail at limit"]])}</>}
  {kind === "map" && <>{json("item_inputs", "Item input schemas JSON")}{json("item_outputs", "Item output schemas JSON")}{number("max_items", "Maximum items")}{number("concurrency", "Concurrent items")}{choice("failure_mode", "Item failure handling", [["fail_fast", "Stop on first failure"], ["collect_errors", "Collect item errors"]])}</>}
 </>;
}
