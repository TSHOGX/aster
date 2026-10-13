"use client";
import { useEffect, useState } from "react";
import { ChevronDown, ChevronRight, UserRound } from "lucide-react";
import { api, encode } from "@/lib/project-api";
import type {
  ReceivedConversation,
  SharedPerson,
} from "@/lib/conversation-source";
import { CopyTasks } from "./copy-tasks";
import { useT } from "@/lib/i18n";

export function SharedNavigation({
  selected,
  reveal,
  onSelect,
  onOpenCopy,
}: {
  selected: string;
  reveal: boolean;
  onSelect: (id: string) => void;
  onOpenCopy: (project: string, session: string) => void;
}) {
  const tr = useT();
  const [toggle, setToggle] = useState<{ key: string; expanded: boolean }>({
    key: "",
    expanded: false,
  });
  const navigationKey = `${reveal}:${selected}`;
  const expanded = toggle.key === navigationKey ? toggle.expanded : reveal;
  const setExpanded = (value: boolean) =>
    setToggle({ key: navigationKey, expanded: value });
  const [sources, setSources] = useState<SharedPerson[]>([]);
  const [after, setAfter] = useState("");
  const [more, setMore] = useState(false);
  const [opened, setOpened] = useState<string[]>([]);
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [located, setLocated] = useState<ReceivedConversation>();
  useEffect(() => {
    if (!selected) return;
    const abort = new AbortController();
    void api<ReceivedConversation>(`/api/conversations/${encode(selected)}`, {
      signal: abort.signal,
    })
      .then((value) => {
        if (abort.signal.aborted) return;
        setLocated(value);
        setOpened((old) => [...new Set([...old, value.source.id])]);
      })
      .catch(() => {
        if (!abort.signal.aborted) setLocated(undefined);
      });
    return () => abort.abort();
  }, [selected, reveal, revision]);
  useEffect(() => {
    const events = new EventSource("/api/conversations/events");
    const changed = () => {
      setAfter("");
      setRevision((n) => n + 1);
    };
    events.addEventListener("changed", changed);
    events.addEventListener("ready", changed);
    events.addEventListener("closed", () => {
      setSources([]);
      setLocated(undefined);
      events.close();
    });
    return () => events.close();
  }, []);
  useEffect(() => {
    if (!expanded) return;
    const abort = new AbortController();
    void api<{ sources: SharedPerson[] }>(
      `/api/conversations/sources?after=${encode(after)}&limit=50`,
      { signal: abort.signal },
    )
      .then((data) => {
        if (abort.signal.aborted) return;
        setSources((old) =>
          after
            ? [
                ...new Map(
                  [...old, ...data.sources].map((s) => [s.id, s]),
                ).values(),
              ]
            : data.sources,
        );
        setMore(data.sources.length === 50);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
  }, [expanded, after, revision]);
  const visible =
    located &&
    located.session_id === selected &&
    !sources.some((s) => s.id === located.source.id)
      ? [located.source, ...sources]
      : sources;
  return (
    <section className="shared-navigation" aria-label={tr("Shared with me")}>
      <button
        className="sidebar-label shared-toggle"
        aria-expanded={expanded}
        onClick={() => setExpanded(!expanded)}
      >
        {tr("Shared with me")}
      </button>
      {expanded && (
        <nav className="project-list">
          {visible.map((person) => (
            <div className="project-group" key={person.id}>
              <button
                className="navigation-title project-toggle"
                aria-expanded={opened.includes(person.id)}
                onClick={() =>
                  setOpened((old) =>
                    old.includes(person.id)
                      ? old.filter((id) => id !== person.id)
                      : [...old, person.id],
                  )
                }
                title={`@${person.username}`}
              >
                {opened.includes(person.id) ? (
                  <ChevronDown size={14} />
                ) : (
                  <ChevronRight size={14} />
                )}
                <UserRound size={17} />
                <span>
                  {person.display_name} <small>@{person.username}</small>
                </span>
              </button>
              {opened.includes(person.id) && (
                <ReceivedList
                  key={`${person.id}:${revision}`}
                  source={person.id}
                  selected={selected}
                  located={
                    located?.source.id === person.id ? located : undefined
                  }
                  onSelect={onSelect}
                />
              )}
            </div>
          ))}
          {!loading && !error && !visible.length && (
            <p className="list-hint">
              {tr("No conversations have been shared with you.")}
            </p>
          )}
          {loading && (
            <p className="list-hint" role="status">
              {tr("Loading…")}
            </p>
          )}
          {error && (
            <p className="list-hint" role="alert">
              {error}
              <button onClick={() => setRevision((n) => n + 1)}>
                {tr("Retry")}
              </button>
            </p>
          )}
          {more && (
            <button
              className="list-hint"
              disabled={loading}
              onClick={() => setAfter(sources.at(-1)!.cursor!)}
            >
              {tr("Show more")}
            </button>
          )}
          <CopyTasks onOpen={onOpenCopy} />
        </nav>
      )}
    </section>
  );
}
function ReceivedList({
  source,
  selected,
  located,
  onSelect,
}: {
  source: string;
  selected: string;
  located?: ReceivedConversation;
  onSelect: (id: string) => void;
}) {
  const tr = useT();
  const [items, setItems] = useState<ReceivedConversation[]>([]);
  const [after, setAfter] = useState("");
  const [more, setMore] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const abort = new AbortController();
    void api<{ conversations: ReceivedConversation[] }>(
      `/api/conversations?source=${encode(source)}&after=${encode(after)}&limit=50`,
      { signal: abort.signal },
    )
      .then((data) => {
        if (abort.signal.aborted) return;
        setItems((old) =>
          after
            ? [
                ...new Map(
                  [...old, ...data.conversations].map((v) => [v.session_id, v]),
                ).values(),
              ]
            : data.conversations,
        );
        setMore(data.conversations.length === 50);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
  }, [source, after, attempt]);
  const visible =
    located && !items.some((i) => i.session_id === located.session_id)
      ? [located, ...items]
      : items;
  return (
    <div className="session-list">
      {visible.map((item) => (
        <div
          key={item.session_id}
          className={`navigation-row session-row ${selected === item.session_id ? "selected" : ""}`}
        >
          <button
            className="navigation-title"
            aria-current={selected === item.session_id ? "page" : undefined}
            onClick={() => onSelect(item.session_id)}
            title={item.title}
          >
            <span>{item.title}</span>
          </button>
        </div>
      ))}
      {loading && (
        <small className="list-hint" role="status">
          {tr("Loading…")}
        </small>
      )}
      {error && (
        <p role="alert" className="list-hint">
          {error}
          <button onClick={() => setAttempt((n) => n + 1)}>
            {tr("Retry")}
          </button>
        </p>
      )}
      {more && (
        <button
          className="list-hint"
          disabled={loading}
          onClick={() => setAfter(items.at(-1)!.cursor!)}
        >
          {tr("Show more")}
        </button>
      )}
    </div>
  );
}
