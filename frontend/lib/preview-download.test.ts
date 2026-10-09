import { afterEach, expect, it, vi } from "vitest";
import { readPreview } from "./project-api";
import { PREVIEW_LIMITS, PreviewFailure } from "./preview-budget";

afterEach(() => vi.unstubAllGlobals());

it("reads exactly 20 MiB and reports progress", async () => {
  const bytes = new Uint8Array(PREVIEW_LIMITS.blobBytes);
  vi.stubGlobal("fetch", vi.fn(async () => new Response(bytes, { headers: { "content-length": String(bytes.length) } })));
  const progress = vi.fn();
  expect((await readPreview("/file", new AbortController().signal, undefined, progress)).size).toBe(bytes.length);
  expect(progress).toHaveBeenLastCalledWith(bytes.length, bytes.length);
});

it("cancels oversized streams, with or without an accurate Content-Length", async () => {
  for (const length of [undefined, "10", String(PREVIEW_LIMITS.blobBytes + 1)]) {
    const cancel = vi.fn();
    const stream = new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(PREVIEW_LIMITS.blobBytes + 1)); }, cancel });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(stream, { headers: length ? { "content-length": length } : {} })));
    await expect(readPreview("/file", new AbortController().signal)).rejects.toMatchObject({ code: "source_too_large", stage: "download" } satisfies Partial<PreviewFailure>);
    expect(cancel).toHaveBeenCalledOnce();
    expect(stream.locked).toBe(false);
  }
});
