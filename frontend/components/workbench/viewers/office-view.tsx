"use client";

import { useEffect, useRef, useState } from "react";
import { Download, Loader2 } from "lucide-react";

import { fetchArtifactBlob } from "@/lib/api";

export type OfficeKind = "spreadsheet" | "document" | "presentation";

type Props = {
  sessionId: string;
  path: string;
  href: string;
  kind: OfficeKind;
};

type Phase =
  | { phase: "loading" }
  | { phase: "ready" }
  | { phase: "error"; message: string };

// Read-only renderers for Office documents. Each library is imported lazily
// (only when a file of that kind is actually opened) so the ~3-5 MB of viewer
// code never lands in the initial bundle. All three render imperatively into a
// ref'd container, so we drive them from an effect and tear them down on
// unmount / file switch.
export function OfficeView({ sessionId, path, href, kind }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<Phase>({ phase: "loading" });
  // Bumped by the ResizeObserver (pptx only) to re-render slides at the new
  // pane width. Word reflows on its own (plain DOM), so it ignores this.
  const [sizeTick, setSizeTick] = useState(0);

  // Spreadsheet reflow. x-spreadsheet (inside @js-preview/excel) renders to a
  // fixed-size canvas whose dimensions it only recomputes from the wrapper's
  // clientWidth/Height when it receives a `window` `resize` event (its internal
  // listener calls reload()). Toggling the workbench between docked and full
  // screen resizes this container, NOT the window, so without a nudge the sheet
  // stays locked at whatever size it had on first render (the "only a sliver,
  // not scaled up" bug). We can't call reload() directly — the instance only
  // exposes preview/save/destroy — so we re-emit a window resize when the
  // container's own box changes, which drives the library's existing handler.
  useEffect(() => {
    if (kind !== "spreadsheet") return;
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let last = { w: el.clientWidth, h: el.clientHeight };
    let timer: ReturnType<typeof setTimeout> | null = null;
    const ro = new ResizeObserver(() => {
      const w = el.clientWidth;
      const h = el.clientHeight;
      // Ignore sub-pixel churn; only react to a meaningful box change.
      if (Math.abs(w - last.w) < 8 && Math.abs(h - last.h) < 8) return;
      last = { w, h };
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => window.dispatchEvent(new Event("resize")), 100);
    });
    ro.observe(el);
    return () => {
      if (timer) clearTimeout(timer);
      ro.disconnect();
    };
  }, [kind]);

  // Debounced width observer for the slide renderer, which needs an explicit
  // pixel size. Declared before the render effect so the container is measured
  // by the time we (re)initialise.
  useEffect(() => {
    if (kind !== "presentation") return;
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let last = el.clientWidth;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const ro = new ResizeObserver(() => {
      const next = el.clientWidth;
      // Ignore sub-pixel churn; only re-render on a meaningful width change.
      if (Math.abs(next - last) < 24) return;
      last = next;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => setSizeTick((t) => t + 1), 150);
    });
    ro.observe(el);
    return () => {
      if (timer) clearTimeout(timer);
      ro.disconnect();
    };
  }, [kind]);

  useEffect(() => {
    let cancelled = false;
    // Handle for the imperative viewer instance, so we can destroy() it on
    // cleanup (excel/pptx expose destroy; docx renders plain DOM we just clear).
    let disposer: (() => void) | null = null;
    const container = containerRef.current;

    void (async () => {
      if (!container) return;
      // Set inside the async task (not synchronously in the effect body) so a
      // remount doesn't trigger a cascading render before paint.
      setState({ phase: "loading" });

      const res = await fetchArtifactBlob(sessionId, path);
      if (cancelled) return;
      if (!res.ok) {
        setState({
          phase: "error",
          message: res.status === 413 ? "File is too large to preview." : `Couldn't load file: ${res.error}`
        });
        return;
      }

      try {
        if (kind === "spreadsheet") {
          const { default: jsPreviewExcel } = await import("./office-excel-loader");
          if (cancelled) return;
          container.replaceChildren();
          const instance = jsPreviewExcel.init(container, { minColLength: 20, minRowLength: 40 });
          disposer = () => instance.destroy();
          await instance.preview(res.buffer);
        } else if (kind === "document") {
          const docx = await import("docx-preview");
          if (cancelled) return;
          container.replaceChildren();
          await docx.renderAsync(res.buffer, container, undefined, {
            className: "docx",
            inWrapper: true,
            ignoreWidth: false,
            ignoreHeight: false,
            breakPages: true,
            renderHeaders: true,
            renderFooters: true,
            renderFootnotes: true,
            renderEndnotes: true,
            useBase64URL: true
          });
          disposer = () => container.replaceChildren();
        } else {
          const { init } = await import("pptx-preview");
          if (cancelled) return;
          container.replaceChildren();
          const width = Math.max(320, container.clientWidth);
          const previewer = init(container, {
            width,
            height: Math.round((width * 9) / 16),
            mode: "slide"
          });
          disposer = () => previewer.destroy();
          await previewer.preview(res.buffer);
        }
        if (cancelled) {
          disposer?.();
          disposer = null;
          return;
        }
        setState({ phase: "ready" });
      } catch (err) {
        if (cancelled) return;
        // Tear down the partially-initialised viewer before showing the error
        // overlay. x-spreadsheet's init() injects positioned layers (editor,
        // selector, toolbar) with z-index up to 1001 and pointer-events:auto;
        // left in place they paint over the overlay and swallow clicks, so the
        // Download fallback becomes unselectable/unclickable. Clearing the
        // container removes that leftover DOM entirely.
        try {
          disposer?.();
        } catch {
          // ignore teardown failures — we're already in the error path
        }
        disposer = null;
        container.replaceChildren();
        // The underlying viewers throw parser-internal messages (sometimes
        // non-English, e.g. x-spreadsheet's "未获取到数据…"). Surface a single,
        // clear, app-consistent line and let the user fall back to Download.
        setState({
          phase: "error",
          message: "This file couldn't be previewed. It may use features the viewer doesn't support."
        });
      }
    })();

    return () => {
      cancelled = true;
      disposer?.();
      if (container) container.replaceChildren();
    };
  }, [sessionId, path, kind, sizeTick]);

  const name = path.split("/").pop() ?? path;

  return (
    <div className="office-view relative h-full min-h-0 w-full overflow-auto bg-[var(--color-surface-muted)]">
      <div
        ref={containerRef}
        data-office-kind={kind}
        className={
          kind === "spreadsheet"
            ? "h-full w-full"
            : "flex min-h-full w-full justify-center px-4 py-6"
        }
      />
      {state.phase === "loading" ? (
        <div className="pointer-events-none absolute inset-0 z-[1100] flex items-center justify-center gap-2 bg-[var(--color-surface-muted)] text-sm text-[var(--color-foreground-muted)]">
          <Loader2 className="h-4 w-4 animate-spin" /> Rendering preview…
        </div>
      ) : null}
      {state.phase === "error" ? (
        <div className="absolute inset-0 z-[1100] flex items-center justify-center bg-[var(--color-surface-muted)] px-6 text-center">
          <div>
            <p className="text-sm">{state.message}</p>
            <a
              href={href}
              download
              className="mt-3 inline-flex items-center gap-1.5 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-xs text-[var(--color-foreground)] hover:bg-[var(--color-surface-muted)]"
            >
              <Download className="h-3.5 w-3.5" /> Download {name}
            </a>
          </div>
        </div>
      ) : null}
    </div>
  );
}
