"use client";
import "@xyflow/react/dist/style.css";
import dagre from "@dagrejs/dagre";
import { Background, Controls, Handle, MarkerType, Position, ReactFlow, ReactFlowProvider, type Connection, type Edge, type Node, type NodeProps } from "@xyflow/react";
import { useMemo } from "react";
import { useT } from "../lib/i18n";

type JSONMap = Record<string, unknown>;
export type CanvasWorkflow = { inputs?: JSONMap; outputs?: JSONMap; nodes?: JSONMap[]; edges?: JSONMap[] };
export type NodeState = { state: string; label?: string };
type Point = { x: number; y: number };
type Data = { id: string; kind: string; inputs: string[]; outputs: string[]; detail?: string; state?: NodeState; boundary?: "input" | "output" };
const width = 220, rowHeight = 22;
const height = (data: Data) => 64 + Math.max(data.inputs.length, data.outputs.length, 1) * rowHeight + (data.detail ? 18 : 0);
const stateIcon: Record<string, string> = { succeeded: "✓", completed: "✓", failed: "✕", running: "●", queued: "○", skipped: "–", cancelled: "⊘", paused: "Ⅱ" };

// Positions are layout data only: moving a node never changes the executable digest.
// Existing drafts key positions by node ID at the top level of the layout.
export function readLayout(layout: JSONMap): Record<string, Point> {
  const result: Record<string, Point> = {};
  for (const [id, value] of Object.entries(layout)) {
    const point = value as Partial<Point> | null;
    if (point && typeof point === "object" && Number.isFinite(point.x) && Number.isFinite(point.y)) result[id] = { x: point.x!, y: point.y! };
  }
  return result;
}
export function writeLayout(layout: JSONMap, positions: Record<string, Point>): JSONMap {
  const next: JSONMap = { ...layout };
  for (const [id, point] of Object.entries(positions)) next[id] = { ...(layout[id] as JSONMap | undefined), x: Math.round(point.x), y: Math.round(point.y) };
  return next;
}
function nodeData(workflow: CanvasWorkflow): Data[] {
  const nodes = (workflow.nodes ?? []).filter(node => node && typeof node === "object").map(node => {
    const kind = String(node.kind ?? "unknown");
    const detail = kind === "loop" ? `max ${Number(node.max_iterations) || "?"} iterations` : kind === "map" ? `max ${Number(node.max_items) || "?"} items` : kind === "subgraph" ? `depth ≤ ${Number(node.max_depth) || "?"}` : undefined;
    return { id: String(node.id), kind, inputs: Object.keys((node.input_ports as JSONMap) ?? {}), outputs: Object.keys((node.output_ports as JSONMap) ?? {}), detail };
  });
  const inputs = Object.keys(workflow.inputs ?? {}), outputs = Object.keys(workflow.outputs ?? {});
  return [
    ...(inputs.length ? [{ id: "$input", kind: "input", inputs: [], outputs: inputs, boundary: "input" as const }] : []),
    ...nodes,
    ...(outputs.length ? [{ id: "$output", kind: "output", inputs: outputs, outputs: [], boundary: "output" as const }] : []),
  ];
}
const split = (port: string) => { const index = port.indexOf("."); return [port.slice(0, index), port.slice(index + 1)]; };
function edgeData(workflow: CanvasWorkflow, invalid: Set<string>): Edge[] {
  return (workflow.edges ?? []).map((edge, index) => {
    const [source, sourceHandle] = split(String(edge.from)), [target, targetHandle] = split(String(edge.to));
    return { id: `${index}:${String(edge.from)}->${String(edge.to)}`, source, sourceHandle: `out:${sourceHandle}`, target, targetHandle: `in:${targetHandle}`, markerEnd: { type: MarkerType.ArrowClosed }, className: invalid.has(source) || invalid.has(target) ? "edge-invalid" : undefined, data: { index } };
  });
}
export function autoLayout(workflow: CanvasWorkflow): Record<string, Point> {
  const graph = new dagre.graphlib.Graph(); graph.setGraph({ rankdir: "LR", nodesep: 30, ranksep: 50 }); graph.setDefaultEdgeLabel(() => ({}));
  for (const data of nodeData(workflow)) graph.setNode(data.id, { width, height: height(data) });
  for (const edge of workflow.edges ?? []) { const [from] = split(String(edge.from)), [to] = split(String(edge.to)); if (graph.hasNode(from) && graph.hasNode(to)) graph.setEdge(from, to); }
  dagre.layout(graph);
  return Object.fromEntries(graph.nodes().map(id => { const node = graph.node(id); return [id, { x: node.x - node.width / 2, y: node.y - node.height / 2 }]; }));
}

