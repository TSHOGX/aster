import { afterEach, expect, it, vi } from "vitest";
import { api } from "./project-api";
import {
  configureInteractionMetrics,
  interactionSamples,
  startTiming,
} from "./interaction-metrics";
afterEach(() => {
  configureInteractionMetrics(false);
  vi.unstubAllGlobals();
});
it("is opt-in and retains bounded category-only timings for failed and successful reads", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(() => Promise.resolve(new Response("{}"))),
  );
  await api(
    "/api/projects/private/sessions/secret/view/pages?token=credential",
  );
  expect(interactionSamples()).toEqual([]);
  configureInteractionMetrics(true);
  await api(
    "/api/projects/private/sessions/secret/view/pages?token=credential",
  );
  expect(
    interactionSamples().map((s) => [s.category, s.phase, s.status]),
  ).toEqual([
    ["conversation", "network", 200],
    ["conversation", "parse", 200],
  ]);
  expect(JSON.stringify(interactionSamples())).not.toMatch(
    /private|secret|credential/,
  );
  for (let i = 0; i < 600; i++) startTiming("directory", "queue")();
  expect(interactionSamples()).toHaveLength(512);
  configureInteractionMetrics(false);
  expect(interactionSamples()).toEqual([]);
});
