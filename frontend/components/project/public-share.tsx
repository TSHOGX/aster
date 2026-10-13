"use client";
import { useCallback, useEffect, useMemo, useState } from "react";
import { displayName } from "@web/brand";
import { api, encode } from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import type { ConversationSource } from "@/lib/conversation-source";
import { ResourceProvider } from "./resource-provider";
import { SharedReader } from "./shared-workspace";
import "./workbench.css";
export function PublicShare({ token }: { token: string }) {
  return (
    <ResourceProvider key={token} actor="public-share">
      <ShareContent token={token} />
    </ResourceProvider>
  );
}
function ShareContent({ token }: { token: string }) {
  const tr = useT();
  const [metadata, setMetadata] = useState<{
    id: string;
    can_copy?: boolean;
    title: string;
    files: { path: string }[];
    allow_download: boolean;
    access_revision: number;
    expires_at: string;
  }>();
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState("");
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  useEffect(() => {
    const abort = new AbortController();
    void api<NonNullable<typeof metadata>>(`/api/shared/${encode(token)}`, {
      signal: abort.signal,
    })
      .then((value) => {
        if (!abort.signal.aborted) setMetadata(value);
      })
      .catch((e) => {
        if (!abort.signal.aborted) {
          setMetadata(undefined);
          setError(String(e));
        }
      });
    return () => abort.abort();
  }, [token, attempt]);
  const source = useMemo<ConversationSource | undefined>(
    () =>
      metadata
        ? {
            kind: "publicSnapshot",
            authorityRevision: metadata.access_revision,
            key: `public:${metadata.id}:${metadata.access_revision}`,
            session: metadata.id,
            base: `/api/shared/${encode(token)}`,
            events: `/api/shared/${encode(token)}/events`,
            capabilities: {
              read: true,
              edit: false,
              run: false,
              copy: metadata.can_copy !== false,
              download: metadata.allow_download,
            },
          }
        : undefined,
    [metadata, token],
  );
  return (
    <main className="public-share-reader">
      <header className="public-share-brand">
        <small>{displayName}</small>
      </header>
      {metadata && source ? (
        <SharedReader
          actor="public"
          source={source}
          title={metadata.title}
          attribution={`${tr("Shared snapshot")} · ${tr("Expires")} ${new Date(metadata.expires_at).toLocaleString()}`}
          paths={metadata.files.map((f) => f.path)}
          onMetadata={reload}
        />
      ) : (
        <div className="empty-state" role={error ? "alert" : "status"}>
          {error || tr("Loading…")}
          {error && <button onClick={reload}>{tr("Retry")}</button>}
        </div>
      )}
    </main>
  );
}
