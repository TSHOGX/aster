import type { Message, SessionSnapshot, TimelineItem } from "./project-api";
// Content revisions follow rows, not control metadata. A live control refresh
// can advance session.revision without refreshing the historical reading window.
const revisions = new WeakMap<object, number>();
const byID = <T extends { id: string }>(
  a: readonly T[],
  b: readonly T[],
  previousRevision: number,
  nextRevision: number,
): T[] => {
  const rows = new Map<string, T>();
  for (const [items, revision] of [
    [a, previousRevision],
    [b, nextRevision],
  ] as const) {
    for (const item of items) {
      if (!revisions.has(item)) revisions.set(item, revision);
      const old = rows.get(item.id);
      if (!old || revisions.get(item)! >= revisions.get(old)!) {
        const equal = old && Object.keys(item).length === Object.keys(old).length &&
          Object.keys(item).every(k => item[k as keyof T] === old[k as keyof T]);
        if (equal) revisions.set(old, revisions.get(item)!);
        rows.set(item.id, equal ? old : item);
      }
    }
  }
  return [...rows.values()];
};
const timeline = (s: SessionSnapshot): TimelineItem[] =>
  s.timeline ??
  s.messages.map((m) => ({
    id: m.id,
    cursor: m.sequence,
    turn_id: m.turn_id || "",
    kind: m.role,
    content: m.content,
    message_id: m.id,
  }));
export const oldestMessageSequence = (messages: readonly Message[]): number =>
  Math.min(
    ...messages.filter((m) => m.sequence > 0).map((m) => m.sequence),
    Infinity,
  );
/** Pages add to one continuous transcript; polling refreshes its live end only. */
export function mergeConversationPage(
  current: SessionSnapshot | null,
  next: SessionSnapshot,
  older = false,
): SessionSnapshot {
  if (!current) {
    byID([], next.messages, 0, next.session.revision);
    byID([], next.timeline ?? [], 0, next.session.revision);
    byID([], next.turn_summaries ?? [], 0, next.session.revision);
    return next;
  }
  if (
    current.session.id !== next.session.id ||
    current.session.project_id !== next.session.project_id
  )
    return current;
  const fresh = next.session.revision >= current.session.revision;
  const messages = byID(
    current.messages,
    next.messages,
    current.session.revision,
    next.session.revision,
  ).sort((a, b) => a.sequence - b.sequence);
  const summaries = byID(
    current.turn_summaries || [],
    next.turn_summaries || [],
    current.session.revision,
    next.session.revision,
  );
  const sequences = new Map(messages.map((m) => [m.id, m.sequence]));
  const items = byID(
    timeline(current),
    timeline(next),
    current.session.revision,
    next.session.revision,
  ).sort(
    (a, b) =>
      a.cursor - b.cursor ||
      (a.order ?? 0) - (b.order ?? 0) ||
      (sequences.get(a.message_id || "") || 0) -
        (sequences.get(b.message_id || "") || 0),
  );
  const extending =
    oldestMessageSequence(next.messages) <
    oldestMessageSequence(current.messages);
  return {
    ...(fresh && !older ? next : current),
    session: fresh ? next.session : current.session,
    messages,
    timeline: items,
    turn_summaries: summaries,
    record_cursor: Math.max(current.record_cursor, next.record_cursor),
    has_earlier:
      older && extending
        ? next.has_earlier
        : oldestMessageSequence(current.messages) <
            oldestMessageSequence(next.messages)
          ? current.has_earlier
          : next.has_earlier,
    has_later:
      Math.max(...next.messages.map((m) => m.sequence), 0) >=
      Math.max(...current.messages.map((m) => m.sequence), 0)
        ? next.has_later
        : current.has_later,
    timeline_truncated: current.timeline_truncated || next.timeline_truncated,
  };
}
