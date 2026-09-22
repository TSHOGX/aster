import { PreviewLimitError, reservePreview } from "./preview-budget";
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
      else reject(error ?? new PreviewLimitError());
    };
    const abort = () => finish(undefined, signal.reason);
    const timer = setTimeout(() => finish(), 10_000);
    signal.addEventListener("abort", abort, { once: true });
    worker.onmessage = (event: MessageEvent<{ plan?: PreviewPlan }>) =>
      finish(event.data.plan);
    worker.onerror = () => finish();
    worker.onmessageerror = () => finish();
    try {
      worker.postMessage({ buffer, path }, [buffer]);
    } catch (error) {
      finish(undefined, error);
    }
  });
}
