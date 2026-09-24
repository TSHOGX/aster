import type { TimelineItem, Turn, TurnSummary } from "./project-api";
export const runningTurn = (state: string) =>
  ["starting", "running", "stopping"].includes(state);
export function elapsedLabel(
  start?: string,
  end?: string,
  now = Date.now(),
): string {
  const began = Date.parse(start || ""),
    finished = end ? Date.parse(end) : now;
  if (!Number.isFinite(began) || !Number.isFinite(finished)) return "—";
  const seconds = Math.max(0, Math.floor((finished - began) / 1000)),
    h = Math.floor(seconds / 3600),
    m = Math.floor((seconds % 3600) / 60),
    s = seconds % 60;
  return h ? `${h}h ${m}m ${s}s` : m ? `${m}m ${s}s` : `${s}s`;
}
export type ConversationTurn = {
  id: string;
  turnId: string;
  segmentId?: string;
  items: TimelineItem[];
  summary?: TurnSummary;
};
export function conversationTurns(
  timeline: readonly TimelineItem[],
  summaries: readonly TurnSummary[],
  turns: readonly Turn[],
): ConversationTurn[] {
  const metadata = new Map(summaries.map((s) => [s.id, s]));
  for (const turn of turns)
    if (!metadata.has(turn.id))
      metadata.set(turn.id, {
        id: turn.id,
        state: turn.state,
        created_at: turn.created_at,
        finished_at: ["completed", "failed", "canceled", "unknown"].includes(
          turn.state,
        )
          ? turn.updated_at
          : undefined,
      });
  const groups: ConversationTurn[] = [];
  for (const item of timeline) {
    const previous = groups.at(-1);
    if (
      previous &&
      previous.turnId === item.turn_id &&
      previous.segmentId === item.segment_id &&
      item.turn_id
    )
      previous.items.push(item);
    else
      groups.push({
        id: item.id,
        turnId: item.turn_id,
        segmentId: item.segment_id,
        items: [item],
        summary: metadata.get(item.turn_id),
      });
  }
  return groups;
}
export function finalItems(turn: ConversationTurn): TimelineItem[] {
  if (turn.summary?.state !== "completed" || !turn.summary.final_message_id)
    return [];
  return turn.items.filter(
    (item) =>
      item.kind === "assistant" &&
      item.message_id === turn.summary!.final_message_id,
  );
}
