"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { MarkdownView } from "@/components/workbench/markdown-view";
import {
  api,
  command,
  newID,
  recalled,
  remember,
  sessionURL,
  type AgentRecord,
  type AgentSession,
  type SessionSnapshot,
  type Turn,
} from "@/lib/project-api";
import {
  acceptRecords,
  acceptSnapshot,
  emptySessionView,
  type SessionView,
} from "@/lib/project-projections";

export function SessionPanel({
  actor,
  session,
  tick,
  onClose,
  onChanged,
  onFile,
  targetMessage,
}: {
  actor: string;
  session: AgentSession;
  tick: number;
  onClose: () => void;
  onChanged: () => void;
  onFile: (path: string) => void;
  targetMessage: string;
}) {
  const key = `project-view:${actor}:${session.project_id}:draft:${session.id}`;
  const [draft, setDraft] = useState(() =>
    (() => {
      const saved = recalled<unknown>(key, "");
      return typeof saved === "string" ? saved.slice(0, 64000) : "";
    })(),
  );
  const [view, setView] = useState<SessionView>(emptySessionView);
  const state = useRef(view);
  useEffect(() => {
    state.current = view;
  }, [view]);
  const [before, setBefore] = useState(0);
  const [focusMessage, setFocusMessage] = useState(targetMessage);
  useEffect(() => {
    if (!targetMessage) return;
    const timer = setTimeout(() => {
      setFocusMessage(targetMessage);
      setBefore(0);
    }, 0);
    return () => clearTimeout(timer);
  }, [targetMessage]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef(0);
  const invalidate = useCallback(() => {
    request.current++;
  }, []);
  const base = sessionURL(session.project_id, session.id);
  const refresh = useCallback(async () => {
    const serial = ++request.current;
    try {
      const next = await api<SessionSnapshot>(
        `${base}/snapshot?before=${before}${focusMessage ? `&message=${encodeURIComponent(focusMessage)}` : ""}`,
      );
      if (serial !== request.current) return;
      setView((current) => acceptSnapshot(current, next));
      const records = await api<{ records: AgentRecord[] }>(
        `${base}/records?after=${state.current.snapshot ? state.current.cursor : next.record_cursor}`,
      );
      if (serial === request.current)
        setView((current) => acceptRecords(current, records.records));
    } catch (error) {
      if (serial === request.current)
        setError(
          error instanceof Error
            ? error.message
            : "Could not refresh this conversation.",
        );
    }
  }, [base, before, focusMessage]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
    return invalidate;
  }, [refresh, tick, invalidate]);
  useEffect(() => {
    remember(key, draft);
  }, [draft, key]);
  useEffect(() => {
    if (!focusMessage) return;
    const timer = setTimeout(
      () =>
        document
          .querySelector(`[data-message-id="${CSS.escape(focusMessage)}"]`)
          ?.scrollIntoView({ block: "center" }),
      100,
    );
    return () => clearTimeout(timer);
  }, [focusMessage, view.snapshot]);
  const current = view.snapshot?.session || session;
  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      await refresh();
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : "The action failed.");
    } finally {
      setBusy(false);
    }
  };
  const send = (held: boolean) => {
    if (!draft.trim()) return;
    const text = draft;
    void act(async () => {
      await command(`${base}/turns`, {
        request_id: newID(),
        content: text,
        held,
      });
      setDraft("");
      setBefore(0);
    });
  };
  const control = (turn: Turn, action: string) =>
    void act(() =>
      command(`${base}/turns/${encodeURIComponent(turn.id)}/control`, {
        action,
        revision: current.revision,
      }),
    );
  const active = view.snapshot?.turns.some((turn) =>
    ["starting", "running", "stopping"].includes(turn.state),
  );
  const queue =
    view.snapshot?.turns.filter((turn) =>
      ["queued", "held"].includes(turn.state),
    ) || [];
  return (
    <section
      className="agent-panel"
      aria-label={`Agent ${current.title}`}
      data-session-id={session.id}
    >
      <header className="panel-heading">
        <div>
          <strong>{current.title}</strong>
          <span className={`state-dot ${current.state}`} />{" "}
          <small>{current.state}</small>
        </div>
        <div className="row-actions">
          <button
            title="Rename this Agent Session"
            onClick={() => {
              const title = window.prompt("Session title", current.title);
              if (title?.trim())
                void act(() =>
                  command(base, { title, revision: current.revision }, "PATCH"),
                );
            }}
          >
            Rename
          </button>
          <button onClick={onClose}>Close view</button>
          <button
            className="danger"
            onClick={() => {
              if (
                window.confirm(
                  "Delete this Agent Session and its conversation? Project files and other Sessions remain.",
                )
              )
                void act(() =>
                  command(
                    `${base}?revision=${current.revision}`,
                    undefined,
                    "DELETE",
                  ),
                );
            }}
          >
            Delete
          </button>
        </div>
      </header>
      {error || current.failure ? (
        <p className="notice error" role="alert">
          {error || current.failure}
        </p>
      ) : null}
      <div
        className="conversation-scroll"
        onClick={(event) => {
          const link = (event.target as HTMLElement).closest("a");
          const href = link?.getAttribute("href");
          if (href?.startsWith("/workspace/")) {
            event.preventDefault();
            onFile(href.slice(11));
          }
        }}
      >
        {view.snapshot?.has_earlier && (
          <button
            onClick={() => {
              setFocusMessage("");
              setBefore(view.snapshot?.messages[0]?.sequence || 0);
            }}
          >
            Earlier messages
          </button>
        )}
        {(before > 0 || focusMessage) && (
          <button
            onClick={() => {
              setFocusMessage("");
              setBefore(0);
            }}
          >
            Return to latest
          </button>
        )}
        {!view.snapshot?.messages.length && (
          <div className="empty-state">
            <h3>Start a conversation</h3>
            <p>
              This agent shares the Project workspace with your other agents and
              terminals.
            </p>
          </div>
        )}
        {view.snapshot?.messages.map((message) => (
          <article
            className={`chat-message ${message.role}`}
            key={message.id}
            data-message-id={message.id}
          >
            <small>{message.role === "user" ? "You" : "Agent"}</small>
            <MarkdownView content={message.content} />
            {[
              ...new Set(
                message.content.match(/\/workspace\/[^\s`"'<>\])]+/g) || [],
              ),
            ]
              .slice(0, 12)
              .map((path) => (
                <button
                  className="file-mention"
                  key={path}
                  onClick={() => onFile(path.slice(11))}
                >
                  {path}
                </button>
              ))}
          </article>
        ))}
        {view.partial && (
          <article
            className="chat-message assistant streaming"
            aria-live="polite"
          >
            <small>Agent · writing</small>
            <MarkdownView content={view.partial} highlight={false} />
          </article>
        )}
        {view.tools.length > 0 && (
          <details className="tool-trace">
            <summary>Tool activity ({view.tools.length})</summary>
            {view.tools.map((tool) => (
              <details key={tool.id}>
                <summary>
                  {tool.name} · {tool.state}
                </summary>
                <pre>{tool.detail}</pre>
              </details>
            ))}
          </details>
        )}
        {active && !view.partial && (
          <p className="working-indicator" role="status">
            Agent is working…
          </p>
        )}
      </div>
      {queue.length > 0 && (
        <div className="prompt-queue">
          {queue.map((turn) => (
            <div key={turn.id}>
              <span>
                {turn.state}: {turn.content.slice(0, 120)}
              </span>
              <div className="row-actions">
                {turn.state === "held" ? (
                  <>
                    <button
                      disabled={busy}
                      onClick={() => control(turn, "release")}
                    >
                      Release
                    </button>
                    {active && (
                      <button
                        disabled={busy}
                        onClick={() =>
                          void act(() =>
                            command(`${base}/control`, {
                              action: "steer",
                              source: turn.id,
                              request_id: newID(),
                              revision: current.revision,
                            }),
                          )
                        }
                      >
                        Steer
                      </button>
                    )}
                  </>
                ) : (
                  <button disabled={busy} onClick={() => control(turn, "held")}>
                    Hold
                  </button>
                )}
                <button
                  disabled={busy}
                  onClick={() => control(turn, "canceled")}
                >
                  Cancel prompt
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
      <form
        className="agent-composer"
        onSubmit={(event) => {
          event.preventDefault();
          send(false);
        }}
      >
        <textarea
          aria-label={`Message ${current.title}`}
          value={draft}
          maxLength={64000}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Ask this agent to work in your Project…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              send(false);
            }
          }}
        />
        <div className="composer-actions">
          <small>Shared /workspace · Ctrl ↵ to send</small>
          <div className="row-actions">
            {active && (
              <button
                type="button"
                disabled={busy}
                onClick={() =>
                  void act(() =>
                    command(`${base}/control`, {
                      action: "interrupt",
                      request_id: newID(),
                      revision: current.revision,
                    }),
                  )
                }
              >
                Stop turn
              </button>
            )}
            <button
              type="button"
              disabled={busy || !draft.trim()}
              onClick={() => send(true)}
            >
              Hold prompt
            </button>
            <button
              className="primary"
              disabled={busy || !draft.trim() || current.state === "blocked"}
            >
              Send
            </button>
          </div>
        </div>
      </form>
    </section>
  );
}
