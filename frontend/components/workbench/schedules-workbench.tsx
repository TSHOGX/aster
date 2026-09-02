"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Archive,
  CalendarClock,
  Check,
  ChevronRight,
  Clock3,
  Loader2,
  Pencil,
  Pause,
  Play,
  Plus,
  Sparkles,
  Trash2,
  X
} from "lucide-react";

import { useHarness, useSessions } from "@/components/harness-provider";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import {
  createSchedule,
  deleteSchedule,
  fetchScheduleRuns,
  fetchSchedules,
  runSchedule,
  updateSchedule
} from "@/lib/api";
import type { ApiSchedule, ApiScheduleRun } from "@/lib/types";

type ScheduleForm = {
  name: string;
  prompt: string;
  cron_expr: string;
  timezone: string;
  frequency: FrequencyMode;
  intervalMinutes: number;
  time: string;
  weekday: number;
  customRepeat: CustomRepeat;
  customInterval: number;
  customMinute: number;
  customWeekday: number;
  customDay: number;
  customMonth: number;
  customRawCron: string;
  timeout_seconds: number;
  max_retries: number;
  retry_backoff_seconds: number;
  result_mode: "new_session" | "existing_session";
  target_session_id: string;
};

type FrequencyMode = "manual" | "interval" | "daily" | "weekdays" | "weekly" | "custom";
type CustomRepeat = "hourly" | "daily" | "weekly" | "monthly" | "yearly";

