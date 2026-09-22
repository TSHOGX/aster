"use client";
import { ComposerInput } from "./composer-input";
import { useT } from "@/lib/i18n";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ArrowDown, ArrowUp, Square } from "lucide-react";
import {
  mergeConversationPage,
  oldestMessageSequence,
} from "@/lib/conversation-cache";
import { TurnNavigator } from "./turn-navigator";
import { TurnView } from "./turn-view";
import { conversationTurns } from "@/lib/conversation-turns";
import { Menu, useDialog } from "./ui";
import { knownWorkspaceFiles, workspacePath } from "@/lib/workspace-links";
import {
  api,
  APIError,
  command,
  newID,
  recalled,
  remember,
  sessionURL,
  type AgentSession,
  type SessionSnapshot,
  type TimelineItem,
  type TurnIndexItem,
  type Turn,
} from "@/lib/project-api";
import { emptySessionView, type SessionView } from "@/lib/project-projections";
import { useResources } from "./resource-provider";
import { startTiming } from "@/lib/interaction-metrics";
export function SessionPanel({
  actor,
  session,
  tick,
  onChanged,
  onFile,
  targetMessage,
  showHistory = false,
}: {
  actor: string;
  session: AgentSession;
  tick: number;
  onChanged: () => void;
  onFile: (path: string) => void;
  targetMessage: string;
  showHistory?: boolean;
}) {
  const tr = useT();
  const ask = useDialog();
  const resources = useResources();
  const windowKey = useMemo(() => ({project: session.project_id, kind: "conversation" as const, identity: `window:${session.id}`}), [session.project_id, session.id]);
  const cachedWindow = resources.peek<SessionSnapshot>(windowKey);
  const readAbort = useRef(new AbortController());
  const key = `project-view:${actor}:${session.project_id}:draft:${session.id}`;
  const [draft, setDraft] = useState(() =>
    (() => {
      const saved = recalled<unknown>(key, "");
      return typeof saved === "string" ? saved.slice(0, 64000) : "";
    })(),
  );
  const [view, setView] = useState<SessionView>(() => ({...emptySessionView(), snapshot: cachedWindow || null}));
  const [readState, setReadState] = useState<"loading" | "ready" | "pending" | "error" | "denied">(cachedWindow ? "ready" : "loading");
  const [readError, setReadError] = useState("");
  const positionKey = key + ":position";
  const scroll = useRef<HTMLDivElement>(null);
  const following = useRef(
    recalled<{ follow: boolean }>(positionKey, { follow: true }).follow,
  );
  const restored = useRef(false);
  const [atBottom, setAtBottom] = useState(true);
  const sendLock = useRef(false);
  const [pendingSend, setPendingSend] = useState(() =>
    recalled<{
      id: string;
      content: string;
      held: boolean;
      startedAt?: string;
    } | null>(key + ":send", null),
  );
  const hasPending = !!pendingSend;
  const [sentTurn, setSentTurn] = useState<Turn | null>(null);
  const [focusMessage, setFocusMessage] = useState(targetMessage);
  const [focusTurn, setFocusTurn] = useState(() =>
    targetMessage
      ? ""
      : recalled<{ turn?: string }>(positionKey, {}).turn || "",
  );
  const snapshot = useRef<SessionSnapshot | null>(cachedWindow || null);
  const navigationSerial = useRef(0);
  const navigationPending = useRef(false);
  const alive = useRef(true);
  const windowEpoch = useRef(0);
  const programmaticUntil = useRef(0);
  const historyFlight = useRef<Promise<boolean> | null>(null);
  const scrollAnchor = useRef<{ id: string; top: number } | null>(null);
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [loadingNewer, setLoadingNewer] = useState(false);
  const [navigationTarget, setNavigationTarget] = useState<{
    kind: "turn" | "message";
    id: string;
  } | null>(() =>
    targetMessage
      ? { kind: "message", id: targetMessage }
      : focusTurn
        ? { kind: "turn", id: focusTurn }
        : null,
  );
  const locate = (kind: "turn" | "message", id: string): HTMLElement | null => {
    const el = scroll.current;
    if (!el) return null;
    return kind === "turn"
      ? el.querySelector<HTMLElement>(
          `[data-turn-id="${CSS.escape(id)}"] .chat-message.user`,
        )
      : el.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(id)}"]`);
  };
  const scrollToTarget = (target: HTMLElement, smooth = false) => {
    const el = scroll.current;
    if (!el) return;
    following.current = false;
    restored.current = true;
    programmaticUntil.current = performance.now() + 450;
    el.scrollTo({
      top:
        el.scrollTop +
        target.getBoundingClientRect().top -
        el.getBoundingClientRect().top -
        16,
      behavior:
        smooth && !window.matchMedia("(prefers-reduced-motion: reduce)").matches
          ? "smooth"
          : "instant",
    });
  };
  const jumpTurn = (id: string) => {
    navigationSerial.current++;
    navigationPending.current = false;
    following.current = false;
    setAtBottom(false);
    setFocusMessage("");
    setFocusTurn(id);
    const target = locate("turn", id);
    if (target) {
      setNavigationTarget(null);
      scrollToTarget(target, true);
    } else setNavigationTarget({ kind: "turn", id });
  };
  useEffect(() => {
    if (!targetMessage) return;
    const timer = setTimeout(() => {
      navigationSerial.current++;
      setNavigationTarget({ kind: "message", id: targetMessage });
      setFocusMessage(targetMessage);
      setFocusTurn("");
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
  const snapshotFlight = useRef<{ key: string; serial: number } | null>(null);
  const invalidate = useCallback(() => {
    request.current++;
  }, []);
  const base = sessionURL(session.project_id, session.id);
  const readPage = useCallback((url: string) => resources.read<SessionSnapshot>(
    {project: session.project_id, kind: "conversation", identity: url},
    signal => api<SessionSnapshot>(url, {signal}), readAbort.current.signal,
  ), [resources, session.project_id]);
  useEffect(() => resources.pin(windowKey), [resources, windowKey]);
  const applyPage = useCallback(
    (next: SessionSnapshot, older = false, replace = false) => {
      const done = startTiming("conversation", "commit");
      const merged = replace
        ? next
        : mergeConversationPage(snapshot.current, next, older);
      snapshot.current = merged;
      resources.put(windowKey, merged);
      setView((current) => ({ ...current, snapshot: merged }));
      setReadState("ready");
      setReadError("");
      done();
    },
    [resources, windowKey],
  );
  const refresh = useCallback(async () => {
    if (navigationPending.current) return;
    if (
      snapshotFlight.current?.key === base &&
      snapshotFlight.current.serial === request.current
    ) {
      return;
    }
    const serial = ++request.current;
    snapshotFlight.current = { key: base, serial };
    try {
      const next = await readPage(
        `${base}/snapshot?before=0&records=0`,
      );
      if (serial === request.current && alive.current) {
        const history = snapshot.current;
        // Keep live controls fresh without splicing a distant tail into a
        // historical reading window. Only adjacent pages extend its content.
        applyPage(
          history?.has_later
            ? {
                ...next,
                messages: history.messages,
                timeline: history.timeline,
                has_earlier: history.has_earlier,
                has_later: true,
              }
            : next,
        );
      }
    } catch (e) {
      if (serial === request.current && alive.current) {
        if (e instanceof APIError && [401, 403, 404].includes(e.status)) {
          resources.clear(session.project_id);
          snapshot.current = null;
          setView(emptySessionView());
        }
        setReadState(e instanceof APIError && e.status === 202 ? "pending" : e instanceof APIError && [401, 403, 404].includes(e.status) ? "denied" : "error");
        setReadError(
          e instanceof Error
            ? e.message
            : "Could not refresh this conversation.",
        );
      }
    } finally {
      if (snapshotFlight.current?.serial === serial)
        snapshotFlight.current = null;
    }
  }, [base, applyPage, readPage, resources, session.project_id]);
  const loadAdjacent = useCallback(
    async (direction: "older" | "newer"): Promise<boolean> => {
      if (historyFlight.current) return historyFlight.current;
      const current = snapshot.current,
        older = direction === "older",
        epoch = windowEpoch.current;
      if (!current || !(older ? current.has_earlier : current.has_later))
        return false;
      const sequence = older
        ? oldestMessageSequence(current.messages)
        : Math.max(...current.messages.map((m) => m.sequence), 0);
      if (!Number.isFinite(sequence)) return false;
      if (older) setLoadingHistory(true);
      else setLoadingNewer(true);
      following.current = false;
      const flight = (async () => {
        try {
          const next = await readPage(
            `${base}/snapshot?${older ? "before" : "after"}=${sequence}&records=0`,
          );
          if (!alive.current || windowEpoch.current !== epoch) return false;
          const advanced = older
            ? oldestMessageSequence(next.messages) < sequence
            : next.messages.some((m) => m.sequence > sequence);
          if (!advanced) {
            applyPage(
              {
                ...snapshot.current!,
                [older ? "has_earlier" : "has_later"]: false,
              },
              false,
              true,
            );
            return false;
          }
          const el = scroll.current;
          if (older && el) {
            const top = el.getBoundingClientRect().top;
            const anchor = [
              ...el.querySelectorAll<HTMLElement>("[data-timeline-id]"),
            ].find(
              (node) =>
                node.getBoundingClientRect().height > 0 &&
                node.getBoundingClientRect().bottom > top,
            );
            if (anchor)
              scrollAnchor.current = {
                id: anchor.dataset.timelineId!,
                top: anchor.getBoundingClientRect().top,
              };
          }
          applyPage(next, older);
          return true;
        } catch (e) {
          if (alive.current && windowEpoch.current === epoch)
            setError(String(e));
          return false;
        } finally {
          historyFlight.current = null;
          if (alive.current) {
            setLoadingHistory(false);
            setLoadingNewer(false);
          }
        }
      })();
      historyFlight.current = flight;
      return flight;
    },
    [base, applyPage, readPage],
  );
  const requestEdge = (direction: "older" | "newer") => {
    const el = scroll.current;
    if (!el || navigationPending.current) return;
    if (
      direction === "older" &&
      el.scrollTop < 100 &&
      snapshot.current?.has_earlier
    )
      void loadAdjacent("older");
    if (
      direction === "newer" &&
      el.scrollHeight - el.scrollTop - el.clientHeight < 100 &&
      snapshot.current?.has_later
    )
      void loadAdjacent("newer");
  };
  const showLatest = async () => {
    const serial = ++navigationSerial.current;
    windowEpoch.current++;
    request.current++;
    navigationPending.current = true;
    setNavigationTarget(null);
    setFocusTurn("");
    setFocusMessage("");

    try {
      const latest = await readPage(
        `${base}/snapshot?before=0&records=0`,
      );
      if (!alive.current || serial !== navigationSerial.current) return;
      applyPage(latest, false, !!snapshot.current?.has_later);
      following.current = true;
      restored.current = true;
      requestAnimationFrame(() => {
        if (serial !== navigationSerial.current) return;
        navigationPending.current = false;
        scroll.current?.scrollTo({
          top: scroll.current.scrollHeight,
          behavior: "instant",
        });
      });
    } catch (e) {
      if (!alive.current || serial !== navigationSerial.current) return;
      setError(String(e));
      navigationPending.current = false;
    }
  };
  useEffect(() => {
    alive.current = true;
    readAbort.current = new AbortController();
    return () => {
      readAbort.current.abort();
      alive.current = false;
      invalidate();
    };
  }, [refresh, invalidate]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
  }, [tick, refresh]);
  useLayoutEffect(() => {
    const saved = scrollAnchor.current,
      el = scroll.current;
    if (saved && el) {
      const anchor = el.querySelector<HTMLElement>(
        `[data-timeline-id="${CSS.escape(saved.id)}"]`,
      );
      if (anchor) {
        programmaticUntil.current = performance.now() + 150;
        el.scrollTop += anchor.getBoundingClientRect().top - saved.top;
      }
      scrollAnchor.current = null;
    }
  }, [view.snapshot]);
  // A missing destination loads a surrounding message window, not a turn filter.
  // Keep this window contiguous by extending its two edges as the reader scrolls.
  useEffect(() => {
    if (!navigationTarget) return;
    let gone = false;
    const serial = ++navigationSerial.current;
    windowEpoch.current++;
    request.current++;
    navigationPending.current = true;
    following.current = false;
    scrollAnchor.current = null;
    void (async () => {
      try {
        if (!locate(navigationTarget.kind, navigationTarget.id)) {
          const next = await readPage(
            `${base}/snapshot?${navigationTarget.kind}=${encodeURIComponent(navigationTarget.id)}&records=0`,
          );
          if (gone || !alive.current || serial !== navigationSerial.current)
            return;
          applyPage(next, false, true);
        }
        requestAnimationFrame(() => {
          if (gone || serial !== navigationSerial.current) return;
          const target = locate(navigationTarget.kind, navigationTarget.id);
          if (target) scrollToTarget(target);
          else setError(tr("This conversation is unavailable."));
          navigationPending.current = false;
          setNavigationTarget(null);
        });
      } catch (e) {
        if (!gone && alive.current && serial === navigationSerial.current) {
          navigationPending.current = false;
          setError(String(e));
          setNavigationTarget(null);
        }
      }
    })();
    return () => {
      gone = true;
    };
  }, [navigationTarget, base, applyPage, readPage, tr]);
  useEffect(() => {
    const saved = recalled<{ scroll?: number; follow?: boolean }>(
      positionKey,
      {},
    );
    remember(positionKey, {
      scroll: saved.scroll || 0,
      follow: saved.follow ?? false,
      turn: focusTurn,
    });
  }, [positionKey, focusTurn]);
  useEffect(() => {
    remember(key, draft);
  }, [draft, key]);
  useEffect(() => {
    if (sentTurn && view.snapshot?.turns.some((t) => t.id === sentTurn.id))
      void Promise.resolve().then(() => setSentTurn(null));
  }, [sentTurn, view.snapshot]);
  const current = view.snapshot?.session || session;
  let timeline: TimelineItem[] =
    view.snapshot?.timeline ??
    view.snapshot?.messages.map((m) => ({
      id: m.id,
      cursor: m.sequence,
      turn_id: m.turn_id || "",
      kind: m.role,
      content: m.content,
      message_id: m.id,
    })) ??
    [];
  const activeTurns = new Set(
    view.snapshot?.turns
      .filter((t) => ["starting", "running", "stopping"].includes(t.state))
      .map((t) => t.id),
  );
  const displayTurns = [...(view.snapshot?.turns || [])];
  if (sentTurn && !displayTurns.some((t) => t.id === sentTurn.id))
    displayTurns.push(sentTurn);
  if (
    pendingSend &&
    !pendingSend.held &&
    !displayTurns.some((t) => t.request_id === pendingSend.id)
  )
    displayTurns.push({
      id: `pending:${pendingSend.id}`,
      session_id: session.id,
      content: pendingSend.content,
      state: error ? "unknown" : "sending",
      sequence: Number.MAX_SAFE_INTEGER,
      created_at: pendingSend.startedAt,
    });
  if (!view.snapshot?.has_later && !focusMessage && !focusTurn) {
    const missing = displayTurns.filter(
      (t) =>
        !timeline.some((i) => i.turn_id === t.id) &&
        (["sending", "queued", "starting", "running", "stopping"].includes(
          t.state,
        ) ||
          t.id.startsWith("pending:")),
    );
    timeline = [
      ...timeline,
      ...missing.map((t) => ({
        id: `${t.id}/user`,
        cursor: Number.MAX_SAFE_INTEGER,
        turn_id: t.id,
        kind: "user",
        content: t.content,
      })),
    ];
  }
  const turns = conversationTurns(
    timeline,
    view.snapshot?.turn_summaries || [],
    displayTurns,
  );
  const navigationTurns: TurnIndexItem[] = turns
    .filter((t) => t.turnId && !t.turnId.startsWith("pending:"))
    .map((t, i) => ({
      id: t.turnId,
      sequence:
        t.summary?.sequence ||
        displayTurns.find((x) => x.id === t.turnId)?.sequence ||
        i + 1,
      state: t.summary?.state || "unknown",
      created_at: t.summary?.created_at || "",
      user_preview: (
        t.items.find((x) => x.kind === "user")?.content ||
        displayTurns.find((x) => x.id === t.turnId)?.content ||
        ""
      ).slice(0, 600),
      assistant_preview:
        t.summary?.state === "completed"
          ? t.items
              .filter(
                (x) =>
                  x.kind === "assistant" &&
                  x.message_id === t.summary?.final_message_id,
              )
              .map((x) => x.content || "")
              .join("\n")
              .slice(0, 1200)
          : "",
    }));
  const workspaceFiles = knownWorkspaceFiles(
    timeline.flatMap((item) => [
      item.content || "",
      "arguments" in item ? item.arguments || "" : "",
      "result" in item ? item.result || "" : "",
    ]),
  );
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
    if (sendLock.current || busy || (!draft.trim() && !pendingSend)) return;
    sendLock.current = true;
    const intent = pendingSend || {
      id: newID(),
      content: draft,
      held,
      startedAt: new Date().toISOString(),
    };
    setPendingSend(intent);
    remember(key + ":send", intent);
    void act(async () => {
      const accepted = await command<Turn>(`${base}/turns`, {
        request_id: intent.id,
        content: intent.content,
        held: intent.held,
      });
      if (!intent.held)
        setSentTurn({
          ...accepted,
          content: intent.content,
          request_id: intent.id,
          created_at: accepted.created_at || intent.startedAt,
        });
      setPendingSend(null);
      remember(key + ":send", null);
      await showLatest();
      setDraft("");

      setFocusTurn("");
      setFocusMessage("");
      setNavigationTarget(null);
      navigationSerial.current++;
      navigationPending.current = false;
      following.current = true;
    }).finally(() => {
      sendLock.current = false;
    });
  };
  useEffect(() => {
    if (showHistory) void Promise.resolve().then(() => loadTurns());
  }, [showHistory]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const el = scroll.current;
    if (!el || !view.snapshot) return;
    if (!restored.current && !navigationTarget) {
      const saved = recalled<{ scroll: number }>(positionKey, { scroll: 0 });
      el.scrollTop = following.current ? el.scrollHeight : saved.scroll;
      restored.current = true;
    } else if (following.current && !navigationTarget)
      el.scrollTop = el.scrollHeight;
  }, [
    view.snapshot,
    view.partial,
    focusMessage,
    focusTurn,
    positionKey,
    navigationTarget,
  ]);
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
      {error || readError || current.failure ? (
        <p className="notice error" role="alert">
          {error || readError || current.failure}
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
              void (async () => {
                const accepted = await ask({
                  title: tr("Continue from history"),
                  description: tr(
                    "Continue from the retained native history? Prior unknown effects remain unknown and queued prompts will stay held.",
                  ),
                  choices: [
                    { label: tr("Continue from history"), value: "continue" },
                  ],
                });
                if (accepted)
                  void act(() =>
                    command(`${base}/control`, {
                      action: "continue",
                      revision: current.revision,
                    }),
                  );
              })();
            }}
          >
            {tr("Continue from history")}
          </button>
        </section>
      )}
      <div className="conversation-reading-area">
        <div
          className="conversation-scroll"
          ref={scroll}
          onWheel={(e) => {
            programmaticUntil.current = 0;
            if (
              !(e.target as HTMLElement).closest(
                "pre,.table-scroll,textarea",
              ) &&
              e.deltaY !== 0
            )
              requestEdge(e.deltaY < 0 ? "older" : "newer");
          }}
          onKeyDown={(e) => {
            if ((e.target as HTMLElement).closest("pre,.table-scroll,textarea"))
              return;
            if (e.key === "PageUp" || e.key === "ArrowUp") requestEdge("older");
            if (e.key === "PageDown" || e.key === "ArrowDown")
              requestEdge("newer");
          }}
          onTouchMove={() => {
            programmaticUntil.current = 0;
          }}
          onScroll={() => {
            const el = scroll.current!;
            following.current =
              !navigationPending.current &&
              !snapshot.current?.has_later &&
              el.scrollHeight - el.scrollTop - el.clientHeight < 64;
            setAtBottom(following.current);
            if (
              !navigationPending.current &&
              performance.now() > programmaticUntil.current
            ) {
              if (el.scrollTop < 100 && snapshot.current?.has_earlier)
                void loadAdjacent("older");
              else if (
                el.scrollHeight - el.scrollTop - el.clientHeight < 100 &&
                snapshot.current?.has_later
              )
                void loadAdjacent("newer");
            }
            remember(positionKey, {
              turn: focusTurn,
              scroll: el.scrollTop,
              follow: following.current,
            });
          }}
          onClick={(event) => {
            const link = (event.target as HTMLElement).closest("a");
            const href = link?.getAttribute("href");
            if (href) {
              let path: string | null = null;
              try {
                path = workspacePath(decodeURIComponent(href), workspaceFiles);
              } catch {
                /* Malformed links are not file targets. */
              }
              if (path) {
                event.preventDefault();
                onFile(path);
              }
            }
          }}
        >
          {(navigationTarget || loadingHistory) && (
            <p className="history-loading" role="status">
              {tr("Loading earlier conversation…")}
            </p>
          )}
          {!view.snapshot && readState === "loading" && (
            <p className="history-loading" role="status">{tr("Loading…")}</p>
          )}
          {!timeline.length && view.snapshot && readState === "ready" && !view.snapshot.has_earlier && !view.snapshot.has_later && (
            <div className="empty-state">
              <h3>{tr("Start a conversation")}</h3>
              <p>
                {tr(
                  "This agent shares the Project workspace with your other agents and terminals.",
                )}
              </p>
            </div>
          )}
          {turns.map((turn) => (
            <TurnView
              key={turn.id}
              turn={turn}
              files={workspaceFiles}
              activeTurns={activeTurns}
              messages={view.snapshot?.messages || []}
              actor={actor}
              project={session.project_id}
              session={session.id}
              targetMessage={focusMessage}
            />
          ))}
          {loadingNewer && (
            <p className="history-loading" role="status">
              {tr("Loading…")}
            </p>
          )}
          {view.snapshot?.timeline_truncated && (
            <small className="history-limit">
              {tr("Only retained tool activity is shown.")}
            </small>
          )}
        </div>
        <TurnNavigator
          base={base}
          revision={current.revision}
          local={navigationTurns}
          scroll={scroll}
          onJump={jumpTurn}
        />
      </div>
      {(!atBottom || view.snapshot?.has_later) && (
        <button
          className="jump-latest"
          aria-label={tr("Return to latest")}
          title={tr("Return to latest")}
          onClick={() => void showLatest()}
        >
          <ArrowDown size={18} />
        </button>
      )}
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
        <details className="prompt-queue">
          <summary>
            {tr("Queued prompts")} · {queue.length}
          </summary>
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
        </details>
      )}
      <form
        className="agent-composer"
        onSubmit={(event) => {
          event.preventDefault();
          send(false);
        }}
      >
        <ComposerInput
          aria-label={`Message ${current.title}`}
          value={pendingSend?.content || draft}
          disabled={hasPending}
          maxLength={64000}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={tr("Ask this agent to work in your Project\u2026")}
          onSend={() => {
            if (!busy && current.state !== "blocked") send(false);
          }}
        />
        <div className="composer-actions">
          <Menu
            label={tr("Conversation actions")}
            items={[
              { label: tr("Turn history"), action: () => void loadTurns() },
              {
                label: tr("Hold prompt"),
                disabled: busy || !draft.trim() || hasPending,
                action: () => send(true),
              },
            ]}
          />
          <div className="row-actions">
            {active && (
              <button
                type="button"
                className="stop-button"
                aria-label={tr("Stop turn")}
                title={tr("Stop turn")}
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
                <Square size={14} fill="currentColor" />
              </button>
            )}
            <button
              className="send-button"
              aria-label={hasPending ? tr("Retry") : tr("Send")}
              title={hasPending ? tr("Retry") : tr("Send")}
              disabled={
                busy ||
                (!draft.trim() && !hasPending) ||
                current.state === "blocked"
              }
            >
              <ArrowUp size={18} />
            </button>
          </div>
        </div>
      </form>
    </section>
  );
}
