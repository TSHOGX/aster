"use client";

import { memo, useEffect, useRef, useState, type ReactNode } from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import { useT } from "@/lib/i18n";
import { readingAnchor, saveReadingAnchor } from "@/lib/message-window";
import { markWorkbench } from "@/lib/performance";

function TranscriptFooter({ context }: { context?: { footer?: ReactNode } }) {
  return (
    <div className="mx-auto w-full max-w-[var(--measure)] px-4 pb-40 sm:px-6">
      {context?.footer}
    </div>
  );
}
const COMPONENTS = { Footer: TranscriptFooter };
export const VirtualTranscript = memo(function VirtualTranscript({
  sessionId,
  rows,
  render,
  footer,
  loadOlder,
  hasOlder,
  hasNewer,
  loadNewer,
  jumpId,
  onJumpDone,
  onLatest,
}: {
  sessionId: string;
  rows: import("@/lib/types").ApiMessage[];
  render: (row: import("@/lib/types").ApiMessage, index: number) => ReactNode;
  footer?: ReactNode;
  hasOlder?: boolean;
  hasNewer?: boolean;
  loadOlder?: () => void;
  loadNewer?: () => void;
  jumpId?: number;
  onJumpDone?: () => void;
  onLatest?: () => void;
}) {
  const t = useT();
  const host = useRef<HTMLDivElement>(null);
  const list = useRef<VirtuosoHandle>(null);
  const [initial] = useState(() => readingAnchor(sessionId));
  const [initialLocation] = useState(() => ({
    index:
      initial && !initial.following
        ? Math.max(
            0,
            rows.findIndex((r) => r.id === initial.messageId),
          )
        : rows.length - 1,
    align:
      initial && !initial.following ? ("start" as const) : ("end" as const),
  }));
  const [following, setFollowing] = useState(initial?.following ?? true);
  const followingRef = useRef(initial?.following ?? true);
  const anchor = useRef(initial);
  const previousFirst = useRef(rows[0]?.id);
  const correcting = useRef(false);
  const capture = () => {
    if (correcting.current) return;
    const rect = host.current?.getBoundingClientRect();
    const scroller = host.current?.querySelector<HTMLElement>(
      "[data-virtuoso-scroller]",
    );
    if (scroller)
      followingRef.current =
        scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <
          40 && !hasNewer;
    const row = [
      ...(host.current?.querySelectorAll<HTMLElement>("[data-message-id]") ??
        []),
    ].find((el) => el.getBoundingClientRect().bottom > (rect?.top ?? 0));
    if (!row || !rect) return;
    anchor.current = {
      messageId: Number(row.dataset.messageId),
      offset: row.getBoundingClientRect().top - rect.top,
      following: followingRef.current,
    };
    saveReadingAnchor(sessionId, anchor.current);
  };
  useEffect(() => {
    const saved = anchor.current;
    const changed = previousFirst.current !== rows[0]?.id;
    previousFirst.current = rows[0]?.id;
    const id =
      jumpId ??
      (!saved?.following && (changed || initial)
        ? saved?.messageId
        : undefined);
    const index = rows.findIndex((row) => row.id === id);
    if (index < 0) return;
    correcting.current = true;
    let frame = 0,
      attempts = 0,
      stable = 0;
    list.current?.scrollToIndex({ index, align: jumpId ? "center" : "start" });
    const settle = () => {
      const el = host.current?.querySelector<HTMLElement>(
        `[data-message-id="${id}"]`,
      );
      const rect = host.current?.getBoundingClientRect();
      if (el && rect) {
        const offset = jumpId
          ? Math.max(0, (rect.height - el.offsetHeight) / 2)
          : (saved?.offset ?? 0);
        const delta = el.getBoundingClientRect().top - rect.top - offset;
        if (Math.abs(delta) > 1) {
          stable = 0;
          list.current?.scrollBy({ top: delta });
        } else stable++;
      } else list.current?.scrollToIndex({ index, align: "start" });
      if (++attempts < 30 && stable < 4) frame = requestAnimationFrame(settle);
      else {
        correcting.current = false;
        if (jumpId) onJumpDone?.();
      }
    };
    frame = requestAnimationFrame(settle);
    return () => {
      cancelAnimationFrame(frame);
      correcting.current = false;
    };
  }, [rows, jumpId, initial, onJumpDone]);
  useEffect(() => {
    let frame = 0,
      attempts = 0;
    const ready = () => {
      const rect = host.current?.getBoundingClientRect();
      const selector =
        initial && !initial.following
          ? `[data-message-id="${initial.messageId}"]`
          : "[data-message-id]:last-child";
      const target =
        initial && !initial.following
          ? host.current?.querySelector(selector)
          : host.current?.querySelectorAll("[data-message-id]");
      const row =
        target instanceof NodeList ? target[target.length - 1] : target;
      const box = row?.getBoundingClientRect();
      if (
        box &&
        rect &&
        box.bottom > rect.top &&
        box.top < rect.bottom &&
        !correcting.current
      ) {
        markWorkbench("readable");
        return;
      }
      if (++attempts < 120) frame = requestAnimationFrame(ready);
    };
    frame = requestAnimationFrame(() => {
      frame = requestAnimationFrame(ready);
    });
    return () => cancelAnimationFrame(frame);
  }, [initial]);
  return (
    <div
      ref={host}
      className="relative min-h-0 flex-1"
      onScrollCapture={capture}
    >
      <Virtuoso
        ref={list}
        data={rows}
        style={{ overflowAnchor: "none" }}
        increaseViewportBy={300}
        initialTopMostItemIndex={initialLocation}
        totalListHeightChanged={() => {
          if (followingRef.current && !hasNewer && !correcting.current)
            list.current?.autoscrollToBottom();
        }}
        computeItemKey={(_, row) => row.id}
        followOutput={following && !hasNewer ? "auto" : false}
        atBottomStateChange={(bottom) => {
          if (correcting.current) return;
          followingRef.current = bottom && !hasNewer;
          setFollowing(bottom && !hasNewer);
          capture();
        }}
        startReached={() => {
          if (hasOlder) {
            capture();
            loadOlder?.();
          }
        }}
        endReached={() => {
          if (hasNewer) loadNewer?.();
        }}
        itemContent={(index, row) => (
          <div
            data-message-id={row.id}
            id={`msg-${row.id}`}
            className="mx-auto w-full max-w-[var(--measure)] px-4 sm:px-6"
          >
            {render(row, index)}
          </div>
        )}
        components={COMPONENTS}
        context={{ footer }}
      />
      {(!following || hasNewer) && (
        <button
          className="absolute bottom-40 right-4 rounded border bg-[var(--color-surface)] px-3 py-1 text-xs"
          onClick={() => {
            followingRef.current = true;
            setFollowing(true);
            onLatest?.();
            list.current?.scrollToIndex({
              index: rows.length - 1,
              align: "end",
            });
          }}
        >
          ↓ {t("perf.latest")}
        </button>
      )}
    </div>
  );
});
