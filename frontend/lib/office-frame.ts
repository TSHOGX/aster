import { PreviewFailure, reservePreview } from "./preview-budget";
import type { PreviewRepresentation } from "./preview-representation";

export const OFFICE_CHANNEL = "battery-office-preview-v1";
export type OfficeKind = "presentation";
export type OfficePosition = {
  top: number;
  left: number;
  slide: number;
  focus?: string;
};
export type OfficeFrame = {
  ready: Promise<void>;
  position: () => OfficePosition;
  activate: (previous?: OfficeFrame) => void;
  dispose: () => void;
};

/** Each third-party renderer gets its own module state and browser document. */
export function prepareOfficeFrame(
  host: HTMLElement,
  representation: PreviewRepresentation,
  kind: OfficeKind,
  signal: AbortSignal,
): OfficeFrame {
  signal.throwIfAborted();
  const releaseFrame = reservePreview(0, "frames");
  const frame = document.createElement("iframe");
  frame.src = "/preview/office";
  frame.setAttribute("sandbox", "allow-scripts");
  frame.title = representation.path;
  frame.className = "office-frame";
  frame.style.cssText =
    "position:absolute;inset:0;width:100%;height:100%;border:0;visibility:hidden";
  frame.tabIndex = -1;
  frame.setAttribute("aria-hidden", "true");
  const theme = () =>
    frame.contentWindow?.postMessage(
      {
        channel: OFFICE_CHANNEL,
        kind: "theme",
        theme:
          document.documentElement.dataset.theme === "dark" ? "dark" : "light",
      },
      "*",
    );
  const themeObserver = new MutationObserver(theme);
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"],
  });
  let disposed = false,
    sent = false,
    settled = false;
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const ready = new Promise<void>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  const finish = (_value?: void, error?: unknown) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    if (error) reject(error);
    else resolve();
  };
  const dispose = () => {
    if (disposed) return;
    disposed = true;
    signal.removeEventListener("abort", abort);
    window.removeEventListener("message", message);
    themeObserver.disconnect();
    frame.remove();
    releaseFrame();
    finish(
      undefined,
      signal.reason ?? new DOMException("Preview closed", "AbortError"),
    );
  };
  const abort = () => dispose();
  const timer = setTimeout(() => {
    finish(
      undefined,
      new PreviewFailure("parse_timeout", "render", { retryable: true }),
    );
    dispose();
  }, 15_000);
  const message = (event: MessageEvent) => {
    if (
      disposed ||
      event.source !== frame.contentWindow ||
      event.origin !== "null" ||
      event.data?.channel !== OFFICE_CHANNEL
    )
      return;
    if (event.data.kind === "boot" && !sent) {
      sent = true;
      void representation.blob
        .arrayBuffer()
        .then((buffer) => {
          if (!disposed)
            frame.contentWindow?.postMessage(
              { channel: OFFICE_CHANNEL, kind: "render", format: kind, buffer },
              "*",
              [buffer],
            );
        })
        .catch((error) => finish(undefined, error));
    } else if (event.data.kind === "ready") {
      finish();
    } else if (event.data.kind === "failed") {
      finish(
        undefined,
        new PreviewFailure("renderer_failed", "render", { retryable: true }),
      );
    } else if (event.data.kind === "position" && event.data.position) {
      const p = event.data.position as OfficePosition;
      if (
        [p.top, p.left, p.slide].every(Number.isFinite) &&
        p.top >= 0 &&
        p.top <= 10_000_000 &&
        p.left >= 0 &&
        p.left <= 10_000_000 &&
        Number.isInteger(p.slide) &&
        p.slide >= 0 &&
        p.slide < 10000
      )
        savedPosition = {
          top: p.top,
          left: p.left,
          slide: p.slide,
          focus:
            typeof p.focus === "string" && p.focus.length < 128
              ? p.focus
              : undefined,
        };
    } else if (
      event.data.kind === "escape" &&
      frame.style.visibility !== "hidden"
    ) {
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    }
  };
  // Snapshot only the outgoing frame's own reading position, never application data.
  let savedPosition: OfficePosition = { top: 0, left: 0, slide: 0 };
  const position = () => savedPosition;
  const result: OfficeFrame = {
    ready,
    position,
    activate(previous) {
      if (disposed) return;
      const saved = previous?.position();
      frame.style.visibility = "visible";
      frame.removeAttribute("aria-hidden");
      frame.removeAttribute("tabindex");
      if (saved)
        frame.contentWindow?.postMessage(
          { channel: OFFICE_CHANNEL, kind: "restore", position: saved },
          "*",
        );
      theme();
    },
    dispose,
  };
  signal.addEventListener("abort", abort, { once: true });
  window.addEventListener("message", message);
  host.appendChild(frame);
  return result;
}
