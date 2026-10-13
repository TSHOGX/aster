"use client";

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";

// Popovers escape scrolling panes and stay within the visual viewport, including
// the reduced viewport above a mobile keyboard. A containing dialog stays on top.
export function usePickerPopover(maxWidth = 420, minWidth = 220) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<CSSProperties | null>(null);
  const [container, setContainer] = useState<HTMLElement | null>(null);
  const measure = useCallback((): CSSProperties | null => {
    if (!triggerRef.current) return null;
    const rect = triggerRef.current.getBoundingClientRect();
    const viewport = window.visualViewport;
    const top = viewport?.offsetTop || 0,
      left = viewport?.offsetLeft || 0;
    const height = viewport?.height || window.innerHeight;
    const availableWidth = viewport?.width || window.innerWidth;
    const width = Math.min(
      Math.max(rect.width, minWidth),
      maxWidth,
      availableWidth - 24,
    );
    const upperEdge = Math.min(rect.top - 6, top + height - 12);
    const lowerEdge = Math.max(rect.bottom + 6, top + 12);
    const below = top + height - 12 - lowerEdge,
      above = upperEdge - top - 12;
    const upwards = below < 280 && above > below;
    return {
      width,
      left: Math.max(
        left + 12,
        Math.min(rect.left, left + availableWidth - width - 12),
      ),
      ...(upwards
        ? { bottom: window.innerHeight - upperEdge }
        : { top: lowerEdge }),
      maxHeight: Math.max(0, Math.min(360, upwards ? above : below)),
    };
  }, [maxWidth, minWidth]);
  const close = useCallback((restoreFocus = false) => {
    setPosition(null);
    if (restoreFocus) triggerRef.current?.focus({ preventScroll: true });
  }, []);
  const open = useCallback(() => {
    if (triggerRef.current?.matches(":disabled")) return;
    setContainer(triggerRef.current?.closest<HTMLElement>("dialog, [data-picker-container]") || document.body);
    setPosition(measure());
  }, [measure]);
  const tabFromTrigger = useCallback(
    (backwards: boolean) => {
      const trigger = triggerRef.current;
      if (!trigger) return false;
      const root = trigger.closest<HTMLElement>("dialog, [data-picker-container]") || document.body;
      const controls = [
        ...root.querySelectorAll<HTMLElement>(
          'a[href],button,input:not([type="hidden"]),textarea,select,[tabindex]',
        ),
      ].filter(
        (node) =>
          node.tabIndex >= 0 &&
          !node.matches(":disabled") &&
          !node.closest("[inert]") &&
          node.getClientRects().length > 0 &&
          !panelRef.current?.contains(node),
      );
      const next = controls[controls.indexOf(trigger) + (backwards ? -1 : 1)];
      close();
      (next || trigger).focus();
      return !!next;
    },
    [close],
  );
  const isOpen = !!position;
  useEffect(() => {
    if (!isOpen) return;
    const outside = (event: Event) => {
      if (
        event.target instanceof Node &&
        !panelRef.current?.contains(event.target) &&
        !triggerRef.current?.contains(event.target)
      )
        close();
    };
    const scroll = (event: Event) => {
      if (
        event.target instanceof Node &&
        panelRef.current?.contains(event.target)
      )
        return;
      // Opening a control can itself finish a pending scroll into view. Keep
      // the popup attached to its anchor instead of immediately dismissing it.
      const rect = triggerRef.current?.getBoundingClientRect();
      const top = window.visualViewport?.offsetTop || 0;
      const bottom =
        top + (window.visualViewport?.height || window.innerHeight);
      if (
        !rect ||
        ((rect.bottom < top || rect.top > bottom) &&
          !panelRef.current?.contains(document.activeElement))
      )
        close();
      else setPosition(measure());
    };
    const reposition = () => setPosition(measure());
    const disabled = new MutationObserver(() => {
      if (triggerRef.current?.matches(":disabled")) close();
    });
    const trigger = triggerRef.current;
    if (trigger) {
      disabled.observe(trigger, {
        attributes: true,
        attributeFilter: ["disabled"],
      });
      let ancestor = trigger.parentElement;
      while (ancestor) {
        if (ancestor.tagName === "FIELDSET")
          disabled.observe(ancestor, {
            attributes: true,
            attributeFilter: ["disabled"],
          });
        ancestor = ancestor.parentElement;
      }
    }
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    document.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", reposition);
    window.visualViewport?.addEventListener("resize", reposition);
    window.visualViewport?.addEventListener("scroll", reposition);
    return () => {
      disabled.disconnect();
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      document.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", reposition);
      window.visualViewport?.removeEventListener("resize", reposition);
      window.visualViewport?.removeEventListener("scroll", reposition);
    };
  }, [isOpen, close, measure]);
  return {
    triggerRef,
    panelRef,
    position,
    container,
    isOpen,
    open,
    close,
    tabFromTrigger,
  };
}
