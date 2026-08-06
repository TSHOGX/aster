"use client";

import { memo, useState } from "react";
import {
  Brain,
  ChevronDown,
  ChevronRight,
  FilePenLine,
  FileText,
  TerminalSquare,
  Wrench
} from "lucide-react";

import type { TraceNode as TraceNodeData } from "@/lib/types";
import { cn } from "@/lib/cn";
import { formatDuration } from "@/lib/format";
import { InlineDiff } from "./inline-diff";

// Render the node's glyph directly rather than aliasing a component to a
// capitalized local (which trips react-hooks/static-components): the chosen
// lucide icon varies per node, so it must not look like a stable component.
function renderIcon(node: TraceNodeData) {
  const className = "h-3.5 w-3.5 shrink-0";
  const style = {
    color: node.kind === "thinking" ? "var(--color-accent)" : "var(--color-foreground-muted)"
  };
  if (node.kind === "thinking") return <Brain className={className} style={style} aria-hidden />;
  switch ((node.toolName ?? "").toLowerCase()) {
    case "read":
      return <FileText className={className} style={style} aria-hidden />;
    case "bash":
      return <TerminalSquare className={className} style={style} aria-hidden />;
    case "write":
    case "edit":
      return <FilePenLine className={className} style={style} aria-hidden />;
    default:
      return <Wrench className={className} style={style} aria-hidden />;
  }
}

function headline(node: TraceNodeData): string {
  if (node.kind === "thinking") return node.label?.trim() || "Thinking";
  const name = node.toolName?.trim() || "tool";
  return node.label?.trim() ? `${name} · ${node.label.trim()}` : name;
}

function TraceNodeImpl({ node }: { node: TraceNodeData }) {
  const [open, setOpen] = useState(false);
  const duration = formatDuration(node.durationMs);
  const hasArgs = node.args !== undefined && Object.keys(node.args).length > 0;
  const hasDetail = hasArgs || Boolean(node.resultSummary) || Boolean(node.diff);
  const expandable = hasDetail;

  const summary = (
    <>
      {renderIcon(node)}
      <span className="min-w-0 flex-1 truncate text-left text-[var(--color-foreground)]">
        {headline(node)}
      </span>
      {node.isError ? (
        <span
          className="h-1.5 w-1.5 shrink-0 rounded-full"
          style={{ backgroundColor: "var(--color-danger)" }}
          title="Tool reported an error"
          aria-label="error"
        />
      ) : null}
      {duration ? (
        <span className="shrink-0 font-mono text-2xs tabular-nums text-[var(--color-foreground-muted)]">
          {duration}
        </span>
      ) : null}
    </>
  );

  if (!expandable) {
    return (
      <div className="flex items-center gap-2 rounded-[var(--radius-sm)] px-2 py-1 text-2xs">
        {summary}
      </div>
    );
  }

  return (
    <div className="rounded-[var(--radius-sm)]">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 rounded-[var(--radius-sm)] px-2 py-1 text-2xs hover:bg-[var(--color-surface-muted)]"
      >
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0 text-[var(--color-foreground-muted)]" aria-hidden />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0 text-[var(--color-foreground-muted)]" aria-hidden />
        )}
        {summary}
      </button>
      {open ? (
        <div className="trace-expand mt-1 flex flex-col gap-2 px-2 pb-2 pl-7">
          {hasArgs ? (
            <pre className="m-0 overflow-x-auto rounded-[var(--radius-sm)] border border-[var(--color-code-border)] bg-[var(--color-code-bg)] px-2.5 py-1.5 font-mono text-2xs leading-relaxed text-[var(--color-code-ink)]">
              {JSON.stringify(node.args, null, 2)}
            </pre>
          ) : null}
          {node.resultSummary ? (
            <pre
              className={cn(
                "m-0 max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-[var(--radius-sm)] border border-[var(--color-code-border)] bg-[var(--color-code-bg)] px-2.5 py-1.5 font-mono text-2xs leading-relaxed",
                node.isError ? "text-[var(--color-danger)]" : "text-[var(--color-foreground-muted)]"
              )}
            >
              {node.resultSummary}
            </pre>
          ) : null}
          {node.diff ? <InlineDiff diff={node.diff} /> : null}
        </div>
      ) : null}
    </div>
  );
}

// Memoized so an unchanged trace node doesn't re-render as sibling nodes stream in.
export const TraceNode = memo(TraceNodeImpl);
TraceNode.displayName = "TraceNode";
