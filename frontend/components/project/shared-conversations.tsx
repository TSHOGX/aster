"use client";
import { useEffect, useState } from "react";
import { api, APIError, encode, type Conversation, type Message } from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import { MarkdownView } from "@/components/workbench/markdown-view";

export function SharedConversations({ selected, onSelect }: { selected: string; onSelect: (id: string) => void }) {
  const tr = useT();
  const [page, setPage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [list, setList] = useState<{ key: string; items?: Conversation[]; error?: string }>();
  const key = `${page}:${attempt}`;
  useEffect(() => {
    const controller = new AbortController();
    void api<{ conversations: Conversation[] }>(`/api/conversations?after=${encode(page)}&limit=50`, { signal: controller.signal })
      .then(data => { if (!controller.signal.aborted) setList({ key, items: data.conversations }); })
      .catch(e => { if (!controller.signal.aborted) setList({ key, error: String(e) }); });
    return () => controller.abort();
  }, [key, page]);
  const current = list?.key === key ? list : undefined;
  return <section className="adjacent-panel">
    <h2>{tr("Shared with me")}</h2>
    {!current && <p role="status">{tr("Loading…")}</p>}
    {current?.error && <p className="notice error" role="alert">{current.error} <button onClick={() => setAttempt(n => n + 1)}>{tr("Retry")}</button></p>}
    <div className="row-actions">{current?.items?.map(item => <button key={item.session_id} aria-current={selected === item.session_id ? "page" : undefined} onClick={() => onSelect(item.session_id)}>{item.title}</button>)}</div>
    {current?.items?.length === 0 && !selected && <p>{tr("No conversations have been shared with you.")}</p>}
    <div className="row-actions">
      {page && <button onClick={() => setPage("")}>{tr("First page")}</button>}
      {current?.items?.length === 50 && <button onClick={() => setPage(current.items!.at(-1)!.session_id)}>{tr("Show more")}</button>}
    </div>
    {selected && <SharedDetail key={selected} selected={selected} onClose={() => onSelect("")} />}
  </section>;
}

function SharedDetail({ selected, onClose }: { selected: string; onClose: () => void }) {
  const tr = useT();
  const [after, setAfter] = useState(0);
  const [attempt, setAttempt] = useState(0);
  const [detail, setDetail] = useState<{ key: string; title?: string; messages?: Message[]; error?: string }>();
  const key = `${after}:${attempt}`;
  useEffect(() => {
    const controller = new AbortController();
    const path = `/api/conversations/${encode(selected)}`;
    let source: EventSource | undefined;
    let running = false;
    let again = false;
    const refresh = async () => {
      if (running) { again = true; return; }
      running = true;
      try {
        // Grant eligibility is independent of the paginated list and the broader
        // owner-or-reader permission of the generic message endpoint.
        const conversation = await api<Conversation>(path, { signal: controller.signal });
        const data = await api<{ messages: Message[] }>(`${path}/messages?after=${after}&limit=100`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        setDetail({ key, title: conversation.title, messages: data.messages });
        if (!source) {
          source = new EventSource(`${path}/events`);
          source.addEventListener("conversation", () => void refresh());
          source.addEventListener("closed", () => { source?.close(); void refresh(); });
          source.onerror = () => {
            // Reconnection checks eligibility without reloading message bodies.
            void api<Conversation>(path, { signal: controller.signal }).catch(e => {
              if (!controller.signal.aborted && e instanceof APIError && [401, 403, 404].includes(e.status)) {
                source?.close();
                controller.abort();
                setDetail({ key, error: tr("This shared conversation is unavailable.") });
              }
            });
          };
        }
      } catch (e) {
        if (controller.signal.aborted) return;
        source?.close();
        source = undefined;
        again = false;
        setDetail({ key, error: e instanceof APIError && [401, 403, 404].includes(e.status) ? tr("This shared conversation is unavailable.") : String(e) });
      } finally {
        running = false;
        if (again && !controller.signal.aborted) { again = false; void refresh(); }
      }
    };
    void refresh();
    return () => { controller.abort(); source?.close(); };
  }, [selected, after, key, tr]);
  const current = detail?.key === key ? detail : undefined;
  return <section aria-label={tr("Shared conversation")}>
    <button onClick={onClose}>{tr("Close")}</button>
    {!current && <p role="status">{tr("Loading…")}</p>}
    {current?.error && <p className="notice error" role="alert">{current.error} <button onClick={() => setAttempt(n => n + 1)}>{tr("Retry")}</button></p>}
    {current?.title && <h3>{current.title}</h3>}
    {current?.messages?.map(message => <article className={`chat-message ${message.role}`} key={message.id}><small>{message.role}</small><MarkdownView content={message.content} /></article>)}
    {after > 0 && <button onClick={() => setAfter(0)}>{tr("First page")}</button>}
    {current?.messages?.length === 100 && <button onClick={() => setAfter(current.messages!.at(-1)!.sequence)}>{tr("Next messages")}</button>}
  </section>;
}
