"use client";

import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Bot, Hash, Loader2, MessageSquare, Search, User } from "lucide-react";

import { useHarness } from "@/components/harness-provider";
import { searchSessions } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatRelative, statusLabel } from "@/lib/format";
import type { ApiSession, SearchHit } from "@/lib/types";

const DEBOUNCE_MS = 150;
const RECENT_LIMIT = 8;

type SearchPaletteProps = {
  onClose: () => void;
  onOpenResult: (hit: SearchHit) => void;
};

// Turn a recent session into a synthetic title hit so the zero-state and search
// results share one rendering + navigation path.
function sessionToHit(session: ApiSession): SearchHit {
  return {
    kind: "title",
    session_id: session.id,
    title: session.title?.trim() || session.id,
    status: session.status,
    mode: session.mode,
    snippet: session.title?.trim() || session.id,
    created_at: session.updated_at
  };
}

// Split a snippet on the ⟦…⟧ match markers into plain / highlighted runs. Raw
// markers are stripped; unmatched text (no markers) returns a single plain run.
type SnippetPart = { text: string; match: boolean };

function parseSnippet(snippet: string): SnippetPart[] {
  const parts: SnippetPart[] = [];
  let rest = snippet;
  while (rest.length > 0) {
    const start = rest.indexOf("⟦"); // ⟦ open marker
    if (start === -1) {
      parts.push({ text: rest, match: false });
      break;
    }
    if (start > 0) parts.push({ text: rest.slice(0, start), match: false });
    const end = rest.indexOf("⟧", start + 1); // ⟧ close marker
    if (end === -1) {
      // Unbalanced opening marker: treat the remainder as plain text.
      parts.push({ text: rest.slice(start + 1), match: false });
      break;
    }
    parts.push({ text: rest.slice(start + 1, end), match: true });
    rest = rest.slice(end + 1);
  }
  return parts;
}

function HitIcon({ hit }: { hit: SearchHit }) {
  const className = "h-4 w-4 shrink-0 text-[var(--color-foreground-muted)]";
  if (hit.kind === "title") {
    return hit.mode === "shell" ? <Hash className={className} /> : <MessageSquare className={className} />;
  }
  return hit.role === "assistant" ? <Bot className={className} /> : <User className={className} />;
}

function Snippet({ snippet }: { snippet: string }) {
  const parts = useMemo(() => parseSnippet(snippet), [snippet]);
  return (
    <span className="line-clamp-2 text-xs text-[var(--color-foreground-muted)]">
      {parts.map((part, i) =>
        part.match ? (
          <mark
            key={i}
            className="rounded-[var(--radius-xs)] bg-[var(--color-accent-soft)] px-0.5 text-[var(--color-foreground)]"
          >
            {part.text}
          </mark>
        ) : (
          <Fragment key={i}>{part.text}</Fragment>
        )
      )}
    </span>
  );
}

function ResultRow({
  hit,
  active,
  onHover,
  onSelect,
  rowRef
}: {
  hit: SearchHit;
  active: boolean;
  onHover: () => void;
  onSelect: () => void;
  rowRef: (el: HTMLLIElement | null) => void;
}) {
  const title = hit.title?.trim() || hit.session_id;
  const meta = [hit.role, formatRelative(hit.created_at), statusLabel(hit.status)].filter(Boolean).join(" · ");
  return (
    <li ref={rowRef} role="option" aria-selected={active}>
      <button
        type="button"
        onMouseMove={onHover}
        onClick={onSelect}
        className={cn(
          "flex w-full items-start gap-2.5 rounded-[var(--radius)] px-3 py-2 text-left transition-colors",
          active ? "bg-[var(--color-surface-muted)]" : "hover:bg-[var(--color-surface-muted)]"
        )}
      >
        <span className="mt-0.5">
          <HitIcon hit={hit} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm text-[var(--color-foreground)]">{title}</span>
          {meta ? (
            <span className="mt-0.5 block truncate text-[11px] text-[var(--color-foreground-muted)]">{meta}</span>
          ) : null}
          {hit.kind === "message" ? (
            <span className="mt-1 block">
              <Snippet snippet={hit.snippet} />
            </span>
          ) : null}
        </span>
      </button>
    </li>
  );
}

