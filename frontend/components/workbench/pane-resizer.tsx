"use client";

import { useEffect, useState } from "react";

import { cn } from "@/lib/cn";

/**
 * Drag handle for the right workspace panel's left edge.
 *
 * The hit area is deliberately wider than the visible line (a 9px strip against
 * a 1px rule): a 1px target is a mouse-hunting exercise, so the strip is
 * invisible until hovered and then paints a 2px accent bar in place.
 *
 * Width flows one way — this reports deltas, the parent owns the number. Pointer
 * capture keeps the drag alive when the cursor outruns the strip, which it will
 * on any fast drag.
 */
export function PaneResizer({
  onResize,
  onNudge,
  onReset,
  onDragStateChange,
  ariaLabel = "Resize panel",
  hint,
  width,
  min,
  max
}: {
  // Called with the pointer's viewport x while dragging; the parent converts it
  // to a width. Passing the raw coordinate (rather than a delta) keeps the
  // handle from accumulating rounding drift over a long drag.
  onResize: (clientX: number) => void;
  // Keyboard resizing works in relative steps instead, so it gets its own
  // channel — feeding a delta into onResize would read as a viewport coordinate.
  onNudge: (deltaPx: number) => void;
  onReset?: () => void;
  // Lets the parent drop width transitions for the duration of the drag.
  onDragStateChange?: (dragging: boolean) => void;
  /** Tooltip text; supplied translated by the parent. */
  hint?: string;
  ariaLabel?: string;
  width: number;
  min: number;
  max: number;
}) {
  const [dragging, setDragging] = useState(false);
  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    // Ignore secondary buttons: a right-click drag should open a context menu.
    if (e.button !== 0) return;
    e.preventDefault();
    setDragging(true);
    onDragStateChange?.(true);
  };

  // Listeners live on the window for the duration of the drag so the pointer can
  // leave the handle (and the panel) without stalling it. `onResize` is a real
  // dependency here; the parent memoizes it, so this subscribes once per drag.
  useEffect(() => {
    if (!dragging) return;
    const onMove = (e: PointerEvent) => onResize(e.clientX);
    const stop = () => {
      setDragging(false);
      onDragStateChange?.(false);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", stop);
    window.addEventListener("pointercancel", stop);
    // Suppress text selection and flickering cursors across the whole document
    // while the drag is live; both make a resize feel broken.
    const prevUserSelect = document.body.style.userSelect;
    const prevCursor = document.body.style.cursor;
    document.body.style.userSelect = "none";
    document.body.style.cursor = "col-resize";
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", stop);
      window.removeEventListener("pointercancel", stop);
      document.body.style.userSelect = prevUserSelect;
      document.body.style.cursor = prevCursor;
    };
  }, [dragging, onResize, onDragStateChange]);

  // Keyboard resizing: the separator is focusable, so arrows nudge it and Home
  // restores the default. Without this the panel width is mouse-only.
  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 48 : 16;
    if (e.key === "ArrowLeft") {
      e.preventDefault();
      // The handle is on the panel's left edge, so dragging it left *widens* the
      // panel — ArrowLeft therefore adds width.
      onNudge(step);
    } else if (e.key === "ArrowRight") {
      e.preventDefault();
      onNudge(-step);
    } else if (e.key === "Home" && onReset) {
      e.preventDefault();
      onReset();
    }
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={ariaLabel}
      aria-valuenow={Math.round(width)}
      aria-valuemin={min}
      aria-valuemax={max}
      tabIndex={0}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      onDoubleClick={onReset}
      title={hint}
      className={cn(
        "group absolute inset-y-0 left-0 z-30 flex w-[9px] -translate-x-1/2 cursor-col-resize touch-none items-stretch justify-center",
        "focus-visible:outline-none"
      )}
    >
      <span
        className={cn(
          "my-0 w-[2px] rounded-full transition-colors duration-150",
          dragging
            ? "bg-[var(--color-accent)]"
            : "bg-transparent group-hover:bg-[var(--color-accent)] group-focus-visible:bg-[var(--color-accent)]"
        )}
      />
    </div>
  );
}
