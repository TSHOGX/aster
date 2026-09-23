"use client";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { ChevronDown, ChevronUp } from "lucide-react";
import { api, type TurnIndexItem, type TurnIndexPage } from "@/lib/project-api";
import { boundedTurnIndex, turnWaveWidth } from "@/lib/turn-navigation";
import { useT } from "@/lib/i18n";
export function TurnNavigator({
  base,
  revision,
  local,
  scroll,
  onJump,
}: {
  base: string;
  revision: string;
  local: TurnIndexItem[];
  scroll: RefObject<HTMLDivElement | null>;
  onJump: (id: string) => void;
}) {
  const tr = useT(),
    tooltip = useId();
  const [indexWindow, setIndexWindow] = useState<TurnIndexPage>({ turns: [] }),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [current, setCurrent] = useState(""),
    [hovered, setHovered] = useState<string | null>(null),
    [position, setPosition] = useState({ left: 0, top: 0, width: 320 });
  const index = indexWindow.turns,
    older = indexWindow.next_before,
    newer = indexWindow.next_after;
  const windowRef = useRef(indexWindow);
  const request = useRef<AbortController | null>(null);
  const pendingDirection = useRef<"older" | "newer" | null>(null);
  const rail = useRef<HTMLDivElement>(null),
    closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null),
    generation = useRef(0),
    first = useRef(true);
  const load = useCallback(
    async function load(direction: "older" | "newer" | "refresh" = "refresh") {
      if (request.current) {
        if (direction !== "refresh") pendingDirection.current = direction;
        return;
      }
      const old = windowRef.current;
      const cursor =
        direction === "older"
          ? old.next_before
          : direction === "newer"
            ? old.next_after
            : 0;
      if (direction !== "refresh" && !cursor) return;
      const abort = new AbortController();
      request.current = abort;
      const version = generation.current;
      setLoading(true);
      const anchor =
        rail.current &&
        [...rail.current.querySelectorAll<HTMLElement>("[data-nav-turn]")].find(
          (n) =>
            n.getBoundingClientRect().bottom >
            rail.current!.getBoundingClientRect().top,
        );
      const saved = anchor
        ? {
            id: anchor.dataset.navTurn!,
            top: anchor.getBoundingClientRect().top,
          }
        : undefined;
      try {
        const selector =
          direction === "older"
            ? `before=${cursor}`
            : direction === "newer"
              ? `after=${cursor}`
              : old.next_after && old.turns.length
                ? `before=${old.turns.at(-1)!.sequence + 1}`
                : "before=0";
        const limit =
          direction === "refresh" ? Math.max(100, old.turns.length) : 100;
        const page = await api<TurnIndexPage>(
          `${base}/turn-index?${selector}&limit=${limit}`,
          { signal: abort.signal },
        );
        if (version !== generation.current || abort.signal.aborted) return;
        const next = boundedTurnIndex(
          old,
          page,
          direction === "refresh" ? "replace" : direction,
        );
        windowRef.current = next;
        setIndexWindow(next);
        setError("");
        requestAnimationFrame(() => {
          if (version !== generation.current || !rail.current) return;
          if (first.current) {
            first.current = false;
            rail.current.scrollTop = rail.current.scrollHeight;
          } else if (saved) {
            const node = rail.current.querySelector<HTMLElement>(
              `[data-nav-turn="${CSS.escape(saved.id)}"]`,
            );
            if (node)
              rail.current.scrollTop +=
                node.getBoundingClientRect().top - saved.top;
          }
        });
      } catch (e) {
        if (version === generation.current && !abort.signal.aborted)
          setError(String(e));
      } finally {
        if (request.current === abort) {
          request.current = null;
          const next = pendingDirection.current;
          pendingDirection.current = null;
          if (next && version === generation.current && !abort.signal.aborted)
            void load(next);
          else setLoading(false);
        }
      }
    },
    [base],
  );
  useEffect(() => {
    const version = ++generation.current;
    const timer = setTimeout(() => void load(), 0);
    return () => {
      generation.current = version + 1;
      request.current?.abort();
      request.current = null;
      pendingDirection.current = null;
      clearTimeout(timer);
      if (closeTimer.current) clearTimeout(closeTimer.current);
    };
  }, [load]);
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout>;
    const refresh = () => {
      if (document.hidden) return;
      if (request.current) {
        timer = setTimeout(refresh, 200);
        return;
      }
      void load();
    };
    timer = setTimeout(refresh, 700);
    return () => clearTimeout(timer);
  }, [revision, load]);
  const localByID = new Map(local.map((t) => [t.id, t]));
  const turns = index.map((t) => {
    const here = localByID.get(t.id);
    return here
      ? {
          ...t,
          state: here.state,
          user_preview: (here.user_preview || t.user_preview).slice(0, 600),
          assistant_preview: (
            here.assistant_preview || t.assistant_preview
          ).slice(0, 1200),
        }
      : t;
  });
  const pinned = localByID.get(current);
  if (pinned && !turns.some((t) => t.id === pinned.id))
    turns.push({
      ...pinned,
      user_preview: pinned.user_preview.slice(0, 600),
      assistant_preview: pinned.assistant_preview.slice(0, 1200),
    });
  turns.sort((a, b) => a.sequence - b.sequence);
  const entryKey = turns.map((t) => t.id).join("|");
  useEffect(() => {
    const el = scroll.current;
    if (!el) return;
    let frame = 0;
    const update = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const bounds = el.getBoundingClientRect(),
          point = bounds.top + Math.min(120, bounds.height * 0.25);
        const nodes = [...el.querySelectorAll<HTMLElement>("[data-turn-id]")];
        let target: HTMLElement | undefined = nodes[0];
        for (const node of nodes) {
          if (node.getBoundingClientRect().top <= point) target = node;
          else break;
        }
        if (el.scrollHeight - el.scrollTop - el.clientHeight < 8)
          target = nodes.at(-1);
        if (target) setCurrent(target.dataset.turnId || "");
      });
    };
    update();
    el.addEventListener("scroll", update, { passive: true });
    const resize = new ResizeObserver(update);
    resize.observe(el);
    for (const child of el.children) resize.observe(child);
    return () => {
      cancelAnimationFrame(frame);
      el.removeEventListener("scroll", update);
      resize.disconnect();
    };
  }, [scroll, entryKey, revision]);
  useEffect(() => {
    if (hovered || !current) return;
    rail.current
      ?.querySelector<HTMLElement>(`[data-nav-turn="${CSS.escape(current)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [current, hovered]);
  const show = (id: string, button: HTMLElement) => {
    if (closeTimer.current) clearTimeout(closeTimer.current);
    setHovered(id);
    const rect = button.getBoundingClientRect();
    const left = rect.right + 10,
      width = Math.max(180, Math.min(360, window.innerWidth - left - 12));
    setPosition({
      left: Math.min(left, window.innerWidth - width - 8),
      top: Math.max(8, Math.min(rect.top - 24, window.innerHeight - 280)),
      width,
    });
  };
  const close = () => {
    closeTimer.current = setTimeout(() => setHovered(null), 140);
  };
  useEffect(() => {
    const hide = () => setHovered(null);
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") hide();
    };
    window.addEventListener("resize", hide);
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener("resize", hide);
      window.removeEventListener("keydown", key);
    };
  }, []);
  const hoverIndex = hovered ? turns.findIndex((t) => t.id === hovered) : null;
  const preview = turns.find((t) => t.id === hovered);
  if (!turns.length) return null;
  return (
    <nav
      className="turn-navigator"
      aria-label={tr("Turn navigation")}
      aria-busy={loading}
      onPointerLeave={close}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) close();
      }}
    >
      <div className="turn-rail" ref={rail}>
        {older && (
          // Keep paging intent available during background refresh. load()
          // coalesces a queued direction behind the single in-flight request.
          <button
            type="button"
            className="turn-rail-more"
            aria-label={tr("Earlier turns")}
            title={tr("Earlier turns")}
            onClick={() => void load("older")}
          >
            <ChevronUp size={12} />
          </button>
        )}
        {turns.map((turn, i) => (
          <button
            key={turn.id}
            type="button"
            className={`turn-tick ${current === turn.id ? "is-current" : ""} ${hovered === turn.id ? "is-preview" : ""} ${!index.some((t) => t.id === turn.id) ? "is-pinned" : ""}`}
            data-nav-turn={turn.id}
            aria-label={tr("Jump to turn {number}", { number: turn.sequence })}
            aria-current={current === turn.id ? "step" : undefined}
            aria-describedby={hovered === turn.id ? tooltip : undefined}
            tabIndex={
              current === turn.id ||
              (!turns.some((t) => t.id === current) && i === turns.length - 1)
                ? 0
                : -1
            }
            style={
              {
                "--tick-width": `${turnWaveWidth(i, hoverIndex === -1 ? null : hoverIndex)}px`,
              } as CSSProperties
            }
            onPointerEnter={(e) => {
              if (e.pointerType !== "touch") show(turn.id, e.currentTarget);
            }}
            onFocus={(e) => show(turn.id, e.currentTarget)}
            onClick={() => {
              setCurrent(turn.id);
              setHovered(null);
              onJump(turn.id);
            }}
            onKeyDown={(e) => {
              if (["ArrowUp", "ArrowDown", "Home", "End"].includes(e.key)) {
                e.preventDefault();
                const n =
                  e.key === "Home"
                    ? 0
                    : e.key === "End"
                      ? turns.length - 1
                      : Math.max(
                          0,
                          Math.min(
                            turns.length - 1,
                            i + (e.key === "ArrowUp" ? -1 : 1),
                          ),
                        );
                rail.current
                  ?.querySelector<HTMLButtonElement>(
                    `[data-nav-turn="${CSS.escape(turns[n].id)}"]`,
                  )
                  ?.focus();
              }
            }}
          >
            <span aria-hidden />
          </button>
        ))}
        {newer && (
          <button
            type="button"
            className="turn-rail-more"
            aria-label={tr("Later turns")}
            title={tr("Later turns")}
            onClick={() => void load("newer")}
          >
            <ChevronDown size={12} />
          </button>
        )}
        {error && (
          <button
            type="button"
            className="turn-rail-more"
            aria-label={tr("Retry turn navigation")}
            title={error}
            onClick={() => void load()}
          >
            !
          </button>
        )}
      </div>
      {preview &&
        createPortal(
          <div
            id={tooltip}
            role="tooltip"
            className="turn-preview-card"
            style={position}
            onPointerEnter={() => {
              if (closeTimer.current) clearTimeout(closeTimer.current);
            }}
            onPointerLeave={close}
          >
            <div className="turn-preview-meta">
              <span>{tr("Turn {number}", { number: preview.sequence })}</span>
              {preview.created_at && (
                <time>
                  {new Date(preview.created_at).toLocaleTimeString(undefined, {
                    hour: "numeric",
                    minute: "2-digit",
                  })}
                </time>
              )}
            </div>
            <div className="turn-preview-user">
              {preview.user_preview || tr("New message")}
            </div>
            <div className="turn-preview-answer">
              {preview.assistant_preview ||
                ([
                  "sending",
                  "queued",
                  "starting",
                  "running",
                  "stopping",
                ].includes(preview.state)
                  ? tr("Response in progress…")
                  : tr("No final answer"))}
            </div>
          </div>,
          document.body,
        )}
    </nav>
  );
}
