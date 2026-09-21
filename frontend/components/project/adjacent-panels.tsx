"use client";
import { useCallback, useEffect, useState } from "react";
import {
  api,
  command,
  encode,
  newID,
  projectURL,
  sessionURL,
  type AgentSession,
  type Conversation,
  type Message,
  type Notice,
  type Schedule,
  type ScheduleRun,
  type SearchResult,
  type Share,
} from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";

export function SchedulePanel({
  project,
  sessions,
  onOpen,
}: {
  project: string;
  sessions: AgentSession[];
  onOpen: (id: string) => void;
}) {
  const [schedules, setSchedules] = useState<Schedule[]>([]);
  const [runs, setRuns] = useState<ScheduleRun[]>([]);
  const [error, setError] = useState("");
  const [title, setTitle] = useState("");
  const [prompt, setPrompt] = useState("");
  const [cron, setCron] = useState("");
  const [session, setSession] = useState("");
  const [busy, setBusy] = useState(false);
  const base = `${projectURL(project)}/schedules`;
  const refresh = useCallback(async () => {
    try {
      setSchedules((await api<{ schedules: Schedule[] }>(base)).schedules);
    } catch (e) {
      setError(String(e));
    }
  }, [base]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
  }, [refresh]);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Schedule action failed.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="adjacent-panel">
      <h2>Project schedules</h2>
      <p>
        Runs share this Project’s workspace. Disabling a schedule stops future
        admissions.
      </p>
      {error && (
        <p role="alert" className="notice error">
          {error}
        </p>
      )}
      <form
        className="schedule-form"
        onSubmit={(e) => {
          e.preventDefault();
          void act(async () => {
            await command(base, {
              id: newID(),
              title,
              prompt,
              cron,
              timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
              session_id: session,
              enabled: true,
            });
            setTitle("");
            setPrompt("");
          });
        }}
      >
        <label>
          Schedule name
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            required
            maxLength={256}
          />
        </label>
        <label>
          Prompt
          <textarea
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            required
            maxLength={64000}
          />
        </label>
        <label>
          Target Session
          <select value={session} onChange={(e) => setSession(e.target.value)}>
            <option value="">Create a new Session per run</option>
            {sessions.map((s) => (
              <option key={s.id} value={s.id}>
                {s.title}
              </option>
            ))}
          </select>
        </label>
        <label>
          Cron expression
          <input
            value={cron}
            onChange={(e) => setCron(e.target.value)}
            placeholder="Leave empty for manual runs"
          />
          <small>
            Five fields, in {Intl.DateTimeFormat().resolvedOptions().timeZone}
          </small>
        </label>
        <button className="primary" disabled={busy}>
          Create schedule
        </button>
      </form>
      {schedules.map((s) => (
        <article className="schedule-card" key={s.id}>
          <h3>{s.title}</h3>
          <p>{s.prompt}</p>
          <small>
            {s.cron || "Manual only"} · {s.timezone} ·{" "}
            {s.enabled ? "enabled" : "disabled"}
            {s.next_at ? ` · next ${new Date(s.next_at).toLocaleString()}` : ""}
          </small>
          <div className="row-actions">
            <button
              disabled={busy || !s.enabled}
              onClick={() =>
                void act(async () => {
                  const run = await command<ScheduleRun>(
                    `${base}/${encode(s.id)}/run`,
                    { request_id: newID() },
                  );
                  onOpen(run.session_id);
                })
              }
            >
              Run now
            </button>
            <button
              disabled={busy}
              onClick={() =>
                void act(() =>
                  command(
                    `${base}/${encode(s.id)}`,
                    { ...s, enabled: !s.enabled },
                    "PATCH",
                  ),
                )
              }
            >
              {s.enabled ? "Disable" : "Enable"}
            </button>
            <button
              disabled={busy}
              onClick={() =>
                void act(async () => {
                  const prompt = window.prompt("Schedule prompt", s.prompt);
                  if (prompt)
                    await command(
                      `${base}/${encode(s.id)}`,
                      { ...s, prompt },
                      "PATCH",
                    );
                })
              }
            >
              Edit prompt
            </button>
            <button
              onClick={() =>
                void act(async () =>
                  setRuns(
                    (
                      await api<{ runs: ScheduleRun[] }>(
                        `${base}/${encode(s.id)}/runs`,
                      )
                    ).runs,
                  ),
                )
              }
            >
              Run history
            </button>
            <button
              className="danger"
              onClick={() => {
                if (
                  window.confirm(
                    "Delete this schedule? Admitted runs retain their own controls.",
                  )
                )
                  void act(() =>
                    command(
                      `${base}/${encode(s.id)}?revision=${s.revision}`,
                      undefined,
                      "DELETE",
                    ),
                  );
              }}
            >
              Delete schedule
            </button>
          </div>
        </article>
      ))}
      {runs.length > 0 && (
        <section>
          <h3>Run history</h3>
          {runs.map((run) => (
            <button
              className="history-row"
              key={run.id}
              onClick={() => onOpen(run.session_id)}
            >
              {run.due_at
                ? new Date(run.due_at).toLocaleString()
                : "Manual run"}{" "}
              · {run.state}
            </button>
          ))}
        </section>
      )}
    </section>
  );
}
export function SharePanel({
  project,
  sessions,
}: {
  project: string;
  sessions: AgentSession[];
}) {
  const [session, setSession] = useState(sessions[0]?.id || "");
  const [reader, setReader] = useState("");
  const [paths, setPaths] = useState("");
  const [shares, setShares] = useState<Share[]>([]);
  const [link, setLink] = useState("");
  const [error, setError] = useState("");
  const refresh = useCallback(async () => {
    if (!session) return;
    try {
      setShares(
        (
          await api<{ shares: Share[] }>(
            `${sessionURL(project, session)}/shares`,
          )
        ).shares,
      );
    } catch (e) {
      setError(String(e));
    }
  }, [project, session]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
  }, [refresh]);
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
      <h2>Share a conversation</h2>
      <p>
        Readers receive the conversation and explicitly frozen files. Live
        Project resources stay private.
      </p>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      <label>
        Agent Session
        <select value={session} onChange={(e) => setSession(e.target.value)}>
          <option value="">Choose a Session</option>
          {sessions.map((s) => (
            <option key={s.id} value={s.id}>
              {s.title}
            </option>
          ))}
        </select>
      </label>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void act(() =>
            command(`${sessionURL(project, session)}/readers`, { reader }),
          );
        }}
      >
        <label>
          Read-only reader username
          <input
            value={reader}
            onChange={(e) => setReader(e.target.value)}
            required
          />
        </label>
        <div className="row-actions">
          <button disabled={!session}>Grant conversation access</button>
          <button
            type="button"
            disabled={!session || !reader}
            onClick={() =>
              void act(() =>
                command(
                  `${sessionURL(project, session)}/readers/${encode(reader)}`,
                  undefined,
                  "DELETE",
                ),
              )
            }
          >
            Revoke reader
          </button>
        </div>
      </form>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void act(async () => {
            const result = await command<{ token: string }>(
              `${sessionURL(project, session)}/shares`,
              {
                paths: paths
                  .split("\n")
                  .map((p) => p.trim().replace(/^\/workspace\//, ""))
                  .filter(Boolean),
              },
            );
            setLink(`${location.origin}/s/${result.token}`);
          });
        }}
      >
        <label>
          Frozen files (one Project path per line)
          <textarea
            value={paths}
            onChange={(e) => setPaths(e.target.value)}
            placeholder="results/report.md"
          />
        </label>
        <button disabled={!session} className="primary">
          Create public snapshot · 7 days
        </button>
      </form>
      {link && (
        <p className="share-link">
          <a href={link} target="_blank" rel="noreferrer">
            {link}
          </a>
          <button onClick={() => void navigator.clipboard.writeText(link)}>
            Copy link
          </button>
        </p>
      )}
      {shares.map((share) => (
        <article className="schedule-card" key={share.id}>
          <strong>{share.title}</strong> · {share.state}
          <small>Expires {new Date(share.expires_at).toLocaleString()}</small>
          <div className="row-actions">
            {share.state === "published" && (
              <button
                onClick={() =>
                  void act(async () => {
                    const result = await command<{ token: string }>(
                      `${projectURL(project)}/shares/${encode(share.id)}/rotate`,
                    );
                    setLink(`${location.origin}/s/${result.token}`);
                  })
                }
              >
                Rotate link
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
              Revoke snapshot
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
  onOpen: (project: string, session?: string) => void;
}) {
  const [items, setItems] = useState<Notice[]>([]);
  const [error, setError] = useState("");
  const [unread, setUnread] = useState(0);
  const load = useCallback(async (after = "") => {
    try {
      const result = await api<{ notifications: Notice[]; unread: number }>(
        `/api/notifications?after=${encode(after)}&limit=50`,
      );
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
        Inbox <small>{unread} unread</small>
      </h2>
      {error && <p className="notice error">{error}</p>}
      {!items.length && <p>No notifications.</p>}
      {items.map((notice) => (
        <article className="schedule-card" key={notice.id}>
          <strong>
            {notice.title}
            {!notice.read ? " · unread" : ""}
          </strong>
          <p>{notice.body}</p>
          <small>{new Date(notice.created_at).toLocaleString()}</small>
          <div className="row-actions">
            {notice.project_id && (
              <button
                onClick={() => onOpen(notice.project_id!, notice.session_id)}
              >
                Open conversation
              </button>
            )}
            <button
              onClick={() =>
                void command(
                  `/api/notifications/${encode(notice.id)}/read`,
                ).then(() => load())
              }
            >
              Mark read
            </button>
          </div>
        </article>
      ))}
      <div className="row-actions">
        <button onClick={() => void load()}>Latest</button>
        {items.length === 50 && (
          <button onClick={() => void load(items.at(-1)?.id)}>
            Older notifications
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
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [error, setError] = useState("");
  const search = async (after = "") => {
    try {
      setResults(
        (
          await api<{ results: SearchResult[] }>(
            `/api/search?q=${encode(query)}&after=${encode(after)}`,
          )
        ).results,
      );
      setError("");
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <section className="adjacent-panel">
      <h2>Search conversations</h2>
      <form
        className="search-form"
        onSubmit={(e) => {
          e.preventDefault();
          void search();
        }}
      >
        <input
          aria-label="Search conversations"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          required
          maxLength={256}
          placeholder="Search a topic, decision, or result…"
        />
        <button className="primary">Search</button>
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
          <small>Project {result.project_id.slice(0, 8)}</small>
        </button>
      ))}
      {results.length === 50 && (
        <button onClick={() => void search(results.at(-1)?.key)}>
          More results
        </button>
      )}
    </section>
  );
}
export function SharedConversations({
  initialSession = "",
}: {
  initialSession?: string;
}) {
  const [items, setItems] = useState<Conversation[]>([]);
  const [selected, setSelected] = useState(initialSession);
  const [messages, setMessages] = useState<Message[]>([]);
  const [error, setError] = useState("");
  const [after, setAfter] = useState(0);
  useEffect(() => {
    void api<{ conversations: Conversation[] }>("/api/conversations")
      .then((data) => setItems(data.conversations))
      .catch((e) => setError(String(e)));
  }, []);
  useEffect(() => {
    if (!selected) return;
    let canceled = false;
    const refresh = async () => {
      try {
        const data = await api<{ messages: Message[] }>(
          `/api/conversations/${encode(selected)}/messages?after=${after}&limit=100`,
        );
        if (!canceled) setMessages(data.messages);
      } catch (e) {
        if (!canceled) {
          setMessages([]);
          setError(String(e));
        }
      }
    };
    void refresh();
    const source = new EventSource(
      `/api/conversations/${encode(selected)}/events`,
    );
    source.addEventListener("conversation", () => void refresh());
    source.onerror = () => void refresh();
    return () => {
      canceled = true;
      source.close();
    };
  }, [selected, after]);
  return (
    <section className="adjacent-panel">
      <h2>Shared with me</h2>
      {error && <p className="notice error">{error}</p>}
      <div className="row-actions">
        {items.map((item) => (
          <button
            key={item.session_id}
            onClick={() => {
              setSelected(item.session_id);
              setAfter(0);
            }}
          >
            {item.title}
          </button>
        ))}
      </div>
      {!items.length && <p>No conversations have been shared with you.</p>}
      {messages.map((message) => (
        <article className={`chat-message ${message.role}`} key={message.id}>
          <small>{message.role}</small>
          <MarkdownView content={message.content} />
        </article>
      ))}
      {after > 0 && <button onClick={() => setAfter(0)}>First page</button>}
      {messages.length === 100 && (
        <button onClick={() => setAfter(messages.at(-1)?.sequence || 0)}>
          Next messages
        </button>
      )}
    </section>
  );
}
