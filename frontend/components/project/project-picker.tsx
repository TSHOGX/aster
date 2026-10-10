"use client";

import { useEffect, useId, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { Check, ChevronDown, Folder, Search } from "lucide-react";
import { useT } from "@/lib/i18n";
import { type Project } from "@/lib/project-api";

export function ProjectPicker({
  projects,
  value,
  onChange,
  disabled = false,
  label,
  emptyOption,
  fallbackTitle,
}: {
  projects: Project[];
  value: string;
  onChange: (id: string) => void;
  disabled?: boolean;
  label?: string;
  emptyOption?: string;
  fallbackTitle?: string;
}) {
  const tr = useT();
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const [position, setPosition] = useState<CSSProperties | null>(null);
  const [container, setContainer] = useState<HTMLElement | null>(null);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState("");
  const available = projects.filter(
    (p) => !["deleted", "deleting"].includes(p.state),
  );
  const options = [
    ...(emptyOption ? [{ id: "", title: emptyOption }] : []),
    ...available,
  ].filter((p) =>
    p.title.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()),
  );
  const activeIndex = Math.max(
    0,
    options.findIndex((p) => p.id === active),
  );
  const activeOption = options[activeIndex];
  const title =
    projects.find((p) => p.id === value)?.title ||
    fallbackTitle ||
    emptyOption ||
    tr("Choose a Project");
  const close = (restore = false) => {
    setPosition(null);
    if (restore) trigger.current?.focus();
  };
  const open = () => {
    const rect = trigger.current!.getBoundingClientRect();
    setContainer(trigger.current!.closest("dialog") || document.body);
    setQuery("");
    setActive(value);
    setPosition(popupPosition(rect));
  };
  const isOpen = !!position;
  useEffect(() => {
    if (!isOpen) return;
    search.current?.focus({ preventScroll: true });
    const outside = (e: PointerEvent | FocusEvent) => {
      if (
        !popup.current?.contains(e.target as Node) &&
        !trigger.current?.contains(e.target as Node)
      )
        setPosition(null);
    };
    const dismiss = () => setPosition(null);
    const reposition = () => {
      if (trigger.current)
        setPosition(popupPosition(trigger.current.getBoundingClientRect()));
    };
    const scroll = (e: Event) => {
      if (e.target instanceof Node && popup.current?.contains(e.target)) return;
      // Opening/focusing the picker can finish a pending scroll into view,
      // especially in WebKit. Keep a visible trigger's popup attached to it.
      const rect = trigger.current?.getBoundingClientRect();
      const top = window.visualViewport?.offsetTop || 0;
      const bottom = top + (window.visualViewport?.height || window.innerHeight);
      if (!rect || rect.bottom < top || rect.top > bottom) dismiss();
      else setPosition(popupPosition(rect));
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    document.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", reposition);
    window.visualViewport?.addEventListener("resize", reposition);
    window.visualViewport?.addEventListener("scroll", reposition);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      document.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", reposition);
      window.visualViewport?.removeEventListener("resize", reposition);
      window.visualViewport?.removeEventListener("scroll", reposition);
    };
  }, [isOpen]);
  useEffect(() => {
    if (position)
      document
        .getElementById(`${id}-option-${activeIndex}`)
        ?.scrollIntoView({ block: "nearest" });
  }, [position, id, activeIndex, query]);
  const choose = (next: string) => {
    close(true);
    onChange(next);
  };
  return (
    <div className="project-select">
      <button
        ref={trigger}
        type="button"
        className="project-select-trigger"
        aria-label={label || tr("Project")}
        aria-haspopup="listbox"
        aria-expanded={!!position}
        aria-controls={position ? `${id}-list` : undefined}
        disabled={disabled}
        title={title}
        onClick={() => (position ? close() : open())}
        onKeyDown={(e) => {
          if (["ArrowDown", "ArrowUp"].includes(e.key)) {
            e.preventDefault();
            open();
          }
        }}
      >
        <Folder size={16} aria-hidden="true" />
        <span>{title}</span>
        <ChevronDown size={14} aria-hidden="true" />
      </button>
      {position &&
        container &&
        createPortal(
          <div
            ref={popup}
            className="project-select-popover"
            style={position}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229)
                return;
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                close(true);
              } else if (e.key === "Tab") {
                close(true);
              } else if (
                ["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)
              ) {
                e.preventDefault();
                const next =
                  e.key === "Home"
                    ? 0
                    : e.key === "End"
                      ? options.length - 1
                      : (activeIndex +
                          (e.key === "ArrowDown" ? 1 : -1) +
                          options.length) %
                        options.length;
                setActive(options[next]?.id || "");
              } else if (e.key === "Enter") {
                e.preventDefault();
                if (activeOption) choose(activeOption.id);
              }
            }}
          >
            <div className="project-select-search">
              <Search size={16} aria-hidden="true" />
              <input
                ref={search}
                role="combobox"
                aria-label={tr("Search projects")}
                aria-autocomplete="list"
                aria-expanded="true"
                aria-controls={`${id}-list`}
                aria-activedescendant={
                  activeOption ? `${id}-option-${activeIndex}` : undefined
                }
                placeholder={tr("Search projects")}
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setActive(value);
                }}
              />
            </div>
            <div
              className="project-select-options"
              role="listbox"
              id={`${id}-list`}
              aria-label={label || tr("Project")}
            >
              {options.map((p, index) => (
                <div
                  key={p.id}
                  id={`${id}-option-${index}`}
                  role="option"
                  aria-selected={p.id === value}
                  data-active={index === activeIndex}
                  className="project-select-option"
                  title={p.title}
                  onPointerMove={() => setActive(p.id)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => choose(p.id)}
                >
                  <Folder size={16} aria-hidden="true" />
                  <span>{p.title}</span>
                  {p.id === value && <Check size={16} aria-hidden="true" />}
                </div>
              ))}
            </div>
            {!options.length && (
              <p className="project-select-empty" role="status">
                {tr("No matching projects")}
              </p>
            )}
          </div>,
          container,
        )}
    </div>
  );
}

// Keep the search and options within the visible viewport, including when a
// touch keyboard opens. Flip above the trigger when there is more room there.
function popupPosition(rect: DOMRect): CSSProperties {
  const viewport = window.visualViewport;
  const top = viewport?.offsetTop || 0;
  const left = viewport?.offsetLeft || 0;
  const height = viewport?.height || window.innerHeight;
  const viewportWidth = viewport?.width || window.innerWidth;
  const width = Math.min(320, viewportWidth - 24);
  const below = top + height - rect.bottom - 12;
  const above = rect.top - top - 12;
  const upwards = below < 260 && above > below;
  return {
    width,
    left: Math.max(
      left + 12,
      Math.min(rect.left, left + viewportWidth - width - 12),
    ),
    ...(upwards
      ? { bottom: window.innerHeight - rect.top + 6 }
      : { top: rect.bottom + 6 }),
    maxHeight: Math.max(100, Math.min(360, upwards ? above : below)),
  };
}
