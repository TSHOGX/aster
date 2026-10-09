/** Tab-wide admission; estimates are working-set reservations, not heap readings. */
export const PREVIEW_LIMITS = Object.freeze({
  blobBytes: 20 * 1024 * 1024,
  retainedBytes: 256 * 1024 * 1024,
  renderBytes: 96 * 1024 * 1024,
  expandedBytes: 40 * 1024 * 1024,
  entryBytes: 8 * 1024 * 1024,
  entries: 1024,
  xmlNodes: 100_000,
  xmlDepth: 64,
  cells: 20_000,
  rows: 2000,
  columns: 256,
  sheets: 8,
  slides: 40,
  paragraphs: 2000,
  pages: 100,
  imagePixels: 16_000_000,
  totalPixels: 24_000_000,
  renderers: 2,
  queued: 2,
});

export type PreviewFailureCode =
  | "source_too_large" | "container_invalid" | "unsupported_feature"
  | "content_limit" | "resource_busy" | "parse_timeout" | "renderer_failed";
export type PreviewFailureStage = "download" | "container" | "parse" | "render" | "session";
export type PreviewFailureData = {
  code: PreviewFailureCode;
  stage: PreviewFailureStage;
  actual?: number;
  limit?: number;
  metric?: "bytes" | "entries" | "nodes" | "cells" | "rows" | "slides" | "pixels" | "frames" | "parses";
  retryable: boolean;
};

/** Only bounded structural counts cross the Worker boundary, never parser text or file contents. */
export class PreviewFailure extends Error implements PreviewFailureData {
  readonly name = "PreviewFailure";
  readonly retryable: boolean;
  readonly actual?: number;
  readonly limit?: number;
  readonly metric?: PreviewFailureData["metric"];
  constructor(readonly code: PreviewFailureCode, readonly stage: PreviewFailureStage,
    details: Omit<Partial<PreviewFailureData>, "code" | "stage"> = {}) {
    super(code);
    this.retryable = details.retryable ?? false;
    this.actual = details.actual;
    this.limit = details.limit;
    this.metric = details.metric;
  }
  toJSON(): PreviewFailureData {
    return { code: this.code, stage: this.stage, retryable: this.retryable,
      ...(this.actual !== undefined ? { actual: this.actual } : {}),
      ...(this.limit !== undefined ? { limit: this.limit } : {}),
      ...(this.metric ? { metric: this.metric } : {}) };
  }
}

export function previewFailureFromWire(value: unknown): PreviewFailure {
  if (!value || typeof value !== "object") return new PreviewFailure("renderer_failed", "parse", { retryable: true });
  const data = value as Partial<PreviewFailureData>;
  const codes: PreviewFailureCode[] = ["source_too_large", "container_invalid", "unsupported_feature", "content_limit", "resource_busy", "parse_timeout", "renderer_failed"];
  const stages: PreviewFailureStage[] = ["download", "container", "parse", "render", "session"];
  if (!data.code || !codes.includes(data.code) || !data.stage || !stages.includes(data.stage))
    return new PreviewFailure("renderer_failed", "parse", { retryable: true });
  return new PreviewFailure(data.code, data.stage, {
    retryable: data.retryable === true,
    actual: Number.isSafeInteger(data.actual) && (data.actual ?? -1) >= 0 ? data.actual : undefined,
    limit: Number.isSafeInteger(data.limit) && (data.limit ?? -1) >= 0 ? data.limit : undefined,
    metric: ["bytes", "entries", "nodes", "cells", "rows", "slides", "pixels", "frames", "parses"].includes(data.metric ?? "") ? data.metric : undefined,
  });
}

type Usage = {
  bytes: number;
  urls: number;
  renderers: number;
  observers: number;
  workers: number;
  frames: number;
  parses: number;
  queued: number;
};
const usage: Usage = {
  bytes: 0,
  urls: 0,
  renderers: 0,
  observers: 0,
  workers: 0,
  frames: 0,
  parses: 0,
  queued: 0,
};
export function previewUsage(): Readonly<Usage> {
  return { ...usage };
}
if (
  "document" in globalThis &&
  process.env.NEXT_PUBLIC_INTERACTION_METRICS === "1"
) {
  Object.defineProperty(globalThis, "batteryPreviewUsage", {
    value: previewUsage,
    configurable: true,
  });
}

export function reservePreview(
  bytes: number,
  kind?: "urls" | "renderers" | "observers" | "workers" | "frames",
) {
  if (
    !Number.isSafeInteger(bytes) ||
    bytes < 0 ||
    usage.bytes + bytes > PREVIEW_LIMITS.retainedBytes ||
    ((kind === "renderers" || kind === "frames") &&
      usage[kind] >= PREVIEW_LIMITS.renderers)
  )
    throw new PreviewFailure("resource_busy", "session", { retryable: true,
      metric: kind === "frames" ? "frames" : undefined });
  usage.bytes += bytes;
  if (kind) usage[kind]++;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    usage.bytes -= bytes;
    if (kind) usage[kind]--;
  };
}

type Waiter = {
  signal: AbortSignal;
  resolve: (release: () => void) => void;
  reject: (error: unknown) => void;
  abort: () => void;
};
const waiters: Waiter[] = [];
function grant() {
  usage.parses = 1;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    usage.parses = 0;
    const next = waiters.shift();
    usage.queued = waiters.length;
    if (next) {
      next.signal.removeEventListener("abort", next.abort);
      next.resolve(grant());
    }
  };
}
export function acquirePreviewParse(signal: AbortSignal): Promise<() => void> {
  signal.throwIfAborted();
  if (!usage.parses) return Promise.resolve(grant());
  if (waiters.length >= PREVIEW_LIMITS.queued)
    return Promise.reject(new PreviewFailure("resource_busy", "session", { retryable: true, metric: "parses" }));
  return new Promise((resolve, reject) => {
    const waiter: Waiter = {
      signal,
      resolve,
      reject,
      abort: () => {
        const i = waiters.indexOf(waiter);
        if (i < 0) return;
        waiters.splice(i, 1);
        usage.queued = waiters.length;
        reject(signal.reason);
      },
    };
    waiters.push(waiter);
    usage.queued = waiters.length;
    signal.addEventListener("abort", waiter.abort, { once: true });
  });
}
