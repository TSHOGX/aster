"use client";
import {
  useCallback,
  useLayoutEffect,
  useRef,
  type TextareaHTMLAttributes,
} from "react";

/** Shared composer input for drafts and existing conversations. */
export function ComposerInput({
  value,
  onSend,
  ...props
}: Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "value" | "onKeyDown"> & {
  value: string;
  onSend: () => void;
}) {
  const input = useRef<HTMLTextAreaElement>(null),
    host = useRef<HTMLDivElement>(null),
    composing = useRef(false);
  const syncTextFade = useCallback(() => {
    const el = input.current,
      wrapper = host.current;
    if (!el || !wrapper) return;
    // Fade only actual overflowing text. Empty values/placeholder wrapping and
    // subpixel scroll-height rounding must never leave an edge decoration.
    const overflowing =
      el.value.trim().length > 0 && el.scrollHeight - el.clientHeight > 1;
    const top = overflowing ? Math.min(20, Math.max(0, el.scrollTop)) : 0;
    const bottom = overflowing
      ? Math.min(
          20,
          Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop),
        )
      : 0;
    wrapper.dataset.scrollTop = String(top > 1);
    wrapper.dataset.scrollBottom = String(bottom > 1);
    el.style.setProperty("--text-fade-top", `${top > 1 ? top : 0}px`);
    el.style.setProperty("--text-fade-bottom", `${bottom > 1 ? bottom : 0}px`);
  }, []);
  const fit = useCallback(() => {
    const el = input.current;
    if (!el || el.clientWidth === 0) return;
    const top = el.scrollTop;
    const styles = getComputedStyle(el);
    const min = parseFloat(styles.minHeight) || 58,
      max = parseFloat(styles.maxHeight) || 280;
    el.style.height = "0px";
    el.style.height = `${Math.max(min, Math.min(max, el.scrollHeight))}px`;
    el.scrollTop = top;
    if (!el.value) {
      el.scrollTop = 0;
      el.style.height = `${min}px`;
    }
    el.style.overflowY = el.value ? "auto" : "hidden";
    syncTextFade();
  }, [syncTextFade]);
  useLayoutEffect(() => {
    fit();
  }, [value, fit]);
  useLayoutEffect(() => {
    const el = host.current;
    if (!el) return;
    let width = -1;
    const observer = new ResizeObserver(() => {
      if (el.clientWidth !== width) {
        width = el.clientWidth;
        fit();
      }
    });
    observer.observe(el);
    window.addEventListener("resize", fit);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", fit);
    };
  }, [fit]);
  return (
    <div className="composer-input-scroll" ref={host}>
      <textarea
        {...props}
        ref={input}
        value={value}
        rows={2}
        onScroll={syncTextFade}
        onCompositionStart={() => {
          composing.current = true;
        }}
        onCompositionEnd={() => {
          composing.current = false;
        }}
        onKeyDown={(e) => {
          if (
            e.key === "Enter" &&
            !e.shiftKey &&
            !e.altKey &&
            !composing.current &&
            !e.nativeEvent.isComposing &&
            e.nativeEvent.keyCode !== 229
          ) {
            e.preventDefault();
            if (!e.repeat) onSend();
          }
        }}
      />
    </div>
  );
}
