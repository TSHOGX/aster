import { preflightBytes } from "./preview-preflight";
import { PreviewFailure } from "./preview-budget";

self.onmessage = async (
  event: MessageEvent<{ buffer: ArrayBuffer; path: string }>,
) => {
  try {
    const plan = await preflightBytes(
      new Uint8Array(event.data.buffer),
      event.data.path,
      new AbortController().signal,
    );
    self.postMessage({ plan });
  } catch (error) {
    // Only vetted codes and numeric counts may leave the Worker.
    const failure = error instanceof PreviewFailure
      ? error
      : new PreviewFailure("container_invalid", "parse");
    self.postMessage({ failure: failure.toJSON() });
  }
};
