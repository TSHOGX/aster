import { PreviewFailure, previewFailureFromWire, reservePreview } from "./preview-budget";
import { OFFICE_LIMITS, officeKind } from "../office/protocol";
import type { PreviewPlan } from "./preview-preflight";

/** A single admitted validation transfers its buffer and owns a terminable Worker. */
export async function validatePreview(
  blob: Blob,
  path: string,
  signal: AbortSignal,
): Promise<PreviewPlan> {
  signal.throwIfAborted();
  const buffer = await blob.arrayBuffer();
  signal.throwIfAborted();
  const release = reservePreview(0, "workers");
  let worker: Worker;
  try {
    worker = new Worker(
      new URL("./preview-preflight.worker.ts", import.meta.url),
      { type: "module" },
    );
  } catch (error) {
    release();
    throw error;
  }
  return new Promise((resolve, reject) => {
    let done = false;
    const finish = (plan?: PreviewPlan, error?: unknown) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      worker.terminate();
      release();
      if (plan) resolve(plan);
      else reject(error ?? new PreviewFailure("renderer_failed", "parse", { retryable: true }));
    };
    const abort = () => finish(undefined, signal.reason);
    const timer = setTimeout(() => finish(undefined, new PreviewFailure("parse_timeout", "parse", { retryable: true })), officeKind(path) ? OFFICE_LIMITS.inspectMs : 10_000);
    signal.addEventListener("abort", abort, { once: true });
    worker.onmessage = (event: MessageEvent<{ plan?: PreviewPlan; failure?: unknown }>) =>
      finish(event.data.plan, event.data.failure ? previewFailureFromWire(event.data.failure) : undefined);
    worker.onerror = () => finish(undefined, new PreviewFailure("renderer_failed", "parse", { retryable: true }));
    worker.onmessageerror = () => finish(undefined, new PreviewFailure("renderer_failed", "parse", { retryable: true }));
    try {
      worker.postMessage({ buffer, path }, [buffer]);
    } catch (error) {
      finish(undefined, error);
    }
  });
}
