"use client";
import { useEffect, useRef, useState } from "react";
import {
  OFFICE_CHANNEL,
  type OfficeKind,
  type OfficePosition,
} from "@/lib/office-frame";
import { PREVIEW_LIMITS } from "@/lib/preview-budget";
import { useT } from "@/lib/i18n";
import styles from "./office-frame-content.module.css";

/** No authority or file fetching lives here; the admitted parent transfers bytes. */
export function OfficeFrameContent() {
  const tr = useT();
  const host = useRef<HTMLDivElement>(null);
  const [slides, setSlides] = useState({ index: 0, count: 0 });
  const navigate = useRef<(index: number) => void>(() => {});
  useEffect(() => {
    const content = host.current;
    if (!content || parent === window) return;
    let gone = false,
      started = false;
    let currentSlide = 0;
    let dispose = () => {},
      resize = () => {};
    const send = (kind: string, extra = {}) =>
      parent.postMessage({ channel: OFFICE_CHANNEL, kind, ...extra }, "*");
    const stage = document.createElement("div");
    stage.style.height = "100%";
    content.appendChild(stage);
    const render = async (buffer: ArrayBuffer, format: OfficeKind) => {
      try {
        if (format === "presentation") {
          const { init } = await import("pptx-preview");
          if (gone) return;
          const view = init(stage, { width: 720, height: 405, mode: "slide" });
          dispose = () => view.destroy();
          await view.load(buffer);
          if (gone) return;
          const height = view.htmlRender.renderPort.height;
          if (!Number.isFinite(height) || height < 240 || height > 2160)
            throw new Error("Unsupported slide aspect ratio");
          view.htmlRender.options.viewPort!.height = height;
          view.wrapper.style.height = `${height}px`;
          navigate.current = (index) => {
            const next = Math.max(0, Math.min(view.slideCount - 1, index));
            view.renderSingleSlide(next);
            currentSlide = next;
            content.dataset.slide = String(next);
            setSlides({ index: next, count: view.slideCount });
            send("position", {
              position: {
                top: content.scrollTop,
                left: content.scrollLeft,
                slide: next,
              },
            });
          };
          navigate.current(0);
          resize = () => {
            view.wrapper.style.zoom = String(
              Math.max(1, content.clientWidth) / 720,
            );
          };
          resize();
        }
        // Chart initialization is deferred by the pinned library. Do not report
        // readiness or retire the previous frame before that task has run.
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        if (!gone) send("ready");
      } catch {
        if (!gone) send("failed");
      }
    };
    const message = (event: MessageEvent) => {
      if (
        event.source !== parent ||
        event.origin !== location.origin ||
        event.data?.channel !== OFFICE_CHANNEL
      )
        return;
      if (event.data.kind === "render" && !started) {
        const { buffer, format } = event.data as {
          buffer: ArrayBuffer;
          format: OfficeKind;
        };
        if (
          !(buffer instanceof ArrayBuffer) ||
          buffer.byteLength > PREVIEW_LIMITS.blobBytes ||
          format !== "presentation"
        )
          return;
        started = true;
        void render(buffer, format);
      } else if (event.data.kind === "restore") {
        const position = event.data.position as OfficePosition;
        if (
          !position ||
          ![position.top, position.left, position.slide].every(Number.isFinite)
        )
          return;
        content.scrollTop = position.top;
        content.scrollLeft = position.left;
        if (content.dataset.slide !== undefined)
          navigate.current(position.slide);
        if (position.focus)
          requestAnimationFrame(() => {
            if (gone) return;
            const target = document.querySelector<HTMLElement>(
              `[data-preview-focus="${CSS.escape(position.focus!)}"]`,
            );
            if (target && !target.matches(":disabled"))
              target.focus({ preventScroll: true });
            else content.focus({ preventScroll: true });
          });
      } else if (
        event.data.kind === "theme" &&
        (event.data.theme === "dark" || event.data.theme === "light")
      ) {
        document.documentElement.dataset.theme = event.data.theme;
      }
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) send("escape");
      if (
        (event.key === "ArrowRight" || event.key === "ArrowLeft") &&
        !(event.target instanceof HTMLInputElement) &&
        content.dataset.slide !== undefined
      ) {
        event.preventDefault();
        navigate.current(currentSlide + (event.key === "ArrowRight" ? 1 : -1));
      }
    };
    const resized = () => resize();
    const position = () =>
      send("position", {
        position: {
          top: content.scrollTop,
          left: content.scrollLeft,
          slide: currentSlide,
          focus:
            document.activeElement?.getAttribute("data-preview-focus") ||
            undefined,
        },
      });
    window.addEventListener("message", message);
    window.addEventListener("keydown", escape);
    window.addEventListener("resize", resized);
    content.addEventListener("scroll", position, { passive: true });
    window.addEventListener("focusin", position);
    send("boot");
    return () => {
      gone = true;
      window.removeEventListener("message", message);
      window.removeEventListener("keydown", escape);
      window.removeEventListener("resize", resized);
      content.removeEventListener("scroll", position);
      window.removeEventListener("focusin", position);
      navigate.current = () => {};
      dispose();
      stage.remove();
    };
  }, []);
  return (
    <div className={styles.shell}>
      {slides.count > 0 && (
        <nav aria-label={tr("Slides")} className={styles.navigation}>
          <button
            data-preview-focus="previous"
            disabled={slides.index === 0}
            onClick={() => navigate.current(slides.index - 1)}
          >
            {tr("Previous slide")}
          </button>
          <span aria-live="polite">
            {slides.index + 1} / {slides.count}
          </span>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const value = Number(
                new FormData(event.currentTarget).get("slide"),
              );
              if (
                Number.isSafeInteger(value) &&
                value >= 1 &&
                value <= slides.count
              )
                navigate.current(value - 1);
            }}
          >
            <label htmlFor="office-slide-number">{tr("Slide number")}</label>
            <input
              key={slides.index}
              id="office-slide-number"
              name="slide"
              type="number"
              min={1}
              max={slides.count}
              defaultValue={slides.index + 1}
              onKeyDown={(event) => {
                if (event.key !== "Enter") return;
                event.preventDefault();
                const value = Number(event.currentTarget.value);
                if (
                  Number.isSafeInteger(value) &&
                  value >= 1 &&
                  value <= slides.count
                )
                  navigate.current(value - 1);
              }}
            />
            <button
              type="button"
              onClick={(event) => {
                const form = event.currentTarget.form;
                if (!form) return;
                const value = Number(new FormData(form).get("slide"));
                if (
                  Number.isSafeInteger(value) &&
                  value >= 1 &&
                  value <= slides.count
                )
                  navigate.current(value - 1);
              }}
            >
              {tr("Go to slide")}
            </button>
          </form>
          <button
            data-preview-focus="next"
            disabled={slides.index + 1 >= slides.count}
            onClick={() => navigate.current(slides.index + 1)}
          >
            {tr("Next slide")}
          </button>
        </nav>
      )}
      <div
        className={`office-frame-content ${styles.content}`}
        data-preview-focus="content"
        tabIndex={-1}
        ref={host}
      />
    </div>
  );
}
