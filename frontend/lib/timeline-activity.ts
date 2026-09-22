import type { TimelineItem } from "./project-api";
export type TimelineSegment =
  | { kind: "message"; id: string; item: TimelineItem }
  | {
      kind: "activity";
      id: string;
      items: TimelineItem[];
      live?: TimelineItem;
      active: TimelineItem[];
    };

/** Collapse only adjacent activity in the same turn; prose is a hard boundary. */
export function groupTimeline(
  items: readonly TimelineItem[],
  activeTurns: ReadonlySet<string>,
): TimelineSegment[] {
  const segments: TimelineSegment[] = [];
  for (const item of items) {
    if (item.kind !== "tool" && item.kind !== "thinking") {
      segments.push({ kind: "message", id: item.id, item });
      continue;
    }
    const previous = segments.at(-1);
    if (
      previous?.kind === "activity" &&
      previous.items[0].turn_id === item.turn_id
    )
      previous.items.push(item);
    else
      segments.push({
        kind: "activity",
        id: item.id,
        items: [item],
        active: [],
      });
  }
  for (let i = 0; i < segments.length; i++) {
    const segment = segments[i];
    if (segment.kind !== "activity") continue;
    segment.active = segment.items.filter(
      (item) =>
        activeTurns.has(item.turn_id) &&
        ((item.kind === "tool" &&
          ["pending", "running"].includes(item.state || "")) ||
          // Native thinking blocks remain 'streaming' while text in the same
          // message streams. Once prose follows, that thinking is no longer live.
          (item.kind === "thinking" &&
            item.state === "streaming" &&
            item === segment.items.at(-1) &&
            !segments[i + 1])),
    );
    segment.live = segment.active.at(-1);
  }
  return segments;
}
export function activityPreview(item: TimelineItem): string {
  if (item.kind === "thinking")
    return (item.content || "").replace(/\s+/g, " ").trim().slice(-200);
  const raw = item.arguments || "";
  try {
    const args: unknown = JSON.parse(raw);
    if (args && typeof args === "object") {
      const data = args as Record<string, unknown>;
      for (const key of [
        "command",
        "cmd",
        "path",
        "file_path",
        "filePath",
        "query",
        "url",
        "pattern",
      ])
        if (typeof data[key] === "string")
          return data[key].replace(/\s+/g, " ").trim().slice(0, 300);
    }
  } catch {
    /* Bounded argument previews may be truncated JSON. */
  }
  return raw.replace(/\s+/g, " ").trim().slice(0, 300);
}

/** Render text tool output as text; retain structured metadata when no text exists. */
export function activityResult(raw: string): string {
  try {
    const value = JSON.parse(raw);
    if (typeof value === "string") return value;
    if (value && typeof value === "object") {
      if (
        Array.isArray(value.content) &&
        value.content.length &&
        value.content.every(
          (block: { type?: string; text?: unknown }) =>
            block?.type === "text" && typeof block.text === "string",
        )
      )
        return value.content
          .map((block: { text: string }) => block.text)
          .join("\n");
      return JSON.stringify(value, null, 2);
    }
  } catch {
    /* Partial output can be plain text or bounded JSON. */
  }
  return raw;
}
export function activityArguments(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}
