"use client";

import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Check, ChevronDown, Search } from "lucide-react";
import { useT } from "@/lib/i18n";
import { usePickerPopover } from "./picker-popover";

export type PickerOption = {
  value: string;
  label: string;
  description?: string;
  searchText?: string;
};

export function SelectPicker({
  label,
  value,
  options,
  onChange,
  searchLabel,
  placeholder,
  description,
  describedBy,
  disabled = false,
  required = false,
  invalid = false,
  icon,
  maxWidth,
}: {
  label: string;
  value: string;
  options: PickerOption[];
  onChange: (value: string) => void;
  searchLabel?: string;
  placeholder?: string;
  description?: string;
  describedBy?: string;
  disabled?: boolean;
  required?: boolean;
  invalid?: boolean;
  icon?: ReactNode;
  maxWidth?: number;
}) {
  const tr = useT(),
    id = useId();
  const {
    triggerRef,
    panelRef,
    position,
    container,
    isOpen,
    open: openPopover,
    close,
    tabFromTrigger,
  } = usePickerPopover(maxWidth);
  const searchRef = useRef<HTMLInputElement>(null),
    listRef = useRef<HTMLDivElement>(null);
  const typeahead = useRef({ text: "", at: 0 });
  const [query, setQuery] = useState(""),
    [active, setActive] = useState(value);
  const needle = query.trim().toLocaleLowerCase().replaceAll("_", " ");
  const visible = options.filter((option) =>
    `${option.label} ${option.description || ""} ${option.searchText || ""}`
      .toLocaleLowerCase()
      .replaceAll("_", " ")
      .includes(needle),
  );
  const activeIndex = Math.max(
    0,
    visible.findIndex((option) => option.value === active),
  );
  const title =
    options.find((option) => option.value === value)?.label ||
    value ||
    placeholder ||
    tr("Choose an option");
  const activeID = visible.length ? `${id}-option-${activeIndex}` : undefined;
  const open = () => {
    setQuery("");
    setActive(value);
    typeahead.current = { text: "", at: 0 };
    openPopover();
  };
  const choose = (next: string) => {
    close(true);
    if (next !== value) onChange(next);
  };
  useEffect(() => {
    if (isOpen)
      (searchRef.current || listRef.current)?.focus({ preventScroll: true });
  }, [isOpen]);
  useEffect(() => {
    if (isOpen && activeID)
      document.getElementById(activeID)?.scrollIntoView({ block: "nearest" });
  }, [isOpen, activeID, query]);
  return (
    <div className="selection-picker">
      <span className="selection-picker-label" id={`${id}-label`}>
        {label}
      </span>
      <button
        ref={triggerRef}
        type="button"
        role="combobox"
        className="selection-picker-trigger"
        aria-labelledby={`${id}-label`}
        aria-describedby={[
          `${id}-value`,
          description ? `${id}-help` : "",
          describedBy,
        ]
          .filter(Boolean)
          .join(" ")}
        aria-expanded={isOpen}
        aria-haspopup="listbox"
        aria-controls={isOpen ? `${id}-list` : undefined}
        aria-required={required || undefined}
        aria-invalid={invalid || undefined}
        disabled={disabled}
        title={title}
        onClick={() => (isOpen ? close() : open())}
        onKeyDown={(event) => {
          if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
            event.preventDefault();
            open();
          }
        }}
      >
        {icon}
        <span id={`${id}-value`}>{title}</span>
        <ChevronDown size={16} aria-hidden="true" />
      </button>
      {description && <small id={`${id}-help`}>{description}</small>}
      {position &&
        container &&
        !disabled &&
        createPortal(
          <div
            ref={panelRef}
            className="selection-picker-popover"
            style={position}
            onKeyDown={(event) => {
              if (
                event.nativeEvent.isComposing ||
                event.nativeEvent.keyCode === 229
              )
                return;
              if (event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                close(true);
              } else if (event.key === "Tab") {
                if (tabFromTrigger(event.shiftKey)) event.preventDefault();
                event.stopPropagation();
              } else if (
                ["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) &&
                (!searchLabel || !["Home", "End"].includes(event.key))
              ) {
                event.preventDefault();
                event.stopPropagation();
                const next =
                  event.key === "Home"
                    ? 0
                    : event.key === "End"
                      ? visible.length - 1
                      : (activeIndex +
                          (event.key === "ArrowDown" ? 1 : -1) +
                          visible.length) %
                        visible.length;
                setActive(visible[next]?.value || "");
              } else if (
                event.key === "Enter" ||
                (!searchLabel && event.key === " ")
              ) {
                event.preventDefault();
                event.stopPropagation();
                if (visible[activeIndex]) choose(visible[activeIndex].value);
              } else if (
                !searchLabel &&
                event.key.length === 1 &&
                !event.ctrlKey &&
                !event.metaKey &&
                !event.altKey
              ) {
                event.preventDefault();
                const now = event.timeStamp;
                const text = (
                  (now - typeahead.current.at < 700
                    ? typeahead.current.text
                    : "") + event.key.toLocaleLowerCase()
                ).slice(-128);
                typeahead.current = { text, at: now };
                const prefix = [...text].every((char) => char === text[0])
                  ? text[0]
                  : text;
                const ordered = [
                  ...visible.slice(activeIndex + 1),
                  ...visible.slice(0, activeIndex + 1),
                ];
                const match = ordered.find((option) =>
                  option.label.toLocaleLowerCase().startsWith(prefix),
                );
                if (match) setActive(match.value);
              }
            }}
          >
            {searchLabel && (
              <div className="selection-picker-search">
                <Search size={16} aria-hidden="true" />
                <input
                  ref={searchRef}
                  role="combobox"
                  aria-label={searchLabel}
                  aria-autocomplete="list"
                  aria-expanded="true"
                  aria-controls={`${id}-list`}
                  aria-activedescendant={activeID}
                  placeholder={searchLabel}
                  autoComplete="off"
                  maxLength={128}
                  spellCheck={false}
                  value={query}
                  onChange={(event) => {
                    setQuery(event.target.value);
                    setActive("");
                  }}
                />
              </div>
            )}
            <div
              ref={listRef}
              className="selection-picker-options"
              id={`${id}-list`}
              role="listbox"
              aria-label={label}
              tabIndex={searchLabel ? -1 : 0}
              aria-activedescendant={searchLabel ? undefined : activeID}
            >
              {visible.map((option, index) => (
                <div
                  key={option.value}
                  id={`${id}-option-${index}`}
                  role="option"
                  aria-label={option.label}
                  aria-describedby={
                    option.description
                      ? `${id}-description-${index}`
                      : undefined
                  }
                  aria-selected={option.value === value}
                  data-active={index === activeIndex}
                  className="selection-picker-option"
                  onPointerMove={() => setActive(option.value)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={(event) => {
                    event.stopPropagation();
                    choose(option.value);
                  }}
                >
                  <span>
                    <span>{option.label}</span>
                    {option.description && (
                      <small id={`${id}-description-${index}`}>
                        {option.description}
                      </small>
                    )}
                  </span>
                  {option.value === value && (
                    <Check size={16} aria-hidden="true" />
                  )}
                </div>
              ))}
            </div>
            {!visible.length && (
              <p className="selection-picker-empty" role="status">
                {tr("No matching options")}
              </p>
            )}
          </div>,
          container,
        )}
    </div>
  );
}
