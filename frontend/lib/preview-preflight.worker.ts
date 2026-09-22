import { preflightBytes } from "./preview-preflight";

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
  } catch {
    // Do not reflect document contents or parser diagnostics into the UI.
    self.postMessage({ failed: true });
  }
};
