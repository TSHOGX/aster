export function buildEventsStreamUrl(lastEventId?: number | null) {
  const base =
    typeof window !== "undefined"
      ? window.location.origin
      : "http://127.0.0.1:8000";
  const url = new URL("/api/events/stream", base);
  if (lastEventId !== undefined && lastEventId !== null) {
    url.searchParams.set("last_event_id", String(lastEventId));
  }
  return url.toString();
}
