import { useEffect, useState, type RefObject } from "react";

// Close an open popover on an outside mousedown (and, optionally, on Escape).
// `refs` are the elements considered "inside" — a mousedown landing on any of
// them is ignored. Mirrors the effect the notification bell, message-actions
// menu, and sidebar row menu each wrote inline: the two-ref variants pass
// [trigger, panel]; the sidebar passes [menu] and opts into Escape.
export function useDismissOnOutside(
  open: boolean,
  refs: Array<RefObject<HTMLElement | null>>,
  onDismiss: () => void,
  options?: { escape?: boolean }
) {
  const escape = options?.escape ?? false;
  useEffect(() => {
    if (!open) return;
    const handleMouseDown = (event: MouseEvent) => {
      const target = event.target as Node;
      for (const ref of refs) {
        if (ref.current?.contains(target)) return;
      }
      onDismiss();
    };
    document.addEventListener("mousedown", handleMouseDown);
    let handleKey: ((e: KeyboardEvent) => void) | undefined;
    if (escape) {
      handleKey = (e: KeyboardEvent) => {
        if (e.key === "Escape") onDismiss();
      };
      document.addEventListener("keydown", handleKey);
    }
    return () => {
      document.removeEventListener("mousedown", handleMouseDown);
      if (handleKey) document.removeEventListener("keydown", handleKey);
    };
    // refs is a fresh array literal each render; callers pass stable ref objects
    // so only `open` meaningfully gates (re)subscription, as in the originals.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, escape, onDismiss]);
}

export type AnchorPosition = { top: number; left: number };

// Anchor a fixed-position (portalled) panel of known `width` under `anchorRef`,
// tracking resize/scroll while `open`. `align` picks which edge lines up with
// the trigger: "left" aligns the panel's left to the trigger's left (the
// notification bell); "right" aligns the panel's right to the trigger's right
// (the message-actions menu). `gap` is the vertical offset below the trigger
// and `margin` the minimum viewport inset. Returns null until measured.
export function useAnchoredPosition(
  open: boolean,
  anchorRef: RefObject<HTMLElement | null>,
  opts: { width: number; gap: number; margin: number; align: "left" | "right" }
): AnchorPosition | null {
  const { width, gap, margin, align } = opts;
  const [pos, setPos] = useState<AnchorPosition | null>(null);
  useEffect(() => {
    if (!open) return;
    const updatePosition = () => {
      const rect = anchorRef.current?.getBoundingClientRect();
      if (!rect) return;
      const anchored = align === "left" ? rect.left : rect.right - width;
      const left = Math.min(anchored, window.innerWidth - width - margin);
      setPos({ top: rect.bottom + gap, left: Math.max(left, margin) });
    };
    updatePosition();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, width, gap, margin, align]);
  return pos;
}
