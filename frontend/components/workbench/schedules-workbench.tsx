"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { CalendarClock, Check, ChevronDown, Clock3, Loader2, MoreHorizontal, Pause, Play, Plus, RefreshCw, Sparkles, Trash2, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { createSchedule, deleteSchedule, fetchScheduleRuns, fetchSchedules, runSchedule, updateSchedule } from "@/lib/api";
import type { ApiSchedule, ApiScheduleRun } from "@/lib/types";

const EMPTY_FORM = { name: "", prompt: "", cron_expr: "", timezone: "Asia/Shanghai", timeout_seconds: 1800, max_retries: 0 };

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
  if (!schedule.enabled) return { text: "已暂停", tone: "muted" };
  return { text: "已开启", tone: "green" };
}

export function SchedulesWorkbench() {
  const [schedules, setSchedules] = useState<ApiSchedule[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [runs, setRuns] = useState<ApiScheduleRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [runsLoading, setRunsLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState(EMPTY_FORM);
  const [error, setError] = useState<string | null>(null);

  const selected = useMemo(() => schedules.find((item) => item.id === selectedId) ?? null, [schedules, selectedId]);

  const refresh = useCallback(async () => {
    const result = await fetchSchedules();
    if (result.ok) {
      setSchedules(result.data.schedules ?? []);
      setSelectedId((current) => current && result.data.schedules.some((item) => item.id === current) ? current : result.data.schedules[0]?.id ?? null);
    } else setError(result.error);
    setLoading(false);
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  useEffect(() => {
    if (!selectedId) { setRuns([]); return; }
    let cancelled = false;
    setRunsLoading(true);
    void fetchScheduleRuns(selectedId).then((result) => { if (!cancelled && result.ok) setRuns(result.data.runs ?? []); }).finally(() => { if (!cancelled) setRunsLoading(false); });
    return () => { cancelled = true; };
  }, [selectedId]);

  const submit = async () => {
    if (!form.name.trim() || !form.prompt.trim()) { setError("请填写任务名称和提示词"); return; }
    setSaving(true); setError(null);
    const result = await createSchedule({ ...form, name: form.name.trim(), prompt: form.prompt.trim() });
    setSaving(false);
    if (!result.ok) { setError(result.error); return; }
    setSchedules((items) => [result.data, ...items]); setSelectedId(result.data.id); setFormOpen(false); setForm(EMPTY_FORM);
  };

  const toggle = async () => {
    if (!selected) return;
    const result = await updateSchedule(selected.id, { enabled: !selected.enabled, version: selected.version });
    if (!result.ok) { setError(result.error); return; }
    setSchedules((items) => items.map((item) => item.id === result.data.id ? result.data : item));
  };

  const runNow = async () => {
    if (!selected) return;
    setRunning(true); setError(null);
    const result = await runSchedule(selected.id);
    setRunning(false);
    if (!result.ok) { setError(result.error); return; }
    setRuns((items) => [result.data, ...items]);
  };

  const remove = async () => {
    if (!selected || !window.confirm(`删除“${selected.name}”？`)) return;
    const result = await deleteSchedule(selected.id);
    if (!result.ok) { setError(result.error); return; }
    const next = schedules.filter((item) => item.id !== selected.id); setSchedules(next); setSelectedId(next[0]?.id ?? null);
  };

  return (
    <div className="grid h-dvh min-h-0 overflow-hidden bg-[var(--color-background)]" style={{ gridTemplateColumns: "clamp(220px,18vw,280px) minmax(300px,34vw) minmax(0,1fr)" }}>
      <aside className="flex min-h-0 flex-col border-r border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="flex items-center gap-2 px-5 pb-3 pt-6 text-sm font-semibold"><CalendarClock className="h-5 w-5 text-[var(--color-accent)]" /> 已安排</div>
        <div className="px-3"><Button variant="primary-soft" size="lg" className="w-full justify-start gap-2 rounded-[var(--radius-lg)] text-2xs" onClick={() => { setForm(EMPTY_FORM); setFormOpen(true); }}><Plus className="h-4 w-4" /> 创建任务</Button></div>
        <nav className="mt-5 space-y-1 px-3 text-sm">
          <a href="/" className="flex items-center gap-2 rounded-[var(--radius)] px-3 py-2 text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)]">对话工作台</a>
          <div className="flex items-center gap-2 rounded-[var(--radius)] bg-[var(--color-accent-soft)] px-3 py-2 font-medium text-[var(--color-accent)]"><Clock3 className="h-4 w-4" /> 定时任务</div>
        </nav>
        <div className="mt-auto border-t border-[var(--color-border)] px-5 py-4 text-xs text-[var(--color-foreground-muted)]">任务会在独立沙箱中运行，完成后自动清理。</div>
      </aside>

      <section className="min-h-0 overflow-y-auto border-r border-[var(--color-border)] bg-[var(--color-background)]">
        <div className="sticky top-0 z-10 border-b border-[var(--color-border)] bg-[var(--color-background)]/95 px-5 pb-4 pt-6 backdrop-blur"><div className="flex items-center justify-between"><div><p className="text-xs font-medium text-[var(--color-accent)]">任务中心</p><h1 className="mt-1 text-xl font-semibold">你的自动化</h1></div><button className="rounded p-2 text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)]" onClick={() => void refresh()} title="刷新"><RefreshCw className="h-4 w-4" /></button></div><div className="mt-4 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-foreground-muted)]">搜索已安排任务</div></div>
        <div className="px-5 py-5">
          {loading ? <div className="flex justify-center py-12"><Loader2 className="h-5 w-5 animate-spin text-[var(--color-accent)]" /></div> : schedules.length === 0 ? <EmptyState onCreate={() => { setForm(EMPTY_FORM); setFormOpen(true); }} /> : <>
            <div className="mb-4 flex items-center gap-4 text-xs text-[var(--color-foreground-muted)]"><span className="font-medium text-[var(--color-foreground)]">全部</span><span>已开启 {schedules.filter((item) => item.enabled).length}</span><span>已暂停 {schedules.filter((item) => !item.enabled).length}</span></div>
            <div className="space-y-2">{schedules.map((item) => { const status = statusLabel(item); return <button key={item.id} onClick={() => setSelectedId(item.id)} className={cn("group w-full rounded-[var(--radius-lg)] border p-4 text-left transition-colors", item.id === selectedId ? "border-[var(--color-accent)] bg-[var(--color-surface)] shadow-sm" : "border-transparent bg-[var(--color-surface)] hover:border-[var(--color-border-strong)]")}><div className="flex items-start gap-3"><span className={cn("mt-1 h-2.5 w-2.5 rounded-full", status.tone === "green" ? "bg-emerald-500" : "bg-slate-400")} /><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{item.name}</span><span className="mt-1 block truncate text-xs text-[var(--color-foreground-muted)]">{item.cron_expr ? `${item.cron_expr} · ${item.timezone}` : "手动运行"}</span><span className="mt-2 block text-xs text-[var(--color-foreground-muted)]">{relative(item.last_run_at)} · {status.text}</span></span><MoreHorizontal className="h-4 w-4 shrink-0 text-[var(--color-foreground-muted)] opacity-0 transition-opacity group-hover:opacity-100" /></div></button>; })}</div>
            <div className="mt-8"><p className="mb-3 text-xs font-medium uppercase tracking-wider text-[var(--color-foreground-muted)]">建议</p><button onClick={() => { setForm({ ...EMPTY_FORM, name: "每日简报", prompt: "给我一份晨间简报，包含我的日程安排、重要未读邮件，以及今天需要我关注的事项。", cron_expr: "0 8 * * *" }); setFormOpen(true); }} className="flex w-full items-start gap-3 rounded-[var(--radius-lg)] border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface)] p-4 text-left hover:border-[var(--color-accent)]"><Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-violet-500" /><span><span className="block text-sm font-medium">每日简报</span><span className="mt-1 block text-xs text-[var(--color-foreground-muted)]">每天早上整理你最需要关注的信息</span></span></button></div>
          </>}
        </div>
      </section>

      <section className="min-h-0 overflow-y-auto bg-[var(--color-surface)]"><div className="flex items-center justify-between border-b border-[var(--color-border)] px-8 py-5"><div className="text-xs font-medium text-[var(--color-accent)]">{selected ? "活跃" : "开始使用"}</div><div className="flex items-center gap-1"><button className="rounded p-2 text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)]" title="更多"><MoreHorizontal className="h-4 w-4" /></button>{selected ? <button className="rounded p-2 text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)]" onClick={() => void remove()} title="删除"><Trash2 className="h-4 w-4" /></button> : null}</div></div>{selected ? <div className="mx-auto max-w-3xl px-8 py-8"><div className="flex items-center justify-between"><div><h2 className="text-2xl font-semibold">{selected.name}</h2><p className="mt-2 text-sm text-[var(--color-foreground-muted)]">{selected.cron_expr ? `按 ${selected.cron_expr} · ${selected.timezone} 运行` : "手动触发任务"}</p></div><Button variant="primary" className="gap-2" disabled={running} onClick={() => void runNow()}>{running ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} 立即运行</Button></div><div className="mt-8 divide-y rounded-[var(--radius-lg)] border border-[var(--color-border)]"><DetailRow label="运行于" value={selected.cron_expr ? `每次运行都新建聊天 · ${selected.timezone}` : "手动运行"} /><DetailRow label="超时" value={`${selected.timeout_seconds} 秒`} /><DetailRow label="重试" value={`${selected.max_retries} 次`} /><DetailRow label="状态" value={<button className="flex items-center gap-2" onClick={() => void toggle()}>{selected.enabled ? <Check className="h-4 w-4 text-emerald-500" /> : <Pause className="h-4 w-4" />}{selected.enabled ? "已开启" : "已暂停"}<ChevronDown className="h-3.5 w-3.5" /></button>} /></div><div className="mt-7 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-background)] p-5"><p className="text-xs font-medium text-[var(--color-foreground-muted)]">任务提示词</p><p className="mt-3 whitespace-pre-wrap text-sm leading-6">{selected.prompt}</p></div><div className="mt-8"><div className="mb-3 flex items-center justify-between"><h3 className="text-sm font-semibold">运行历史</h3><span className="text-xs text-[var(--color-foreground-muted)]">{runs.length} 次</span></div>{runsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : runs.length === 0 ? <p className="text-sm text-[var(--color-foreground-muted)]">还没有运行记录。</p> : <div className="space-y-2">{runs.map((run) => <div key={run.id} className="flex items-center justify-between rounded border border-[var(--color-border)] px-3 py-2 text-xs"><span>{new Date(run.scheduled_at).toLocaleString("zh-CN")}</span><span className={cn("font-medium", run.status === "succeeded" ? "text-emerald-600" : run.status === "running" || run.status === "queued" ? "text-amber-600" : "text-[var(--color-danger)]")}>{run.status}</span></div>)}</div>}</div></div> : <div className="flex h-full items-center justify-center px-8 text-center"><div><CalendarClock className="mx-auto h-10 w-10 text-[var(--color-foreground-muted)]" /><h2 className="mt-4 text-lg font-semibold">创建你的第一个定时任务</h2><p className="mt-2 max-w-sm text-sm text-[var(--color-foreground-muted)]">让 Agent 在固定时间自动完成重复工作，并在每次运行后留下可追踪的历史记录。</p><Button variant="primary" className="mt-6 gap-2" onClick={() => { setForm(EMPTY_FORM); setFormOpen(true); }}><Plus className="h-4 w-4" /> 创建任务</Button></div></div>}{error ? <div className="fixed bottom-5 right-5 z-50 rounded-[var(--radius)] bg-[var(--color-danger)] px-4 py-3 text-sm text-white shadow-lg">{error}<button className="ml-3" onClick={() => setError(null)}><X className="inline h-4 w-4" /></button></div> : null}</section>

      {formOpen ? <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"><div className="w-full max-w-xl rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-[var(--shadow-xl)]"><div className="flex items-center justify-between"><h2 className="text-lg font-semibold">创建定时任务</h2><button onClick={() => setFormOpen(false)}><X className="h-5 w-5 text-[var(--color-foreground-muted)]" /></button></div><div className="mt-5 space-y-4"><label className="block text-sm"><span className="mb-1.5 block font-medium">名称</span><input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="例如：每日简报" className="w-full rounded border border-[var(--color-border)] bg-transparent px-3 py-2 outline-none focus:border-[var(--color-accent)]" /></label><label className="block text-sm"><span className="mb-1.5 block font-medium">提示词</span><textarea value={form.prompt} onChange={(e) => setForm({ ...form, prompt: e.target.value })} rows={4} placeholder="告诉 Agent 每次运行要完成什么" className="w-full resize-y rounded border border-[var(--color-border)] bg-transparent px-3 py-2 outline-none focus:border-[var(--color-accent)]" /></label><div className="grid grid-cols-2 gap-3"><label className="block text-sm"><span className="mb-1.5 block font-medium">Cron（留空表示仅手动）</span><input value={form.cron_expr} onChange={(e) => setForm({ ...form, cron_expr: e.target.value })} placeholder="0 8 * * *" className="w-full rounded border border-[var(--color-border)] bg-transparent px-3 py-2 outline-none focus:border-[var(--color-accent)]" /></label><label className="block text-sm"><span className="mb-1.5 block font-medium">时区</span><input value={form.timezone} onChange={(e) => setForm({ ...form, timezone: e.target.value })} placeholder="Asia/Shanghai" className="w-full rounded border border-[var(--color-border)] bg-transparent px-3 py-2 outline-none focus:border-[var(--color-accent)]" /></label></div></div><div className="mt-6 flex justify-end gap-2"><Button variant="ghost" onClick={() => setFormOpen(false)}>取消</Button><Button variant="primary-soft" size="lg" disabled={saving} onClick={() => void submit()}>{saving ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}创建任务</Button></div></div></div> : null}
    </div>
  );
}

function EmptyState({ onCreate }: { onCreate: () => void }) { return <div className="py-10 text-center"><p className="text-sm text-[var(--color-foreground-muted)]">还没有安排任务</p><button onClick={onCreate} className="mt-3 text-sm font-medium text-[var(--color-accent)] hover:underline">创建第一个任务</button></div>; }
function DetailRow({ label, value }: { label: string; value: React.ReactNode }) { return <div className="flex items-center justify-between px-5 py-4 text-sm"><span className="text-[var(--color-foreground-muted)]">{label}</span><span>{value}</span></div>; }
