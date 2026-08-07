"use client";

import { memo, useState } from "react";
import { ChevronDown, ChevronRight, ListTree } from "lucide-react";

import type { TraceNode as TraceNodeData } from "@/lib/types";
import { formatDuration } from "@/lib/format";
import { TraceNode } from "./trace-node";
import { useT, type TFunction } from "@/lib/i18n";

// Above this many nodes the list collapses behind a one-line summary so a long
// run of thinking/tool steps doesn't push the reply far down the page.
const COLLAPSE_THRESHOLD = 2;

function summarize(nodes: TraceNodeData[], t: TFunction): string {
  const tools = nodes.filter((n) => n.kind === "tool").length;
  const total = nodes.reduce((sum, n) => sum + (n.durationMs ?? 0), 0);
  // Chinese has no plural inflection, so the count goes through the catalog
  // rather than being pluralized with an English "s" here.
  const parts = [t(nodes.length === 1 ? "trace.step" : "trace.steps", { count: nodes.length })];
  if (tools > 0) parts.push(t(tools === 1 ? "trace.tool" : "trace.tools", { count: tools }));
  const dur = formatDuration(total);
  if (dur) parts.push(dur);
  return parts.join(" · ");
}

// defaultOpen: committed replies collapse (answer first); a live turn expands
// so tool progress is visible as it happens.
function TraceListImpl({
  nodes,
  defaultOpen = false
}: {
  nodes: TraceNodeData[];
  defaultOpen?: boolean;
}) {
  const t = useT();
  const [open, setOpen] = useState(defaultOpen);
  if (!nodes || nodes.length === 0) return null;

  const list = (
    <div className="flex flex-col gap-0.5 border-l-2 border-[var(--color-border)] pl-2.5">
      {nodes.map((node, i) => (
        <TraceNode key={`${node.kind}-${i}`} node={node} />
      ))}
    </div>
  );

  // Short traces render inline — a collapse header would be more chrome than
  // content.
  if (nodes.length <= COLLAPSE_THRESHOLD) {
    return <div className="mb-2">{list}</div>;
  }

  const hasError = nodes.some((n) => n.isError);

  return (
    <div className="mb-2">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 rounded-[var(--radius-sm)] px-2 py-1 text-2xs text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)]"
      >
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0" aria-hidden />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0" aria-hidden />
        )}
        <ListTree className="h-3.5 w-3.5 shrink-0" aria-hidden />
        <span className="min-w-0 flex-1 truncate text-left">
          {open ? t("trace.executionTrace") : summarize(nodes, t)}
        </span>
        {hasError ? (
          <span
            className="h-1.5 w-1.5 shrink-0 rounded-full"
            style={{ backgroundColor: "var(--color-danger)" }}
            title={t("trace.stepError")}
            aria-label="error"
          />
        ) : null}
      </button>
      {open ? <div className="trace-expand mt-1 pl-2">{list}</div> : null}
    </div>
  );
}

// Memoized so a committed trace list doesn't re-parse as new turns stream in.
export const TraceList = memo(TraceListImpl);
TraceList.displayName = "TraceList";
