"use client";

import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { usePickerPopover } from "./picker-popover";

export type PickerOption = {
  value: string;
  label: string;
  description?: string;
  searchText?: string;
  disabled?: boolean;
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
  showLabel = true,
  emptyLabel = "No matching options",
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
  showLabel?: boolean;
  emptyLabel?: string;
}) {
  const id = useId();
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
  const enabledIndices = visible.flatMap((option, index) =>
    option.disabled ? [] : [index],
  );
  const requestedIndex = visible.findIndex((option) => option.value === active);
  const activeIndex = enabledIndices.includes(requestedIndex)
    ? requestedIndex
    : (enabledIndices[0] ?? -1);
  const title =
    options.find((option) => option.value === value)?.label ||
    value ||
    placeholder ||
    "Choose an option";
  const activeID = activeIndex >= 0 ? `${id}-option-${activeIndex}` : undefined;
  const open = (edge?: "first" | "last") => {
    if (triggerRef.current?.matches(":disabled")) return;
    setQuery("");
    const selectable = options.filter((option) => !option.disabled);
    setActive(
      edge === "first"
        ? selectable[0]?.value || ""
        : edge === "last"
          ? selectable.at(-1)?.value || ""
          : value,
    );
    typeahead.current = { text: "", at: 0 };
    openPopover();
  };
  const choose = (next: string) => {
    if (
      triggerRef.current?.matches(":disabled") ||
      options.find((option) => option.value === next)?.disabled
    )
      return;
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
      <span
        className={
          showLabel
            ? "selection-picker-label"
            : "selection-picker-label selection-picker-label--hidden"
        }
        id={`${id}-label`}
      >
        {label}
      </span>
      <button
        ref={triggerRef}
        type="button"
        role="combobox"
        data-value={value}
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
            open(
              event.key === "Home"
                ? "first"
                : event.key === "End"
                  ? "last"
                  : event.key === "ArrowUp" &&
                      !options.some(
                        (option) => option.value === value && !option.disabled,
                      )
                    ? "last"
                    : undefined,
            );
          }
        }}
      >
        {icon}
        <span id={`${id}-value`}>{title}</span>
        <svg
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          aria-hidden="true"
        >
          <path d="m7 10 5 5 5-5" />
        </svg>
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
                const at = enabledIndices.indexOf(activeIndex);
                const next =
                  event.key === "Home"
                    ? enabledIndices[0]
                    : event.key === "End"
                      ? enabledIndices.at(-1)
                      : enabledIndices[
                          (at +
                            (event.key === "ArrowDown" ? 1 : -1) +
                            enabledIndices.length) %
                            enabledIndices.length
                        ];
                if (next !== undefined) setActive(visible[next].value);
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
                const match = ordered.find(
                  (option) =>
                    !option.disabled &&
                    option.label.toLocaleLowerCase().startsWith(prefix),
                );
                if (match) setActive(match.value);
              }
            }}
          >
            {searchLabel && (
              <div className="selection-picker-search">
                <svg
                  width="16"
                  height="16"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.6"
                  aria-hidden="true"
                >
                  <circle cx="10" cy="10" r="6" />
                  <path d="m15 15 5 5" />
                </svg>
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
                  aria-disabled={option.disabled || undefined}
                  data-value={option.value}
                  data-active={index === activeIndex}
                  className="selection-picker-option"
                  onPointerMove={() => {
                    if (!option.disabled) setActive(option.value);
                  }}
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
                    <svg
                      width="16"
                      height="16"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.8"
                      aria-hidden="true"
                    >
                      <path d="m5 12 4 4 10-10" />
                    </svg>
                  )}
                </div>
              ))}
            </div>
            {!visible.length && (
              <p className="selection-picker-empty" role="status">
                {emptyLabel}
              </p>
            )}
          </div>,
          container,
        )}
    </div>
  );
}
