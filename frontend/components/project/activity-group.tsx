"use client";
import { useId, useState } from "react";
import { Brain, ChevronRight, TerminalSquare } from "lucide-react";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { useT } from "@/lib/i18n";
import {
  activityArguments,
  activityPreview,
  activityResult,
  type TimelineSegment,
} from "@/lib/timeline-activity";

export function ActivityGroup({
  segment,
  workspaceFiles,
  targetItem = "",
  targetItemOpen = false,
}: {
  segment: Extract<TimelineSegment, { kind: "activity" }>;
  workspaceFiles: readonly string[];
  targetItem?: string;
  targetItemOpen?: boolean;
}) {
  const tr = useT();
  const [expanded, setExpanded] = useState(() =>
    segment.items.some((item) => item.id === targetItem),
  );
  const [initialOpen] = useState(targetItemOpen ? targetItem : "");
  const detailsId = useId();
  const { items, live, active } = segment;
  const tools = items.filter((item) => item.kind === "tool");
  const names = [...new Set(tools.map((item) => item.name || "Tool"))].join(
    ", ",
  );
  const thinking = live?.kind === "thinking" ? live : undefined;
  const preview = thinking ? activityPreview(thinking) : "";
  const label = tools.length
    ? `${names} • ${tr("Tool activity")} (${tools.length})`
    : tr("Thinking");
  return (
    <div
      className={`activity-group ${live ? "is-live" : ""}`}
      data-activity-id={segment.id}
      data-live-kind={live?.kind}
      data-expanded={expanded}
    >
      <button
        type="button"
        className={`activity-summary ${live ? "is-active" : ""}`}
        aria-expanded={expanded}
        aria-controls={detailsId}
        onClick={() => setExpanded(!expanded)}
        title={`${label}${preview ? ` — ${preview}` : ""}`}
      >
        {tools.length ? (
          <TerminalSquare className="activity-icon" size={16} aria-hidden />
        ) : (
          <Brain className="activity-icon" size={16} aria-hidden />
        )}
        <span className="activity-line">
          <span className="activity-label">{label}</span>
          {preview && <span className="activity-preview">{preview}</span>}
        </span>
        <ChevronRight className="activity-chevron" size={13} aria-hidden />
      </button>
      <div className="activity-details" id={detailsId}>
        {items.map((item) => {
          const isActive = active.includes(item);
          if (item.kind !== "tool")
            return (
              <details
                key={item.id}
                className="thinking-trace"
                open={item.id === initialOpen || undefined}
                data-timeline-id={item.id}
                hidden={!expanded}
              >
                <summary
                  className={`activity-row ${isActive ? "is-active" : ""}`}
                >
                  <Brain className="activity-icon" size={16} aria-hidden />
                  <span className="activity-line">{tr("Thinking")}</span>
                  <ChevronRight
                    className="activity-chevron"
                    size={13}
                    aria-hidden
                  />
                </summary>
                <div className="activity-output">
                  <MarkdownView
                    content={item.content || ""}
                    workspaceFiles={workspaceFiles}
                  />
                </div>
              </details>
            );
          const status =
            item.state === "failed"
              ? tr("Failed")
              : item.state === "completed"
                ? tr("Completed")
                : isActive
                  ? item.state === "pending"
                    ? tr("Preparing")
                    : tr("Running")
                  : tr("Outcome unknown");
          const detail = activityPreview(item);
          return (
            <details
              key={item.id}
              className="tool-trace"
              open={item.id === initialOpen || undefined}
              data-timeline-id={item.id}
              data-state={
                isActive
                  ? item.state
                  : item.state === "running" || item.state === "pending"
                    ? "unknown"
                    : item.state
              }
              hidden={!expanded && !isActive}
            >
              <summary
                className={`activity-row ${isActive ? "is-active" : ""}`}
                title={`${item.name || "Tool"} · ${status}${detail ? ` — ${detail}` : ""}`}
              >
                <TerminalSquare
                  className="activity-icon"
                  size={16}
                  aria-hidden
                />
                <span className="activity-line">
                  <span className="activity-label">
                    {isActive ? `${status} ` : ""}
                    {item.name || "Tool"}
                  </span>
                  {detail && <span className="activity-preview">{detail}</span>}
                </span>
                {!isActive && (
                  <span
                    className={`activity-state ${item.state === "failed" ? "is-failed" : ""}`}
                  >
                    {status}
                  </span>
                )}
                <ChevronRight
                  className="activity-chevron"
                  size={13}
                  aria-hidden
                />
              </summary>
              <div className="tool-panel">
                <div className="tool-panel-heading">
                  <span>{item.name || "Tool"}</span>
                  <span>{status}</span>
                </div>
                <div className="activity-output">
                  {item.arguments && (
                    <section>
                      <h4>{tr("Tool call")}</h4>
                      <pre>{activityArguments(item.arguments)}</pre>
                    </section>
                  )}
                  {item.result ? (
                    <section>
                      <h4>{tr("Tool result")}</h4>
                      <pre>{activityResult(item.result)}</pre>
                    </section>
                  ) : (
                    <p className="tool-pending">
                      {isActive
                        ? item.state === "pending"
                          ? tr("Waiting to execute…")
                          : tr("Waiting for tool result…")
                        : tr("No tool result")}
                    </p>
                  )}
                </div>
              </div>
            </details>
          );
        })}
      </div>
    </div>
  );
}
