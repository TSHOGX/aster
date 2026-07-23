"use client";

import type { TraceNode as TraceNodeData } from "@/lib/types";
import { TraceNode } from "./trace-node";

export function TraceList({ nodes }: { nodes: TraceNodeData[] }) {
  if (!nodes || nodes.length === 0) return null;
  return (
    <div className="mb-2 flex flex-col gap-0.5 border-l-2 border-[var(--color-border)] pl-2.5">
      {nodes.map((node, i) => (
        <TraceNode key={`${node.kind}-${i}`} node={node} />
      ))}
    </div>
  );
}
