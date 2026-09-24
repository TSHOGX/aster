"use client";

import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Check, Clock3 } from "lucide-react";
import { useT } from "@/lib/i18n";
import { usePickerPopover } from "./picker-popover";

function normalizedTime(value: string) {
  const match =
    /^(\d{1,2}):(\d{1,2})$/.exec(value) || /^(\d{1,2})(\d{2})$/.exec(value);
  return match && +match[1] < 24 && +match[2] < 60
    ? `${match[1].padStart(2, "0")}:${match[2].padStart(2, "0")}`
    : value;
}

export function TimePicker({
  value,
  onChange,
  disabled = false,
  describedBy,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  describedBy?: string;
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
  } = usePickerPopover(320, 280);
  const [draft, setDraft] = useState(value);
  const hourRef = useRef<HTMLDivElement>(null);
  const [hour, minute] = draft.split(":");
  const valid = /^([01]\d|2[0-3]):[0-5]\d$/.test(value);
  const open = () => {
    setDraft(valid ? value : "09:00");
    openPopover();
  };
  useEffect(() => {
    if (isOpen) hourRef.current?.focus({ preventScroll: true });
  }, [isOpen]);
  useEffect(() => {
    if (!isOpen) return;
    for (const part of ["hour", "minute"]) {
      const option = document.getElementById(
        `${id}-${part}-${part === "hour" ? hour : minute}`,
      );
      const list = option?.parentElement;
      if (option && list)
        list.scrollTop +=
          option.getBoundingClientRect().top -
          list.getBoundingClientRect().top -
          (list.clientHeight - option.clientHeight) / 2;
    }
  }, [isOpen, id, hour, minute]);
  const confirm = () => {
    close(true);
    if (draft !== value) onChange(draft);
  };
  return (
    <div className="selection-picker time-picker">
      <label htmlFor={`${id}-input`}>{tr("Time")}</label>
      <div className="time-picker-field">
        <input
          id={`${id}-input`}
          aria-label={tr("Time")}
          type="text"
          inputMode="numeric"
          autoComplete="off"
          placeholder="09:00"
          maxLength={5}
          required
          pattern="([01][0-9]|2[0-3]):[0-5][0-9]"
          aria-invalid={!valid || undefined}
          aria-describedby={describedBy}
          disabled={disabled}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          onBlur={(event) => {
            const next = normalizedTime(event.target.value);
            if (next !== value) onChange(next);
          }}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown" && event.altKey) {
              event.preventDefault();
              open();
            }
          }}
        />
        <button
          ref={triggerRef}
          className="time-picker-toggle"
          type="button"
          aria-label={tr("Choose time")}
          aria-haspopup="dialog"
          aria-expanded={isOpen}
          aria-controls={isOpen ? `${id}-dialog` : undefined}
          disabled={disabled}
          onClick={() => (isOpen ? close() : open())}
        >
          <Clock3 size={18} aria-hidden="true" />
        </button>
      </div>
      {position &&
        container &&
        !disabled &&
        createPortal(
          <div
            ref={panelRef}
            className="selection-picker-popover time-picker-popover"
            style={position}
            role="dialog"
            aria-label={tr("Choose time")}
            id={`${id}-dialog`}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                close(true);
              }
              if (
                event.key === "Tab" &&
                event.shiftKey &&
                event.target === hourRef.current
              ) {
                if (tabFromTrigger(true)) event.preventDefault();
                event.stopPropagation();
              }
            }}
          >
            <div className="time-picker-heading">
              <Clock3 size={16} aria-hidden="true" />
              <span>{draft}</span>
              <small>{tr("24-hour time")}</small>
            </div>
            <div className="time-picker-columns">
              {(["hour", "minute"] as const).map((part) => {
                const selected = part === "hour" ? hour : minute,
                  limit = part === "hour" ? 24 : 60;
                const change = (number: string) =>
                  setDraft(
                    part === "hour"
                      ? `${number}:${minute}`
                      : `${hour}:${number}`,
                  );
                return (
                  <div className="time-picker-column" key={part}>
                    <span id={`${id}-${part}-label`}>
                      {part === "hour" ? tr("Hour") : tr("Minute")}
                    </span>
                    <div
                      role="listbox"
                      tabIndex={0}
                      ref={part === "hour" ? hourRef : undefined}
                      aria-labelledby={`${id}-${part}-label`}
                      aria-activedescendant={`${id}-${part}-${selected}`}
                      className="time-picker-values"
                      onKeyDown={(event) => {
                        if (
                          ["ArrowDown", "ArrowUp", "Home", "End"].includes(
                            event.key,
                          )
                        ) {
                          event.preventDefault();
                          event.stopPropagation();
                          const next =
                            event.key === "Home"
                              ? 0
                              : event.key === "End"
                                ? limit - 1
                                : (+selected +
                                    (event.key === "ArrowDown" ? 1 : -1) +
                                    limit) %
                                  limit;
                          change(String(next).padStart(2, "0"));
                        } else if (event.key === "Enter") {
                          event.preventDefault();
                          event.stopPropagation();
                          confirm();
                        }
                      }}
                    >
                      {Array.from({ length: limit }, (_, n) =>
                        String(n).padStart(2, "0"),
                      ).map((number) => (
                        <div
                          key={number}
                          id={`${id}-${part}-${number}`}
                          role="option"
                          aria-label={number}
                          aria-selected={number === selected}
                          onMouseDown={(event) => event.preventDefault()}
                          onClick={(event) => {
                            event.currentTarget.parentElement?.focus({
                              preventScroll: true,
                            });
                            change(number);
                          }}
                        >
                          <span>{number}</span>
                          {number === selected && (
                            <Check size={14} aria-hidden="true" />
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
            <footer>
              <button type="button" onClick={() => close(true)}>
                {tr("Cancel")}
              </button>
              <button
                type="button"
                className="picker-confirm"
                onKeyDown={(event) => {
                  if (event.key === "Tab" && !event.shiftKey) {
                    if (tabFromTrigger(false)) event.preventDefault();
                    event.stopPropagation();
                  }
                }}
                onClick={confirm}
              >
                {tr("Done")}
              </button>
            </footer>
          </div>,
          container,
        )}
    </div>
  );
}
