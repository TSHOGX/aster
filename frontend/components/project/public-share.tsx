"use client";
import { brandName } from "@/lib/ui-config";
import { useT } from "@/lib/i18n";
import { useEffect, useState } from "react";
import { api, encode, type Message, type Share } from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { FilePreview } from "./file-preview";
import { ResourceProvider } from "./resource-provider";
import "./workbench.css";
export function PublicShare({ token }: { token: string }) {
  return <ResourceProvider key={token} actor="public-share"><ShareContent token={token} /></ResourceProvider>;
}
function ShareContent({ token }: { token: string }) {
  const tr = useT();
  const [share, setShare] = useState<Share | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [after, setAfter] = useState(0);
  const [selected, setSelected] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    void Promise.all([
      api<Share>(`/api/shared/${encode(token)}`, { signal: abort.signal }),
      api<{
        messages: Message[];
      }>(`/api/shared/${encode(token)}/messages?after=${after}&limit=100`, {
        signal: abort.signal,
      }),
    ])
      .then(([share, response]) => {
        setShare(share);
        setMessages(response.messages);
      })
      .catch((e) => {
        if (!abort.signal.aborted) {
          setShare(null);
          setMessages([]);
          setError(
            e instanceof Error ? e.message : "This share is unavailable.",
          );
        }
      });
    return () => abort.abort();
  }, [token, after]);
  const url = `/api/shared/${encode(token)}/files?path=${encode(selected)}`;
  return (
    <main className="public-share">
      <header>
        <small>
          {brandName} · {tr("Shared snapshot")}
        </small>
        <h1>{share?.title || tr("Shared conversation")}</h1>
        <p>{tr("A frozen conversation and its selected files.")}</p>
      </header>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      {messages.map((message) => (
        <article className={`chat-message ${message.role}`} key={message.id}>
          <small>{message.role}</small>
          <MarkdownView content={message.content} />
        </article>
      ))}
      <div className="row-actions">
        {after > 0 && (
          <button onClick={() => setAfter(0)}>{tr("First messages")}</button>
        )}
        {messages.length === 100 && (
          <button onClick={() => setAfter(messages.at(-1)?.sequence || 0)}>
            {tr("Next messages")}
          </button>
        )}
      </div>
      {share?.files.length ? (
        <section>
          <h2>{tr("Frozen files")}</h2>
          <div className="row-actions">
            {share.files.map((file) => (
              <button key={file.path} onClick={() => setSelected(file.path)}>
                {file.path}
              </button>
            ))}
          </div>
        </section>
      ) : null}
      {selected && share && (
        <section className="selected-file">
          <header className="panel-heading">
            <strong>{selected}</strong>
            <a href={url} download>
              {tr("Download")}
            </a>
            <button onClick={() => setSelected("")}>{tr("Close file")}</button>
          </header>
          <FilePreview key={selected} project="public-share" path={selected} href={url} />
        </section>
      )}
    </main>
  );
}
