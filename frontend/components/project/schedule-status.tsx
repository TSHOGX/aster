"use client";

import { useT, type MessageKey } from "@/lib/i18n";
import { scheduleStateLabels } from "@/lib/schedule-recurrence";

const runLabels: Record<string, MessageKey> = {
  queued: "Queued",
  running: "Running",
  starting: "Starting",
  stopping: "Stopping",
  completed: "Completed",
  failed: "Failed",
  canceled: "Canceled",
  interrupted: "Interrupted",
  held: "Held",
  handled: "Handled",
};

export function ScheduleStatus({
  state,
  run = false,
}: {
  state: string;
  run?: boolean;
}) {
  const tr = useT();
  const label = run
    ? runLabels[state] || "Outcome unknown"
    : scheduleStateLabels[state as keyof typeof scheduleStateLabels];
  return (
    <span className="schedule-status" data-state={state}>
      <span className="schedule-status-dot" aria-hidden="true" />
      {tr(label || "Outcome unknown")}
    </span>
  );
}
