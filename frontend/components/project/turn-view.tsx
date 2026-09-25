"use client";
import { useEffect, useState } from "react";
import { Check, ChevronRight, Copy, ThumbsUp, ThumbsDown } from "lucide-react";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { useT } from "@/lib/i18n";
import { recalled, remember, type TimelineItem } from "@/lib/project-api";
import {
  elapsedLabel,
  finalItems,
  runningTurn,
  type ConversationTurn,
} from "@/lib/conversation-turns";
import { groupTimeline } from "@/lib/timeline-activity";
import { ActivityGroup } from "./activity-group";
import { IconButton } from "./ui";

export function TurnClock({
  start,
  end,
  running,
  elapsed,
}: {
  start?: string;
  end?: string;
  running: boolean;
  elapsed?: number;
}) {
  const tr = useT();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [running]);
  return (
    <span>
      {tr("Worked for {duration}", { duration: elapsed !== undefined ? elapsedLabel(new Date(0).toISOString(), new Date(elapsed).toISOString()) : elapsedLabel(start, end, now) })}
    </span>
  );
}
function Transcript({
  items,
  files,
  activeRuns,
  loadMessage,
  loadDetail,
  targetItem,
  targetItemOpen,
}: {
  items: TimelineItem[];
  files: readonly string[];
  activeRuns: ReadonlySet<string>;
  loadMessage: (id: string) => Promise<string>;
  loadDetail: (item: TimelineItem) => Promise<string>;
  targetItem?: string;
  targetItemOpen?: boolean;
}) {
  const tr = useT();
  const [downloadError, setDownloadError] = useState("");
  const download = async (item: TimelineItem) => {
    try {
      const text =
        item.kind === "tool" || item.kind === "thinking"
          ? await loadDetail(item)
          : item.message_id
            ? await loadMessage(item.message_id)
            : await loadDetail(item);
      const url = URL.createObjectURL(
        new Blob([text], { type: "text/plain;charset=utf-8" }),
      );
      const a = document.createElement("a");
      a.href = url;
      a.download = "conversation-content.txt";
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      setDownloadError(error instanceof Error ? error.message : String(error));
    }
  };
  return (
    <>
      {downloadError && (
        <p role="alert" className="notice error">
          {downloadError}
        </p>
      )}
      {groupTimeline(items, activeRuns).map((segment) => {
        if (segment.kind === "activity")
          return (
            <ActivityGroup
              key={segment.id}
              segment={segment}
              workspaceFiles={files}
              targetItem={targetItem}
              targetItemOpen={targetItemOpen}
              download={download}
            />
          );
        const item = segment.item;
        return (
          <article
            key={item.id}
            className={`chat-message ${item.kind} ${item.state === "streaming" ? "streaming" : ""}`}
            data-message-id={item.message_id}
            data-timeline-id={item.id}
          >
            <MarkdownView
              content={(item.content || "").slice(0, 64000)}
              workspaceFiles={files}
              highlight={item.state !== "streaming"}
            />
            {item.truncated && (
              <button onClick={() => void download(item)}>
                {tr("Download full message")}
              </button>
            )}
          </article>
        );
      })}
    </>
  );
}
export function TurnView({
  turn,
  files,
  activeRuns,
  loadMessage,
  loadDetail,
  actor,
  project,
  session,
  targetMessage,
  targetItem = "",
  targetItemOpen = false,
}: {
  turn: ConversationTurn;
  files: readonly string[];
  activeRuns: ReadonlySet<string>;
  loadMessage: (id: string) => Promise<string>;
  loadDetail: (item: TimelineItem) => Promise<string>;
  actor: string;
  project: string;
  session: string;
  targetMessage: string;
  targetItem?: string;
  targetItemOpen?: boolean;
}) {
  const tr = useT();
  const final = finalItems(turn);
  const summary = turn.summary;
  const finishedAt = summary?.finished_at;
  const completed = ["completed", "continued"].includes(turn.summary?.state || "") && final.length > 0;
  const outcomeLabel = summary?.state === "canceled"
    ? tr("Interrupted")
    : summary?.state === "failed"
      ? tr("Failed")
      : summary?.state === "stopping" || summary?.state === "settling"
        ? tr("Stopping…")
        : summary?.state === "unknown"
          ? tr("Outcome unknown")
          : "";
  const [expanded, setExpanded] = useState(false);
  const [collapsedTarget, setCollapsedTarget] = useState("");
  const [copied, setCopied] = useState(false),
    [copyError, setCopyError] = useState(false);
  const feedbackKey = `project-view:${actor}:${project}:feedback:${session}:${turn.segmentId || turn.turnId}`;
  const [feedback, setFeedback] = useState<"up" | "down" | null>(() => {
    const value = recalled<unknown>(feedbackKey, null);
    return value === "up" || value === "down" ? value : null;
  });
  const users = turn.items.filter((i) => i.kind === "user");
  const body = turn.items.filter((i) => i.kind !== "user");
  const finalIDs = new Set(final.map((i) => i.id));
  const intermediate = body.filter((i) => !finalIDs.has(i.id));
  const targetInHistory = intermediate.some(
    (i) =>
      (!!targetMessage && i.message_id === targetMessage) ||
      (!!targetItem && i.id === targetItem),
  );
  const targetKey = targetItem || targetMessage;
  const open =
    !completed ||
    expanded ||
    (targetInHistory && collapsedTarget !== targetKey);
  const running = !!turn.summary && runningTurn(summary!.state);
  const vote = (value: "up" | "down") => {
    const next = feedback === value ? null : value;
    setFeedback(next);
    remember(feedbackKey, next);
  };
  const copy = async () => {
    setCopyError(false);
    try {
      const fullFinal = turn.summary?.final_message_id
        ? await loadMessage(turn.summary.final_message_id)
        : final.map((i) => i.content || "").join("\n\n");
      if (navigator.clipboard?.writeText)
        await navigator.clipboard.writeText(fullFinal);
      else {
        const el = document.createElement("textarea");
        el.value = fullFinal;
        el.style.position = "fixed";
        el.style.opacity = "0";
        document.body.appendChild(el);
        el.select();
        const ok = document.execCommand("copy");
        el.remove();
        if (!ok) throw new Error("Copy unavailable");
      }
      setCopied(true);
    } catch {
      setCopyError(true);
    }
  };
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1800);
    return () => clearTimeout(timer);
  }, [copied]);
  return (
    <section className="conversation-turn" data-turn-id={turn.turnId}>
      <Transcript
        items={users}
        files={files}
        activeRuns={activeRuns}
        loadMessage={loadMessage}
        loadDetail={loadDetail}
      />
      {summary && (
        <div className={`turn-work ${running ? "is-running" : ""}`}>
          <button
            type="button"
            className="turn-work-toggle"
            aria-expanded={open}
            disabled={!completed || !intermediate.length}
            onClick={() => {
              setExpanded(!open);
              if (open) setCollapsedTarget(targetKey);
            }}
          >
            <TurnClock
              elapsed={summary.elapsed_ms}
              start={summary.finished_at && summary.run_id && summary.elapsed_ms === undefined ? undefined : summary.created_at}
              end={finishedAt}
              running={running}
            />
            {intermediate.length > 0 && (
              <ChevronRight
                size={14}
                className={open ? "expanded" : ""}
                aria-hidden
              />
            )}
          </button>
          {outcomeLabel && (
            <span className="turn-outcome">
              {outcomeLabel}
            </span>
          )}
        </div>
      )}
      {open && (
        <div className="turn-process">
          <Transcript
            items={completed ? intermediate : body}
            files={files}
            activeRuns={activeRuns}
            loadMessage={loadMessage}
            loadDetail={loadDetail}
            targetItem={targetItem}
            targetItemOpen={targetItemOpen}
          />
        </div>
      )}
      {completed && (
        <>
          <div className="turn-final">
            <Transcript
              items={final}
              files={files}
              activeRuns={activeRuns}
              loadMessage={loadMessage}
              loadDetail={loadDetail}
            />
          </div>
          <footer className="answer-actions">
            <IconButton
              label={copied ? tr("code.copied") : tr("Copy answer")}
              onClick={() => void copy()}
            >
              {copied ? <Check size={16} /> : <Copy size={16} />}
            </IconButton>
            <IconButton
              label={tr("Like")}
              aria-pressed={feedback === "up"}
              title={tr("Feedback is saved in this browser")}
              onClick={() => vote("up")}
            >
              <ThumbsUp size={16} />
            </IconButton>
            <IconButton
              label={tr("Dislike")}
              aria-pressed={feedback === "down"}
              title={tr("Feedback is saved in this browser")}
              onClick={() => vote("down")}
            >
              <ThumbsDown size={16} />
            </IconButton>
            {finishedAt && Number.isFinite(Date.parse(finishedAt)) && (
              <time
                dateTime={finishedAt}
                title={new Date(finishedAt).toLocaleString()}
              >
                {new Date(finishedAt).toLocaleTimeString(undefined, {
                  hour: "numeric",
                  minute: "2-digit",
                })}
              </time>
            )}
            {copyError && (
              <span role="status">
                {tr("Copy failed. Select the answer to copy it.")}
              </span>
            )}
          </footer>
        </>
      )}
    </section>
  );
}
