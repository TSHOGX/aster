import type { AgentRecord, Message, SessionSnapshot } from "./project-api";
export type SessionView = {
  snapshot: SessionSnapshot | null;
  cursor: number;
  partial: string;
  partialTurn: string;
  tools: { id: string; name: string; state: string; detail: string }[];
};
export const emptySessionView = (): SessionView => ({
  snapshot: null,
  cursor: 0,
  partial: "",
  partialTurn: "",
  tools: [],
});
export function acceptSnapshot(
  current: SessionView,
  next: SessionSnapshot,
): SessionView {
  const previous = current.snapshot;
  if (
    previous &&
    (previous.session.id !== next.session.id ||
      previous.session.project_id !== next.session.project_id ||
      previous.session.revision > next.session.revision)
  )
    return current;
  let base = current;
  if (!previous && next.records) {
    base = acceptRecords(
      { ...emptySessionView(), snapshot: next },
      next.records,
    );
  }
  const settled = !next.turns.some(
    (t) =>
      t.id === base.partialTurn &&
      ["starting", "running", "stopping"].includes(t.state),
  );
  return {
    ...base,
    snapshot: { ...next, messages: uniqueMessages(next.messages) },
    cursor: previous ? base.cursor : next.record_cursor,
    partial: settled ? "" : base.partial,
    partialTurn: settled ? "" : base.partialTurn,
  };
}
export function acceptRecords(
  current: SessionView,
  records: AgentRecord[],
): SessionView {
  let { cursor, partial, partialTurn } = current;
  const active = new Set(
    current.snapshot?.turns
      .filter((t) => ["starting", "running", "stopping"].includes(t.state))
      .map((t) => t.id),
  );
  if (!active.has(partialTurn)) {
    partial = "";
    partialTurn = "";
  }
  let tools = [...current.tools];
  for (const record of records) {
    if (record.cursor <= cursor) continue;
    cursor = record.cursor;
    if (record.kind !== "native") continue;
    const event = record.payload;
    if (event.type === "message_update" && active.has(record.turn_id)) {
      if (partialTurn !== record.turn_id) {
        partial = "";
        partialTurn = record.turn_id;
      }
      const delta = event.assistantMessageEvent as
        | { type?: string; delta?: string }
        | undefined;
      if (delta?.type === "text_delta" && typeof delta.delta === "string")
        partial = (partial + delta.delta).slice(-64 * 1024);
    }
    if (
      active.has(record.turn_id) &&
      (event.type === "message_start" ||
        event.type === "agent_end" ||
        event.type === "message_end")
    )
      partial = "";
    if (
      event.type === "tool_execution_start" ||
      event.type === "tool_execution_end"
    ) {
      const id =
        record.turn_id + "/" + String(event.toolCallId || record.cursor);
      const prior = tools.find((tool) => tool.id === id);
      const tool = {
        id,
        name: String(event.toolName || prior?.name || "Tool"),
        state:
          event.type === "tool_execution_start"
            ? "running"
            : event.isError
              ? "failed"
              : "completed",
        detail: JSON.stringify(event.args || event.result || {}).slice(0, 8192),
      };
      tools = [...tools.filter((entry) => entry.id !== id), tool].slice(-50);
    }
  }
  return { ...current, cursor, partial, partialTurn, tools };
}
export function uniqueMessages(messages: Message[]): Message[] {
  return [...new Map(messages.map((message) => [message.id, message])).values()]
    .sort((a, b) => a.sequence - b.sequence)
    .slice(-100);
}
