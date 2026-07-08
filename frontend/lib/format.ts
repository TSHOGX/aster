import type { SessionStatus } from "./types";

const SHORT_TIME = new Intl.DateTimeFormat("en-US", { hour: "2-digit", minute: "2-digit" });

export function formatTime(value: string | null | undefined) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return SHORT_TIME.format(date);
}

export function formatRelative(value: string | null | undefined) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const diff = (Date.now() - date.getTime()) / 1000;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.round(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.round(diff / 3600)}h ago`;
  return `${Math.round(diff / 86400)}d ago`;
}

const FULL_DATETIME = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

export function formatFullTimestamp(value: string | null | undefined) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return FULL_DATETIME.format(date);
}

const DAY_LABEL = new Intl.DateTimeFormat("en-US", { month: "long", day: "numeric", year: "numeric" });

export function dayKey(value: string | null | undefined) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
}

export function formatDayDivider(value: string | null | undefined) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const now = new Date();
  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const diffDays = Math.round((startOfDay(now) - startOfDay(date)) / 86400000);
  if (diffDays === 0) return "Today";
  if (diffDays === 1) return "Yesterday";
  return DAY_LABEL.format(date);
}

export function formatBytes(size: number | undefined | null) {
  if (size === null || size === undefined || !Number.isFinite(size) || size < 0) return "-";
  if (size < 1024) return `${size} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = size / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

export function statusLabel(status: string) {
  switch (status as SessionStatus) {
    case "created":
      return "Ready";
    case "running_active":
      return "Running";
    case "running_idle":
      return "Idle";
    case "checkpointing":
      return "Checkpointing";
    case "checkpointed":
      return "Checkpointed";
    case "failed":
      return "Failed";
    case "destroyed":
      return "Ended";
    default:
      return status || "Unknown";
  }
}

export function isAcceptingInput(status: string) {
  return status === "created" || status === "running_idle" || status === "checkpointed";
}

export function isTerminal(status: string) {
  return status === "failed" || status === "destroyed";
}
