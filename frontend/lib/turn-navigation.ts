import type { TurnIndexItem } from "./project-api";
export function mergeTurnIndex(
  current: readonly TurnIndexItem[],
  incoming: readonly TurnIndexItem[],
): TurnIndexItem[] {
  return [
    ...new Map([...current, ...incoming].map((t) => [t.id, t])).values(),
  ].sort((a, b) => a.sequence - b.sequence);
}
export function turnWaveWidth(index: number, hovered: number | null): number {
  return hovered === null
    ? 9
    : 9 + 21 * Math.exp(-Math.pow(index - hovered, 2) / 3.2);
}
