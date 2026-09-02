export type FrequencyMode = "manual" | "interval" | "daily" | "weekdays" | "weekly" | "custom";
export type CustomRepeat = "hourly" | "daily" | "weekly" | "monthly" | "yearly";

export type ScheduleFrequencyForm = {
  frequency: FrequencyMode;
  intervalMinutes: number;
  time: string;
  weekday: number;
  customRepeat: CustomRepeat;
  customInterval: number;
  customMinute: number;
  customWeekdays: number[];
  customDay: number;
  customMonth: number;
  customRawCron: string;
};

export const WEEKDAYS = [
  { value: 1, label: "星期一" },
  { value: 2, label: "星期二" },
  { value: 3, label: "星期三" },
  { value: 4, label: "星期四" },
  { value: 5, label: "星期五" },
  { value: 6, label: "星期六" },
  { value: 0, label: "星期日" }
] as const;
export const MONTHS = Array.from({ length: 12 }, (_, index) => ({ value: index + 1, label: `${index + 1} 月` }));
export const DAYS = Array.from({ length: 31 }, (_, index) => ({ value: index + 1, label: `${index + 1} 日` }));
export const TIME_OPTIONS = Array.from({ length: 96 }, (_, index) => {
  const hour = Math.floor(index / 4);
  const minute = (index % 4) * 15;
  const value = `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
  return { value, label: value };
});

function timeParts(value: string) {
  const [hour, minute] = value.split(":").map(Number);
  return { hour: Number.isFinite(hour) ? hour : 0, minute: Number.isFinite(minute) ? minute : 0 };
}

function timeValue(hour: number, minute: number) {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

export function parseCron(expr?: string | null): Partial<ScheduleFrequencyForm> {
  const parts = (expr ?? "").trim().split(/\s+/);
  if (!expr?.trim() || parts.length !== 5) return { frequency: "manual" };
  const [minute, hour, dayOfMonth, month, dayOfWeek] = parts;
  const numericMinute = /^\d+$/.test(minute) ? Number(minute) : null;
  const numericHour = /^\d+$/.test(hour) ? Number(hour) : null;
  const time = numericMinute !== null && numericHour !== null ? timeValue(numericHour, numericMinute) : "08:00";
  const intervalMinutes = /^\*\/(\d+)$/.exec(minute)?.[1];
  if (intervalMinutes && hour === "*" && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    return { frequency: "interval", intervalMinutes: Number(intervalMinutes) };
  }
  if (numericMinute !== null && numericHour !== null && dayOfMonth === "*" && month === "*" && dayOfWeek === "1-5") {
    return { frequency: "weekdays", time };
  }
  if (numericMinute !== null && numericHour !== null && dayOfMonth === "*" && month === "*" && /^\d+(,\d+)+$/.test(dayOfWeek)) {
    return { frequency: "custom", customRepeat: "weekly", customWeekdays: dayOfWeek.split(",").map(Number), time };
  }
  if (numericMinute !== null && numericHour !== null && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    return { frequency: "daily", time };
  }
  if (numericMinute !== null && numericHour !== null && dayOfMonth === "*" && month === "*" && /^\d+$/.test(dayOfWeek)) {
    return { frequency: "weekly", weekday: Number(dayOfWeek) === 7 ? 0 : Number(dayOfWeek), time };
  }
  const hourlyInterval = /^\*\/(\d+)$/.exec(hour)?.[1];
  if (numericMinute !== null && hourlyInterval && dayOfMonth === "*" && month === "*" && dayOfWeek === "*") {
    return { frequency: "custom", customRepeat: "hourly", customInterval: Number(hourlyInterval), customMinute: numericMinute };
  }
  const dailyInterval = /^\*\/(\d+)$/.exec(dayOfMonth)?.[1];
  if (numericMinute !== null && numericHour !== null && dailyInterval && month === "*" && dayOfWeek === "*") {
    return { frequency: "custom", customRepeat: "daily", customInterval: Number(dailyInterval), time };
  }
  const monthlyInterval = /^\*\/(\d+)$/.exec(month)?.[1];
  if (numericMinute !== null && numericHour !== null && /^\d+$/.test(dayOfMonth) && monthlyInterval && dayOfWeek === "*") {
    return { frequency: "custom", customRepeat: "monthly", customInterval: Number(monthlyInterval), customDay: Number(dayOfMonth), time };
  }
  if (numericMinute !== null && numericHour !== null && /^\d+$/.test(dayOfMonth) && /^\d+$/.test(month) && dayOfWeek === "*") {
    return { frequency: "custom", customRepeat: "yearly", customDay: Number(dayOfMonth), customMonth: Number(month), time };
  }
  return { frequency: "custom", customRawCron: expr.trim() };
}

export function buildCronExpression(form: ScheduleFrequencyForm) {
  if (form.frequency === "manual") return "";
  if (form.frequency === "interval") return `*/${Math.min(59, Math.max(1, form.intervalMinutes))} * * * *`;
  const { hour, minute } = timeParts(form.time);
  if (form.frequency === "daily") return `${minute} ${hour} * * *`;
  if (form.frequency === "weekdays") return `${minute} ${hour} * * 1-5`;
  if (form.frequency === "weekly") return `${minute} ${hour} * * ${form.weekday}`;
  if (form.customRawCron) return form.customRawCron;
  if (form.customRepeat === "hourly") return `${Math.min(59, Math.max(0, form.customMinute))} */${Math.min(23, Math.max(1, form.customInterval))} * * *`;
  if (form.customRepeat === "daily") return `${minute} ${hour} */${Math.min(31, Math.max(1, form.customInterval))} * *`;
  if (form.customRepeat === "weekly") return `${minute} ${hour} * * ${(form.customWeekdays.length ? form.customWeekdays : [1]).slice().sort((a, b) => a - b).join(",")}`;
  if (form.customRepeat === "monthly") return `${minute} ${hour} ${Math.min(31, Math.max(1, form.customDay))} */${Math.min(12, Math.max(1, form.customInterval))} *`;
  return `${minute} ${hour} ${Math.min(31, Math.max(1, form.customDay))} ${Math.min(12, Math.max(1, form.customMonth))} *`;
}

export function frequencyDescription(expr?: string | null) {
  const parsed = parseCron(expr);
  switch (parsed.frequency) {
    case "interval": return `每隔 ${parsed.intervalMinutes ?? 15} 分钟`;
    case "daily": return `每天 ${parsed.time ?? "08:00"}`;
    case "weekdays": return `每个工作日 ${parsed.time ?? "08:00"}`;
    case "weekly": return `每周${WEEKDAYS.find((day) => day.value === parsed.weekday)?.label.replace("星期", "") ?? "一"} ${parsed.time ?? "08:00"}`;
    case "custom": {
      if (parsed.customRepeat === "hourly") return `每隔 ${parsed.customInterval ?? 1} 小时，在第 ${String(parsed.customMinute ?? 0).padStart(2, "0")} 分钟`;
      if (parsed.customRepeat === "daily") return `每隔 ${parsed.customInterval ?? 1} 天 ${parsed.time ?? "08:00"}`;
      if (parsed.customRepeat === "weekly") return `${(parsed.customWeekdays ?? [1]).map((value) => WEEKDAYS.find((day) => day.value === value)?.label.replace("星期", "") ?? "一").join("、")} ${parsed.time ?? "08:00"}`;
      if (parsed.customRepeat === "monthly") return `每隔 ${parsed.customInterval ?? 1} 个月的 ${parsed.customDay ?? 1} 日 ${parsed.time ?? "08:00"}`;
      if (parsed.customRepeat === "yearly") return `每年 ${parsed.customMonth ?? 1} 月 ${parsed.customDay ?? 1} 日 ${parsed.time ?? "08:00"}`;
      return "自定义频率";
    }
    default: return "仅手动运行";
  }
}
