"use client";
import { Select } from "@web/select";
import { useT } from "@/lib/i18n";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  command,
  encode,
  projectURL,
  sessionURL,
  type AgentSession,
  type Notice,
  type SearchResult,
  type Share,
} from "@/lib/project-api";
import { useCapabilities } from "./capability-provider";
type ReaderAccount = { id: string; username: string; display_name: string; status: string; suspended?: boolean };
export function SharePanel({
  project,
  sessions,
  initialSession,
}: {
  project: string;
  sessions: AgentSession[];
  initialSession?: string;
}) {
  const tr = useT();
  const [session, setSession] = useState(
    initialSession || sessions[0]?.id || "",
  );
  const can = useCapabilities();
  const canShare = can("conversations.share"), canPublish = can("shares.public.create");
  const [reader, setReader] = useState("");
  const [query, setQuery] = useState("");
  const [candidates, setCandidates] = useState<ReaderAccount[]>([]);
  const [readers, setReaders] = useState<ReaderAccount[]>([]);
  const [readerAfter, setReaderAfter] = useState("");
  const [paths, setPaths] = useState("");
  const [shares, setShares] = useState<Share[]>([]);
 const reactivationRequests = useRef(new Map<string,string>());
  const [link, setLink] = useState("");
  const [error, setError] = useState("");
  const refresh = useCallback(async () => {
    if (!session) return;
    try {
      const [snapshots, granted] = await Promise.all([
        api<{ shares: Share[] }>(`${sessionURL(project, session)}/shares`),
        api<{ readers: ReaderAccount[] }>(`${sessionURL(project, session)}/readers?after=${encode(readerAfter)}&limit=50`),
      ]);
      setShares(snapshots.shares);
      setReaders(granted.readers);
    } catch (e) {
      setError(String(e));
    }
  }, [project, session, readerAfter]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
  }, [refresh]);
  useEffect(() => {
    let gone = false;
    const timer = setTimeout(() => {
      if (!session || !canShare) { setCandidates([]); return; }
      void api<{readers:ReaderAccount[]}>(`${sessionURL(project, session)}/reader-candidates?q=${encode(query)}&limit=50`)
        .then(result => { if (!gone) setCandidates(result.readers); })
        .catch(error => { if (!gone) { setCandidates([]); setError(String(error)); } });
    }, 200);
    return () => { gone = true; clearTimeout(timer); };
  }, [project, session, query, canShare]);
  const act = async (fn: () => Promise<unknown>) => {
    setError("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Sharing failed.");
    }
  };
  return (
    <section className="adjacent-panel">
      <h2>{tr("Share a conversation")}</h2>
      <p>
        {tr(
          "Readers receive the conversation and explicitly frozen files. Live Project resources stay private.",
        )}
      </p>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      <label>
        {tr("Agent Session")}
        <Select aria-label={tr("Agent Session")} value={session} onChange={(e) => { setSession(e.target.value); setReader(""); setReaderAfter(""); setLink(""); }}>
          <option value="">{tr("Choose a Session")}</option>
          {sessions.map((s) => (
            <option key={s.id} value={s.id}>
              {s.title}
            </option>
          ))}
        </Select>
      </label>
      <form onSubmit={event => {
        event.preventDefault();
        if (!canShare || !candidates.some(account => account.id === reader)) return;
        void act(() => command(`${sessionURL(project, session)}/readers`, { reader_id: reader }));
      }}>
        <label>{tr("Find a reader")}
          <input value={query} maxLength={128} disabled={!canShare} onChange={event => { setQuery(event.target.value); setReader(""); }} />
        </label>
        <label>{tr("Reader account")}
          <Select aria-label={tr("Reader account")} value={reader} required disabled={!canShare} onChange={event => setReader(event.target.value)}>
            <option value="">{tr("Choose an account")}</option>
            {candidates.map(account => <option key={account.id} value={account.id}>{account.display_name} (@{account.username})</option>)}
          </Select>
        </label>
        <button disabled={!session || !canShare || !reader}>{tr("Grant conversation access")}</button>
      </form>
      {readers.map(account => <article className="schedule-card" key={account.id}>
        <strong>{account.display_name}</strong><small>@{account.username} · {account.status}{account.suspended && ` · ${tr("Access suspended")}`}</small>
        {account.suspended && <button type="button" disabled={!canShare} onClick={() => void act(async () => {
          const key=`reader/${session}/${account.id}`;
          let requestID=reactivationRequests.current.get(key);
          if(!requestID) {requestID=crypto.randomUUID();reactivationRequests.current.set(key,requestID);}
          await command(`${sessionURL(project,session)}/readers/${encode(account.id)}/reactivate`,{request_id:requestID});
          reactivationRequests.current.delete(key);
        })}>{tr("Reactivate reader")}</button>}
        <button type="button" onClick={() => void act(() => command(`${sessionURL(project, session)}/readers/${encode(account.id)}`, undefined, "DELETE"))}>{tr("Revoke reader")}</button>
      </article>)}
      <div className="row-actions">
        {readerAfter && <button onClick={() => setReaderAfter("")}>{tr("Latest")}</button>}
        {readers.length === 50 && <button onClick={() => setReaderAfter(readers.at(-1)!.id)}>{tr("More readers")}</button>}
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void act(async () => {
            const result = await command<{
              token: string;
            }>(`${sessionURL(project, session)}/shares`, {
              paths: paths
                .split("\n")
                .map((p) => p.trim())
                .filter(Boolean),
            });
            setLink(`${location.origin}/s/${result.token}`);
          });
        }}
      >
        <label>
          {tr("Frozen files (one Project path per line)")}
          <textarea
            value={paths}
            onChange={(e) => setPaths(e.target.value)}
            placeholder="results/report.md"
          />
        </label>
        <button disabled={!session || !canPublish} className="primary">
          {tr("Create public snapshot \u00B7 7 days")}
        </button>
      </form>
      {link && (
        <p className="share-link">
          <a href={link} target="_blank" rel="noreferrer">
            {link}
          </a>
          <button onClick={() => void navigator.clipboard.writeText(link)}>
            {tr("Copy link")}
          </button>
        </p>
      )}
      {shares.map((share) => (
        <article className="schedule-card" key={share.id}>
          <strong>{share.title}</strong> · {share.state}{share.suspended && ` · ${tr("Access suspended")}`}
          <small>
            {tr("Expires")}
            {new Date(share.expires_at).toLocaleString()}
          </small>
          <div className="row-actions">
            {share.state === "published" && share.suspended && <button disabled={!canPublish} onClick={() => void act(async () => {
              let requestID=reactivationRequests.current.get(share.id);
              if(!requestID) {requestID=crypto.randomUUID();reactivationRequests.current.set(share.id,requestID);}
              await command(`${projectURL(project)}/shares/${encode(share.id)}/reactivate`,{request_id:requestID});
              reactivationRequests.current.delete(share.id);
            })}>{tr("Reactivate public link")}</button>}
            {share.state === "published" && (
              <button disabled={!canPublish}
                onClick={() =>
                  void act(async () => {
                    const result = await command<{
                      token: string;
                    }>(
                      `${projectURL(project)}/shares/${encode(share.id)}/rotate`,
                    );
                    setLink(`${location.origin}/s/${result.token}`);
                  })
                }
              >
                {tr("Rotate link")}
              </button>
            )}
            <button
              onClick={() =>
                void act(() =>
                  command(
                    `${projectURL(project)}/shares/${encode(share.id)}`,
                    undefined,
                    "DELETE",
                  ),
                )
              }
            >
              {tr("Revoke snapshot")}
            </button>
          </div>
        </article>
      ))}
    </section>
  );
}
export function Inbox({
  onOpen,
}: {
  onOpen: (project: string, session?: string, message?: string) => void;
}) {
  const tr = useT();
  const [items, setItems] = useState<Notice[]>([]);
  const [error, setError] = useState("");
  const [unread, setUnread] = useState(0);
  const load = useCallback(async (after = "") => {
    try {
      const result = await api<{
        notifications: Notice[];
        unread: number;
      }>(`/api/notifications?after=${encode(after)}&limit=50`);
      setItems(result.notifications);
      setUnread(result.unread);
    } catch (e) {
      setError(String(e));
    }
  }, []);
  useEffect(() => {
    void Promise.resolve().then(() => load());
  }, [load]);
  return (
    <section className="adjacent-panel">
      <h2>
        {tr("Inbox")}
        <small>
          {unread}
          {tr("unread")}
        </small>
      </h2>
      {error && <p className="notice error">{error}</p>}
      {!items.length && <p>{tr("No notifications.")}</p>}
      {items.map((notice) => (
        <article className="schedule-card" key={notice.id}>
          <strong>
            {notice.title}
            {!notice.read ? " · unread" : ""}
          </strong>
          <p>{notice.body}</p>
          <small>{new Date(notice.created_at).toLocaleString()}</small>
          <div className="row-actions">
            {notice.schedule_id && notice.project_id && <a href={`/?view=schedules&schedule_project=${encode(notice.project_id)}&schedule=${encode(notice.schedule_id)}&status=exhausted`}>{tr("View task")}</a>}
            {notice.project_id && notice.session_id && (
              <button
                onClick={() => onOpen(notice.project_id!, notice.session_id,notice.message_id)}
              >
                {tr("Open conversation")}
              </button>
            )}
            <button
              onClick={() =>
                void command(
                  `/api/notifications/${encode(notice.id)}/read`,
                ).then(() => load())
              }
            >
              {tr("Mark read")}
            </button>
          </div>
        </article>
      ))}
      <div className="row-actions">
        <button onClick={() => void load()}>{tr("Latest")}</button>
        {items.length === 50 && (
          <button onClick={() => void load(items.at(-1)?.id)}>
            {tr("Older notifications")}
          </button>
        )}
      </div>
    </section>
  );
}
export function SearchPanel({
  onOpen,
}: {
  onOpen: (project: string, session: string, message?: string) => void;
}) {
  const tr = useT();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [error, setError] = useState("");
  const search = async (after = "") => {
    try {
      setResults(
        (
          await api<{
            results: SearchResult[];
          }>(`/api/search?q=${encode(query)}&after=${encode(after)}`)
        ).results,
      );
      setError("");
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <section className="adjacent-panel">
      <h2>{tr("Search conversations")}</h2>
      <form
        className="search-form"
        onSubmit={(e) => {
          e.preventDefault();
          void search();
        }}
      >
        <input
          aria-label={tr("Search conversations")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          required
          maxLength={256}
          placeholder={tr("Search a topic, decision, or result\u2026")}
        />
        <button className="primary">{tr("Search")}</button>
      </form>
      {error && <p className="notice error">{error}</p>}
      {results.map((result) => (
        <button
          className="search-result"
          key={result.key}
          onClick={() =>
            onOpen(result.project_id, result.session_id, result.message_id)
          }
        >
          <strong>{result.title}</strong>
          <span>{result.snippet}</span>
          <small>
            {tr("Project")}
            {result.project_id.slice(0, 8)}
          </small>
        </button>
      ))}
      {results.length === 50 && (
        <button onClick={() => void search(results.at(-1)?.key)}>
          {tr("More results")}
        </button>
      )}
    </section>
  );
}
