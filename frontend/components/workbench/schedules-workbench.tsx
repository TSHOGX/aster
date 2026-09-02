"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Archive,
  CalendarClock,
  Check,
  ChevronRight,
  Clock3,
  Loader2,
  MoreHorizontal,
  Pause,
  Play,
  Plus,
  RefreshCw,
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
  timeout_seconds: number;
  max_retries: number;
  retry_backoff_seconds: number;
  result_mode: "new_session" | "existing_session";
  target_session_id: string;
};

const EMPTY_FORM: ScheduleForm = {
  name: "",
  prompt: "",
  cron_expr: "",
  timezone: "Asia/Shanghai",
  timeout_seconds: 1800,
  max_retries: 0,
  retry_backoff_seconds: 30,
  result_mode: "new_session",
  target_session_id: ""
};

function formFromSchedule(schedule: ApiSchedule): ScheduleForm {
  return {
    name: schedule.name,
    prompt: schedule.prompt,
    cron_expr: schedule.cron_expr ?? "",
    timezone: schedule.timezone,
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

  const selected = useMemo(() => schedules.find((item) => item.id === selectedId) ?? null, [schedules, selectedId]);
  const visibleSchedules = useMemo(() => schedules.filter((item) => Boolean(item.archived) === showArchived), [schedules, showArchived]);
  const targetSessions = useMemo(() => sessions.filter((session) => session.access !== "reader" && session.status !== "destroyed"), [sessions]);

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
    const load = async () => {
      if (!selectedId) { setRuns([]); return; }
      setRunsLoading(true);
      const result = await fetchScheduleRuns(selectedId);
      if (!cancelled) {
        if (result.ok) setRuns(result.data.runs ?? []);
        else setError(result.error);
        setRunsLoading(false);
      }
    };
    void load();
    const interval = selectedId ? window.setInterval(() => { void load(); }, 5000) : null;
    return () => { cancelled = true; if (interval !== null) window.clearInterval(interval); };
  }, [selectedId]);

  const openCreate = (initial: ScheduleForm = EMPTY_FORM) => { setEditingId(null); setForm(initial); setEditorOpen(true); setError(null); };
  const openEdit = () => { if (selected) { setEditingId(selected.id); setForm(formFromSchedule(selected)); setEditorOpen(true); setError(null); } };
  const updateForm = <K extends keyof ScheduleForm>(key: K, value: ScheduleForm[K]) => setForm((current) => ({ ...current, [key]: value }));

  const save = async () => {
    if (!form.name.trim() || !form.prompt.trim()) { setError("请填写任务名称和提示词"); return; }
    if (form.result_mode === "existing_session" && !form.target_session_id) { setError("请选择结果写入的会话"); return; }
    setSaving(true); setError(null);
    const payload = { ...form, name: form.name.trim(), prompt: form.prompt.trim(), cron_expr: form.cron_expr.trim(), timezone: form.timezone.trim(), target_session_id: form.result_mode === "existing_session" ? form.target_session_id : "" };
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
    const result = await updateSchedule(selected.id, { archived: archiving, enabled: archiving ? false : true, version: selected.version });
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
    selectSession(run.session_id); onOpenSession?.(run.session_id);
  };

  return (
    <div className="grid h-full min-h-0 min-w-0 grid-cols-[minmax(250px,320px)_minmax(0,1fr)] overflow-hidden bg-[var(--color-background)]">
      <section className="min-h-0 overflow-y-auto border-r border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="sticky top-0 z-10 border-b border-[var(--color-border)] bg-[var(--color-surface)]/95 px-5 pb-4 pt-5 backdrop-blur"><div className="flex items-center justify-between"><div><p className="text-xs font-medium text-[var(--color-accent)]">任务中心</p><h1 className="mt-1 text-xl font-semibold">自动化</h1></div><Button variant="ghost" size="icon" onClick={() => void refresh()} title="刷新" aria-label="刷新"><RefreshCw className="h-4 w-4" /></Button></div><div className="mt-4 flex items-center gap-1 rounded-[var(--radius)] bg-[var(--color-surface-muted)] p-1 text-xs"><button className={cn("flex-1 rounded px-2 py-1.5", !showArchived && "bg-[var(--color-surface)] font-medium shadow-sm")} onClick={() => setShowArchived(false)}>进行中</button><button className={cn("flex-1 rounded px-2 py-1.5", showArchived && "bg-[var(--color-surface)] font-medium shadow-sm")} onClick={() => setShowArchived(true)}>已归档</button></div></div>
        <div className="space-y-2 px-4 py-4"><Button variant="ghost" size="lg" className="w-full justify-start gap-2 border border-transparent text-sm hover:border-[var(--color-border)]" onClick={() => openCreate()}><Plus className="h-4 w-4" /> 创建定时任务</Button>{loading ? <div className="flex justify-center py-12"><Loader2 className="h-5 w-5 animate-spin text-[var(--color-accent)]" /></div> : visibleSchedules.length === 0 ? <EmptyState archived={showArchived} onCreate={() => openCreate()} /> : visibleSchedules.map((item) => { const status = statusLabel(item); return <button key={item.id} onClick={() => setSelectedId(item.id)} className={cn("group w-full rounded-[var(--radius-lg)] border p-3.5 text-left transition-colors", item.id === selectedId ? "border-[var(--color-accent)] bg-[var(--color-background)] shadow-sm" : "border-transparent hover:border-[var(--color-border-strong)] hover:bg-[var(--color-background)]")}><div className="flex items-start gap-3"><span className={cn("mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full", status.tone === "green" ? "bg-emerald-500" : status.tone === "archived" ? "bg-slate-400" : "bg-amber-400")} /><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{item.name}</span><span className="mt-1 block truncate text-xs text-[var(--color-foreground-muted)]">{item.cron_expr ? `${item.cron_expr} · ${item.timezone}` : "手动运行"}</span><span className="mt-2 block text-xs text-[var(--color-foreground-muted)]">{relative(item.last_run_at)} · {status.text}</span></span><MoreHorizontal className="h-4 w-4 shrink-0 text-[var(--color-foreground-muted)] opacity-0 transition-opacity group-hover:opacity-100" /></div></button>; })}{!showArchived ? <button onClick={() => openCreate({ ...EMPTY_FORM, name: "每日简报", prompt: "给我一份晨间简报，包含我的日程安排、重要未读邮件，以及今天需要我关注的事项。", cron_expr: "0 8 * * *" })} className="mt-5 flex w-full items-start gap-3 rounded-[var(--radius-lg)] border border-dashed border-[var(--color-border-strong)] p-3.5 text-left hover:border-[var(--color-accent)]"><Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-violet-500" /><span><span className="block text-sm font-medium">每日简报</span><span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">每天早上整理你最需要关注的信息</span></span></button> : null}</div>
      </section>

      <section className="min-h-0 overflow-y-auto bg-[var(--color-background)]"><div className="flex items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-4"><div className="flex items-center gap-2 text-sm font-medium"><CalendarClock className="h-4 w-4 text-[var(--color-accent)]" /> {selected ? selected.name : "定时任务"}</div><div className="flex items-center gap-1">{selected ? <><Button variant="ghost" size="icon" onClick={openEdit} title="编辑" aria-label="编辑"><MoreHorizontal className="h-4 w-4" /></Button><Button variant="ghost" size="icon" onClick={() => void archive()} title={selected.archived ? "恢复" : "归档"} aria-label={selected.archived ? "恢复" : "归档"}><Archive className="h-4 w-4" /></Button><Button variant="danger" size="icon" onClick={() => void remove()} title="永久删除" aria-label="永久删除"><Trash2 className="h-4 w-4" /></Button></> : null}</div></div>{selected ? <div className="mx-auto max-w-3xl px-6 py-7 sm:px-10"><div className="flex flex-wrap items-start justify-between gap-4"><div><span className="text-xs font-medium text-[var(--color-accent)]">{selected.archived ? "已归档" : selected.enabled ? "已开启" : "已暂停"}</span><h2 className="mt-1 text-2xl font-semibold">{selected.name}</h2><p className="mt-2 text-sm text-[var(--color-foreground-muted)]">{selected.cron_expr ? `按 ${selected.cron_expr} · ${selected.timezone} 运行` : "手动触发任务"}</p></div><div className="flex items-center gap-2"><Button variant="ghost" className="gap-2" disabled={selected.archived} onClick={() => void toggle()}>{selected.enabled ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}{selected.enabled ? "暂停" : "启用"}</Button><Button variant="primary" className="gap-2" disabled={running || selected.archived} onClick={() => void runNow()}>{running ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} 立即运行</Button></div></div><div className="mt-8 divide-y rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]"><DetailRow label="结果写入" value={selected.result_mode === "existing_session" ? `已有会话 · ${selected.target_session_id ?? "未选择"}` : "每次运行创建新会话"} /><DetailRow label="超时" value={`${selected.timeout_seconds} 秒`} /><DetailRow label="重试" value={`${selected.max_retries} 次 · 间隔 ${selected.retry_backoff_seconds} 秒`} /><DetailRow label="状态" value={selected.archived ? "已归档" : selected.enabled ? "已开启" : "已暂停"} /></div><div className="mt-6 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-5"><p className="text-xs font-medium text-[var(--color-foreground-muted)]">任务提示词</p><p className="mt-3 whitespace-pre-wrap text-sm leading-6">{selected.prompt}</p></div><div className="mt-8"><div className="mb-3 flex items-center justify-between"><h3 className="text-sm font-semibold">运行历史</h3><span className="text-xs text-[var(--color-foreground-muted)]">{runs.length} 次</span></div>{runsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : runs.length === 0 ? <p className="text-sm text-[var(--color-foreground-muted)]">还没有运行记录。</p> : <div className="space-y-2">{runs.map((run) => <button key={run.id} onClick={() => openRun(run)} disabled={!run.session_id} className="flex w-full items-center justify-between rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3 text-left text-xs transition-colors hover:border-[var(--color-accent)] disabled:cursor-default disabled:hover:border-[var(--color-border)]"><span><span className="block">{new Date(run.scheduled_at).toLocaleString("zh-CN")}</span><span className="mt-1 block text-[var(--color-foreground-muted)]">{run.session_id ? "打开对应会话" : "尚未关联会话"}</span></span><span className={cn("flex items-center gap-1 font-medium", run.status === "succeeded" ? "text-emerald-600" : run.status === "running" || run.status === "queued" ? "text-amber-600" : "text-[var(--color-danger)]")}>{runStatusLabel(run.status)}{run.session_id ? <ChevronRight className="h-3.5 w-3.5" /> : null}</span></button>)}</div>}</div></div> : <div className="flex h-full items-center justify-center px-6 text-center"><div><Clock3 className="mx-auto h-10 w-10 text-[var(--color-foreground-muted)]" /><h2 className="mt-4 text-lg font-semibold">选择一个定时任务</h2><p className="mt-2 max-w-sm text-sm text-[var(--color-foreground-muted)]">在这里创建、编辑和追踪自动化运行。每次运行都会保留为可回看的会话。</p></div></div>}</section>

      {editorOpen ? <ScheduleEditor form={form} selected={Boolean(editingId)} sessions={targetSessions} saving={saving} onChange={updateForm} onClose={() => { setEditorOpen(false); setEditingId(null); }} onSave={() => void save()} /> : null}
      {error ? <div className="fixed bottom-5 right-5 z-50 max-w-sm rounded-[var(--radius)] bg-[var(--color-danger)] px-4 py-3 text-sm text-white shadow-lg">{error}<button className="ml-3" onClick={() => setError(null)} aria-label="关闭"><X className="inline h-4 w-4" /></button></div> : null}
    </div>
  );
}

