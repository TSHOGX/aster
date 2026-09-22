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
import { ChevronUp } from "lucide-react";
import { api, type TurnIndexItem, type TurnIndexPage } from "@/lib/project-api";
import { mergeTurnIndex, turnWaveWidth } from "@/lib/turn-navigation";
import { useT } from "@/lib/i18n";
export function TurnNavigator({
  base,
  revision,
  local,
  scroll,
  onJump,
}: {
  base: string;
  revision: number;
  local: TurnIndexItem[];
  scroll: RefObject<HTMLDivElement | null>;
  onJump: (id: string) => void;
}) {
  const tr = useT(),
    tooltip = useId();
  const [index, setIndex] = useState<TurnIndexItem[]>([]),
    [older, setOlder] = useState<number | undefined>(),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [current, setCurrent] = useState(""),
    [hovered, setHovered] = useState<string | null>(null),
    [position, setPosition] = useState({ left: 0, top: 0, width: 320 });
  const rail = useRef<HTMLDivElement>(null),
    closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null),
    olderBusy = useRef(false),
    generation = useRef(0),
    inFlight = useRef(false),
    first = useRef(true);
  const load = useCallback(
    async (before = 0) => {
      if (before && olderBusy.current) return;
      const version = generation.current;
      if (before) olderBusy.current = true;
      setLoading(true);
      try {
        const page = await api<TurnIndexPage>(
          `${base}/turn-index?before=${before}&limit=100`,
        );
        if (version !== generation.current) return;
        const initial = first.current;
        const previousHeight = rail.current?.scrollHeight || 0,
          previousTop = rail.current?.scrollTop || 0;
        setIndex((old) => mergeTurnIndex(old, page.turns || []));
        setOlder((old) =>
          before ? page.next_before : initial ? page.next_before : old,
        );
        setError("");
        if (first.current) {
          first.current = false;
          requestAnimationFrame(() => {
            if (rail.current)
              rail.current.scrollTop = rail.current.scrollHeight;
          });
        } else if (before)
          requestAnimationFrame(() => {
            if (rail.current)
              rail.current.scrollTop =
                previousTop + rail.current.scrollHeight - previousHeight;
          });
      } catch (e) {
        if (version === generation.current) setError(String(e));
      } finally {
        olderBusy.current = false;
        setLoading(false);
      }
    },
    [base],
  );
  useEffect(() => {
    const version = ++generation.current;
    const timer = setTimeout(() => void load(), 0);
    return () => {
      generation.current = version + 1;
      clearTimeout(timer);
      if (closeTimer.current) clearTimeout(closeTimer.current);
    };
  }, [load]);
  // Coalesce streaming revisions; do not request a navigation page for every token.
  useEffect(() => {
    const timer = setTimeout(() => {
      if (inFlight.current || document.hidden) return;
      inFlight.current = true;
      void load().finally(() => {
        inFlight.current = false;
      });
    }, 700);
    return () => clearTimeout(timer);
  }, [revision, load]);
  const entries = mergeTurnIndex(
    index,
    local.filter((t) => !index.some((x) => x.id === t.id)),
  );
  // Refresh current-page previews immediately while keeping off-page history indexed.
  const localByID = new Map(local.map((t) => [t.id, t]));
  const turns = entries.map((t) =>
    localByID.has(t.id)
      ? {
          ...t,
          ...localByID.get(t.id),
          sequence: t.sequence,
          user_preview: localByID.get(t.id)?.user_preview || t.user_preview,
          assistant_preview:
            localByID.get(t.id)?.assistant_preview || t.assistant_preview,
        }
      : t,
  );
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
      onPointerLeave={close}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) close();
      }}
    >
      <div
        className="turn-rail"
        ref={rail}
        onScroll={() => {
          if (rail.current && rail.current.scrollTop < 12 && older && !loading)
            void load(older);
        }}
      >
        {older && (
          <button
            type="button"
            className="turn-rail-more"
            aria-label={tr("Earlier turns")}
            title={tr("Earlier turns")}
            disabled={loading}
            onClick={() => void load(older)}
          >
            <ChevronUp size={12} />
          </button>
        )}
        {turns.map((turn, i) => (
          <button
            key={turn.id}
            type="button"
            className={`turn-tick ${current === turn.id ? "is-current" : ""} ${hovered === turn.id ? "is-preview" : ""}`}
            data-nav-turn={turn.id}
            aria-label={tr("Jump to turn {number}", { number: turn.sequence })}
            aria-current={current === turn.id ? "step" : undefined}
            aria-describedby={hovered === turn.id ? tooltip : undefined}
            tabIndex={
              current === turn.id || (!current && i === turns.length - 1)
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
        {error && (
          <button
            type="button"
            className="turn-rail-more"
            aria-label={tr("Retry turn navigation")}
            title={error}
            onClick={() => void load(older)}
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
