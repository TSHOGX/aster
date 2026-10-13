"use client";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  useSyncExternalStore,
} from "react";
import { PanelRight, ArrowDown } from "lucide-react";
import {
  api,
  encode,
  recalled,
  remember,
  type TimelineItem,
} from "@/lib/project-api";
import { ConversationRepository } from "@/lib/conversation-repository";
import {
  receivedConversation,
  type ConversationSource,
  type ReceivedConversation,
} from "@/lib/conversation-source";
import { conversationTurns } from "@/lib/conversation-turns";
import { sharedFileSource } from "@/lib/file-source";
import { workspacePath } from "@/lib/workspace-links";
import { useT } from "@/lib/i18n";
import { useResources } from "./resource-provider";
import { ConversationReader } from "./conversation-reader";
import { TurnNavigator } from "./turn-navigator";
import { SharedFilesPanel } from "./shared-files-panel";
import { CopyConversation } from "./copy-conversation";
import { IconButton, Splitter } from "./ui";

export function SharedWorkspace({
  selected,
  actor,
  targetMessage = "",
  onCopied,
}: {
  selected: string;
  actor: string;
  targetMessage?: string;
  onCopied?: (project: string, session: string) => void;
}) {
  const tr = useT();
  const [metadata, setMetadata] = useState<ReceivedConversation>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    if (!selected) return;
    void api<ReceivedConversation>(`/api/conversations/${encode(selected)}`, {
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
  }, [selected, attempt]);
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  const source = useMemo(
    () =>
      metadata
        ? receivedConversation(
            metadata.session_id,
            metadata.revision,
            metadata.allow_download,
            metadata.can_copy !== false,
          )
        : undefined,
    [metadata],
  );
  if (!selected)
    return (
      <div className="empty-state">
        {tr("Choose a shared conversation in the sidebar.")}
      </div>
    );
  if (!metadata || !source)
    return (
      <div className="empty-state" role={error ? "alert" : "status"}>
        {error || tr("Loading…")}
        {error && (
          <button onClick={() => setAttempt((n) => n + 1)}>
            {tr("Retry")}
          </button>
        )}
      </div>
    );
  return (
    <SharedReader
      key={`${source.key}:${targetMessage}`}
      actor={actor}
      source={source}
      title={metadata.title}
      attribution={`${metadata.source.display_name} (@${metadata.source.username})`}
      paths={metadata.paths}
      targetMessage={targetMessage}
      onMetadata={reload}
      onCopied={onCopied}
    />
  );
}

