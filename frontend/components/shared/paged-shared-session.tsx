"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { request } from "@/lib/api";
import { mergeMessageWindow } from "@/lib/message-window";
import type {
  ApiMessage,
  MessagePage,
  SharedLinkSession,
  SharedLinkFile,
} from "@/lib/types";
import { VirtualTranscript } from "../workbench/virtual-transcript";
import { MessageBubble } from "../workbench/message-bubble";
import { LegacySharedSessionView } from "./shared-session-view";
import { SharedFilePanel } from "./shared-file-panel";

export function PagedSharedSession({ token }: { token: string }) {
  const base = `/api/shared/${encodeURIComponent(token)}`;
  const [session, setSession] = useState<SharedLinkSession>();
  const [page, setPage] = useState<MessagePage>();
  const [error, setError] = useState("");
  const [files, setFiles] = useState<SharedLinkFile[]>([]);
  const [fileHistory, setFileHistory] = useState<string[]>([]);
  const [mentions, setMentions] = useState<SharedLinkFile[]>([]);
  const messageRequest = useRef<AbortController | null>(null);
  const [cursor, setCursor] = useState("");
  const [moreFiles, setMoreFiles] = useState(false);
  const loadFiles = useCallback(
    async (after = "", signal?: AbortSignal) => {
      const r = await request<{
        entries: SharedLinkFile[];
        next_cursor: string;
        has_more: boolean;
      }>(`${base}/artifacts/search?cursor=${encodeURIComponent(after)}`, {
        signal,
      });
      if (signal?.aborted) return;
      if (r.ok) {
        setFiles(r.data.entries);
        setCursor(r.data.next_cursor);
        setMoreFiles(r.data.has_more);
      }
    },
    [base],
  );
  const load = useCallback(
    async (query = "", signal?: AbortSignal) => {
      messageRequest.current?.abort();
      const controller = new AbortController();
      messageRequest.current = controller;
      const r = await request<MessagePage>(
        `${base}/messages/page${query ? `?${query}` : ""}`,
        {
          signal: signal
            ? AbortSignal.any([signal, controller.signal])
            : controller.signal,
        },
      );
      if (controller.signal.aborted) return;
      if (signal?.aborted) return;
      if (!r.ok) {
        if (r.status === 404) {
          setPage(undefined);
          setFiles([]);
          setSession(undefined);
        }
        setError(r.status === 404 ? "This link is not available" : r.error);
        return;
      }
      setError("");
      setPage((previous) => ({
        ...r.data,
        messages:
          query && previous
            ? mergeMessageWindow(
                previous.messages ?? [],
                r.data.messages ?? [],
                previous.messages ?? [],
                query.startsWith("before") ? r.data.before_id : r.data.after_id,
              )
            : r.data.messages,
      }));
    },
    [base],
  );
  useEffect(() => {
    const c = new AbortController();
    void request<SharedLinkSession>(base, { signal: c.signal }).then(
      async (r) => {
        if (c.signal.aborted) return;
        if (!r.ok) {
          setError("This link is not available");
          return;
        }
        setSession(r.data);
        if (!r.data.message_pages) return;
        await load("", c.signal);
        await loadFiles("", c.signal);
      },
    );
    return () => {
      c.abort();
      messageRequest.current?.abort();
    };
  }, [base, load, loadFiles]);
  useEffect(() => {
    if (!page?.latest_final_id) return;
    const c = new AbortController();
    void request<{ files: SharedLinkFile[] }>(
      `${base}/messages/${page.latest_final_id}/mentioned-files`,
      { signal: c.signal },
    ).then((r) => {
      if (!c.signal.aborted && r.ok) setMentions(r.data.files);
    });
    return () => c.abort();
  }, [base, page?.latest_final_id]);
  const rows = page?.messages ?? [];
  const render = useCallback(
    (message: ApiMessage) => (
      <MessageBubble
        message={message}
        sessionId={session?.id ?? ""}
        resourceBase={base}
        showActions={false}
      />
    ),
    [session, base],
  );
  if (session && !session.message_pages)
    return <LegacySharedSessionView token={token} />;
  if (!session || !page)
    return <main className="p-6">{error || "Loading…"}</main>;
  return (
    <main className="flex h-dvh min-h-0 flex-col">
      <header className="flex justify-between border-b p-4">
        <span>
          {session.title} · read-only{session.frozen ? " · snapshot" : ""}
        </span>
        <button
          onClick={() => {
            void load();
            void loadFiles();
          }}
        >
          Refresh
        </button>
      </header>
      {error && <p>{error}</p>}
      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        {rows.length ? (
          <VirtualTranscript
            sessionId={base}
            rows={rows}
            render={render}
            hasOlder={page.has_older}
            hasNewer={page.has_newer}
            loadOlder={() => void load(`before_id=${rows[0].id}`)}
            loadNewer={() => void load(`after_id=${rows.at(-1)!.id}`)}
            onLatest={() => void load()}
          />
        ) : (
          <p>No messages</p>
        )}
        <div className="flex min-h-0 w-full flex-col md:w-80">
          <SharedFilePanel
            token={token}
            files={files}
            mentionedFiles={mentions}
          />
          {fileHistory.length > 0 && (
            <button
              onClick={() => {
                const previous = fileHistory.slice(0, -1);
                setFileHistory(previous);
                void loadFiles(previous.at(-1) ?? "");
              }}
            >
              Previous files
            </button>
          )}
          {moreFiles && (
            <button
              onClick={() => {
                setFileHistory((v) => [...v, cursor]);
                void loadFiles(cursor);
              }}
            >
              Next files
            </button>
          )}
        </div>
      </div>
    </main>
  );
}
