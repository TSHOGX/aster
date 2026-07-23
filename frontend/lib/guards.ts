// Narrow an unknown to a plain object shape. Shared by the session-event and
// harness-event reducers so both agree on what counts as a record payload.
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