export function SharedReader({
  actor,
  source,
  title,
  attribution,
  paths,
  targetMessage = "",
  onMetadata,
  onCopied,
}: {
  actor: string;
  source: ConversationSource;
  title: string;
  attribution: string;
  paths: string[];
  targetMessage?: string;
  onMetadata: () => void;
  onCopied?: (project: string, session: string) => void;
}) {
  const tr = useT();
  const resources = useResources();
  const fileSource = useMemo(
    () => sharedFileSource(source, paths),
    [source, paths],
  );
  const repository = useMemo(
    () => new ConversationRepository(resources, source),
    [resources, source],
  );
  const view = useSyncExternalStore(
    repository.subscribe,
    repository.getSnapshot,
    repository.getSnapshot,
  );
  const [available, setAvailable] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [file, setFile] = useState("");
  const [fileRevision, setFileRevision] = useState(0);
  const [filesOpen, setFilesOpen] = useState(false);
  const [fileError, setFileError] = useState("");
  const [fullscreen, setFullscreen] = useState(false);
  const scroll = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  const [focus, setFocus] = useState(targetMessage);
  const bookmarkKey = `shared-reading:${actor}:${source.kind}:${source.session}`;
  const [initialBookmark] = useState(() =>
    recalled<{ item?: string; top?: number }>(bookmarkKey, {}),
  );
  const restored = useRef(false);
  const locating = useRef(false);
  const historyLoading = useRef(false);
  const [readingError, setReadingError] = useState("");
  const [atBottom, setAtBottom] = useState(true);
  const [fileWidth, setFileWidth] = useState(640);
  useEffect(() => {
    if (!fullscreen) return;
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !document.querySelector("dialog[open]"))
        setFullscreen(false);
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [fullscreen]);
  useEffect(() => {
    repository.activate();
    let gone = false;
    const events = new EventSource(source.events!);
    let checkGeneration = 0;
    const suspend = () => {
      checkGeneration++;
      setAvailable(false);
      resources.clear(source.key);
      repository.clear();
    };
    const ready = () => {
      const generation = ++checkGeneration;
      void api<{
        revision?: number;
        access_revision?: number;
        title: string;
        can_copy?: boolean;
      }>(source.base)
        .then((meta) => {
          if (gone || generation !== checkGeneration || document.hidden) return;
          const revision =
            source.kind === "received" ? meta.revision : meta.access_revision;
          if (
            revision !== source.authorityRevision ||
            meta.title !== title ||
            (meta.can_copy !== false) !== source.capabilities.copy
          ) {
            suspend();
            onMetadata();
            return;
          }
          setUnavailable(false);
          setAvailable(true);
          void repository.refresh();
        })
        .catch(() => {
          if (!gone && generation === checkGeneration) {
            suspend();
            setUnavailable(true);
          }
        });
    };
    events.addEventListener("ready", ready);
    events.addEventListener("changed", () => {
      if (source.kind === "received") ready();
      else {
        suspend();
        onMetadata();
      }
    });
    events.addEventListener("closed", () => {
      checkGeneration++;
      suspend();
      setUnavailable(true);
      events.close();
    });
    events.onerror = () => {
      checkGeneration++;
      suspend();
    };
    window.addEventListener("offline", suspend);
    window.addEventListener("pagehide", suspend);
    const restorePage = (event: PageTransitionEvent) => {
      if (event.persisted) onMetadata();
    };
    window.addEventListener("pageshow", restorePage);
    const visibility = () => {
      if (document.hidden) {
        suspend();
      } else {
        onMetadata();
      }
    };
    document.addEventListener("visibilitychange", visibility);
    return () => {
      gone = true;
      events.close();
      repository.close();
      resources.clear(source.key);
      window.removeEventListener("offline", suspend);
      window.removeEventListener("pagehide", suspend);
      window.removeEventListener("pageshow", restorePage);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [repository, resources, source, onMetadata, title]);
  useEffect(() => {
    if (!available || !view.items.length) return;
    const el = scroll.current;
    if (!el) return;
    if (!restored.current) {
      const saved = initialBookmark;
      const target = targetMessage
        ? el.querySelector<HTMLElement>(
            `[data-message-id="${CSS.escape(targetMessage)}"]`,
          )
        : saved.item
          ? el.querySelector<HTMLElement>(
              `[data-timeline-id="${CSS.escape(saved.item)}"]`,
            )
          : undefined;
      if (target) {
        el.scrollTop +=
          target.getBoundingClientRect().top -
          el.getBoundingClientRect().top -
          (targetMessage ? 16 : saved.top || 0);
        following.current = false;
        restored.current = true;
      } else if (targetMessage || saved.item) {
        if (!locating.current) {
          locating.current = true;
          following.current = false;
          void repository
            .load(
              targetMessage
                ? { around_message: targetMessage }
                : { around_item: saved.item },
            )
            .then(() => {
              if (repository.getSnapshot().status === "error") {
                restored.current = true;
                setReadingError(repository.getSnapshot().error);
              }
            })
            .catch((e) => {
              restored.current = true;
              setReadingError(String(e));
            });
        }
        return;
      } else {
        restored.current = true;
      }
    }
    if (following.current) el.scrollTop = el.scrollHeight;
  }, [available, view.items, repository, initialBookmark, targetMessage]);
  useEffect(() => {
    if (!available || !filesOpen || !file || source.kind !== "received") return;
    const events = new EventSource(
      `${source.base}/files/events?path=${encode(file)}`,
    );
    events.addEventListener("changed", () => setFileRevision((n) => n + 1));
    events.addEventListener("closed", () => {
      setFile("");
      events.close();
    });
    events.onerror = () => {
      resources.clear(source.key);
      setFile("");
    };
    return () => events.close();
  }, [available, filesOpen, file, source, resources]);
  const timeline: TimelineItem[] = view.items.map((item, index) => ({
    ...item,
    cursor: index,
    order: item.block,
  }));
  const turns = conversationTurns(timeline, view.summaries);
  const activeRuns = useMemo(
    () =>
      new Set(
        view.items
          .filter(
            (item) =>
              item.run_id &&
              ["running", "streaming"].includes(item.state || ""),
          )
          .map((item) => item.run_id!),
      ),
    [view.items],
  );
  const navigate = async (turn: string) => {
    restored.current = true;
    following.current = false;
    await repository.load({ around_turn: turn });
    requestAnimationFrame(() =>
      scroll.current
        ?.querySelector(`[data-turn-id="${CSS.escape(turn)}"]`)
        ?.scrollIntoView({ block: "start" }),
    );
  };
  const openFile = (path: string) => {
    if (!paths.includes(path)) {
      setFileError(tr("This file has not been shared."));
      return;
    }
    setFileError("");
    setFile(path);
    setFilesOpen(true);
  };
  const older = async () => {
    if (historyLoading.current || !view.before) return;
    historyLoading.current = true;
    const el = scroll.current;
    if (!el) {
      historyLoading.current = false;
      return;
    }
    const old = el.scrollHeight;
    const top = el.scrollTop;
    try {
      await repository.load({ before: view.before }, "older");
      requestAnimationFrame(() => {
        el.scrollTop = top + el.scrollHeight - old;
        historyLoading.current = false;
      });
    } catch (e) {
      historyLoading.current = false;
      setReadingError(String(e));
    }
  };
  if (!available)
    return (
      <div className="empty-state" role="status">
        {unavailable
          ? tr("This shared conversation is unavailable.")
          : tr("Checking shared access…")}
      </div>
    );
  return (
    <div
      className={`project-workspace shared-workspace ${fullscreen ? "shared-fullscreen" : ""}`}
    >
      <header className="workspace-heading">
        <div className="workspace-title">
          <span>{title}</span>
          <small>
            {attribution} · {tr("Read-only")}
          </small>
        </div>
        <div className="workspace-switches">
          <CopyConversation
            source={source}
            title={title}
            paths={paths}
            onCopied={onCopied}
          />
          <IconButton
            label={tr("File preview")}
            aria-pressed={filesOpen}
            onClick={() => setFilesOpen((v) => !v)}
          >
            <PanelRight size={19} />
          </IconButton>
        </div>
      </header>
      {fileError && (
        <p className="notice error" role="alert">
          {fileError}
        </p>
      )}
      <div
        className={`workspace-body ${filesOpen ? "files-visible" : ""}`}
        style={{ "--shared-file-width": `${fileWidth}px` } as CSSProperties}
      >
        <section
          className="agent-panel"
          aria-label={tr("Shared conversation")}
          hidden={fullscreen}
        >
          {readingError && (
            <p role="alert" className="notice error">
              {readingError}
            </p>
          )}
          {view.error && (
            <p role="alert" className="notice error">
              {view.error}
              <button onClick={() => void repository.refresh()}>
                {tr("Retry")}
              </button>
            </p>
          )}
          <div className="conversation-stage">
            <div
              ref={scroll}
              className="conversation-scroll"
              onScroll={() => {
                const el = scroll.current!;
                following.current =
                  el.scrollHeight - el.scrollTop - el.clientHeight < 60;
                setAtBottom(following.current);
                if (restored.current && el.scrollTop < 80 && view.before)
                  void older();
                const item = [
                  ...el.querySelectorAll<HTMLElement>("[data-timeline-id]"),
                ].find(
                  (n) =>
                    n.getBoundingClientRect().bottom >
                    el.getBoundingClientRect().top,
                );
                if (item && restored.current)
                  remember(bookmarkKey, {
                    item: item.dataset.timelineId,
                    top:
                      item.getBoundingClientRect().top -
                      el.getBoundingClientRect().top,
                  });
              }}
              onClick={(event) => {
                const anchor = (event.target as HTMLElement).closest("a");
                const href = anchor?.getAttribute("href");
                if (!href) return;
                if (
                  href.startsWith("/workspace/") ||
                  href.startsWith("/api/projects/")
                ) {
                  event.preventDefault();
                  let path: string | null = null;
                  try {
                    path = workspacePath(decodeURIComponent(href), paths);
                  } catch {}
                  if (path) openFile(path);
                  else setFileError(tr("This file has not been shared."));
                }
              }}
            >
              <div className="conversation-content">
                {view.before && (
                  <button onClick={() => void older()}>
                    {tr("Load earlier messages")}
                  </button>
                )}
                {view.status === "loading" && (
                  <p role="status">{tr("Loading…")}</p>
                )}
                <ConversationReader
                  fileSource={fileSource}
                  turns={turns}
                  files={paths}
                  activeRuns={activeRuns}
                  loadMessage={(id) => repository.messageText(id)}
                  loadDetail={(item) =>
                    repository.detailText(item.id, item.detail_version!)
                  }
                  actor={actor}
                  project={source.key}
                  session={source.session}
                  targetMessage={focus}
                  shared
                />
                {view.after && (
                  <button
                    onClick={() =>
                      void repository.load({ after: view.after }, "newer")
                    }
                  >
                    {tr("Show more")}
                  </button>
                )}
              </div>
            </div>
            <TurnNavigator
              base={source.base}
              revision={view.head?.index_revision || "0"}
              local={[]}
              scroll={scroll}
              onJump={(id) => {
                setFocus("");
                void navigate(id);
              }}
            />
          </div>
          {(!atBottom || view.after) && (
            <button
              className="jump-latest"
              aria-label={tr("Return to latest")}
              onClick={() => {
                restored.current = true;
                following.current = true;
                void repository.load();
              }}
            >
              <ArrowDown size={18} />
            </button>
          )}
        </section>
        {filesOpen && !fullscreen && (
          <div className="shared-files-splitter">
            <Splitter
              label={tr("Resize file preview")}
              value={fileWidth}
              min={400}
              max={1000}
              reverse
              onChange={setFileWidth}
            />
          </div>
        )}
        {filesOpen && (
          <div className="shared-files">
            <SharedFilesPanel
              source={source}
              paths={paths}
              selected={file}
              validation={fileRevision}
              onSelect={openFile}
              fullscreen={fullscreen}
              onFullscreen={() => setFullscreen((v) => !v)}
              onClose={() => {
                setFilesOpen(false);
                setFullscreen(false);
              }}
            />
          </div>
        )}
      </div>
    </div>
  );
}
