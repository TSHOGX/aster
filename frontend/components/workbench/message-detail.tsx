"use client";
import { useEffect, useRef, useState } from "react";
import { useT } from "@/lib/i18n";
import { registerCache } from "@/lib/cache-lifecycle";
import { request } from "@/lib/api";
import { BoundedCache } from "@/lib/bounded-cache";
import type { ApiMessage, TraceNode } from "@/lib/types";
import { VirtualTrace } from "./trace-list";

const traces = new BoundedCache<TraceNode[]>(8 * 1024 * 1024);
registerCache(() => traces.clear());
export function clearMessageDetails() {
  traces.clear();
}
export function messageBase(sessionId: string, id: number, base?: string) {
  return `${base ?? `/api/sessions/${encodeURIComponent(sessionId)}`}/messages/${id}`;
}
export async function completeMessageContent(
  message: ApiMessage,
  base: string,
) {
  if (!message.content_truncated) return message.content;
  let content = "",
    offset = 0;
  for (;;) {
    const res = await request<{
      content: string;
      next_offset: number;
      has_more: boolean;
    }>(`${base}/content?offset=${offset}`);
    if (!res.ok) throw new Error(res.error);
    content += res.data.content;
    if (!res.data.has_more) return content;
    if (res.data.next_offset <= offset)
      throw new Error("Invalid content cursor");
    offset = res.data.next_offset;
  }
}
export function MessagePreview({
  message,
  base,
}: {
  message: ApiMessage;
  base: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <>
      <div className="whitespace-pre-wrap break-words text-sm">
        {message.content}
      </div>
      <button className="mt-2 text-xs underline" onClick={() => setOpen(true)}>
        {t("perf.readFull", { count: message.content_length ?? 0 })}
      </button>
      {open && (
        <ContentReader key={base} base={base} close={() => setOpen(false)} />
      )}
    </>
  );
}
function ContentReader({
  base,
  close,
  node,
}: {
  base: string;
  close: () => void;
  node?: number;
}) {
  const t = useT();
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  const path =
    node === undefined
      ? `${base}/content?`
      : `${base}/trace-content?node=${node}&`;
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<{
    content: string;
    next_offset: number;
    has_more: boolean;
  }>();
  const [error, setError] = useState("");
  useEffect(() => {
    const c = new AbortController();
    void request<{ content: string; next_offset: number; has_more: boolean }>(
      `${path}offset=${offset}`,
      { signal: c.signal },
    ).then((res) => {
      if (c.signal.aborted) return;
      if (res.ok) setPage(res.data);
      else setError(res.error);
    });
    return () => c.abort();
  }, [path, offset]);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      role="dialog"
      aria-modal="true"
      aria-label={t("perf.fullMessage")}
      className="fixed inset-4 z-50 flex flex-col rounded border bg-[var(--color-surface)] p-4 shadow-xl"
    >
      <div className="flex gap-4">
        <button onClick={close}>{t("perf.close")}</button>
        <a href={`${path}download=1`} download>
          {t("perf.download")}
        </a>
        <button
          disabled={!offset}
          onClick={() => {
            setPage(undefined);
            setOffset(Math.max(0, offset - 8192));
          }}
        >
          {t("perf.previous")}
        </button>
        <button
          disabled={!page?.has_more}
          onClick={() => {
            setOffset(page!.next_offset);
            setPage(undefined);
          }}
        >
          {t("perf.next")}
        </button>
      </div>
      <pre className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-4 text-sm">
        {error || page?.content || t("perf.loading")}
      </pre>
    </dialog>
  );
}
export function LazyTrace({ base }: { base: string }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [offset, setOffset] = useState(0);
  const [history, setHistory] = useState<number[]>([]);
  const [detail, setDetail] = useState<number>();
  const [nodes, setNodes] = useState<TraceNode[]>([]);
  const [next, setNext] = useState<number | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    const c = new AbortController();
    void request<{
      trace: TraceNode[];
      next_offset: number;
      has_more: boolean;
    }>(`${base}/trace?offset=${offset}`, { signal: c.signal }).then((res) => {
      if (c.signal.aborted) return;
      if (!res.ok) {
        setError(res.error);
        return;
      }
      traces.set(
        `${base}:${offset}`,
        res.data.trace,
        JSON.stringify(res.data.trace).length * 2,
      );
      setNodes(res.data.trace);
      setNext(res.data.has_more ? res.data.next_offset : null);
    });
    return () => c.abort();
  }, [open, base, offset]);
  return (
    <div>
      <button
        className="text-xs"
        aria-expanded={open}
        onClick={() => {
          if (!open) {
            const cached = traces.get(`${base}:${offset}`);
            if (cached) setNodes(cached);
          }
          setOpen((v) => !v);
        }}
      >
        ▸ {t("perf.trace")}
      </button>
      {open && (
        <>
          {error || <VirtualTrace nodes={nodes} />}
          {nodes
            .filter((n) => n.detail_index !== undefined)
            .map((n) => (
              <button
                key={n.detail_index}
                onClick={() => setDetail(n.detail_index)}
              >
                {t("perf.fullTrace")}
              </button>
            ))}
          {detail !== undefined && (
            <ContentReader
              base={base}
              node={detail}
              close={() => setDetail(undefined)}
            />
          )}
          <button
            disabled={!history.length}
            onClick={() => {
              setOffset(history.at(-1)!);
              setHistory((v) => v.slice(0, -1));
            }}
          >
            {t("perf.previous")}
          </button>
          <button
            disabled={next === null}
            onClick={() => {
              setHistory((v) => [...v, offset]);
              setOffset(next!);
            }}
          >
            {t("perf.next")}
          </button>
        </>
      )}
    </div>
  );
}
