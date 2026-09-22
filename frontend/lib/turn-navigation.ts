import type { TurnIndexItem, TurnIndexPage } from "./project-api";
export const TURN_INDEX_LIMIT = 300;
export function boundedTurnIndex(
  current: TurnIndexPage,
  incoming: TurnIndexPage,
  direction: "older" | "newer" | "replace",
): TurnIndexPage {
  const normalize = (t: TurnIndexItem) => ({
    ...t,
    user_preview: t.user_preview.slice(0, 600),
    assistant_preview: t.assistant_preview.slice(0, 1200),
  });
  let turns = mergeTurnIndex(
    direction === "replace" ? [] : current.turns,
    incoming.turns,
  ).map(normalize);
  let before =
    direction === "newer" && current.turns.length
      ? current.next_before
      : incoming.next_before;
  let after =
    direction === "older" && current.turns.length
      ? current.next_after
      : incoming.next_after;
  if (turns.length > TURN_INDEX_LIMIT) {
    if (direction === "older") {
      turns = turns.slice(0, TURN_INDEX_LIMIT);
      after = turns.at(-1)!.sequence;
    } else {
      turns = turns.slice(-TURN_INDEX_LIMIT);
      before = turns[0].sequence;
    }
  }
  return { turns, next_before: before, next_after: after };
}
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