// The palette only mounts while open (parent gates it), so open/close state and
// the per-open reset are handled purely by mount/unmount.
export function SearchPalette({ onClose, onOpenResult }: SearchPaletteProps) {
  const { state } = useHarness();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchHit[]>([]);
  const [loading, setLoading] = useState(false);
  const [active, setActive] = useState(0);

  const inputRef = useRef<HTMLInputElement>(null);
  const rowRefs = useRef<(HTMLLIElement | null)[]>([]);
  // Monotonic request id: only the newest in-flight response is allowed to win,
  // guarding against out-of-order network completions.
  const requestSeq = useRef(0);

  const trimmed = query.trim();

  // Zero-state: most-recently-updated sessions as quick-open rows.
  const recent = useMemo(() => {
    return [...state.sessions]
      .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
      .slice(0, RECENT_LIMIT)
      .map(sessionToHit);
  }, [state.sessions]);

  const hits = trimmed ? results : recent;

  // Ordered groups + a flat list spanning both, so keyboard nav can walk one
  // index across sections while headers still render between them.
  const titleHits = useMemo(() => hits.filter((h) => h.kind === "title"), [hits]);
  const messageHits = useMemo(() => hits.filter((h) => h.kind === "message"), [hits]);
  const flat = useMemo(() => [...titleHits, ...messageHits], [titleHits, messageHits]);

  // Clamp the highlight into range during render (no setState-in-effect churn).
  const activeIndex = flat.length === 0 ? 0 : Math.min(active, flat.length - 1);

  // Autofocus the input on mount (open).
  useEffect(() => {
    const raf = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(raf);
  }, []);

  // Debounced search. All state writes happen inside async callbacks (never
  // synchronously in the effect body) to satisfy the cascading-render rule.
  useEffect(() => {
    if (!trimmed) return; // empty query renders the recent-sessions zero-state.
    const seq = ++requestSeq.current;
    let cancelled = false;
    const timer = window.setTimeout(async () => {
      setLoading(true);
      const res = await searchSessions(trimmed);
      if (cancelled || seq !== requestSeq.current) return; // superseded / unmounted.
      setResults(res.ok ? res.data.results : []);
      setLoading(false);
    }, DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [trimmed]);

  // Auto-scroll the highlighted row into view.
  useEffect(() => {
    rowRefs.current[activeIndex]?.scrollIntoView({ block: "nearest" });
  }, [activeIndex]);

  const select = useCallback(
    (hit: SearchHit) => {
      onOpenResult(hit);
      onClose();
    },
    [onOpenResult, onClose]
  );

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
        return;
      }
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActive((prev) => (flat.length === 0 ? 0 : (Math.min(prev, flat.length - 1) + 1) % flat.length));
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setActive((prev) =>
          flat.length === 0 ? 0 : (Math.min(prev, flat.length - 1) - 1 + flat.length) % flat.length
        );
        return;
      }
      if (e.key === "Enter") {
        e.preventDefault();
        const hit = flat[activeIndex];
        if (hit) select(hit);
      }
    },
    [flat, activeIndex, onClose, select]
  );

  if (typeof document === "undefined") return null;

  const showLoading = Boolean(trimmed) && loading;
  // rowRefs indices map onto the flat list order (titles first, then messages).
  let flatIndex = 0;
  const registerRow = (i: number) => (el: HTMLLIElement | null) => {
    rowRefs.current[i] = el;
  };

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-[var(--color-overlay)] p-4 pt-[12vh]"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Search sessions and messages"
        className="flex max-h-[70vh] w-full max-w-3xl flex-col overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-xl"
        onKeyDown={onKeyDown}
      >
        <div className="flex items-center gap-2.5 border-b border-[var(--color-border)] px-4 py-3">
          <Search className="h-4 w-4 shrink-0 text-[var(--color-foreground-muted)]" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Search sessions and messages"
            placeholder="Search sessions and messages…"
            className="min-w-0 flex-1 bg-transparent text-sm text-[var(--color-foreground)] outline-none placeholder:text-[var(--color-foreground-muted)]"
          />
          {showLoading ? (
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-[var(--color-foreground-muted)]" />
          ) : null}
        </div>

        <ul role="listbox" aria-label="Search results" className="min-h-0 flex-1 overflow-y-auto p-1.5">
          {flat.length === 0 ? (
            <li className="px-3 py-8 text-center text-xs text-[var(--color-foreground-muted)]">
              {trimmed ? (showLoading ? "Searching…" : "No matches found.") : "No sessions yet."}
            </li>
          ) : (
            <>
              {titleHits.length > 0 ? (
                <li
                  aria-hidden
                  className="px-3 pb-1 pt-2 text-[11px] uppercase tracking-wider text-[var(--color-foreground-muted)]"
                >
                  {trimmed ? "Sessions" : "Recent"}
                </li>
              ) : null}
              {titleHits.map((hit) => {
                const idx = flatIndex++;
                return (
                  <ResultRow
                    key={`t-${hit.session_id}`}
                    hit={hit}
                    active={idx === activeIndex}
                    onHover={() => setActive(idx)}
                    onSelect={() => select(hit)}
                    rowRef={registerRow(idx)}
                  />
                );
              })}
              {messageHits.length > 0 ? (
                <li
                  aria-hidden
                  className="px-3 pb-1 pt-2 text-[11px] uppercase tracking-wider text-[var(--color-foreground-muted)]"
                >
                  Messages
                </li>
              ) : null}
              {messageHits.map((hit) => {
                const idx = flatIndex++;
                return (
                  <ResultRow
                    key={`m-${hit.session_id}-${hit.message_id ?? idx}`}
                    hit={hit}
                    active={idx === activeIndex}
                    onHover={() => setActive(idx)}
                    onSelect={() => select(hit)}
                    rowRef={registerRow(idx)}
                  />
                );
              })}
            </>
          )}
        </ul>

        <div className="flex shrink-0 items-center gap-4 border-t border-[var(--color-border)] px-4 py-2 text-[11px] text-[var(--color-foreground-muted)]">
          <span>↑↓ navigate</span>
          <span>↵ open</span>
          <span>esc close</span>
        </div>
      </div>
    </div>,
    document.body
  );
}
