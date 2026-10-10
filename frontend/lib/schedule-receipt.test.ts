import { expect, it } from "vitest";
import { scheduleReceiptID } from "./schedule-receipt";
import type { TimelineItem } from "./project-api";
const item = (details: unknown): TimelineItem => ({
  id: "tool",
  cursor: 1,
  turn_id: "turn",
  kind: "tool",
  state: "completed",
  name: "schedules_create",
  result: JSON.stringify({ details }),
});
it("validates structured receipts and ignores native-supplied links", () => {
  expect(
    scheduleReceiptID(
      item({
        kind: "schedule_receipt",
        version: 1,
        project_id: "p",
        schedule_id: "task",
        url: "https://evil.invalid",
      }),
      "p",
    ),
  ).toBe("task");
  for (const details of [
    {
      kind: "schedule_receipt",
      version: 1,
      project_id: "foreign",
      schedule_id: "task",
    },
    {
      kind: "schedule_receipt",
      version: 1,
      project_id: "p",
      schedule_id: "../../elsewhere",
    },
    {
      kind: "schedule_receipt",
      version: 2,
      project_id: "p",
      schedule_id: "task",
    },
  ])
    expect(scheduleReceiptID(item(details), "p")).toBeNull();
  expect(
    scheduleReceiptID({ ...item({}), result: "Scheduled task task" }, "p"),
  ).toBeNull();
});
