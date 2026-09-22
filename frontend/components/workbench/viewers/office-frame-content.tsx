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
import { setTheme, THEME_STORAGE_KEY } from "@/lib/theme";

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
    let dispose = () => {},
      resize = () => {};
    const send = (kind: string, extra = {}) =>
      parent.postMessage(
        { channel: OFFICE_CHANNEL, kind, ...extra },
        location.origin,
      );
    const stage = document.createElement("div");
    stage.style.height = "100%";
    content.appendChild(stage);
    const render = async (buffer: ArrayBuffer, format: OfficeKind) => {
      let drawingsUnavailable = false;
      try {
        if (format === "spreadsheet") {
          const { default: renderer } = await import("./office-excel-loader");
          if (gone) return;
          const options = { minColLength: 10, minRowLength: 20, xls: false };
          const view = renderer.init(stage, options);
          const url = URL.createObjectURL(new Blob([buffer]));
          dispose = () => {
            view.destroy();
            URL.revokeObjectURL(url);
          };
          try {
            await view.preview(url);
          } catch (error) {
            if (
              gone ||
              !(error instanceof Error) ||
              !error.message.includes("anchors")
            )
              throw error;
            const converted = { ...options, xls: true };
            view.setOptions(converted);
            await view.preview(url);
            drawingsUnavailable = true;
          }
        } else if (format === "document") {
          const renderer = await import("docx-preview");
          if (gone) return;
          await renderer.renderAsync(buffer, stage, undefined, {
            useBase64URL: true,
            renderAltChunks: false,
          });
        } else {
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
            content.dataset.slide = String(next);
            setSlides({ index: next, count: view.slideCount });
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
        if (!gone) send("ready", { drawingsUnavailable });
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
          !["spreadsheet", "document", "presentation"].includes(format)
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
      }
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) send("escape");
    };
    const resized = () => resize();
    const theme = (event: StorageEvent) => {
      if (
        event.key === THEME_STORAGE_KEY &&
        (event.newValue === "light" || event.newValue === "dark")
      )
        setTheme(event.newValue);
    };
    window.addEventListener("message", message);
    window.addEventListener("keydown", escape);
    window.addEventListener("resize", resized);
    window.addEventListener("storage", theme);
    send("boot");
    return () => {
      gone = true;
      window.removeEventListener("message", message);
      window.removeEventListener("keydown", escape);
      window.removeEventListener("resize", resized);
      window.removeEventListener("storage", theme);
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
