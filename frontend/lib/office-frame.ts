import { PreviewLimitError, reservePreview } from "./preview-budget";
import type { PreviewRepresentation } from "./preview-representation";

export const OFFICE_CHANNEL = "battery-office-preview-v1";
export type OfficeKind = "spreadsheet" | "document" | "presentation";
export type OfficePosition = {
  top: number;
  left: number;
  slide: number;
  focus?: string;
};
export type OfficeFrame = {
  ready: Promise<{ drawingsUnavailable: boolean }>;
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
  frame.title = representation.path;
  frame.className = "office-frame";
  frame.style.cssText =
    "position:absolute;inset:0;width:100%;height:100%;border:0;visibility:hidden";
  frame.tabIndex = -1;
  frame.setAttribute("aria-hidden", "true");
  let disposed = false,
    sent = false,
    settled = false;
  let resolve!: (value: { drawingsUnavailable: boolean }) => void;
  let reject!: (error: unknown) => void;
  const ready = new Promise<{ drawingsUnavailable: boolean }>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  const finish = (
    value?: { drawingsUnavailable: boolean },
    error?: unknown,
  ) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    if (value) resolve(value);
    else reject(error ?? new PreviewLimitError());
  };
  const dispose = () => {
    if (disposed) return;
    disposed = true;
    signal.removeEventListener("abort", abort);
    window.removeEventListener("message", message);
    frame.remove();
    releaseFrame();
    finish(
      undefined,
      signal.reason ?? new DOMException("Preview closed", "AbortError"),
    );
  };
  const abort = () => dispose();
  const timer = setTimeout(() => {
    finish();
    dispose();
  }, 15_000);
  const message = (event: MessageEvent) => {
    if (
      disposed ||
      event.source !== frame.contentWindow ||
      event.origin !== location.origin ||
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
              location.origin,
              [buffer],
            );
        })
        .catch((error) => finish(undefined, error));
    } else if (event.data.kind === "ready") {
      finish({ drawingsUnavailable: event.data.drawingsUnavailable === true });
    } else if (event.data.kind === "failed") {
      finish(undefined, new PreviewLimitError());
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
  const position = (): OfficePosition => {
    const content = frame.contentDocument?.querySelector<HTMLElement>(
      ".office-frame-content",
    );
    const focused = frame.contentDocument?.activeElement;
    return {
      top: content?.scrollTop ?? 0,
      left: content?.scrollLeft ?? 0,
      slide: Number(content?.dataset.slide ?? 0),
      focus:
        document.activeElement === frame
          ? focused?.getAttribute("data-preview-focus") || "content"
          : undefined,
    };
  };
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
          location.origin,
        );
    },
    dispose,
  };
  signal.addEventListener("abort", abort, { once: true });
  window.addEventListener("message", message);
  host.appendChild(frame);
  return result;
}