function ScheduleEditor({ form, selected, sessions, saving, onChange, onClose, onSave }: { form: ScheduleForm; selected: boolean; sessions: ReturnType<typeof useSessions>; saving: boolean; onChange: <K extends keyof ScheduleForm>(key: K, value: ScheduleForm[K]) => void; onClose: () => void; onSave: () => void }) {
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--color-overlay)] p-4"><div className="max-h-[90vh] w-full max-w-2xl overflow-y-auto rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-xl)]"><div className="flex items-center justify-between"><div><p className="text-xs font-medium text-[var(--color-accent)]">{selected ? "编辑任务" : "新建任务"}</p><h2 className="mt-1 text-lg font-semibold">任务配置</h2></div><Button variant="ghost" size="icon" onClick={onClose} aria-label="关闭"><X className="h-5 w-5" /></Button></div><div className="mt-5 space-y-4"><Field label="名称"><input value={form.name} onChange={(e) => onChange("name", e.target.value)} placeholder="例如：每日简报" className={inputClass} /></Field><Field label="提示词"><textarea value={form.prompt} onChange={(e) => onChange("prompt", e.target.value)} rows={5} placeholder="告诉 Agent 每次运行要完成什么" className={`${inputClass} resize-y`} /></Field><div className="grid gap-4 sm:grid-cols-2"><Field label="Cron（留空表示仅手动）"><input value={form.cron_expr} onChange={(e) => onChange("cron_expr", e.target.value)} placeholder="0 8 * * *" className={inputClass} /></Field><Field label="时区"><input value={form.timezone} onChange={(e) => onChange("timezone", e.target.value)} placeholder="Asia/Shanghai" className={inputClass} /></Field><Field label="超时（秒）"><input type="number" min={60} value={form.timeout_seconds} onChange={(e) => onChange("timeout_seconds", Number(e.target.value))} className={inputClass} /></Field><Field label="最大重试次数"><input type="number" min={0} max={10} value={form.max_retries} onChange={(e) => onChange("max_retries", Number(e.target.value))} className={inputClass} /></Field><Field label="重试间隔（秒）"><input type="number" min={0} value={form.retry_backoff_seconds} onChange={(e) => onChange("retry_backoff_seconds", Number(e.target.value))} className={inputClass} /></Field></div><fieldset><legend className="mb-2 text-sm font-medium">运行结果写入</legend><div className="grid gap-2 sm:grid-cols-2"><label className={cn("cursor-pointer rounded-[var(--radius)] border p-3 text-sm", form.result_mode === "new_session" ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)]")}><input type="radio" className="sr-only" checked={form.result_mode === "new_session"} onChange={() => onChange("result_mode", "new_session")} />新建会话<span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">保留独立上下文，运行历史可直接打开</span></label><label className={cn("cursor-pointer rounded-[var(--radius)] border p-3 text-sm", form.result_mode === "existing_session" ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]" : "border-[var(--color-border)]")}><input type="radio" className="sr-only" checked={form.result_mode === "existing_session"} onChange={() => onChange("result_mode", "existing_session")} />写入已有会话<span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">沿用该会话上下文和工作区文件</span></label></div></fieldset>{form.result_mode === "existing_session" ? <Field label="目标会话"><select value={form.target_session_id} onChange={(e) => onChange("target_session_id", e.target.value)} className={inputClass}><option value="">请选择会话</option>{sessions.map((session) => <option key={session.id} value={session.id}>{session.title?.trim() || session.id}</option>)}</select></Field> : null}</div><div className="mt-6 flex justify-end gap-2"><Button variant="ghost" onClick={onClose}>取消</Button><Button variant="primary" disabled={saving} onClick={onSave}>{saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}{selected ? "保存更改" : "创建任务"}</Button></div></div></div>;
}

const inputClass = "w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm outline-none transition-colors focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent-soft)]";
function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block text-sm"><span className="mb-1.5 block font-medium">{label}</span>{children}</label>; }
function DetailRow({ label, value }: { label: string; value: React.ReactNode }) { return <div className="flex items-center justify-between gap-4 px-5 py-4 text-sm"><span className="text-[var(--color-foreground-muted)]">{label}</span><span className="text-right">{value}</span></div>; }
function EmptyState({ archived, onCreate }: { archived: boolean; onCreate: () => void }) { return <div className="py-10 text-center"><p className="text-sm text-[var(--color-foreground-muted)]">{archived ? "还没有归档任务" : "还没有安排任务"}</p>{!archived ? <button onClick={onCreate} className="mt-3 text-sm font-medium text-[var(--color-accent)] hover:underline">创建第一个任务</button> : null}</div>; }
