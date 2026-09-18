import type { ApiMessage, MessagePage } from "./types";

export type ReadingAnchor = {
  messageId: number;
  offset: number;
  following: boolean;
};
const anchors = new Map<string, ReadingAnchor>();
export function readingAnchor(id: string) {
  return anchors.get(id);
}
export function saveReadingAnchor(id: string, value: ReadingAnchor) {
  anchors.delete(id);
  anchors.set(id, value);
  if (anchors.size > 200) anchors.delete(anchors.keys().next().value!);
}
export function clearReadingAnchors() {
  anchors.clear();
}

// A request only owns the rows it read. Changes since its start (SSE and local
// feedback) win over the HTTP snapshot. Equal rows retain identity for memo.
export function mergeMessageWindow(
  current: ApiMessage[],
  incoming: ApiMessage[],
  baseline: ApiMessage[],
  center?: number,
) {
  const before = new Map(baseline.map((m) => [m.id, m]));
  const held = new Map(current.map((m) => [m.id, m]));
  for (const m of incoming) {
    const existing = held.get(m.id);
    if (existing && existing !== before.get(m.id)) continue;
    held.set(
      m.id,
      existing && JSON.stringify(existing) === JSON.stringify(m) ? existing : m,
    );
  }
  let rows = [...held.values()].sort((a, b) => a.id - b.id);
  if (rows.length > 200) {
    const index = center
      ? rows.findIndex((m) => m.id >= center)
      : rows.length - 1;
    const start = Math.max(0, Math.min(rows.length - 200, index - 100));
    rows = rows.slice(start, start + 200);
  }
  return rows;
}
export function boundedMessage(m: ApiMessage): ApiMessage {
  const chars = [...m.content];
  return {
    ...m,
    content: chars.length > 4096 ? chars.slice(0, 2048).join("") : m.content,
    content_truncated: m.content_truncated || chars.length > 4096,
    content_length: m.content_length ?? chars.length,
    has_trace: m.has_trace || !!m.trace?.length,
    trace: undefined,
  };
}
export function pageBounds(messages: ApiMessage[], page: MessagePage) {
  return {
    hasOlder: page.has_older || (messages[0]?.id ?? 0) > page.before_id,
    hasNewer: page.has_newer || (messages.at(-1)?.id ?? 0) < page.latest_id,
  };
}