function WorkflowNode({ data, selected }: NodeProps<Node<Data>>) {
  const tr = useT();
  const state = data.state;
  return <div className={`flow-node kind-${data.kind}${selected ? " selected" : ""}${state ? ` state-${state.state}` : ""}${data.boundary ? " boundary" : ""}`} style={{ width, minHeight: height(data) }}>
    <header><strong>{data.boundary ? tr(data.boundary === "input" ? "Workflow inputs" : "Workflow outputs") : data.id}</strong>{!data.boundary && <span className="flow-kind">{tr(data.kind)}</span>}</header>
    {data.detail && <small className="flow-bound">{data.detail}</small>}
    {state && <p className="flow-state"><span aria-hidden="true">{stateIcon[state.state] ?? "•"}</span> {tr(state.label ?? state.state)}</p>}
    <div className="flow-ports">
      <ul>{data.inputs.map(port => <li key={port}><Handle type="target" position={Position.Left} id={`in:${port}`} />{port}</li>)}</ul>
      <ul className="flow-outputs">{data.outputs.map(port => <li key={port}>{port}<Handle type="source" position={Position.Right} id={`out:${port}`} /></li>)}</ul>
    </div>
  </div>;
}
const nodeTypes = { workflow: WorkflowNode };

export function WorkflowCanvas(props: {
  workflow: CanvasWorkflow; layout: JSONMap; selected?: string; invalid?: string[]; states?: Record<string, NodeState>; readOnly?: boolean;
  onSelect?: (id: string) => void; onMove?: (layout: JSONMap) => void; onConnect?: (from: string, to: string) => void; onRemoveEdge?: (index: number) => void; onRemoveNode?: (id: string) => void;
}) {
  return <ReactFlowProvider><Canvas {...props} /></ReactFlowProvider>;
}
function Canvas({ workflow, layout, selected, invalid = [], states, readOnly, onSelect, onMove, onConnect, onRemoveEdge, onRemoveNode }: Parameters<typeof WorkflowCanvas>[0]) {
  const tr = useT();
  const invalidSet = useMemo(() => new Set(invalid), [invalid]);
  const positions = useMemo(() => {
    const stored = readLayout(layout), fallback = autoLayout(workflow);
    return Object.fromEntries(nodeData(workflow).map(data => [data.id, stored[data.id] ?? fallback[data.id] ?? { x: 0, y: 0 }]));
  }, [layout, workflow]);
  const nodes: Node<Data>[] = useMemo(() => nodeData(workflow).map(data => ({
    id: data.id, type: "workflow", position: positions[data.id], data: { ...data, state: states?.[data.id] },
    selected: data.id === selected, draggable: !readOnly, deletable: !readOnly && !data.boundary, className: invalidSet.has(data.id) ? "node-invalid" : undefined,
  })), [workflow, positions, states, selected, readOnly, invalidSet]);
  const edges = useMemo(() => edgeData(workflow, invalidSet), [workflow, invalidSet]);
  const port = (node: string | null, handle: string | null | undefined) => {
    const name = String(handle ?? "").replace(/^(in|out):/, "");
    return node && name ? `${node}.${name}` : "";
  };
  const valid = (connection: Connection | Edge) => {
    if (connection.source === connection.target || connection.source === "$output" || connection.target === "$input") return false;
    const node = (id: string) => (workflow.nodes ?? []).find(item => String(item.id) === id);
    const portSchema = (id: string, handle: string | null | undefined, side: "input_ports" | "output_ports") => {
      const name = String(handle ?? "").replace(/^(in|out):/, "");
      if (id === "$input") return (workflow.inputs ?? {})[name];
      if (id === "$output") return (workflow.outputs ?? {})[name];
      return ((node(id)?.[side] as JSONMap) ?? {})[name];
    };
    // Only schema-identical ports connect; the server re-validates on save.
    const from = portSchema(connection.source, connection.sourceHandle, "output_ports"), to = portSchema(connection.target, connection.targetHandle, "input_ports");
    return from !== undefined && from === to;
  };
  return <div className="flow-canvas" aria-label={tr(readOnly ? "Executed workflow graph" : "Workflow graph")} role="group">
    <ReactFlow nodes={nodes} edges={edges} nodeTypes={nodeTypes} fitView fitViewOptions={{ padding: 0.15, maxZoom: 1 }} minZoom={0.2} maxZoom={1.75}
      nodesDraggable={!readOnly} nodesConnectable={!readOnly} elementsSelectable deleteKeyCode={readOnly ? null : ["Backspace", "Delete"]}
      isValidConnection={valid}
      onNodeClick={(_, node) => { if (!node.data.boundary) onSelect?.(node.id); }}
      onPaneClick={() => onSelect?.("")}
      onNodeDragStop={(_, node) => onMove?.(writeLayout(layout, { ...positions, [node.id]: node.position }))}
      onConnect={connection => { const from = port(connection.source, connection.sourceHandle), to = port(connection.target, connection.targetHandle); if (from && to) onConnect?.(from, to); }}
      onEdgesDelete={deleted => { for (const edge of [...deleted].sort((a, b) => Number((b.data as { index: number }).index) - Number((a.data as { index: number }).index))) onRemoveEdge?.(Number((edge.data as { index: number }).index)); }}
      onNodesDelete={deleted => { for (const node of deleted) if (!(node.data as Data).boundary) onRemoveNode?.(node.id); }}
      proOptions={{ hideAttribution: true }}>
      <Background gap={20} />
      <Controls showInteractive={false} />
    </ReactFlow>
  </div>;
}
