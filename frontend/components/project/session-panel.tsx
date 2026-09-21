"use client";
import { useT } from "@/lib/i18n";
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
  const tr = useT();
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
  const [turnHistory, setTurnHistory] = useState<Turn[] | null>(null);
  const loadTurns = async (after = 0) => {
    try {
      setTurnHistory(
        (
          await api<{
            turns: Turn[];
          }>(`${base}/turns?after=${after}&limit=50`)
        ).turns,
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : "Turn history unavailable.");
    }
  };
  const request = useRef(0);
  const invalidate = useCallback(() => {
    request.current++;
  }, []);
  const base = sessionURL(session.project_id, session.id);
  const refresh = useCallback(async () => {
    const serial = ++request.current;
    try {
      const next = await api<SessionSnapshot>(
        `${base}/snapshot?before=${before}&records=${state.current.snapshot ? "0" : "1"}${focusMessage ? `&message=${encodeURIComponent(focusMessage)}` : ""}`,
      );
      if (serial !== request.current) return;
      setView((current) => acceptSnapshot(current, next));
      const records = await api<{
        records: AgentRecord[];
      }>(
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
        control_revision: current.control_revision,
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
            title={tr("Rename this Agent Session")}
            onClick={() => {
              const title = window.prompt(tr("Session title"), current.title);
              if (title?.trim())
                void act(() =>
                  command(base, { title, revision: current.revision }, "PATCH"),
                );
            }}
          >
            {tr("Rename")}
          </button>
          <button onClick={onClose}>{tr("Close view")}</button>
          <button
            className="danger"
            onClick={() => {
              if (
                window.confirm(
                  tr(
                    "Delete this Agent Session and its conversation? Project files and other Sessions remain.",
                  ),
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
            {tr("Delete")}
          </button>
        </div>
      </header>
      {error || current.failure ? (
        <p className="notice error" role="alert">
          {error || current.failure}
        </p>
      ) : null}
      {current.state === "blocked" && (
        <section className="notice warning">
          <p>
            {tr(
              "This Session needs recovery. Unknown prior effects will not be repeated.",
            )}
          </p>
          {view.snapshot?.recovery
            ?.filter((r) => r.recovery !== "none")
            .map((r) => (
              <p key={r.id}>
                {r.recovery === "complete"
                  ? tr("Journal reconciled")
                  : r.recovery === "failed"
                    ? tr("Journal recovery unavailable")
                    : tr("Reconciling original journal")}{" "}
                ·{" "}
                {r.cleanup === "settled"
                  ? tr("Child processes settled")
                  : tr("Descendant cleanup is unknown")}
                {r.error ? ` · ${r.error}` : ""}
              </p>
            ))}
          {view.snapshot?.recovery?.some((r) => r.recovery === "failed") && (
            <button
              disabled={busy}
              onClick={() =>
                void act(() =>
                  command(`${base}/control`, {
                    action: "retry_recovery",
                    revision: current.revision,
                  }),
                )
              }
            >
              {tr("Retry journal recovery")}
            </button>
          )}
          <button
            disabled={busy}
            onClick={() => {
              if (
                window.confirm(
                  tr(
                    "Continue from the retained native history? Prior unknown effects remain unknown and queued prompts will stay held.",
                  ),
                )
              )
                void act(() =>
                  command(`${base}/control`, {
                    action: "continue",
                    revision: current.revision,
                  }),
                );
            }}
          >
            {tr("Continue from history")}
          </button>
        </section>
      )}
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
            {tr("Earlier messages")}
          </button>
        )}
        {(before > 0 || focusMessage) && (
          <button
            onClick={() => {
              setFocusMessage("");
              setBefore(0);
            }}
          >
            {tr("Return to latest")}
          </button>
        )}
        {!view.snapshot?.messages.length && (
          <div className="empty-state">
            <h3>{tr("Start a conversation")}</h3>
            <p>
              {tr(
                "This agent shares the Project workspace with your other agents and terminals.",
              )}
            </p>
          </div>
        )}
        {view.snapshot?.messages.map((message) => (
          <article
            className={`chat-message ${message.role}`}
            key={message.id}
            data-message-id={message.id}
          >
            <small>{message.role === "user" ? tr("You") : tr("Agent")}</small>
            <MarkdownView content={message.content.slice(0, 64000)} />
            {message.content.length > 64000 && (
              <button
                onClick={() => {
                  const url = URL.createObjectURL(
                    new Blob([message.content], {
                      type: "text/plain;charset=utf-8",
                    }),
                  );
                  const link = document.createElement("a");
                  link.href = url;
                  link.download = `message-${message.sequence}.txt`;
                  link.click();
                  setTimeout(() => URL.revokeObjectURL(url), 1000);
                }}
              >
                {tr("Download full message")}
              </button>
            )}
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
            <small>{tr("Agent \u00B7 latest output")}</small>
            <MarkdownView content={view.partial} highlight={false} />
          </article>
        )}
        {view.tools.length > 0 && (
          <details className="tool-trace">
            <summary>
              {tr("Recent activity (")}
              {view.tools.length})
            </summary>
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
            {tr("Agent is working\u2026")}
          </p>
        )}
      </div>
      {view.snapshot?.turns
        .filter((turn) => turn.state === "unknown")
        .map((turn) => (
          <p className="notice warning" key={turn.id}>
            {tr("Unknown outcome:")}
            {turn.content.slice(0, 180)}
            {tr(
              ". Inspect any files or external effects before repeating this work.",
            )}
          </p>
        ))}
      {turnHistory && (
        <section className="prompt-queue" aria-label={tr("Turn history")}>
          <div className="row-actions">
            <button onClick={() => setTurnHistory(null)}>
              {tr("Close history")}
            </button>
            <button onClick={() => void loadTurns()}>
              {tr("First turns")}
            </button>
            {turnHistory.length === 50 && (
              <button
                onClick={() =>
                  void loadTurns(turnHistory.at(-1)?.sequence || 0)
                }
              >
                {tr("Next turns")}
              </button>
            )}
          </div>
          {turnHistory.map((turn) => (
            <details key={turn.id}>
              <summary>
                #{turn.sequence} · {turn.state} · {turn.content.slice(0, 80)}
              </summary>
              <pre>{turn.content.slice(0, 8000)}</pre>
              {turn.state === "held" && (
                <button
                  disabled={busy}
                  onClick={() => control(turn, "release")}
                >
                  {tr("Release prompt")}
                </button>
              )}
              {["held", "queued"].includes(turn.state) && (
                <button
                  disabled={busy}
                  onClick={() => control(turn, "canceled")}
                >
                  {tr("Cancel prompt")}
                </button>
              )}
            </details>
          ))}
        </section>
      )}
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
                      {tr("Release")}
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
                              control_revision: current.control_revision,
                            }),
                          )
                        }
                      >
                        {tr("Steer")}
                      </button>
                    )}
                  </>
                ) : (
                  <button disabled={busy} onClick={() => control(turn, "held")}>
                    {tr("Hold")}
                  </button>
                )}
                <button
                  disabled={busy}
                  onClick={() => control(turn, "canceled")}
                >
                  {tr("Cancel prompt")}
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
          placeholder={tr("Ask this agent to work in your Project\u2026")}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              send(false);
            }
          }}
        />
        <div className="composer-actions">
          <div className="row-actions">
            <small>{tr("Shared /workspace \u00B7 Ctrl \u21B5 to send")}</small>
            <button type="button" onClick={() => void loadTurns()}>
              {tr("Turn history")}
            </button>
          </div>
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
                      control_revision: current.control_revision,
                    }),
                  )
                }
              >
                {tr("Stop turn")}
              </button>
            )}
            <button
              type="button"
              disabled={busy || !draft.trim()}
              onClick={() => send(true)}
            >
              {tr("Hold prompt")}
            </button>
            <button
              className="primary"
              disabled={busy || !draft.trim() || current.state === "blocked"}
            >
              {tr("Send")}
            </button>
          </div>
        </div>
      </form>
    </section>
  );
}
