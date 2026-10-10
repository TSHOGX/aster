/** Opt-in, bounded in-memory diagnostics. Never retain URLs, content or actors. */
export type ResourceCategory =
  "conversation" | "directory" | "file" | "control" | "index";
export type InteractionSample = {
  category: ResourceCategory;
  phase: "queue" | "network" | "parse" | "commit";
  milliseconds: number;
  status?: number;
};
let enabled = process.env.NEXT_PUBLIC_INTERACTION_METRICS === "1";
const samples: InteractionSample[] = [];
export function configureInteractionMetrics(value: boolean) {
  enabled = value;
  samples.length = 0;
}
export function interactionSamples(): readonly InteractionSample[] {
  return samples.slice();
}
export function startTiming(
  category: ResourceCategory,
  phase: InteractionSample["phase"],
) {
  if (!enabled) return (_status?: number) => {};
  const start = performance.now();
  return (status?: number) => {
    if (!enabled) return;
    if (samples.length === 512) samples.shift();
    samples.push({
      category,
      phase,
      milliseconds: performance.now() - start,
      status,
    });
  };
}
export function requestCategory(
  path: string,
  mutation: boolean,
): ResourceCategory {
  if (mutation) return "control";
  const route = path.split("?", 1)[0];
  if (/\/files\/(content|previews|snapshots)(\/|$)/.test(route)) return "file";
  if (/\/files$/.test(route)) return "directory";
  if (route.includes("/view/") || route.endsWith("/messages"))
    return "conversation";
  return "index";
}
