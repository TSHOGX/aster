import { expect, it } from "vitest";
import { PreviewFailure, previewFailureFromWire } from "./preview-budget";

it("round-trips only vetted preview diagnostics from a Worker", () => {
  const failure = new PreviewFailure("content_limit", "parse", {
    actual: 41, limit: 40, metric: "slides",
  });
  expect(previewFailureFromWire({ ...failure.toJSON(), path: "/secret/report.pptx", parserText: "private" }).toJSON())
    .toEqual({ code: "content_limit", stage: "parse", actual: 41, limit: 40, metric: "slides", retryable: false });
  expect(previewFailureFromWire({ code: "made_up", stage: "parse", actual: -1 }).toJSON())
    .toEqual({ code: "renderer_failed", stage: "parse", retryable: true });
});
