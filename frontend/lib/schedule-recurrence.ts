import type { Schedule } from "./project-api";
import type { MessageKey } from "./i18n/messages-en";
export type RepeatMode =
  | "once"
  | "daily"
  | "weekdays"
  | "weekly"
  | "monthly"
  | "dates"
  | "interval"
  | "advanced"
  | "manual";
export type Recurrence = {
  mode: RepeatMode;
  time: string;
  weekdays: number[];
  dates: number[];
  advanced: string;
  intervalMinutes: string;
  localTime: string;
  atChoice: string;
};
export const repeatLabels: Record<RepeatMode, MessageKey> = {
  once: "Once",
  interval: "Interval",
  daily: "Daily",
  weekdays: "Monday–Friday",
  weekly: "Weekly",
  monthly: "Monthly",
  dates: "Several monthly dates",
  advanced: "Advanced: Cron expression",
  manual: "Manual only",
};
export const weekdayLabels: MessageKey[] = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];
export function parseRecurrence(
  expression: string,
  at = "",
  zone = "UTC",
): Recurrence {
  const base: Recurrence = {
    mode: at ? "once" : expression ? "advanced" : "manual",
    localTime: at ? localScheduleTime(at, zone) : "",
    atChoice: at,
    time: "09:00",
    weekdays: [1],
    dates: [1],
    advanced: expression,
    intervalMinutes: "15",
  };
  if (!expression) return base;
  const interval = /^@every ([1-9]\d*)m$/.exec(expression);
  if (interval && Number(interval[1]) <= 10080)
    return { ...base, mode: "interval", intervalMinutes: interval[1] };
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) return base;
  const [minute, hour, date, month, day] = fields;
  if (
    !/^\d{1,2}$/.test(minute) ||
    !/^\d{1,2}$/.test(hour) ||
    +minute > 59 ||
    +hour > 23 ||
    month !== "*"
  )
    return base;
  const time = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`;
  if (date === "*" && day === "*") return { ...base, mode: "daily", time };
  if (date === "*" && day === "1-5") return { ...base, mode: "weekdays", time };
  if (date === "*" && /^[0-6](,[0-6])*$/.test(day))
    return {
      ...base,
      mode: "weekly",
      time,
      weekdays: [...new Set(day.split(",").map(Number))],
    };
  if (
    day === "*" &&
    /^\d{1,2}(,\d{1,2})*$/.test(date) &&
    date.split(",").every((n) => +n >= 1 && +n <= 31)
  ) {
    const dates = [...new Set(date.split(",").map(Number))];
    return {
      ...base,
      mode: dates.length === 1 ? "monthly" : "dates",
      time,
      dates,
    };
  }
  return base;
}
export function recurrenceCron(rule: Recurrence): string | null {
  if (rule.mode === "manual" || rule.mode === "once") return "";
  if (rule.mode === "advanced") return rule.advanced;
  if (rule.mode === "interval") {
    const value = Number(rule.intervalMinutes);
    return /^\d+$/.test(rule.intervalMinutes) &&
      Number.isInteger(value) &&
      value >= 1 &&
      value <= 10080
      ? `@every ${value}m`
      : null;
  }
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(rule.time)) return null;
  const [hour, minute] = rule.time.split(":").map(Number);
  const prefix = `${minute} ${hour}`;
  if (rule.mode === "daily") return `${prefix} * * *`;
  if (rule.mode === "weekdays") return `${prefix} * * 1-5`;
  if (rule.mode === "weekly")
    return rule.weekdays.length
      ? `${prefix} * * ${[...rule.weekdays].sort((a, b) => a - b).join(",")}`
      : null;
  const days = rule.mode === "monthly" ? rule.dates.slice(0, 1) : rule.dates;
  return days.length && days.every((n) => n >= 1 && n <= 31)
    ? `${prefix} ${[...days].sort((a, b) => a - b).join(",")} * *`
    : null;
}
export function schedulePreviewKey(
  cron: string | null,
  timezone: string,
  nextAt?: string,
  runAt = "",
) {
  return JSON.stringify([cron, timezone, nextAt || "", runAt]);
}
export function scheduleConfiguration(s: Schedule) {
  return JSON.stringify([
    s.project_id,
    s.title,
    s.prompt,
    s.cron,
    s.timezone,
    s.session_id || "",
    s.state,
    s.trigger_kind,
    s.run_at || "",
    s.expires_at || "",
    s.notify,
    s.max_occurrences || 0,
    s.starts_at || "",
    s.notification_policy || "each_run",
  ]);
}
export function scheduleTime(value: string, zone: string, locale: string) {
  try {
    return new Intl.DateTimeFormat(locale, {
      timeZone: zone,
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
      timeZoneName: "shortOffset",
    }).format(new Date(value));
  } catch {
    return value;
  }
}

// The write DTO includes authored fields only; server projections and origin
// attribution are deliberately excluded. Keep requestId stable across retries.
export function scheduleWrite(s: Schedule, requestId: string) {
  return {
    request_id: requestId,
    id: s.id,
    revision: s.revision,
    title: s.title,
    prompt: s.prompt,
    session_id: s.session_id || "",
    state: s.state,
    trigger_kind: s.trigger_kind,
    cron: s.cron,
    timezone: s.timezone,
    run_at: s.run_at || "",
    expires_at: s.expires_at || "",
    notify: s.notify,
    max_occurrences: s.max_occurrences || 0,
    starts_at: s.starts_at || "",
    notification_policy: s.notification_policy || "each_run",
  };
}

export function localScheduleTime(at: string, zone: string) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(new Date(at));
  const p = Object.fromEntries(parts.map((p) => [p.type, p.value]));
  return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}`;
}
// Enumerate offsets near this wall time so gaps are rejected and repeated
// times require an explicit choice, without silently using browser timezone.
export function resolveLocalScheduleTime(
  local: string,
  zone: string,
): string[] {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(local)) return [];
  const wall = Date.parse(local + ":00Z");
  if (
    !Number.isFinite(wall) ||
    new Date(wall).toISOString().slice(0, 16) !== local
  )
    return [];
  try {
    const offsets = new Set<number>();
    for (let hours = -36; hours <= 36; hours += 6) {
      const sample = wall + hours * 3600000;
      offsets.add(
        Date.parse(
          localScheduleTime(new Date(sample).toISOString(), zone) + ":00Z",
        ) - sample,
      );
    }
    return [...offsets]
      .map((offset) => new Date(wall - offset).toISOString())
      .filter((at) => localScheduleTime(at, zone) === local)
      .sort();
  } catch {
    return [];
  }
}
export function recurrenceRunAt(rule: Recurrence, zone: string) {
  if (rule.mode !== "once") return "";
  if (
    rule.atChoice &&
    Number.isFinite(Date.parse(rule.atChoice)) &&
    localScheduleTime(rule.atChoice, zone) === rule.localTime
  )
    return rule.atChoice;
  const candidates = resolveLocalScheduleTime(rule.localTime, zone);
  return candidates.length === 1
    ? candidates[0]
    : candidates.includes(rule.atChoice)
      ? rule.atChoice
      : "";
}

export const scheduleStateLabels: Record<Schedule["state"], MessageKey> = {
  active: "Enabled",
  paused: "Paused",
  exhausted: "Exhausted",
  canceled: "Canceled",
  deleted: "Deleted",
};
