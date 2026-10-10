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
import { ArrowDown, ArrowUp, CornerUpRight, Pause, Play, Square, X } from "lucide-react";
import { ConversationRepository } from "@/lib/conversation-repository";
import { TurnNavigator } from "./turn-navigator";
import { TurnView } from "./turn-view";
import { conversationTurns } from "@/lib/conversation-turns";
import {
  latestReadingBookmark,
  parseReadingBookmark,
  readingBlockSelector,
  type PausedReadingBookmark,
} from "@/lib/reading-bookmark";
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
  type TimelineItem,
  type TurnIndexItem,
  type Turn,
  type PromptQueuePage,
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
  const key = `project-view:${actor}:${session.project_id}:draft:${session.id}`;
  const positionKey = key + ":position";
  const [initialBookmark] = useState(() =>
    targetMessage
      ? latestReadingBookmark
      : parseReadingBookmark(recalled(positionKey, null)),
  );
  const repository = useMemo(() => {
    const repository = new ConversationRepository(
      resources,
      session.project_id,
      session.id,
    );
    if (!initialBookmark.follow) repository.setAnchor(initialBookmark.item);
    return repository;
  }, [resources, session.project_id, session.id, initialBookmark]);
  const view = useSyncExternalStore(
    repository.subscribe,
    repository.getSnapshot,
    repository.getSnapshot,
  );
  const [draft, setDraft] = useState(() =>
    (() => {
      const saved = recalled<unknown>(key, "");
      return typeof saved === "string" ? saved.slice(0, 64000) : "";
    })(),
  );
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
  const following = useRef(!targetMessage && initialBookmark.follow);
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
  const [queuePage, setQueuePage] = useState<PromptQueuePage | null>(null);
  const [queueError, setQueueError] = useState("");
  const rowLocks = useRef(new Set<string>());
  const [pendingRows, setPendingRows] = useState<ReadonlySet<string>>(new Set());
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [stopPending, setStopPending] = useState(false);
  const queueRequest = useRef(0);
  const queueRevision = useRef<number | null>(null);
  const [focusMessage, setFocusMessage] = useState(targetMessage);
  const [focusItem, setFocusItem] = useState(
    !initialBookmark.follow ? initialBookmark.item : "",
  );
  const [focusTurn, setFocusTurn] = useState("");
  const navigationSerial = useRef(0);
  const navigationPending = useRef(!!targetMessage || !initialBookmark.follow);
  const alive = useRef(true);
  const windowEpoch = useRef(0);
  const programmaticUntil = useRef(0);
  const historyFlights = useRef(new Map<string, Promise<boolean>>());
  const scrollAnchor = useRef<{ id: string; top: number } | null>(null);
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [loadingNewer, setLoadingNewer] = useState(false);
  const [navigationTarget, setNavigationTarget] = useState<{
    kind: "turn" | "message" | "item";
    id: string;
    bookmark?: PausedReadingBookmark;
  } | null>(() =>
    targetMessage
      ? { kind: "message", id: targetMessage }
      : !initialBookmark.follow
        ? { kind: "item", id: initialBookmark.item, bookmark: initialBookmark }
        : null,
  );
  const [navigationLoaded, setNavigationLoaded] =
    useState<typeof navigationTarget>(null);
  const locate = (
    kind: "turn" | "message" | "item",
    id: string,
  ): HTMLElement | null => {
    const el = scroll.current;
    if (!el) return null;
    return kind === "turn"
      ? el.querySelector<HTMLElement>(
          `[data-turn-id="${CSS.escape(id)}"] .chat-message.user`,
        )
      : el.querySelector<HTMLElement>(
          `[${kind === "item" ? "data-timeline-id" : "data-message-id"}="${CSS.escape(id)}"]`,
        );
  };
  const scrollToTarget = useCallback(
    (target: HTMLElement, smooth = false, bookmark?: PausedReadingBookmark) => {
      const el = scroll.current;
      if (!el) return;
      following.current = false;
      setAtBottom(false);
      restored.current = true;
      programmaticUntil.current = performance.now() + 450;
      const inner = target.querySelector<HTMLElement>(".activity-output");
      if (bookmark && inner) {
        inner.scrollTop = bookmark.innerTop;
        inner.scrollLeft = bookmark.innerLeft;
      }
      const blocks = target.querySelectorAll<HTMLElement>(readingBlockSelector);
      const block =
        bookmark && bookmark.block >= 0
          ? blocks[Math.min(bookmark.block, blocks.length - 1)] || target
          : target;
      const offset = bookmark?.offset ?? 16;
      layoutSize.current = sizeKey();
      layoutAnchor.current = { node: block, top: offset };
      el.scrollTo({
        top:
          el.scrollTop +
          block.getBoundingClientRect().top -
          el.getBoundingClientRect().top -
          offset,
        behavior:
          smooth &&
          !window.matchMedia("(prefers-reduced-motion: reduce)").matches
            ? "smooth"
            : "instant",
      });
    },
    [sizeKey],
  );
  const jumpTurn = (id: string) => {
    setError("");
    navigationSerial.current++;
    navigationPending.current = false;
    following.current = false;
    setAtBottom(false);
    setFocusMessage("");
    setFocusItem("");
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
      setFocusItem("");
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
    if (restored.current && !navigationPending.current && following.current)
      remember(positionKey, latestReadingBookmark);
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
      const blocks = [
        ...anchor.querySelectorAll<HTMLElement>(readingBlockSelector),
      ];
      const block =
        blocks.find(
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
      if (
        restored.current &&
        !navigationPending.current &&
        !following.current
      ) {
        const inner = anchor.querySelector<HTMLElement>(".activity-output");
        remember(positionKey, {
          version: 1,
          follow: false,
          item: anchor.dataset.timelineId!,
          block: blocks.indexOf(block),
          offset: block.getBoundingClientRect().top - top,
          expanded: anchor instanceof HTMLDetailsElement && anchor.open,
          innerTop: inner?.scrollTop || 0,
          innerLeft: inner?.scrollLeft || 0,
        });
      }
      if (!following.current)
        scrollAnchor.current = {
          id: anchor.dataset.timelineId!,
          top: anchor.getBoundingClientRect().top,
        };
    }
  }, [repository, sizeKey, positionKey]);
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
    setError("");
    const serial = ++navigationSerial.current;
    windowEpoch.current++;
    navigationPending.current = true;
    setNavigationTarget(null);
    setFocusTurn("");
    setFocusMessage("");
    setFocusItem("");

    try {
      await repository.load();
      if (!alive.current || serial !== navigationSerial.current) return;
      following.current = true;
      setAtBottom(true);
      restored.current = true;
      requestAnimationFrame(() => {
        if (serial !== navigationSerial.current) return;
        navigationPending.current = false;
        scroll.current?.scrollTo({
          top: scroll.current.scrollHeight,
          behavior: "instant",
        });
        captureAnchor();
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
  // Fetch once per navigation demand. Page/status updates drive alignment in a
  // separate effect, including asynchronous projector readiness and missing items.
  useEffect(() => {
    if (!navigationTarget) return;
    let gone = false;
    const serial = ++navigationSerial.current;
    windowEpoch.current++;
    navigationPending.current = true;
    following.current = false;
    scrollAnchor.current = null;
    if (!locate(navigationTarget.kind, navigationTarget.id)) {
      void (async () => {
        try {
          await repository.load(
            navigationTarget.kind === "turn"
              ? { around_turn: navigationTarget.id }
              : navigationTarget.kind === "item"
                ? { around_item: navigationTarget.id }
                : { around_message: navigationTarget.id },
          );
          if (!gone && alive.current && serial === navigationSerial.current)
            setNavigationLoaded(navigationTarget);
        } catch (error) {
          if (!gone && alive.current && serial === navigationSerial.current) {
            setError(String(error));
            navigationPending.current = false;
            setNavigationTarget(null);
          }
        }
      })();
    }
    return () => {
      gone = true;
    };
  }, [navigationTarget, repository]);
  useEffect(() => {
    if (!navigationTarget) return;
    if (
      !locate(navigationTarget.kind, navigationTarget.id) &&
      (navigationLoaded !== navigationTarget ||
        ["pending", "loading", "uninitialized"].includes(view.status))
    )
      return;
    const serial = navigationSerial.current;
    const frame = requestAnimationFrame(() => {
      if (!alive.current || serial !== navigationSerial.current) return;
      const target = locate(navigationTarget.kind, navigationTarget.id);
      if (target) scrollToTarget(target, false, navigationTarget.bookmark);
      else if (
        navigationTarget.kind === "item" &&
        ["available", "empty"].includes(repository.getSnapshot().status)
      ) {
        setError(
          tr(
            "The saved reading position is unavailable. Showing the latest conversation.",
          ),
        );
        following.current = true;
        setAtBottom(true);
        restored.current = true;
        setFocusItem("");
        setFocusMessage("");
        scroll.current?.scrollTo({
          top: scroll.current.scrollHeight,
          behavior: "instant",
        });
      } else setError(tr("This conversation is unavailable."));
      navigationPending.current = false;
      setNavigationTarget(null);
      captureAnchor();
    });
    return () => cancelAnimationFrame(frame);
  }, [
    navigationTarget,
    navigationLoaded,
    repository,
    tr,
    view.items,
    view.status,
    captureAnchor,
    scrollToTarget,
  ]);
  useEffect(() => {
    remember(key, draft);
  }, [draft, key]);
  const current = view.head?.session || session;
  const appliedFrontier = view.applied_source_frontier;
  const loadQueue = useCallback(async (after = 0) => {
    const request = ++queueRequest.current;
    try {
      const query = new URLSearchParams({ after: String(after), limit: "32" });
      if (appliedFrontier !== null) query.set("projection_frontier", appliedFrontier);
      let page = await api<PromptQueuePage>(`${sessionURL(session.project_id, session.id)}/queue?${query}`);
      if (request !== queueRequest.current) return;
      if (after && queueRevision.current !== page.queue_revision) {
        query.set("after", "0");
        page = await api<PromptQueuePage>(`${sessionURL(session.project_id, session.id)}/queue?${query}`);
        if (request !== queueRequest.current) return;
        after = 0;
      }
      queueRevision.current = page.queue_revision;
      setQueuePage((old) => after && old && old.queue_revision === page.queue_revision
        ? { ...page, items: [...old.items, ...page.items] }
        : page);
      setQueueError("");
    } catch (e) {
      if (request === queueRequest.current)
        setQueueError(e instanceof Error ? e.message : tr("The action failed."));
    }
  }, [appliedFrontier, session.id, session.project_id, tr]);
  useEffect(() => {
    void Promise.resolve().then(() => loadQueue());
  }, [loadQueue, view.head?.session.control_revision]);
  useEffect(() => {
    if (!view.head?.active_run)
      void Promise.resolve().then(() => setStopPending(false));
  }, [view.head?.active_run]);
  const timeline: TimelineItem[] = view.items.map((item, index) => ({
    ...item,
    cursor: index,
    order: item.block,
  }));
  const activeRuns = new Set(view.head?.active_run ? [view.head.active_run.id] : []);
  const displayTurns = [...(view.head?.turns || [])];
  const turns = conversationTurns(timeline, view.summaries || []);
  const navigationTurns: TurnIndexItem[] = turns
    .filter((t) => t.turnId)
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
      await command<Turn>(`${base}/turns`, {
        request_id: intent.id,
        content: intent.content,
        held: intent.held,
      });
      setPendingSend(null);
      remember(key + ":send", null);
      await showLatest();
      await loadQueue();
      setDraft((old) => old === intent.content ? "" : old);

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
      el.scrollTop = el.scrollHeight;
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
      const delta =
        anchor?.node.isConnected &&
        anchor.node.getBoundingClientRect().height > 0
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
  const controlKey = key + ":controls";
  const controlRequests = useRef<Record<string, Record<string, unknown>>>(
    recalled(controlKey, {}),
  );
  const forgetControl = useCallback((requestKey: string) => {
    const { [requestKey]: _removed, ...remaining } = controlRequests.current;
    controlRequests.current = remaining;
    remember(controlKey, remaining);
  }, [controlKey]);
  useEffect(() => {
    let closed = false;
    for (const [requestKey, body] of Object.entries(controlRequests.current)) {
      if (typeof body.request_id !== "string") { forgetControl(requestKey); continue; }
      void api(`${base}/controls/${encodeURIComponent(body.request_id)}`)
        .then(async () => {
          if (closed) return;
          forgetControl(requestKey);
          await refresh();
          await loadQueue();
        })
        .catch((error) => {
          if (!closed && !(error instanceof APIError && error.status === 404))
            setQueueError(error instanceof Error ? error.message : tr("The action failed."));
        });
    }
    return () => { closed = true; };
  }, [base, forgetControl, loadQueue, refresh, tr]);
  const sendControl = async (requestKey: string, path: string, intent: Record<string, unknown>) => {
    const body = controlRequests.current[requestKey] || { ...intent, request_id: newID() };
    controlRequests.current = { ...controlRequests.current, [requestKey]: body };
    remember(controlKey, controlRequests.current);
    try {
      const receipt = await command(path, body);
      forgetControl(requestKey);
      return receipt;
    } catch (error) {
      if (error instanceof APIError && error.outcome?.state === "not_admitted")
        forgetControl(requestKey);
      throw error;
    }
  };
  const rowAction = (turn: Turn, action: () => Promise<unknown>) => {
    if (rowLocks.current.has(turn.id)) return;
    rowLocks.current.add(turn.id);
    setPendingRows(new Set(rowLocks.current));
    setRowErrors((old) => ({ ...old, [turn.id]: "" }));
    void action().then(async () => {
      try {
        await refresh();
        await loadQueue();
      } catch (error) {
        setQueueError(error instanceof Error ? error.message : tr("The action failed."));
      }
    }).catch((error) => {
      setRowErrors((old) => ({ ...old, [turn.id]: error instanceof Error ? error.message : tr("The action failed.") }));
    }).finally(() => {
      rowLocks.current.delete(turn.id);
      setPendingRows(new Set(rowLocks.current));
    });
  };
  const control = (turn: Turn, action: string) =>
    rowAction(turn, () =>
      sendControl(`prompt:${turn.id}:${action}`, `${base}/turns/${encodeURIComponent(turn.id)}/control`, {
        action,
        expected_prompt_revision: turn.prompt_revision || 1,
      }),
    );
  const steer = (turn: Turn) =>
    rowAction(turn, () =>
      sendControl(`steer:${turn.id}:${view.head?.active_run?.id}`, `${base}/control`, {
        action: "steer",
        source: turn.id,
        expected_prompt_revision: turn.prompt_revision,
        target_run_id: view.head?.active_run?.id,
        target_incarnation_id: view.head?.active_run?.incarnation_id,
        target_owner: view.head?.active_run?.owner,
      }),
    );
  const activeRun = view.head?.active_run;
  const canSteer = !!activeRun && ["starting", "running"].includes(activeRun.state);
  const canStop = !!activeRun && (["starting", "running"].includes(activeRun.state) ||
    (activeRun.state === "unknown" && view.head?.recovery?.some(r => r.id === activeRun.incarnation_id && r.state === "running")));
  const stopping = stopPending || activeRun?.state === "stopping";
  const stop = async () => {
    if (stopping || !canStop) return;
    setStopPending(true);
    try {
      await sendControl(`stop:${view.head?.active_run?.id}`, `${base}/control`, {
        action: "stop",
        target_run_id: view.head?.active_run?.id,
        target_incarnation_id: view.head?.active_run?.incarnation_id,
        target_owner: view.head?.active_run?.owner,
      });
      await refresh();
      await loadQueue();
    } catch (e) {
      setStopPending(false);
      setError(e instanceof Error ? e.message : tr("The action failed."));
    }
  };
  const active = !!activeRun;
  const queue = queuePage?.items || [];
  const deliveryBusy = (turn: Turn) => ["accepted", "dispatching", "native_queued", "unknown", "consumed"].includes(turn.delivery_state || "");
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
          }}
          onScrollCapture={(event) => {
            if (
              event.target !== event.currentTarget &&
              (event.target as HTMLElement).matches(".activity-output")
            )
              captureAnchor();
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
                activeRuns={activeRuns}
                loadMessage={(id) => repository.messageText(id)}
                loadDetail={(item) =>
                  repository.detailText(item.id, item.detail_version!)
                }
                actor={actor}
                project={session.project_id}
                session={session.id}
                targetMessage={focusMessage}
                targetItem={focusItem}
                targetItemOpen={
                  !initialBookmark.follow && initialBookmark.expanded
                }
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
                  onClick={() => control(turn, "cancel")}
                >
                  {tr("Cancel prompt")}
                </button>
              )}
            </details>
          ))}
        </section>
      )}
      {(queue.length > 0 || pendingSend || queueError) && (
        <details className="prompt-queue">
          <summary>
            {tr("Queued prompts")} · {queuePage?.total_pending ?? queue.length}
          </summary>
          {queueError && <p role="status">{queueError}</p>}
          {pendingSend && !queue.some((turn) => turn.request_id === pendingSend.id) && (
            <div className="prompt-queue-row" aria-busy="true">
              <span>{tr("Sending…")}: {pendingSend.content.slice(0, 120)}</span>
              <div className="row-actions" aria-label={tr("Pending action")}>
                <button type="button" disabled>{tr("Hold")}</button>
                <button type="button" disabled>{tr("Steer")}</button>
                <button type="button" disabled>{tr("Cancel prompt")}</button>
              </div>
            </div>
          )}
          {queue.map((turn) => (
            <div className="prompt-queue-row" key={turn.id} aria-busy={deliveryBusy(turn)}>
              <span>
                {turn.delivery_state === "native_queued" ? tr("Waiting to join current task…")
                  : turn.delivery_state === "consumed" ? tr("Synchronizing…")
                  : turn.delivery_state === "unknown" ? tr("Delivery outcome unknown")
                  : turn.delivery_state === "rejected" ? tr("Delivery rejected")
                  : turn.delivery_state === "removed" ? tr("Delivery removed")
                  : deliveryBusy(turn) ? tr("Submitting to current task…")
                  : turn.state === "held" ? tr("Held") : tr("Queued")}: {turn.content.slice(0, 120)}
              </span>
              <div className="row-actions">
                <button type="button" disabled={pendingRows.has(turn.id) || deliveryBusy(turn)} onClick={() => control(turn, turn.state === "held" ? "release" : "hold")}
                  aria-label={`${turn.state === "held" ? tr("Release") : tr("Hold")}: ${turn.content.slice(0, 80)}`}>
                  {turn.state === "held" ? <Play size={16} /> : <Pause size={16} />}
                  {turn.state === "held" ? tr("Release") : tr("Hold")}
                </button>
                <button type="button" disabled={pendingRows.has(turn.id) || deliveryBusy(turn) || !canSteer} onClick={() => steer(turn)}
                  aria-label={`${tr("Steer")}: ${turn.content.slice(0, 80)}`} title={!active ? tr("No active task") : !canSteer ? activeRun?.state === "stopping" ? tr("Stopping…") : tr("Recovery needed") : tr("Steer")}>
                  <CornerUpRight size={16} />{tr("Steer")}
                </button>
                <button
                  type="button"
                  disabled={pendingRows.has(turn.id) || deliveryBusy(turn)}
                  onClick={() => control(turn, "cancel")}
                  aria-label={`${tr("Cancel prompt")}: ${turn.content.slice(0, 80)}`}
                >
                  <X size={16} />{tr("Cancel prompt")}
                </button>
              </div>
              {(rowErrors[turn.id] || turn.dispatch_error || turn.delivery_error) && <p role="status">{rowErrors[turn.id] || turn.dispatch_error || turn.delivery_error}</p>}
            </div>
          ))}
          {!!queuePage?.next_after && (
            <button type="button" onClick={() => void loadQueue(queuePage.next_after)}>{tr("More prompts")}</button>
          )}
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
          aria-describedby={`composer-hint-${session.id}`}
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
              ...(active ? [{ label: tr("Add to queue"), disabled: busy || !draft.trim() || hasPending, action: () => send(false) }] : []),
              {
                label: tr("Hold prompt"),
                disabled: busy || !draft.trim() || hasPending,
                action: () => send(true),
              },
            ]}
          />
          <small id={`composer-hint-${session.id}`} className="composer-hint">
            {active ? tr("Enter to add to queue · Shift+Enter for a new line") : tr("Enter to send · Shift+Enter for a new line")}
          </small>
          <div className="row-actions">
            {active ? (
              <button
                type="button"
                className="stop-button"
                aria-label={stopping ? tr("Stopping…") : !canStop ? tr("Recovery needed") : tr("Stop current task")}
                title={stopping ? tr("Stopping…") : !canStop ? tr("Recovery needed") : tr("Stop current task")}
                disabled={stopping || !canStop}
                onClick={() => void stop()}
              >
                <Square size={14} fill="currentColor" />
              </button>
            ) : (
              <button
                className="send-button"
                aria-label={hasPending ? tr("Retry") : tr("Send")}
                title={hasPending ? tr("Retry") : tr("Send")}
                disabled={busy || (!draft.trim() && !hasPending) || current.state === "blocked"}
              >
                <ArrowUp size={18} />
              </button>
            )}
          </div>
        </div>
      </form>
    </section>
  );
}
