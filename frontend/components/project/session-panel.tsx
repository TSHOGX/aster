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
  useSyncExternalStore,
} from "react";
import { ArrowDown, ArrowUp, Square } from "lucide-react";
import { ConversationRepository } from "@/lib/conversation-repository";
import { TurnNavigator } from "./turn-navigator";
import { TurnView } from "./turn-view";
import { conversationTurns } from "@/lib/conversation-turns";
import { Menu, useDialog } from "./ui";
import { knownWorkspaceFiles, workspacePath } from "@/lib/workspace-links";
import {
  api,
  command,
  newID,
  recalled,
  remember,
  sessionURL,
  type AgentSession,
  type TimelineItem,
  type TurnIndexItem,
  type Turn,
} from "@/lib/project-api";
import { useResources, useRealtime } from "./resource-provider";
function useWorkspaceFiles(key: string) {
  return useMemo(() => (key ? key.split("\n") : []), [key]);
}
export function SessionPanel({
  actor,
  session,
  onFile,
  targetMessage,
  showHistory = false,
}: {
  actor: string;
  session: AgentSession;
  onFile: (path: string) => void;
  targetMessage: string;
  showHistory?: boolean;
}) {
  const tr = useT();
  const ask = useDialog();
  const resources = useResources();
  const realtime = useRealtime();
  const repository = useMemo(
    () => new ConversationRepository(resources, session.project_id, session.id),
    [resources, session.project_id, session.id],
  );
  const view = useSyncExternalStore(
    repository.subscribe,
    repository.getSnapshot,
    repository.getSnapshot,
  );
  const key = `project-view:${actor}:${session.project_id}:draft:${session.id}`;
  const [draft, setDraft] = useState(() =>
    (() => {
      const saved = recalled<unknown>(key, "");
      return typeof saved === "string" ? saved.slice(0, 64000) : "";
    })(),
  );
  const positionKey = key + ":position";
  const scroll = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const layoutAnchor = useRef<{ node: HTMLElement; top: number } | null>(null);
  const layoutSize = useRef("");
  const manualScroll = useRef(false);
  const sizeKey = useCallback(() => {
    const el = scroll.current,
      body = content.current;
    return el && body
      ? `${el.clientWidth}:${el.clientHeight}:${body.getBoundingClientRect().height}`
      : "";
  }, []);
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
  const navigationSerial = useRef(0);
  const navigationPending = useRef(false);
  const alive = useRef(true);
  const windowEpoch = useRef(0);
  const programmaticUntil = useRef(0);
  const historyFlights = useRef(new Map<string, Promise<boolean>>());
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
  const base = sessionURL(session.project_id, session.id);
  const refresh = repository.refresh;
  const captureAnchor = useCallback(() => {
    const el = scroll.current;
    if (!el) return;
    const size = sizeKey();
    // A native scroll caused by reflow must not replace the pre-reflow bookmark.
    if (
      layoutAnchor.current?.node.isConnected &&
      layoutSize.current !== size &&
      !manualScroll.current
    )
      return;
    const top = el.getBoundingClientRect().top;
    const anchor = [
      ...el.querySelectorAll<HTMLElement>("[data-timeline-id]"),
    ].find(
      (node) =>
        node.getBoundingClientRect().height > 0 &&
        node.getBoundingClientRect().bottom > top,
    );
    if (anchor) {
      const block =
        [...anchor.querySelectorAll<HTMLElement>(".markdown-body > *")].find(
          (node) =>
            node.getBoundingClientRect().height > 0 &&
            node.getBoundingClientRect().bottom > top,
        ) || anchor;
      layoutAnchor.current = {
        node: block,
        top: block.getBoundingClientRect().top - top,
      };
      layoutSize.current = size;
      repository.setAnchor(anchor.dataset.timelineId!, following.current);
      if (!following.current)
        scrollAnchor.current = {
          id: anchor.dataset.timelineId!,
          top: anchor.getBoundingClientRect().top,
        };
    }
  }, [repository, sizeKey]);
  const loadAdjacent = useCallback(
    async (direction: "older" | "newer"): Promise<boolean> => {
      const pending = historyFlights.current.get(direction);
      if (pending) return pending;
      const current = repository.getSnapshot(),
        older = direction === "older",
        epoch = windowEpoch.current;
      const cursor = older ? current.before : current.after;
      if (!cursor) return false;
      following.current = false;
      captureAnchor();
      if (older) setLoadingHistory(true);
      else setLoadingNewer(true);
      const flight = (async () => {
        try {
          await repository.load(
            older ? { before: cursor } : { after: cursor },
            direction,
          );
          return alive.current && windowEpoch.current === epoch;
        } finally {
          historyFlights.current.delete(direction);
          if (alive.current) {
            if (older) setLoadingHistory(false);
            else setLoadingNewer(false);
          }
        }
      })();
      historyFlights.current.set(direction, flight);
      return flight;
    },
    [repository, captureAnchor],
  );
  const requestEdge = (direction: "older" | "newer") => {
    const el = scroll.current;
    if (!el || navigationPending.current) return;
    if (
      direction === "older" &&
      el.scrollTop < 100 &&
      repository.getSnapshot().before
    )
      void loadAdjacent("older");
    if (
      direction === "newer" &&
      el.scrollHeight - el.scrollTop - el.clientHeight < 100 &&
      repository.getSnapshot().after
    )
      void loadAdjacent("newer");
  };
  const showLatest = async () => {
    const serial = ++navigationSerial.current;
    windowEpoch.current++;
    navigationPending.current = true;
    setNavigationTarget(null);
    setFocusTurn("");
    setFocusMessage("");

    try {
      await repository.load();
      if (!alive.current || serial !== navigationSerial.current) return;
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
    repository.activate();
    if (!navigationPending.current) void repository.refresh();
    const unsubscribe = realtime.subscribe(session.project_id, (event) => {
      if (event.resource_kind === "closed") {
        repository.clear();
        return;
      }
      if (
        event.resource_kind === "ready" ||
        event.resource_kind === "resync" ||
        event.resource_kind === "authorization" ||
        (["content", "control"].includes(event.resource_kind) &&
          event.resource_id === session.id)
      ) {
        captureAnchor();
        void repository.refresh();
      }
    });
    return () => {
      alive.current = false;
      unsubscribe();
      repository.close();
    };
  }, [repository, realtime, session.project_id, session.id, captureAnchor]);
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
  }, [view.items, view.summaries]);
  // A missing destination loads a surrounding message window, not a turn filter.
  // Keep this window contiguous by extending its two edges as the reader scrolls.
  useEffect(() => {
    if (!navigationTarget) return;
    let gone = false;
    const serial = ++navigationSerial.current;
    windowEpoch.current++;
    navigationPending.current = true;
    following.current = false;
    scrollAnchor.current = null;
    void (async () => {
      try {
        if (!locate(navigationTarget.kind, navigationTarget.id)) {
          await repository.load(
            navigationTarget.kind === "turn"
              ? { around_turn: navigationTarget.id }
              : { around_message: navigationTarget.id },
          );
          if (gone || !alive.current || serial !== navigationSerial.current)
            return;
          if (repository.getSnapshot().status === "pending") return;
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
  }, [navigationTarget, repository, tr, view.items]);
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
    if (sentTurn && view.head?.turns.some((t) => t.id === sentTurn.id))
      void Promise.resolve().then(() => setSentTurn(null));
  }, [sentTurn, view.head?.turns]);
  const current = view.head?.session || session;
  let timeline: TimelineItem[] = view.items.map((item, index) => ({
    ...item,
    cursor: index,
    order: item.block,
  }));
  const activeTurns = new Set(
    view.head?.turns
      .filter((t) => ["starting", "running", "stopping"].includes(t.state))
      .map((t) => t.id),
  );
  const displayTurns = [...(view.head?.turns || [])];
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
  if (!view.after && !focusMessage && !focusTurn) {
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
  const turns = conversationTurns(timeline, view.summaries || [], displayTurns);
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
  const workspaceFileKey = knownWorkspaceFiles(
    timeline.flatMap((item) => [
      item.content || "",
      "arguments" in item ? item.arguments || "" : "",
      "result" in item ? item.result || "" : "",
    ]),
  ).join("\n");
  const workspaceFiles = useWorkspaceFiles(workspaceFileKey);
  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      await refresh();
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
    if (!el || !view.head) return;
    if (!restored.current && !navigationTarget) {
      const saved = recalled<{ scroll: number }>(positionKey, { scroll: 0 });
      el.scrollTop = following.current ? el.scrollHeight : saved.scroll;
      restored.current = true;
    } else if (following.current && !navigationTarget)
      el.scrollTop = el.scrollHeight;
  }, [
    view.items,
    view.head,
    focusMessage,
    focusTurn,
    positionKey,
    navigationTarget,
  ]);
  useEffect(() => {
    const el = scroll.current,
      body = content.current;
    if (!el || !body) return;
    // Viewport/composer resizing, fonts and process expansion can change layout
    // without a new view revision. Preserve the reader's existing follow intent.
    // One content observer covers the bounded window, not one per message.
    const resize = new ResizeObserver(() => {
      if (!restored.current || navigationPending.current) {
        layoutSize.current = sizeKey();
        return;
      }
      const anchor = layoutAnchor.current;
      const delta = anchor?.node.isConnected
        ? anchor.node.getBoundingClientRect().top -
          el.getBoundingClientRect().top -
          anchor.top
        : 0;
      if (following.current || Math.abs(delta) > 0.5) {
        programmaticUntil.current = performance.now() + 150;
        el.scrollTop = following.current
          ? el.scrollHeight
          : el.scrollTop + delta;
      }
      layoutSize.current = sizeKey();
      captureAnchor();
      manualScroll.current = false;
    });
    resize.observe(el);
    resize.observe(body);
    return () => {
      resize.disconnect();
      layoutAnchor.current = null;
    };
  }, [captureAnchor, sizeKey]);
  const control = (turn: Turn, action: string) =>
    void act(() =>
      command(`${base}/turns/${encodeURIComponent(turn.id)}/control`, {
        action,
        control_revision: current.control_revision,
      }),
    );
  const active = view.head?.turns.some((turn) =>
    ["starting", "running", "stopping"].includes(turn.state),
  );
  const queue =
    view.head?.turns.filter((turn) =>
      ["queued", "held"].includes(turn.state),
    ) || [];
  return (
    <section
      className="agent-panel"
      aria-label={`Agent ${current.title}`}
      data-session-id={session.id}
    >
      {error || view.error || current.failure ? (
        <p className="notice error" role="alert">
          {error || view.error || current.failure}
        </p>
      ) : null}
      {current.state === "blocked" && (
        <section className="notice warning">
          <p>
            {tr(
              "This Session needs recovery. Unknown prior effects will not be repeated.",
            )}
          </p>
          {view.head?.recovery
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
          {view.head?.recovery?.some((r) => r.recovery === "failed") && (
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
            ) {
              manualScroll.current = true;
              requestEdge(e.deltaY < 0 ? "older" : "newer");
            }
          }}
          onKeyDown={(e) => {
            if ((e.target as HTMLElement).closest("pre,.table-scroll,textarea"))
              return;
            if (
              [
                "PageUp",
                "PageDown",
                "ArrowUp",
                "ArrowDown",
                "Home",
                "End",
                " ",
              ].includes(e.key)
            ) {
              programmaticUntil.current = 0;
              manualScroll.current = true;
            }
            if (e.key === "PageUp" || e.key === "ArrowUp") requestEdge("older");
            if (e.key === "PageDown" || e.key === "ArrowDown")
              requestEdge("newer");
          }}
          onTouchMove={() => {
            programmaticUntil.current = 0;
            manualScroll.current = true;
          }}
          onPointerDown={(event) => {
            programmaticUntil.current = 0;
            manualScroll.current = event.target === event.currentTarget;
          }}
          onScroll={() => {
            const el = scroll.current!;
            const size = sizeKey(),
              manual = manualScroll.current;
            if (
              !manual &&
              layoutAnchor.current?.node.isConnected &&
              layoutSize.current !== size
            )
              return;
            layoutSize.current = size;
            manualScroll.current = false;
            if (
              !navigationPending.current &&
              (manual || performance.now() > programmaticUntil.current)
            )
              following.current =
                !repository.getSnapshot().after &&
                el.scrollHeight - el.scrollTop - el.clientHeight < 64;
            setAtBottom(following.current);
            if (
              !navigationPending.current &&
              performance.now() > programmaticUntil.current
            ) {
              if (el.scrollTop < 100 && repository.getSnapshot().before)
                void loadAdjacent("older");
              else if (
                el.scrollHeight - el.scrollTop - el.clientHeight < 100 &&
                repository.getSnapshot().after
              )
                void loadAdjacent("newer");
            }
            captureAnchor();
            remember(positionKey, {
              turn: focusTurn,
              scroll: el.scrollTop,
              follow: following.current,
            });
          }}
          onClick={(event) => {
            if (
              (event.target as HTMLElement).closest(
                "button[aria-expanded],summary",
              )
            ) {
              following.current = false;
              setAtBottom(false);
              captureAnchor();
              remember(positionKey, {
                turn: focusTurn,
                scroll: scroll.current!.scrollTop,
                follow: false,
              });
            }
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
          <div ref={content} className="conversation-content">
            {(navigationTarget || loadingHistory) && (
              <p className="history-loading" role="status">
                {tr("Loading earlier conversation…")}
              </p>
            )}
            {["uninitialized", "loading"].includes(view.status) &&
              !view.items.length && (
                <p className="history-loading" role="status">
                  {tr("Loading…")}
                </p>
              )}
            {!timeline.length && view.status === "empty" && (
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
                loadMessage={(id) => repository.messageText(id)}
                loadDetail={(item) =>
                  repository.detailText(item.id, item.detail_version!)
                }
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
          </div>
        </div>
        <TurnNavigator
          base={base}
          revision={view.head?.index_revision || "0"}
          local={navigationTurns}
          scroll={scroll}
          onJump={jumpTurn}
        />
      </div>
      {(!atBottom || view.after) && (
        <button
          className="jump-latest"
          aria-label={tr("Return to latest")}
          title={tr("Return to latest")}
          onClick={() => void showLatest()}
        >
          <ArrowDown size={18} />
        </button>
      )}
      {view.head?.turns
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
