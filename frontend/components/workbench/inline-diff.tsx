"use client";

import type { TraceDiff } from "@/lib/types";

type Line = { sign: "+" | "-"; text: string };

// Turn a diff payload into a flat list of signed lines: edits render removed
// lines then added lines per hunk; a bare `content` (write) is all additions.
function diffLines(diff: TraceDiff): Line[] {
  const lines: Line[] = [];
  if (diff.edits && diff.edits.length > 0) {
    for (const edit of diff.edits) {
      if (edit.oldText) {
        for (const text of edit.oldText.split("\n")) lines.push({ sign: "-", text });
      }
      if (edit.newText) {
        for (const text of edit.newText.split("\n")) lines.push({ sign: "+", text });
      }
    }
    return lines;
  }
  if (typeof diff.content === "string" && diff.content !== "") {
    for (const text of diff.content.split("\n")) lines.push({ sign: "+", text });
  }
  return lines;
}

export function InlineDiff({ diff }: { diff: TraceDiff }) {
  const lines = diffLines(diff);
  if (lines.length === 0) return null;

  return (
    <div className="overflow-hidden rounded-[var(--radius-sm)] border border-[var(--color-code-border)] bg-[var(--color-code-bg)]">
      {diff.path ? (
        <div className="border-b border-[var(--color-code-border)] px-2.5 py-1 font-mono text-[11px] text-[var(--color-foreground-muted)]">
          {diff.path}
        </div>
      ) : null}
      <div className="overflow-x-auto">
        <pre className="m-0 font-mono text-xs leading-relaxed">
          {lines.map((line, i) => (
            <div
              key={i}
              className="px-2.5 whitespace-pre"
              style={{
                backgroundColor: line.sign === "+" ? "var(--hl-addition-bg)" : "var(--hl-deletion-bg)",
                color: line.sign === "+" ? "var(--hl-addition-fg)" : "var(--hl-deletion-fg)"
              }}
            >
              <span aria-hidden className="mr-2 select-none opacity-70">
                {line.sign}
              </span>
              {line.text || " "}
            </div>
          ))}
        </pre>
      </div>
    </div>
  );
}