const WEEKDAYS = [
  { value: 1, label: "星期一" },
  { value: 2, label: "星期二" },
  { value: 3, label: "星期三" },
  { value: 4, label: "星期四" },
  { value: 5, label: "星期五" },
  { value: 6, label: "星期六" },
  { value: 0, label: "星期日" }
] as const;
const MONTHS = Array.from({ length: 12 }, (_, index) => ({ value: index + 1, label: `${index + 1} 月` }));
const DAYS = Array.from({ length: 31 }, (_, index) => ({ value: index + 1, label: `${index + 1} 日` }));
const TIME_OPTIONS = Array.from({ length: 96 }, (_, index) => {
  const hour = Math.floor(index / 4);
  const minute = (index % 4) * 15;
  const value = `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
  return { value, label: value };
});

const EMPTY_FORM: ScheduleForm = {
  name: "",
  prompt: "",
  cron_expr: "",
  // Replaced with the browser's local zone when the editor opens.
  timezone: "UTC",
  frequency: "manual",
  intervalMinutes: 15,
  time: "08:00",
  weekday: 1,
  customRepeat: "hourly",
  customInterval: 1,
  customMinute: 0,
  customWeekday: 1,
  customDay: 1,
  customMonth: 1,
  customRawCron: "",
  timeout_seconds: 1800,
  max_retries: 0,
  retry_backoff_seconds: 30,
  result_mode: "new_session",
  target_session_id: ""
};

function localTimezone() {
  if (typeof Intl === "undefined") return "UTC";
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function timeParts(value: string) {
  const [hour, minute] = value.split(":").map(Number);
  return { hour: Number.isFinite(hour) ? hour : 0, minute: Number.isFinite(minute) ? minute : 0 };
}

function timeValue(hour: number, minute: number) {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

function parseCron(expr?: string | null): Partial<ScheduleForm> {
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

function buildCronExpression(form: ScheduleForm) {
  if (form.frequency === "manual") return "";
  if (form.frequency === "interval") return `*/${Math.min(59, Math.max(1, form.intervalMinutes))} * * * *`;
  const { hour, minute } = timeParts(form.time);
  if (form.frequency === "daily") return `${minute} ${hour} * * *`;
  if (form.frequency === "weekdays") return `${minute} ${hour} * * 1-5`;
  if (form.frequency === "weekly") return `${minute} ${hour} * * ${form.weekday}`;
  if (form.customRawCron) return form.customRawCron;
  if (form.customRepeat === "hourly") return `${Math.min(59, Math.max(0, form.customMinute))} */${Math.min(23, Math.max(1, form.customInterval))} * * *`;
  if (form.customRepeat === "daily") return `${minute} ${hour} */${Math.min(31, Math.max(1, form.customInterval))} * *`;
  if (form.customRepeat === "weekly") return `${minute} ${hour} * * ${form.customWeekday}`;
  if (form.customRepeat === "monthly") return `${minute} ${hour} ${Math.min(31, Math.max(1, form.customDay))} */${Math.min(12, Math.max(1, form.customInterval))} *`;
  return `${minute} ${hour} ${Math.min(31, Math.max(1, form.customDay))} ${Math.min(12, Math.max(1, form.customMonth))} *`;
}

function frequencyDescription(expr?: string | null) {
  const parsed = parseCron(expr);
  switch (parsed.frequency) {
    case "interval": return `每隔 ${parsed.intervalMinutes ?? 15} 分钟`;
    case "daily": return `每天 ${parsed.time ?? "08:00"}`;
    case "weekdays": return `每个工作日 ${parsed.time ?? "08:00"}`;
    case "weekly": return `每周${WEEKDAYS.find((day) => day.value === parsed.weekday)?.label.replace("星期", "") ?? "一"} ${parsed.time ?? "08:00"}`;
    case "custom": {
      if (parsed.customRepeat === "hourly") return `每隔 ${parsed.customInterval ?? 1} 小时，在第 ${String(parsed.customMinute ?? 0).padStart(2, "0")} 分钟`;
      if (parsed.customRepeat === "daily") return `每隔 ${parsed.customInterval ?? 1} 天 ${parsed.time ?? "08:00"}`;
      if (parsed.customRepeat === "weekly") return `每周${WEEKDAYS.find((day) => day.value === parsed.customWeekday)?.label.replace("星期", "") ?? "一"} ${parsed.time ?? "08:00"}`;
      if (parsed.customRepeat === "monthly") return `每隔 ${parsed.customInterval ?? 1} 个月的 ${parsed.customDay ?? 1} 日 ${parsed.time ?? "08:00"}`;
      if (parsed.customRepeat === "yearly") return `每年 ${parsed.customMonth ?? 1} 月 ${parsed.customDay ?? 1} 日 ${parsed.time ?? "08:00"}`;
      return "自定义频率";
    }
    default: return "仅手动运行";
  }
}

function formFromSchedule(schedule: ApiSchedule): ScheduleForm {
  return {
    ...EMPTY_FORM,
    name: schedule.name,
    prompt: schedule.prompt,
    cron_expr: schedule.cron_expr ?? "",
    timezone: schedule.timezone,
    ...parseCron(schedule.cron_expr),
    timeout_seconds: schedule.timeout_seconds,
    max_retries: schedule.max_retries,
    retry_backoff_seconds: schedule.retry_backoff_seconds,
    result_mode: schedule.result_mode ?? "new_session",
    target_session_id: schedule.target_session_id ?? ""
  };
}

function relative(value?: string | null) {
  if (!value) return "未运行";
  const diff = Date.now() - new Date(value).getTime();
  const minutes = Math.max(0, Math.round(diff / 60000));
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  return `${Math.round(hours / 24)} 天前`;
}

function statusLabel(schedule: ApiSchedule) {
  if (schedule.archived) return { text: "已归档", tone: "archived" };
  if (!schedule.enabled) return { text: "已暂停", tone: "muted" };
  return { text: "已开启", tone: "green" };
}

function runStatusLabel(status: ApiScheduleRun["status"]) {
  return {
    queued: "排队中",
    running: "运行中",
    succeeded: "已完成",
    failed: "失败",
    timed_out: "超时",
    canceled: "已取消"
  }[status];
}

export function SchedulesWorkbench({ onOpenSession }: { onOpenSession?: (id: string) => void }) {
  const { selectSession } = useHarness();
  const sessions = useSessions();
  const [schedules, setSchedules] = useState<ApiSchedule[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [runs, setRuns] = useState<ApiScheduleRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [runsLoading, setRunsLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<ScheduleForm>(EMPTY_FORM);
  const [error, setError] = useState<string | null>(null);
  const [showArchived, setShowArchived] = useState(false);

  const selected = useMemo(() => schedules.find((item) => item.id === selectedId && Boolean(item.archived) === showArchived) ?? null, [schedules, selectedId, showArchived]);
  const visibleSchedules = useMemo(() => schedules.filter((item) => Boolean(item.archived) === showArchived), [schedules, showArchived]);
  const targetSessions = useMemo(() => sessions.filter((session) => session.access !== "reader" && session.mode === "agent" && session.status !== "destroyed"), [sessions]);

  const refresh = useCallback(async () => {
    const result = await fetchSchedules();
    if (result.ok) {
      const next = result.data.schedules ?? [];
      setSchedules(next);
      setSelectedId((current) => {
        if (current && next.some((item) => item.id === current && Boolean(item.archived) === showArchived)) return current;
        return next.find((item) => Boolean(item.archived) === showArchived)?.id ?? null;
      });
    } else setError(result.error);
    setLoading(false);
  }, [showArchived]);

  useEffect(() => {
    const load = async () => { await refresh(); };
    void load();
  }, [refresh]);

  useEffect(() => {
    let cancelled = false;
    const load = async (initial = false) => {
      if (!selectedId) { if (initial) setRuns([]); return; }
      if (initial) {
        setRuns([]);
        setRunsLoading(true);
      }
      const result = await fetchScheduleRuns(selectedId);
      if (!cancelled) {
        if (result.ok) setRuns(result.data.runs ?? []);
        else setError(result.error);
        setRunsLoading(false);
      }
    };
    void load(true);
    const interval = selectedId ? window.setInterval(() => { void load(); }, 5000) : null;
    return () => { cancelled = true; if (interval !== null) window.clearInterval(interval); };
  }, [selectedId]);

  const openCreate = (initial: ScheduleForm = EMPTY_FORM) => {
    setEditingId(null);
    setForm({ ...initial, timezone: initial.timezone === "UTC" ? localTimezone() : initial.timezone, ...parseCron(initial.cron_expr) });
    setEditorOpen(true);
    setError(null);
  };
  const openEdit = () => { if (selected) { setEditingId(selected.id); setForm(formFromSchedule(selected)); setEditorOpen(true); setError(null); } };
  const updateForm = <K extends keyof ScheduleForm>(key: K, value: ScheduleForm[K]) => setForm((current) => ({ ...current, [key]: value, ...(key === "frequency" || key.startsWith("custom") ? { customRawCron: "" } : {}) }));

  const save = async () => {
    if (!form.name.trim() || !form.prompt.trim()) { setError("请填写任务名称和提示词"); return; }
    if (form.result_mode === "existing_session" && !form.target_session_id) { setError("请选择结果写入的会话"); return; }
    setSaving(true); setError(null);
    const payload = {
      name: form.name.trim(),
      prompt: form.prompt.trim(),
      cron_expr: buildCronExpression(form),
      timezone: form.timezone.trim() || localTimezone(),
      timeout_seconds: form.timeout_seconds,
      max_retries: form.max_retries,
      retry_backoff_seconds: form.retry_backoff_seconds,
      result_mode: form.result_mode,
      target_session_id: form.result_mode === "existing_session" ? form.target_session_id : ""
    };
    const editing = editingId ? schedules.find((item) => item.id === editingId) : null;
    const result = editing ? await updateSchedule(editing.id, { ...payload, version: editing.version }) : await createSchedule(payload);
    setSaving(false);
    if (!result.ok) { setError(result.error); return; }
    setSchedules((items) => items.some((item) => item.id === result.data.id) ? items.map((item) => item.id === result.data.id ? result.data : item) : [result.data, ...items]);
    setSelectedId(result.data.id); setEditorOpen(false); setEditingId(null); setForm(EMPTY_FORM);
  };

  const toggle = async () => {
    if (!selected || selected.archived) return;
    const result = await updateSchedule(selected.id, { enabled: !selected.enabled, version: selected.version });
    if (!result.ok) { setError(result.error); return; }
    setSchedules((items) => items.map((item) => item.id === result.data.id ? result.data : item));
  };

  const archive = async () => {
    if (!selected) return;
    const archiving = !selected.archived;
    if (!window.confirm(`${archiving ? "归档" : "恢复"}“${selected.name}”？`)) return;
    const result = await updateSchedule(selected.id, { archived: archiving, version: selected.version });
    if (!result.ok) { setError(result.error); return; }
    setSchedules((items) => items.map((item) => item.id === result.data.id ? result.data : item));
    if (Boolean(result.data.archived) !== showArchived) setSelectedId(null);
  };

  const runNow = async () => {
    if (!selected || selected.archived) return;
    setRunning(true); setError(null);
    const result = await runSchedule(selected.id);
    setRunning(false);
    if (!result.ok) { setError(result.error); return; }
    setRuns((items) => [result.data, ...items]);
  };

  const remove = async () => {
    if (!selected || !window.confirm(`永久删除“${selected.name}”？`)) return;
    const result = await deleteSchedule(selected.id);
    if (!result.ok) { setError(result.error); return; }
    const next = schedules.filter((item) => item.id !== selected.id);
    setSchedules(next); setSelectedId(next.find((item) => Boolean(item.archived) === showArchived)?.id ?? null);
  };

  const openRun = (run: ApiScheduleRun) => {
    if (!run.session_id) return;
    if (onOpenSession) onOpenSession(run.session_id);
    else selectSession(run.session_id);
  };

  return (
    <div className="grid h-full min-h-0 min-w-0 grid-rows-[minmax(0,45%)_minmax(0,55%)] grid-cols-1 overflow-hidden bg-[var(--color-background)] md:grid-rows-1 md:grid-cols-[minmax(250px,320px)_minmax(0,1fr)]">
      <section className="min-h-0 overflow-y-auto border-r border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="sticky top-0 z-10 border-b border-[var(--color-border)] bg-[var(--color-surface)]/95 px-5 pb-4 pt-5 backdrop-blur"><div><p className="text-xs font-medium text-[var(--color-accent)]">任务中心</p><h1 className="mt-1 text-xl font-semibold">自动化</h1></div><div className="mt-4 flex items-center gap-1 rounded-[var(--radius)] bg-[var(--color-surface-muted)] p-1 text-xs"><button className={cn("flex-1 rounded px-2 py-1.5", !showArchived && "bg-[var(--color-surface)] font-medium shadow-sm")} onClick={() => { setShowArchived(false); setSelectedId((current) => schedules.find((item) => !item.archived && item.id === current)?.id ?? schedules.find((item) => !item.archived)?.id ?? null); }}>进行中</button><button className={cn("flex-1 rounded px-2 py-1.5", showArchived && "bg-[var(--color-surface)] font-medium shadow-sm")} onClick={() => { setShowArchived(true); setSelectedId((current) => schedules.find((item) => item.archived && item.id === current)?.id ?? schedules.find((item) => item.archived)?.id ?? null); }}>已归档</button></div></div>
        <div className="space-y-2 px-4 py-4"><Button variant="ghost" size="lg" className="w-full justify-start gap-2 border border-transparent text-sm hover:border-[var(--color-border)]" onClick={() => openCreate()}><Plus className="h-4 w-4" /> 创建定时任务</Button>{loading ? <div className="flex justify-center py-12"><Loader2 className="h-5 w-5 animate-spin text-[var(--color-accent)]" /></div> : visibleSchedules.length === 0 ? <EmptyState archived={showArchived} onCreate={() => openCreate()} /> : visibleSchedules.map((item) => { const status = statusLabel(item); return <button key={item.id} onClick={() => setSelectedId(item.id)} className={cn("w-full rounded-[var(--radius-lg)] border p-3.5 text-left transition-colors", item.id === selectedId ? "border-[var(--color-accent)] bg-[var(--color-background)] shadow-sm" : "border-transparent hover:border-[var(--color-border-strong)] hover:bg-[var(--color-background)]")}><div className="flex items-start gap-3"><span className={cn("mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full", status.tone === "green" ? "bg-emerald-500" : status.tone === "archived" ? "bg-slate-400" : "bg-amber-400")} /><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{item.name}</span><span className="mt-1 block truncate text-xs text-[var(--color-foreground-muted)]">{frequencyDescription(item.cron_expr)}{item.cron_expr ? ` · ${item.timezone}` : ""}</span><span className="mt-2 block text-xs text-[var(--color-foreground-muted)]">{relative(item.last_run_at)} · {status.text}</span></span></div></button>; })}{!showArchived ? <button onClick={() => openCreate({ ...EMPTY_FORM, name: "每日简报", prompt: "给我一份晨间简报，包含我的日程安排、重要未读邮件，以及今天需要我关注的事项。", cron_expr: "0 8 * * *" })} className="mt-5 flex w-full items-start gap-3 rounded-[var(--radius-lg)] border border-dashed border-[var(--color-border-strong)] p-3.5 text-left hover:border-[var(--color-accent)]"><Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-violet-500" /><span><span className="block text-sm font-medium">每日简报</span><span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">每天早上整理你最需要关注的信息</span></span></button> : null}</div>
      </section>

      <section className="flex min-h-0 flex-col overflow-hidden bg-[var(--color-background)]"><div className="z-10 flex shrink-0 items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-4"><div className="flex items-center gap-2 text-sm font-medium"><CalendarClock className="h-4 w-4 text-[var(--color-accent)]" /> {selected ? selected.name : "定时任务"}</div><div className="flex items-center gap-1">{selected ? <><Button variant="ghost" size="icon" onClick={openEdit} title="编辑" aria-label="编辑"><Pencil className="h-4 w-4" /></Button><Button variant="ghost" size="icon" onClick={() => void archive()} title={selected.archived ? "恢复" : "归档"} aria-label={selected.archived ? "恢复" : "归档"}><Archive className="h-4 w-4" /></Button><Button variant="danger" size="icon" onClick={() => void remove()} title="永久删除" aria-label="永久删除"><Trash2 className="h-4 w-4" /></Button></> : null}</div></div><div className="min-h-0 flex-1 overflow-y-auto">{selected ? <div className="mx-auto max-w-3xl px-6 py-7 sm:px-10"><div className="flex flex-wrap items-start justify-between gap-4"><div><span className="text-xs font-medium text-[var(--color-accent)]">{selected.archived ? "已归档" : selected.enabled ? "已开启" : "已暂停"}</span><h2 className="mt-1 text-2xl font-semibold">{selected.name}</h2><p className="mt-2 text-sm text-[var(--color-foreground-muted)]">{selected.cron_expr ? `${frequencyDescription(selected.cron_expr)} · ${selected.timezone}` : "手动触发任务"}</p></div><div className="flex items-center gap-2"><Button variant="ghost" className="gap-2" disabled={selected.archived} onClick={() => void toggle()}>{selected.enabled ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}{selected.enabled ? "暂停" : "启用"}</Button><Button variant="primary" className="gap-2" disabled={running || selected.archived} onClick={() => void runNow()}>{running ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} 立即运行</Button></div></div><div className="mt-8 divide-y rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]"><DetailRow label="结果写入" value={selected.result_mode === "existing_session" ? `已有会话 · ${selected.target_session_id ?? "未选择"}` : "每次运行创建新会话"} /><DetailRow label="超时" value={`${selected.timeout_seconds} 秒`} /><DetailRow label="重试" value={`${selected.max_retries} 次 · 间隔 ${selected.retry_backoff_seconds} 秒`} /><DetailRow label="状态" value={selected.archived ? "已归档" : selected.enabled ? "已开启" : "已暂停"} /></div><div className="mt-6 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-5"><p className="text-xs font-medium text-[var(--color-foreground-muted)]">任务提示词</p><p className="mt-3 whitespace-pre-wrap text-sm leading-6">{selected.prompt}</p></div><div className="mt-8"><div className="mb-3 flex items-center justify-between"><h3 className="text-sm font-semibold">运行历史</h3><span className="text-xs text-[var(--color-foreground-muted)]">{runs.length} 次</span></div>{runsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : runs.length === 0 ? <p className="text-sm text-[var(--color-foreground-muted)]">还没有运行记录。</p> : <div className="space-y-2">{runs.map((run) => <button key={run.id} onClick={() => openRun(run)} disabled={!run.session_id} className="flex w-full items-center justify-between rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3 text-left text-xs transition-colors hover:border-[var(--color-accent)] disabled:cursor-default disabled:hover:border-[var(--color-border)]"><span><span className="block">{new Date(run.scheduled_at).toLocaleString("zh-CN")}</span><span className="mt-1 block text-[var(--color-foreground-muted)]">{run.session_id ? "打开对应会话" : "尚未关联会话"}</span></span><span className={cn("flex items-center gap-1 font-medium", run.status === "succeeded" ? "text-emerald-600" : run.status === "running" || run.status === "queued" ? "text-amber-600" : "text-[var(--color-danger)]")}>{runStatusLabel(run.status)}{run.session_id ? <ChevronRight className="h-3.5 w-3.5" /> : null}</span></button>)}</div>}</div></div> : <div className="flex h-full items-center justify-center px-6 text-center"><div><Clock3 className="mx-auto h-10 w-10 text-[var(--color-foreground-muted)]" /><h2 className="mt-4 text-lg font-semibold">选择一个定时任务</h2><p className="mt-2 max-w-sm text-sm text-[var(--color-foreground-muted)]">在这里创建、编辑和追踪自动化运行。每次运行都会保留为可回看的会话。</p></div></div>}</div></section>

      {editorOpen ? <ScheduleEditorV2 form={form} selected={Boolean(editingId)} sessions={targetSessions} saving={saving} onChange={updateForm} onClose={() => { setEditorOpen(false); setEditingId(null); }} onSave={() => void save()} /> : null}
      {error ? <div className="fixed bottom-5 right-5 z-50 max-w-sm rounded-[var(--radius)] bg-[var(--color-danger)] px-4 py-3 text-sm text-white shadow-lg">{error}<button className="ml-3" onClick={() => setError(null)} aria-label="关闭"><X className="inline h-4 w-4" /></button></div> : null}
    </div>
  );
}

function ScheduleEditorV2({ form, selected, sessions, saving, onChange, onClose, onSave }: { form: ScheduleForm; selected: boolean; sessions: ReturnType<typeof useSessions>; saving: boolean; onChange: <K extends keyof ScheduleForm>(key: K, value: ScheduleForm[K]) => void; onClose: () => void; onSave: () => void }) {
  const frequencyOptions: { value: FrequencyMode; label: string; description: string }[] = [
    { value: "manual", label: "仅手动", description: "需要时手动运行，不自动执行" },
    { value: "interval", label: "间隔", description: "每隔几分钟重复执行" },
    { value: "daily", label: "每天", description: "每天在固定时间运行" },
    { value: "weekdays", label: "工作日", description: "周一至周五在固定时间运行" },
    { value: "weekly", label: "每周", description: "每周指定一天和时间运行" },
    { value: "custom", label: "自定义", description: "按小时、天、月或年设置" }
  ];
  const selectNumber = (key: keyof ScheduleForm, value: string) => onChange(key as never, Number(value) as never);
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--color-overlay)] p-4"><div className="flex max-h-[92vh] w-full max-w-2xl flex-col overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)]"><div className="flex shrink-0 items-center justify-between border-b border-[var(--color-border)] px-6 py-5"><div><p className="text-xs font-medium text-[var(--color-accent)]">{selected ? "编辑任务" : "新建任务"}</p><h2 className="mt-1 text-lg font-semibold">任务配置</h2></div><Button variant="ghost" size="icon" onClick={onClose} aria-label="关闭"><X className="h-5 w-5" /></Button></div><div className="min-h-0 flex-1 overflow-y-auto px-6 py-5"><div className="space-y-5"><Field label="名称"><input value={form.name} onChange={(e) => onChange("name", e.target.value)} placeholder="例如：每日简报" className={inputClass} /></Field><Field label="提示词"><textarea value={form.prompt} onChange={(e) => onChange("prompt", e.target.value)} rows={5} placeholder="告诉 Agent 每次运行要完成什么" className={`${inputClass} resize-y`} /></Field><fieldset><legend className="mb-2 text-sm font-medium">运行频率</legend><div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label="运行频率">{frequencyOptions.map((option) => <button key={option.value} type="button" role="radio" aria-checked={form.frequency === option.value} onClick={() => onChange("frequency", option.value)} className={cn("rounded-[var(--radius)] border p-3 text-left transition-colors", form.frequency === option.value ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)] hover:border-[var(--color-border-strong)]")}><span className="block text-sm font-medium">{option.label}</span><span className="mt-1 block text-xs leading-5 text-[var(--color-foreground-muted)]">{option.description}</span></button>)}</div></fieldset>{form.frequency === "manual" ? <div className="rounded-[var(--radius)] bg-[var(--color-surface-muted)] px-3 py-2.5 text-xs text-[var(--color-foreground-muted)]">此任务不会自动运行，你仍可以在详情页随时点击“立即运行”。</div> : null}{form.frequency === "interval" ? <Field label="每隔几分钟运行一次"><input type="number" min={1} max={59} step={1} value={form.intervalMinutes} onChange={(e) => selectNumber("intervalMinutes", e.target.value)} className={inputClass} /><span className="mt-1.5 block text-xs text-[var(--color-foreground-muted)]">支持 1–59 分钟，适合轮询和提醒类任务。</span></Field> : null}{form.frequency === "daily" || form.frequency === "weekdays" ? <Field label="运行时间"><TimeSelect value={form.time} onChange={(value) => onChange("time", value)} /></Field> : null}{form.frequency === "weekly" ? <div className="grid gap-4 sm:grid-cols-2"><Field label="运行日"><select value={form.weekday} onChange={(e) => selectNumber("weekday", e.target.value)} className={inputClass}>{WEEKDAYS.map((day) => <option key={day.value} value={day.value}>{day.label}</option>)}</select></Field><Field label="运行时间"><TimeSelect value={form.time} onChange={(value) => onChange("time", value)} /></Field></div> : null}{form.frequency === "custom" ? <div className="space-y-4 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-background)] p-4"><Field label="重复类型"><select value={form.customRepeat} onChange={(e) => onChange("customRepeat", e.target.value as CustomRepeat)} className={inputClass}><option value="hourly">每小时</option><option value="daily">每天</option><option value="weekly">每周</option><option value="monthly">每月</option><option value="yearly">每年</option></select></Field>{form.customRepeat === "hourly" ? <div className="grid gap-4 sm:grid-cols-2"><Field label="每隔多少小时"><input type="number" min={1} max={23} value={form.customInterval} onChange={(e) => selectNumber("customInterval", e.target.value)} className={inputClass} /></Field><Field label="在第几分钟"><select value={form.customMinute} onChange={(e) => selectNumber("customMinute", e.target.value)} className={inputClass}>{Array.from({ length: 60 }, (_, minute) => <option key={minute} value={minute}>{minute} 分</option>)}</select></Field></div> : null}{form.customRepeat === "daily" ? <div className="grid gap-4 sm:grid-cols-2"><Field label="每隔多少天"><input type="number" min={1} max={31} value={form.customInterval} onChange={(e) => selectNumber("customInterval", e.target.value)} className={inputClass} /></Field><Field label="运行时间"><TimeSelect value={form.time} onChange={(value) => onChange("time", value)} /></Field></div> : null}{form.customRepeat === "weekly" ? <div className="grid gap-4 sm:grid-cols-2"><Field label="运行日"><select value={form.customWeekday} onChange={(e) => selectNumber("customWeekday", e.target.value)} className={inputClass}>{WEEKDAYS.map((day) => <option key={day.value} value={day.value}>{day.label}</option>)}</select></Field><Field label="运行时间"><TimeSelect value={form.time} onChange={(value) => onChange("time", value)} /></Field></div> : null}{form.customRepeat === "monthly" ? <div className="grid gap-4 sm:grid-cols-3"><Field label="每隔几个月"><input type="number" min={1} max={12} value={form.customInterval} onChange={(e) => selectNumber("customInterval", e.target.value)} className={inputClass} /></Field><Field label="每月第几日"><select value={form.customDay} onChange={(e) => selectNumber("customDay", e.target.value)} className={inputClass}>{DAYS.map((day) => <option key={day.value} value={day.value}>{day.label}</option>)}</select></Field><Field label="运行时间"><TimeSelect value={form.time} onChange={(value) => onChange("time", value)} /></Field></div> : null}{form.customRepeat === "yearly" ? <div className="grid gap-4 sm:grid-cols-3"><Field label="月份"><select value={form.customMonth} onChange={(e) => selectNumber("customMonth", e.target.value)} className={inputClass}>{MONTHS.map((month) => <option key={month.value} value={month.value}>{month.label}</option>)}</select></Field><Field label="日期"><select value={form.customDay} onChange={(e) => selectNumber("customDay", e.target.value)} className={inputClass}>{DAYS.map((day) => <option key={day.value} value={day.value}>{day.label}</option>)}</select></Field><Field label="运行时间"><TimeSelect value={form.time} onChange={(value) => onChange("time", value)} /></Field></div> : null}<p className="text-xs leading-5 text-[var(--color-foreground-muted)]">每月日期不存在时，该月会自动跳过；所有时间均按当地时区执行。</p></div> : null}<div className="grid gap-4 sm:grid-cols-2"><Field label="超时（秒）"><input type="number" min={60} value={form.timeout_seconds} onChange={(e) => selectNumber("timeout_seconds", e.target.value)} className={inputClass} /></Field><Field label="最大重试次数"><input type="number" min={0} max={10} value={form.max_retries} onChange={(e) => selectNumber("max_retries", e.target.value)} className={inputClass} /></Field><Field label="重试间隔（秒）"><input type="number" min={0} value={form.retry_backoff_seconds} onChange={(e) => selectNumber("retry_backoff_seconds", e.target.value)} className={inputClass} /></Field></div><fieldset><legend className="mb-2 text-sm font-medium">运行结果写入</legend><div className="grid gap-2 sm:grid-cols-2"><label className={cn("cursor-pointer rounded-[var(--radius)] border p-3 text-sm", form.result_mode === "new_session" ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)]")}><input type="radio" className="sr-only" checked={form.result_mode === "new_session"} onChange={() => onChange("result_mode", "new_session")} />新建会话<span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">保留独立上下文，运行历史可直接打开</span></label><label className={cn("cursor-pointer rounded-[var(--radius)] border p-3 text-sm", form.result_mode === "existing_session" ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)]")}><input type="radio" className="sr-only" checked={form.result_mode === "existing_session"} onChange={() => onChange("result_mode", "existing_session")} />写入已有会话<span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">沿用该会话上下文和工作区文件</span></label></div></fieldset>{form.result_mode === "existing_session" ? <Field label="目标会话"><select value={form.target_session_id} onChange={(e) => onChange("target_session_id", e.target.value)} className={inputClass}><option value="">请选择会话</option>{sessions.map((session) => <option key={session.id} value={session.id}>{session.title?.trim() || session.id}</option>)}</select></Field> : null}</div></div><div className="flex shrink-0 justify-end gap-2 border-t border-[var(--color-border)] px-6 py-4"><Button variant="ghost" onClick={onClose}>取消</Button><Button variant="primary" disabled={saving} onClick={onSave}>{saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}{selected ? "保存更改" : "创建任务"}</Button></div></div></div>;
}

function TimeSelect({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <select value={value} onChange={(event) => onChange(event.target.value)} className={inputClass}>{TIME_OPTIONS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select>;
}

function ScheduleEditor({ form, selected, sessions, saving, onChange, onClose, onSave }: { form: ScheduleForm; selected: boolean; sessions: ReturnType<typeof useSessions>; saving: boolean; onChange: <K extends keyof ScheduleForm>(key: K, value: ScheduleForm[K]) => void; onClose: () => void; onSave: () => void }) {
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--color-overlay)] p-4"><div className="max-h-[90vh] w-full max-w-2xl overflow-y-auto rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-xl)]"><div className="flex items-center justify-between"><div><p className="text-xs font-medium text-[var(--color-accent)]">{selected ? "编辑任务" : "新建任务"}</p><h2 className="mt-1 text-lg font-semibold">任务配置</h2></div><Button variant="ghost" size="icon" onClick={onClose} aria-label="关闭"><X className="h-5 w-5" /></Button></div><div className="mt-5 space-y-4"><Field label="名称"><input value={form.name} onChange={(e) => onChange("name", e.target.value)} placeholder="例如：每日简报" className={inputClass} /></Field><Field label="提示词"><textarea value={form.prompt} onChange={(e) => onChange("prompt", e.target.value)} rows={5} placeholder="告诉 Agent 每次运行要完成什么" className={`${inputClass} resize-y`} /></Field><div className="grid gap-4 sm:grid-cols-2"><Field label="Cron（留空表示仅手动）"><input value={form.cron_expr} onChange={(e) => onChange("cron_expr", e.target.value)} placeholder="0 8 * * *" className={inputClass} /></Field><Field label="时区"><input value={form.timezone} onChange={(e) => onChange("timezone", e.target.value)} placeholder="Asia/Shanghai" className={inputClass} /></Field><Field label="超时（秒）"><input type="number" min={60} value={form.timeout_seconds} onChange={(e) => onChange("timeout_seconds", Number(e.target.value))} className={inputClass} /></Field><Field label="最大重试次数"><input type="number" min={0} max={10} value={form.max_retries} onChange={(e) => onChange("max_retries", Number(e.target.value))} className={inputClass} /></Field><Field label="重试间隔（秒）"><input type="number" min={0} value={form.retry_backoff_seconds} onChange={(e) => onChange("retry_backoff_seconds", Number(e.target.value))} className={inputClass} /></Field></div><fieldset><legend className="mb-2 text-sm font-medium">运行结果写入</legend><div className="grid gap-2 sm:grid-cols-2"><label className={cn("cursor-pointer rounded-[var(--radius)] border p-3 text-sm", form.result_mode === "new_session" ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)]")}><input type="radio" className="sr-only" checked={form.result_mode === "new_session"} onChange={() => onChange("result_mode", "new_session")} />新建会话<span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">保留独立上下文，运行历史可直接打开</span></label><label className={cn("cursor-pointer rounded-[var(--radius)] border p-3 text-sm", form.result_mode === "existing_session" ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)]")}><input type="radio" className="sr-only" checked={form.result_mode === "existing_session"} onChange={() => onChange("result_mode", "existing_session")} />写入已有会话<span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">沿用该会话上下文和工作区文件</span></label></div></fieldset>{form.result_mode === "existing_session" ? <Field label="目标会话"><select value={form.target_session_id} onChange={(e) => onChange("target_session_id", e.target.value)} className={inputClass}><option value="">请选择会话</option>{sessions.map((session) => <option key={session.id} value={session.id}>{session.title?.trim() || session.id}</option>)}</select></Field> : null}</div><div className="mt-6 flex justify-end gap-2"><Button variant="ghost" onClick={onClose}>取消</Button><Button variant="primary" disabled={saving} onClick={onSave}>{saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}{selected ? "保存更改" : "创建任务"}</Button></div></div></div>;
}

const inputClass = "w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm outline-none transition-colors focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent-soft)]";
function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block text-sm"><span className="mb-1.5 block font-medium">{label}</span>{children}</label>; }
function DetailRow({ label, value }: { label: string; value: React.ReactNode }) { return <div className="flex items-center justify-between gap-4 px-5 py-4 text-sm"><span className="text-[var(--color-foreground-muted)]">{label}</span><span className="text-right">{value}</span></div>; }
function EmptyState({ archived, onCreate }: { archived: boolean; onCreate: () => void }) { return <div className="py-10 text-center"><p className="text-sm text-[var(--color-foreground-muted)]">{archived ? "还没有归档任务" : "还没有安排任务"}</p>{!archived ? <button onClick={onCreate} className="mt-3 text-sm font-medium text-[var(--color-accent)] hover:underline">创建第一个任务</button> : null}</div>; }
