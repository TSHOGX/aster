import { expect, it } from "vitest";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import {
  configureInteractionMetrics,
  interactionSamples,
  startTiming,
} from "./interaction-metrics";

it.skipIf(!process.env.INTERACTION_BENCHMARK_OUTPUT)(
  "records enabled/disabled timing-ring overhead separately from browser profiling",
  () => {
    const samples: { enabled: boolean; nanosecondsPerTiming: number }[] = [];
    const count = 10000;
    try {
      for (let trial = -1; trial < 30; trial++)
        for (const enabled of trial % 2 ? [true, false] : [false, true]) {
          configureInteractionMetrics(enabled);
          const start = performance.now();
          for (let i = 0; i < count; i++)
            startTiming("conversation", "queue")();
          const elapsed = performance.now() - start;
          expect(interactionSamples()).toHaveLength(enabled ? 512 : 0);
          if (trial >= 0)
            samples.push({
              enabled,
              nanosecondsPerTiming: (elapsed * 1e6) / count,
            });
        }
      const output = process.env.INTERACTION_BENCHMARK_OUTPUT!;
      mkdirSync(output, { recursive: true });
      writeFileSync(
        resolve(output, "instrumentation.json"),
        JSON.stringify(
          {
            node: process.version,
            count,
            warmup: 1,
            trialsPerMode: 30,
            samples,
            method:
              "alternating enabled/disabled modes in isolated Node/Vitest; timing closure creation and completion; not browser network/paint or production profiling overhead",
          },
          null,
          2,
        ),
      );
    } finally {
      configureInteractionMetrics(false);
    }
  },
);
