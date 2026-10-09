import { expect, it } from "vitest";
import {
  acquirePreviewParse,
  PREVIEW_LIMITS,
  previewUsage,
  reservePreview,
} from "./preview-budget";

it("reserves overlapping representations and releases each lease only once", () => {
  const before = previewUsage();
  const one = reservePreview(PREVIEW_LIMITS.retainedBytes / 2, "renderers");
  const two = reservePreview(PREVIEW_LIMITS.retainedBytes / 2, "renderers");
  expect(() => reservePreview(1)).toThrow("resource_busy");
  expect(() => reservePreview(0, "renderers")).toThrow("resource_busy");
  one();
  one();
  two();
  expect(previewUsage()).toEqual(before);
});

it("bounds queued parses and removes canceled selection work before it can start", async () => {
  const a = new AbortController(),
    b = new AbortController(),
    c = new AbortController();
  const release = await acquirePreviewParse(a.signal);
  const canceled = acquirePreviewParse(b.signal);
  const rejected = expect(canceled).rejects.toThrow("superseded");
  const waiting = acquirePreviewParse(c.signal);
  await expect(
    acquirePreviewParse(new AbortController().signal),
  ).rejects.toThrow("resource_busy");
  b.abort(new Error("superseded"));
  await rejected;
  expect(previewUsage()).toMatchObject({ parses: 1, queued: 1 });
  release();
  release();
  const next = await waiting;
  expect(previewUsage()).toMatchObject({ parses: 1, queued: 0 });
  next();
  expect(previewUsage()).toMatchObject({ parses: 0, queued: 0 });
});
