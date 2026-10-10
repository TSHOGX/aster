import type { TimelineItem } from "./project-api";

// Native details are untrusted presentation. Only canonical IDs in the current
// authenticated Project can select a product read or a relative navigation link.
export function scheduleReceiptID(
  item: TimelineItem,
  project: string,
): string | null {
  if (
    item.kind !== "tool" ||
    item.state !== "completed" ||
    !item.name?.startsWith("schedules_") ||
    !item.result
  )
    return null;
  try {
    const details = JSON.parse(item.result).details;
    return details?.kind === "schedule_receipt" &&
      details.version === 1 &&
      details.project_id === project &&
      typeof details.schedule_id === "string" &&
      /^[A-Za-z0-9_-]{1,128}$/.test(details.schedule_id)
      ? details.schedule_id
      : null;
  } catch {
    return null;
  }
}
