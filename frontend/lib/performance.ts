// Fixed-size diagnostic ring: no message contents, tokens or bearer URLs.
type Sample = { stage: string; time: number; duration?: number };
const samples: Sample[] = [];
let selectedAt: number | undefined;
export function markWorkbench(stage: string, duration?: number) {
  if (typeof performance === "undefined") return;
  if (stage === "select") selectedAt = performance.now();
  samples.push({
    stage,
    time: performance.now(),
    duration:
      duration ??
      (stage === "readable" && selectedAt !== undefined
        ? performance.now() - selectedAt
        : undefined),
  });
  if (samples.length > 512) samples.splice(0, samples.length - 512);
  performance.mark(`workbench:${stage}`);
  performance.clearMarks(`workbench:${stage}`);
}
export function readWorkbenchPerformance() {
  return samples.slice();
}

if (typeof window !== "undefined")
  Object.defineProperty(window, "__workbenchPerformance", {
    configurable: true,
    value: readWorkbenchPerformance,
  });
