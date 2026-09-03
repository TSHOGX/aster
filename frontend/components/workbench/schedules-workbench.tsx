"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  Archive,
  Bell,
  Check,
  ChevronDown,
  ChevronRight,
  ClipboardList,
  FileSearch,
  Loader2,
  Pencil,
  Pause,
  Play,
  Plus,
  Search,
  Sparkles,
  SquarePen,
  Trash2,
  X,
  Zap,
} from "lucide-react";

import { useHarness, useSessions } from "@/components/harness-provider";
import { Badge, StatusPill } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/cn";
import { useDismissOnOutside } from "@/lib/use-popover";
import { brandName } from "@/lib/ui-config";
import {
  createSchedule,
  deleteSchedule,
  fetchScheduleRuns,
  fetchSchedules,
  runSchedule,
  updateSchedule,
} from "@/lib/api";
import { useI18n, type MessageKey } from "@/lib/i18n";
import type { ApiSchedule, ApiScheduleRun } from "@/lib/types";
import {
  buildCronExpression,
  DAYS,
  frequencyDescriptionLocalized,
  MONTHS,
  parseCron,
  TIME_OPTIONS,
  WEEKDAYS,
  type CustomRepeat,
  type FrequencyMode,
} from "@/lib/schedule-frequency";

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
  customWeekdays: number[];
  customDay: number;
  customMonth: number;
  customRawCron: string;
  timeout_seconds: number;
  max_retries: number;
  retry_backoff_seconds: number;
  result_mode: "new_session" | "existing_session";
  target_session_id: string;
};

type ScheduleFilter = "all" | "enabled" | "paused" | "completed";

const SCHEDULE_FILTERS: { value: ScheduleFilter; label: MessageKey }[] = [
  { value: "all", label: "schedule.filterAll" },
  { value: "enabled", label: "schedule.filterEnabled" },
  { value: "paused", label: "schedule.filterPaused" },
  { value: "completed", label: "schedule.filterCompleted" },
];

const EMPTY_FORM: ScheduleForm = {
  name: "",
  prompt: "",
  cron_expr: "",
  timezone: "UTC",
  frequency: "manual",
  intervalMinutes: 15,
  time: "08:00",
  weekday: 1,
  customRepeat: "hourly",
  customInterval: 1,
  customMinute: 0,
  customWeekdays: [1],
  customDay: 1,
  customMonth: 1,
  customRawCron: "",
  timeout_seconds: 1800,
  max_retries: 0,
  retry_backoff_seconds: 30,
  result_mode: "new_session",
  target_session_id: "",
};

// Schedule metadata changes rarely; keep it fresh in the background without
// competing with the 5-second run-history poll for the selected task.
const SCHEDULE_REFRESH_INTERVAL_MS = 30_000;

function localTimezone() {
  if (typeof Intl === "undefined") return "UTC";
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
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
    target_session_id: schedule.target_session_id ?? "",
  };
}

function formatDate(
  value: string | null | undefined,
  locale: "en" | "zh",
  timeZone?: string,
) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  try {
    return new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en-US", {
      dateStyle: "medium",
      timeStyle: "short",
      ...(timeZone ? { timeZone } : {}),
    }).format(date);
  } catch {
    return new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en-US", {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(date);
  }
}

function relative(
  value: string | null | undefined,
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
) {
  if (!value) return t("schedule.never");
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return t("schedule.never");
  const minutes = Math.max(0, Math.round((Date.now() - timestamp) / 60000));
  if (minutes < 1) return t("schedule.justNow");
  if (minutes < 60)
    return t(minutes === 1 ? "schedule.minuteAgo" : "schedule.minutesAgo", {
      count: minutes,
    });
  const hours = Math.round(minutes / 60);
  if (hours < 24)
    return t(hours === 1 ? "schedule.hourAgo" : "schedule.hoursAgo", {
      count: hours,
    });
  const days = Math.round(hours / 24);
  return t(days === 1 ? "schedule.dayAgo" : "schedule.daysAgo", {
    count: days,
  });
}

function runsEqual(previous: ApiScheduleRun[], next: ApiScheduleRun[]) {
  return (
    previous.length === next.length &&
    previous.every((run, index) => {
      const candidate = next[index];
      return (
        candidate?.id === run.id &&
        candidate.status === run.status &&
        candidate.session_id === run.session_id &&
        candidate.finished_at === run.finished_at &&
        candidate.error === run.error
      );
    })
  );
}

function runTone(
  status: ApiScheduleRun["status"],
): "running" | "idle" | "ready" | "failed" | "muted" {
  if (status === "succeeded") return "idle";
  if (status === "queued") return "ready";
  if (status === "running") return "running";
  if (status === "failed" || status === "timed_out") return "failed";
  return "muted";
}

function scheduleTone(schedule: ApiSchedule): "idle" | "ready" | "muted" {
  if (schedule.archived) return "muted";
  return schedule.enabled ? "idle" : "ready";
}

function matchesScheduleFilter(schedule: ApiSchedule, filter: ScheduleFilter) {
  if (filter === "completed") return Boolean(schedule.archived);
  if (filter === "enabled") return !schedule.archived && schedule.enabled;
  if (filter === "paused") return !schedule.archived && !schedule.enabled;
  return true;
}

function nextRunSummary(
  value: string | null | undefined,
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
) {
  if (!value) return t("schedule.nextRunUnavailable");
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return t("schedule.nextRunUnavailable");
  const minutes = Math.max(0, Math.round((timestamp - Date.now()) / 60000));
  if (minutes < 60)
    return t("schedule.nextRunInMinutes", { count: Math.max(1, minutes) });
  const hours = Math.round(minutes / 60);
  if (hours < 24) return t("schedule.nextRunInHours", { count: hours });
  const days = Math.round(hours / 24);
  return days === 1
    ? t("schedule.nextRunTomorrow")
    : t("schedule.nextRunInDays", { count: days });
}

export function SchedulesWorkbench({
  onOpenSession,
}: {
  onOpenSession?: (id: string) => void;
}) {
  const { selectSession } = useHarness();
  const sessions = useSessions();
  const { t, locale } = useI18n();
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
  const [statusFilter, setStatusFilter] = useState<ScheduleFilter>("all");
  const [query, setQuery] = useState("");
  const [createMenuOpen, setCreateMenuOpen] = useState(false);
  const createMenuRef = useRef<HTMLDivElement | null>(null);

  const selected = useMemo(
    () =>
      schedules.find(
        (item) =>
          item.id === selectedId && matchesScheduleFilter(item, statusFilter),
      ) ?? null,
    [schedules, selectedId, statusFilter],
  );
  const visibleSchedules = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    return schedules.filter((item) => {
      if (!matchesScheduleFilter(item, statusFilter)) return false;
      if (!normalized) return true;
      return `${item.name} ${item.prompt}`
        .toLocaleLowerCase()
        .includes(normalized);
    });
  }, [query, schedules, statusFilter]);
  const targetSessions = useMemo(
    () =>
      sessions.filter(
        (session) =>
          session.access !== "reader" &&
          session.mode === "agent" &&
          session.status !== "destroyed",
      ),
    [sessions],
  );
  const selectedRunId = selected?.id ?? null;

  const refresh = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    const result = await fetchSchedules();
    if (result.ok) {
      const next = result.data.schedules ?? [];
      setSchedules(next);
      setSelectedId((current) => {
        if (current && next.some((item) => item.id === current)) return current;
        // Do not auto-select the first task. Opening the automation area is
        // intentionally a list-first state; details are an explicit choice.
        return null;
      });
    } else setError(result.error);
    if (!silent) setLoading(false);
  }, []);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      await refresh();
      // Keep the async boundary explicit so the effect only coordinates the
      // request; refresh owns the state update after the response arrives.
      if (cancelled) return;
    };
    void load();
    const interval = window.setInterval(() => {
      void refresh(true);
    }, SCHEDULE_REFRESH_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [refresh]);

  useEffect(() => {
    let cancelled = false;
    let inFlight = false;
    const load = async (initial = false) => {
      if (!selectedRunId) {
        if (initial) setRuns([]);
        return;
      }
      if (inFlight) return;
      inFlight = true;
      if (initial) {
        setRuns([]);
        setRunsLoading(true);
      }
      try {
        const result = await fetchScheduleRuns(selectedRunId);
        if (cancelled) return;
        if (result.ok)
          setRuns((current) => {
            const next = result.data.runs ?? [];
            return runsEqual(current, next) ? current : next;
          });
        else setError(result.error);
      } finally {
        inFlight = false;
        if (!cancelled) setRunsLoading(false);
      }
    };
    void load(true);
    const interval = selectedRunId
      ? window.setInterval(() => {
          void load();
        }, 5000)
      : null;
    return () => {
      cancelled = true;
      if (interval !== null) window.clearInterval(interval);
    };
  }, [selectedRunId]);

  useEffect(() => {
    if (!editorOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setEditorOpen(false);
    };
    document.addEventListener("keydown", onKeyDown);
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.body.style.overflow = previousOverflow;
    };
  }, [editorOpen]);

  useEffect(() => {
    if (!selectedId || editorOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setSelectedId(null);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [editorOpen, selectedId]);

  useDismissOnOutside(
    createMenuOpen,
    [createMenuRef],
    () => setCreateMenuOpen(false),
    { escape: true },
  );

  const openCreate = (initial: ScheduleForm = EMPTY_FORM) => {
    setEditingId(null);
    setForm({
      ...initial,
      timezone: initial.timezone === "UTC" ? localTimezone() : initial.timezone,
      ...parseCron(initial.cron_expr),
    });
    setEditorOpen(true);
    setError(null);
  };
  const openEdit = () => {
    if (!selected) return;
    setEditingId(selected.id);
    setForm(formFromSchedule(selected));
    setEditorOpen(true);
    setError(null);
  };
  const updateForm = <K extends keyof ScheduleForm>(
    key: K,
    value: ScheduleForm[K],
  ) =>
    setForm((current) => ({
      ...current,
      [key]: value,
      ...(key === "frequency" ||
      (key.startsWith("custom") && key !== "customRawCron")
        ? { customRawCron: "" }
        : {}),
    }));

  const save = async () => {
    if (!form.name.trim() || !form.prompt.trim()) {
      setError(t("schedule.validationRequired"));
      return;
    }
    if (form.result_mode === "existing_session" && !form.target_session_id) {
      setError(t("schedule.validationTarget"));
      return;
    }
    setSaving(true);
    setError(null);
    const payload = {
      name: form.name.trim(),
      prompt: form.prompt.trim(),
      cron_expr: buildCronExpression(form),
      timezone: form.timezone.trim() || localTimezone(),
      timeout_seconds: Math.max(60, form.timeout_seconds),
      max_retries: Math.min(10, Math.max(0, form.max_retries)),
      retry_backoff_seconds: Math.max(0, form.retry_backoff_seconds),
      result_mode: form.result_mode,
      target_session_id:
        form.result_mode === "existing_session" ? form.target_session_id : "",
    };
    const editing = editingId
      ? schedules.find((item) => item.id === editingId)
      : null;
    const result = editing
      ? await updateSchedule(editing.id, {
          ...payload,
          version: editing.version,
        })
      : await createSchedule(payload);
    setSaving(false);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setSchedules((items) =>
      items.some((item) => item.id === result.data.id)
        ? items.map((item) => (item.id === result.data.id ? result.data : item))
        : [result.data, ...items],
    );
    setSelectedId(result.data.id);
    setEditorOpen(false);
    setEditingId(null);
    setForm(EMPTY_FORM);
  };

  const toggle = async () => {
    if (!selected || selected.archived) return;
    const result = await updateSchedule(selected.id, {
      enabled: !selected.enabled,
      version: selected.version,
    });
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setSchedules((items) =>
      items.map((item) => (item.id === result.data.id ? result.data : item)),
    );
  };

  const archive = async () => {
    if (!selected) return;
    const archiving = !selected.archived;
    if (
      !window.confirm(
        t(archiving ? "schedule.confirmArchive" : "schedule.confirmRestore", {
          name: selected.name,
        }),
      )
    )
      return;
    const result = await updateSchedule(selected.id, {
      archived: archiving,
      version: selected.version,
    });
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setSchedules((items) =>
      items.map((item) => (item.id === result.data.id ? result.data : item)),
    );
    if (!matchesScheduleFilter(result.data, statusFilter)) setSelectedId(null);
  };

  const runNow = async () => {
    if (!selected || selected.archived) return;
    setRunning(true);
    setError(null);
    const result = await runSchedule(selected.id);
    setRunning(false);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setRuns((items) => [result.data, ...items]);
    void refresh(true);
  };

  const remove = async () => {
    if (
      !selected ||
      !window.confirm(t("schedule.confirmDelete", { name: selected.name }))
    )
      return;
    const result = await deleteSchedule(selected.id);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    const next = schedules.filter((item) => item.id !== selected.id);
    setSchedules(next);
    setSelectedId(null);
  };

  const openRun = (run: ApiScheduleRun) => {
    if (!run.session_id) return;
    if (onOpenSession) onOpenSession(run.session_id);
    else selectSession(run.session_id);
  };

  const setFilter = (filter: ScheduleFilter) => {
    setStatusFilter(filter);
    setQuery("");
    setSelectedId(null);
  };

  const suggestions = [
    {
      icon: <Bell className="h-5 w-5" />,
      iconClassName: "text-sky-500",
      title: t("schedule.suggestionDaily"),
      meta: t("schedule.suggestionDailyMeta"),
      body: t("schedule.suggestionDailyBody"),
      form: {
        ...EMPTY_FORM,
        name: t("schedule.suggestionDaily"),
        prompt: t("schedule.suggestionDailyBody"),
        cron_expr: "0 8 * * 1-5",
      },
    },
    {
      icon: <ClipboardList className="h-5 w-5" />,
      iconClassName: "text-violet-500",
      title: t("schedule.suggestionWeekly"),
      meta: t("schedule.suggestionWeeklyMeta"),
      body: t("schedule.suggestionWeeklyBody"),
      form: {
        ...EMPTY_FORM,
        name: t("schedule.suggestionWeekly"),
        prompt: t("schedule.suggestionWeeklyBody"),
        cron_expr: "0 16 * * 5",
      },
    },
    {
      icon: <FileSearch className="h-5 w-5" />,
      iconClassName: "text-emerald-500",
      title: t("schedule.suggestionMonitor"),
      meta: t("schedule.suggestionMonitorMeta"),
      body: t("schedule.suggestionMonitorBody"),
      form: {
        ...EMPTY_FORM,
        name: t("schedule.suggestionMonitor"),
        prompt: t("schedule.suggestionMonitorBody"),
        cron_expr: "0 9 * * 1-5",
      },
    },
  ];

  return (
    <div className="relative h-full min-h-0 overflow-hidden bg-[var(--color-surface)]">
      <div
        ref={createMenuRef}
        className="absolute right-5 top-4 z-20 sm:right-7 sm:top-5"
      >
        <div className="relative shrink-0">
          <Button
            variant="outline"
            className="h-9 gap-2 rounded-[var(--radius)] px-3 text-sm shadow-[var(--shadow-xs)]"
            aria-haspopup="menu"
            aria-expanded={createMenuOpen}
            onClick={() => setCreateMenuOpen((open) => !open)}
          >
            <SquarePen className="h-4 w-4 text-[var(--color-accent)]" />
            {t("schedule.create")}
            <ChevronDown className="h-4 w-4 opacity-70" />
          </Button>
          {createMenuOpen ? (
            <div
              role="menu"
              className="absolute right-0 top-11 z-20 min-w-[12rem] overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] py-1 shadow-[var(--shadow-lg)]"
            >
              <button
                type="button"
                role="menuitem"
                className="flex w-full px-3 py-2 text-left text-sm hover:bg-[var(--color-surface-muted)]"
                onClick={() => {
                  setCreateMenuOpen(false);
                  openCreate();
                }}
              >
                {t("schedule.createBlank")}
              </button>
              <button
                type="button"
                role="menuitem"
                className="flex w-full px-3 py-2 text-left text-sm hover:bg-[var(--color-surface-muted)]"
                onClick={() => {
                  setCreateMenuOpen(false);
                  openCreate({
                    ...EMPTY_FORM,
                    name: t("schedule.templateTitle"),
                    prompt: t("schedule.templatePrompt"),
                    cron_expr: "0 8 * * 1-5",
                  });
                }}
              >
                {t("schedule.createFromSuggestion")}
              </button>
            </div>
          ) : null}
        </div>
      </div>
      <div
        className={cn(
          "h-full overflow-y-auto bg-[var(--color-surface)]",
          selected && "lg:pr-[min(42vw,560px)]",
        )}
        onMouseDown={(event) => {
          if (selected && event.target === event.currentTarget) {
            setSelectedId(null);
          }
        }}
      >
        <main
          className="mx-auto w-full max-w-3xl px-6 pb-24 pt-12 sm:px-10 sm:pt-16 lg:px-12"
          onMouseDown={(event) => {
            if (selected && event.target === event.currentTarget) {
              setSelectedId(null);
            }
          }}
        >
          <div className="flex items-start gap-6 pr-20 sm:pr-0">
            <div className="min-w-0">
              <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">
                {t("schedule.pageTitle")}
              </h1>
              <p className="mt-3 max-w-xl text-base leading-7 text-[var(--color-foreground-muted)] sm:text-lg">
                {t("schedule.pageSubtitle", { brand: brandName })}
              </p>
            </div>
          </div>

          <label className="relative mt-9 block">
            <Search className="pointer-events-none absolute left-4 top-1/2 h-5 w-5 -translate-y-1/2 text-[var(--color-foreground-muted)]" />
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("schedule.searchPlaceholder")}
              aria-label={t("schedule.search")}
              className="h-12 w-full rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface)] py-2 pl-12 pr-4 text-base outline-none transition-colors placeholder:text-[var(--color-foreground-muted)] focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent-soft)]"
            />
          </label>

          <div
            className="mt-8 flex max-w-full items-center gap-1 overflow-x-auto pb-1 text-sm"
            role="tablist"
            aria-label={t("schedule.pageTitle")}
          >
            {SCHEDULE_FILTERS.map((filter) => (
              <button
                type="button"
                key={filter.value}
                role="tab"
                aria-selected={statusFilter === filter.value}
                onClick={() => setFilter(filter.value)}
                className={cn(
                  "shrink-0 rounded-full px-4 py-2 transition-colors",
                  statusFilter === filter.value
                    ? "bg-[var(--color-surface-muted)] font-semibold text-[var(--color-foreground)]"
                    : "text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]",
                )}
              >
                {t(filter.label)}
              </button>
            ))}
          </div>

          <section className="mt-10">
            <div className="flex items-baseline justify-between gap-4">
              <h2 className="text-base font-semibold">
                {t("schedule.scheduled")}
              </h2>
              <span className="text-sm text-[var(--color-foreground-muted)]">
                {visibleSchedules.length}
              </span>
            </div>
            {loading ? (
              <div className="mt-4">
                <ScheduleListSkeleton />
              </div>
            ) : visibleSchedules.length === 0 ? (
              <IndexEmptyState
                filtered={Boolean(query.trim())}
                t={t}
                onCreate={() => openCreate()}
              />
            ) : (
              <div className="mt-3 divide-y divide-[var(--color-border)] border-y border-[var(--color-border)]">
                {visibleSchedules.map((item) => (
                  <ScheduleIndexRow
                    key={item.id}
                    schedule={item}
                    selected={item.id === selectedId}
                    locale={locale}
                    t={t}
                    onSelect={setSelectedId}
                  />
                ))}
              </div>
            )}
          </section>

          <section className="mt-10">
            <h2 className="text-base font-semibold">
              {t("schedule.suggestions")}
            </h2>
            <div className="mt-3 divide-y divide-[var(--color-border)]">
              {suggestions.map((suggestion) => (
                <SuggestionRow
                  key={suggestion.title}
                  icon={suggestion.icon}
                  iconClassName={suggestion.iconClassName}
                  title={suggestion.title}
                  meta={suggestion.meta}
                  body={suggestion.body}
                  onClick={() => openCreate(suggestion.form)}
                />
              ))}
            </div>
          </section>
        </main>
      </div>

      {selected ? (
        <aside className="absolute inset-y-0 right-0 z-30 flex w-full max-w-[560px] flex-col border-l border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)]">
          <div className="flex shrink-0 items-center justify-between border-b border-[var(--color-border)] px-5 py-3.5">
            <div className="min-w-0 truncate text-sm font-medium">
              {selected.name}
            </div>
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon"
                onClick={openEdit}
                aria-label={t("schedule.edit")}
                title={t("schedule.edit")}
              >
                <Pencil className="h-4 w-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => void archive()}
                aria-label={
                  selected.archived
                    ? t("schedule.restore")
                    : t("schedule.archive")
                }
                title={
                  selected.archived
                    ? t("schedule.restore")
                    : t("schedule.archive")
                }
              >
                <Archive className="h-4 w-4" />
              </Button>
              <Button
                variant="danger"
                size="icon"
                onClick={() => void remove()}
                aria-label={t("schedule.delete")}
                title={t("schedule.delete")}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => setSelectedId(null)}
                aria-label={t("schedule.closeDetails")}
                title={t("schedule.closeDetails")}
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">
            <ScheduleDetail
              schedule={selected}
              runs={runs}
              runsLoading={runsLoading}
              running={running}
              locale={locale}
              t={t}
              drawer
              onToggle={() => void toggle()}
              onRun={() => void runNow()}
              onOpenRun={openRun}
            />
          </div>
        </aside>
      ) : null}
      {editorOpen ? (
        <ScheduleEditor
          form={form}
          selected={Boolean(editingId)}
          sessions={targetSessions}
          saving={saving}
          locale={locale}
          t={t}
          onChange={updateForm}
          onClose={() => {
            setEditorOpen(false);
            setEditingId(null);
          }}
          onSave={() => void save()}
        />
      ) : null}
      {error ? (
        <div
          role="alert"
          className="fixed bottom-5 right-5 z-[60] flex max-w-sm items-start gap-3 rounded-[var(--radius-lg)] border border-red-200 bg-[var(--color-danger)] px-4 py-3 text-sm text-white shadow-[var(--shadow-lg)]"
        >
          <span className="min-w-0 flex-1">{error}</span>
          <button
            type="button"
            onClick={() => setError(null)}
            aria-label={t("common.close")}
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      ) : null}
    </div>
  );
}

function ScheduleListSkeleton() {
  return (
    <div className="divide-y divide-[var(--color-border)] border-y border-[var(--color-border)]">
      {Array.from({ length: 4 }, (_, index) => (
        <div
          key={index}
          className="skeleton-pulse flex items-start gap-4 px-3 py-5"
        >
          <div className="mt-1 h-5 w-5 rounded-full border-2 border-[var(--color-border-strong)]" />
          <div className="min-w-0 flex-1">
            <div className="h-4 w-2/5 rounded bg-[var(--color-surface-muted)]" />
            <div className="mt-2 h-3 w-3/5 rounded bg-[var(--color-surface-muted)]" />
          </div>
        </div>
      ))}
    </div>
  );
}

function IndexEmptyState({
  filtered,
  t,
  onCreate,
}: {
  filtered: boolean;
  t: (key: MessageKey, params?: Record<string, string | number>) => string;
  onCreate: () => void;
}) {
  if (filtered)
    return (
      <div className="px-3 py-10 text-center text-sm text-[var(--color-foreground-muted)]">
        {t("search.noResults")}
      </div>
    );
  return (
    <div className="px-3 py-10 text-center">
      <p className="text-sm text-[var(--color-foreground-muted)]">
        {t("schedule.noScheduled")}
      </p>
      <button
        type="button"
        onClick={onCreate}
        className="mt-3 text-sm font-medium text-[var(--color-accent)] hover:underline"
      >
        {t("schedule.createFirst")}
      </button>
    </div>
  );
}

function ScheduleIndexRow({
  schedule,
  selected,
  locale,
  t,
  onSelect,
}: {
  schedule: ApiSchedule;
  selected: boolean;
  locale: "en" | "zh";
  t: (key: MessageKey, params?: Record<string, string | number>) => string;
  onSelect: (id: string | null) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => onSelect(selected ? null : schedule.id)}
      aria-current={selected ? "true" : undefined}
      className={cn(
        "group flex w-full items-start gap-4 px-3 py-5 text-left transition-colors",
        selected
          ? "bg-[var(--color-surface-muted)]"
          : "hover:bg-[var(--color-surface-muted)]/60",
      )}
    >
      <span
        className={cn(
          "mt-1 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border-2",
          selected
            ? "border-[var(--color-accent)] bg-[var(--color-accent)]"
            : "border-[var(--color-border-strong)] bg-transparent",
        )}
      >
        {selected ? (
          <Check className="h-3 w-3 text-white" strokeWidth={3} />
        ) : null}
        <span className="sr-only">
          {schedule.archived
            ? t("schedule.archivedStatus")
            : schedule.enabled
              ? t("schedule.enabled")
              : t("schedule.paused")}
        </span>
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="min-w-0 truncate text-base font-semibold">
            {schedule.name}
          </span>
          <span className="text-sm text-[var(--color-foreground-muted)]">
            {frequencyDescriptionLocalized(schedule.cron_expr, locale)}
          </span>
        </span>
        <span className="mt-1 block truncate text-sm text-[var(--color-foreground-muted)]">
          {schedule.archived
            ? t("schedule.archivedStatus")
            : !schedule.enabled
              ? t("schedule.paused")
              : schedule.cron_expr
                ? `${t("schedule.nextRun")} ${nextRunSummary(schedule.next_run_at, t)}`
                : t("schedule.manual")}
          {schedule.cron_expr && schedule.enabled
            ? ` · ${schedule.timezone}`
            : ""}
        </span>
      </span>
      {selected ? (
        <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-[var(--color-foreground-muted)]" />
      ) : null}
    </button>
  );
}

function SuggestionRow({
  icon,
  iconClassName,
  title,
  meta,
  body,
  onClick,
}: {
  icon: ReactNode;
  iconClassName: string;
  title: string;
  meta: string;
  body: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full items-start gap-4 px-3 py-4 text-left transition-colors hover:bg-[var(--color-surface-muted)]/60"
    >
      <span className={cn("mt-0.5 shrink-0", iconClassName)}>{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="text-base font-semibold">{title}</span>
          <span className="text-sm text-[var(--color-foreground-muted)]">
            {meta}
          </span>
        </span>
        <span className="mt-1 block text-sm leading-6 text-[var(--color-foreground-muted)]">
          {body}
        </span>
      </span>
    </button>
  );
}

function ScheduleDetail({
  schedule,
  runs,
  runsLoading,
  running,
  locale,
  t,
  drawer = false,
  onToggle,
  onRun,
  onOpenRun,
}: {
  schedule: ApiSchedule;
  runs: ApiScheduleRun[];
  runsLoading: boolean;
  running: boolean;
  locale: "en" | "zh";
  t: (key: MessageKey, params?: Record<string, string | number>) => string;
  drawer?: boolean;
  onToggle: () => void;
  onRun: () => void;
  onOpenRun: (run: ApiScheduleRun) => void;
}) {
  const successCount = runs.filter((run) => run.status === "succeeded").length;
  const successRate = runs.length
    ? Math.round((successCount / runs.length) * 100)
    : null;
  return (
    <div
      className={cn(
        "mx-auto w-full",
        drawer ? "px-5 py-6" : "max-w-4xl px-5 py-7 sm:px-8 lg:px-12",
      )}
    >
      <div className="flex flex-wrap items-start justify-between gap-5">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <StatusPill tone={scheduleTone(schedule)}>
              {schedule.archived
                ? t("schedule.archivedStatus")
                : schedule.enabled
                  ? t("schedule.enabled")
                  : t("schedule.paused")}
            </StatusPill>
            <Badge>{t("schedule.agent")}</Badge>
          </div>
          <h2 className="mt-3 break-words text-2xl font-semibold tracking-tight sm:text-3xl">
            {schedule.name}
          </h2>
          <p className="mt-2 flex items-center gap-2 text-sm text-[var(--color-foreground-muted)]">
            <Zap className="h-4 w-4 text-[var(--color-accent)]" />
            {frequencyDescriptionLocalized(schedule.cron_expr, locale)}
            {schedule.cron_expr ? <span>· {schedule.timezone}</span> : null}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button
            variant="outline"
            className="gap-2"
            disabled={schedule.archived}
            onClick={onToggle}
          >
            {schedule.enabled ? (
              <Pause className="h-4 w-4" />
            ) : (
              <Play className="h-4 w-4" />
            )}
            {schedule.enabled ? t("schedule.pause") : t("schedule.enable")}
          </Button>
          <Button
            variant="primary"
            className="gap-2"
            disabled={running || schedule.archived}
            aria-busy={running}
            onClick={onRun}
          >
            {running ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Play className="h-4 w-4" />
            )}
            {running ? t("schedule.running") : t("schedule.runNow")}
          </Button>
        </div>
      </div>
      <div className="mt-8 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label={t("schedule.nextRun")}
          value={
            schedule.archived
              ? t("schedule.archivedStatus")
              : schedule.cron_expr && schedule.enabled
                ? formatDate(schedule.next_run_at, locale, schedule.timezone)
                : !schedule.enabled
                  ? t("schedule.paused")
                  : t("schedule.manual")
          }
        />
        <StatCard
          label={t("schedule.lastRun")}
          value={relative(schedule.last_run_at, t)}
        />
        <StatCard
          label={t("schedule.runHistory")}
          value={t("schedule.runCount", { count: runs.length })}
        />
        <StatCard
          label={t("schedule.successRate")}
          value={successRate === null ? "—" : `${successRate}%`}
        />
      </div>
      <div
        className={cn(
          "mt-8 grid gap-6",
          !drawer && "lg:grid-cols-[minmax(0,1.2fr)_minmax(260px,.8fr)]",
        )}
      >
        <div className="space-y-6">
          <Card className="overflow-hidden">
            <div className="border-b border-[var(--color-border)] px-5 py-4">
              <h3 className="text-sm font-semibold">{t("schedule.prompt")}</h3>
            </div>
            <p className="whitespace-pre-wrap px-5 py-4 text-sm leading-6">
              {schedule.prompt}
            </p>
          </Card>
          <Card className="overflow-hidden">
            <div className="flex items-center justify-between border-b border-[var(--color-border)] px-5 py-4">
              <h3 className="text-sm font-semibold">
                {t("schedule.runHistory")}
              </h3>
              <span className="text-xs text-[var(--color-foreground-muted)]">
                {t("schedule.runCount", { count: runs.length })}
              </span>
            </div>
            {runsLoading ? (
              <div className="flex justify-center py-10">
                <Loader2 className="h-5 w-5 animate-spin text-[var(--color-accent)]" />
              </div>
            ) : runs.length === 0 ? (
              <p className="px-5 py-10 text-sm text-[var(--color-foreground-muted)]">
                {t("schedule.noRuns")}
              </p>
            ) : (
              <div className="divide-y divide-[var(--color-border)]">
                {runs.map((run) => (
                  <button
                    type="button"
                    key={run.id}
                    onClick={() => onOpenRun(run)}
                    disabled={!run.session_id}
                    className="flex w-full items-center gap-4 px-5 py-3.5 text-left transition-colors hover:bg-[var(--color-surface-muted)] disabled:cursor-default disabled:hover:bg-transparent"
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm">
                        {formatDate(
                          run.scheduled_at,
                          locale,
                          schedule.timezone,
                        )}
                      </span>
                      <span className="mt-1 block truncate text-xs text-[var(--color-foreground-muted)]">
                        {run.error ||
                          run.output_summary ||
                          (run.session_id
                            ? t("schedule.openSession")
                            : t("schedule.noSession"))}
                      </span>
                    </span>
                    <StatusPill tone={runTone(run.status)}>
                      {t(`schedule.status.${run.status}` as MessageKey)}
                    </StatusPill>
                    {run.session_id ? (
                      <ChevronRight className="h-4 w-4 shrink-0 text-[var(--color-foreground-muted)]" />
                    ) : null}
                  </button>
                ))}
              </div>
            )}
          </Card>
        </div>
        <Card className="h-fit divide-y divide-[var(--color-border)] overflow-hidden">
          <DetailRow
            label={t("schedule.result")}
            value={
              schedule.result_mode === "existing_session"
                ? t("schedule.existingSession")
                : t("schedule.newSession")
            }
          />
          <DetailRow label={t("schedule.timezone")} value={schedule.timezone} />
          <DetailRow
            label={t("schedule.timeout")}
            value={`${schedule.timeout_seconds}s`}
          />
          <DetailRow
            label={t("schedule.retries")}
            value={`${schedule.max_retries} · ${schedule.retry_backoff_seconds}s`}
          />
        </Card>
      </div>
    </div>
  );
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <Card className="px-4 py-3.5">
      <p className="text-xs text-[var(--color-foreground-muted)]">{label}</p>
      <p className="mt-1.5 truncate text-sm font-medium" title={value}>
        {value}
      </p>
    </Card>
  );
}

function ScheduleEditor({
  form,
  selected,
  sessions,
  saving,
  locale,
  t,
  onChange,
  onClose,
  onSave,
}: {
  form: ScheduleForm;
  selected: boolean;
  sessions: ReturnType<typeof useSessions>;
  saving: boolean;
  locale: "en" | "zh";
  t: (key: MessageKey, params?: Record<string, string | number>) => string;
  onChange: <K extends keyof ScheduleForm>(
    key: K,
    value: ScheduleForm[K],
  ) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const frequencyOptions: {
    value: FrequencyMode;
    label: MessageKey;
    description: MessageKey;
  }[] = [
    {
      value: "manual",
      label: "schedule.editor.manual",
      description: "schedule.editor.manualDescription",
    },
    {
      value: "interval",
      label: "schedule.editor.interval",
      description: "schedule.editor.intervalDescription",
    },
    {
      value: "daily",
      label: "schedule.editor.daily",
      description: "schedule.editor.dailyDescription",
    },
    {
      value: "weekdays",
      label: "schedule.editor.weekdays",
      description: "schedule.editor.weekdaysDescription",
    },
    {
      value: "weekly",
      label: "schedule.editor.weekly",
      description: "schedule.editor.weeklyDescription",
    },
    {
      value: "custom",
      label: "schedule.editor.custom",
      description: "schedule.editor.customDescription",
    },
  ];
  const selectNumber = (key: keyof ScheduleForm, value: string) =>
    onChange(key as never, Number(value) as never);
  const cronPreview = buildCronExpression(form);
  const weekdayLabels =
    locale === "zh"
      ? ["一", "二", "三", "四", "五", "六", "日"]
      : ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  return (
    <div
      className="fixed inset-0 z-50 flex items-end justify-center bg-[var(--color-overlay)] p-0 sm:items-center sm:p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby="schedule-editor-title"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="flex max-h-[94vh] w-full max-w-3xl flex-col overflow-hidden rounded-t-[var(--radius-xl)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)] sm:max-h-[90vh] sm:rounded-[var(--radius-xl)]">
        <div className="flex shrink-0 items-center justify-between border-b border-[var(--color-border)] px-5 py-4 sm:px-6">
          <div>
            <p className="text-xs font-medium text-[var(--color-accent)]">
              {t(selected ? "schedule.editor.edit" : "schedule.editor.new")}
            </p>
            <h2
              id="schedule-editor-title"
              className="mt-1 text-lg font-semibold"
            >
              {t("schedule.editor.configuration")}
            </h2>
          </div>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label={t("common.close")}
          >
            <X className="h-5 w-5" />
          </Button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-5 sm:px-6">
          <div className="space-y-6">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t("schedule.editor.name")}>
                <input
                  autoFocus
                  value={form.name}
                  onChange={(event) => onChange("name", event.target.value)}
                  placeholder={t("schedule.editor.namePlaceholder")}
                  maxLength={200}
                  className={inputClass}
                />
              </Field>
              <Field
                label={t("schedule.editor.timezone")}
                hint={t("schedule.editor.timezoneHint")}
              >
                <input
                  value={form.timezone}
                  onChange={(event) => onChange("timezone", event.target.value)}
                  placeholder="Asia/Shanghai"
                  className={inputClass}
                />
              </Field>
            </div>
            <Field label={t("schedule.prompt")}>
              <textarea
                value={form.prompt}
                onChange={(event) => onChange("prompt", event.target.value)}
                rows={5}
                placeholder={t("schedule.editor.promptPlaceholder")}
                className={`${inputClass} resize-y`}
              />
            </Field>
            <fieldset>
              <legend className="mb-2 text-sm font-medium">
                {t("schedule.editor.frequency")}
              </legend>
              <div className="grid gap-2 sm:grid-cols-3" role="radiogroup">
                {frequencyOptions.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    role="radio"
                    aria-checked={form.frequency === option.value}
                    onClick={() => onChange("frequency", option.value)}
                    className={cn(
                      "rounded-[var(--radius-lg)] border p-3 text-left transition-colors",
                      form.frequency === option.value
                        ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]"
                        : "border-[var(--color-border)] hover:border-[var(--color-border-strong)]",
                    )}
                  >
                    <span className="block text-sm font-medium">
                      {t(option.label)}
                    </span>
                    <span className="mt-1 block text-xs leading-5 text-[var(--color-foreground-muted)]">
                      {t(option.description)}
                    </span>
                  </button>
                ))}
              </div>
            </fieldset>
            {form.frequency === "manual" ? (
              <Hint>{t("schedule.editor.manualHint")}</Hint>
            ) : null}
            {form.frequency === "interval" ? (
              <Field
                label={t("schedule.editor.everyMinutes")}
                hint={t("schedule.editor.intervalHint")}
              >
                <input
                  type="number"
                  min={1}
                  max={59}
                  step={1}
                  value={form.intervalMinutes}
                  onChange={(event) =>
                    selectNumber("intervalMinutes", event.target.value)
                  }
                  className={inputClass}
                />
              </Field>
            ) : null}
            {form.frequency === "daily" || form.frequency === "weekdays" ? (
              <Field label={t("schedule.editor.runTime")}>
                <TimeSelect
                  value={form.time}
                  onChange={(value) => onChange("time", value)}
                />
              </Field>
            ) : null}
            {form.frequency === "weekly" ? (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("schedule.editor.runDay")}>
                  <select
                    value={form.weekday}
                    onChange={(event) =>
                      selectNumber("weekday", event.target.value)
                    }
                    className={inputClass}
                  >
                    {WEEKDAYS.map((day, index) => (
                      <option key={day.value} value={day.value}>
                        {locale === "zh"
                          ? `星期${weekdayLabels[index]}`
                          : weekdayLabels[index]}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label={t("schedule.editor.runTime")}>
                  <TimeSelect
                    value={form.time}
                    onChange={(value) => onChange("time", value)}
                  />
                </Field>
              </div>
            ) : null}
            {form.frequency === "custom" ? (
              <CustomFrequency
                form={form}
                locale={locale}
                t={t}
                onChange={onChange}
                selectNumber={selectNumber}
              />
            ) : null}
            <div className="grid gap-4 border-t border-[var(--color-border)] pt-5 sm:grid-cols-3">
              <Field label={t("schedule.editor.timeout")}>
                <input
                  type="number"
                  min={60}
                  value={form.timeout_seconds}
                  onChange={(event) =>
                    selectNumber("timeout_seconds", event.target.value)
                  }
                  className={inputClass}
                />
              </Field>
              <Field label={t("schedule.editor.maxRetries")}>
                <input
                  type="number"
                  min={0}
                  max={10}
                  value={form.max_retries}
                  onChange={(event) =>
                    selectNumber("max_retries", event.target.value)
                  }
                  className={inputClass}
                />
              </Field>
              <Field label={t("schedule.editor.retryBackoff")}>
                <input
                  type="number"
                  min={0}
                  value={form.retry_backoff_seconds}
                  onChange={(event) =>
                    selectNumber("retry_backoff_seconds", event.target.value)
                  }
                  className={inputClass}
                />
              </Field>
            </div>
            <fieldset>
              <legend className="mb-2 text-sm font-medium">
                {t("schedule.editor.output")}
              </legend>
              <div className="grid gap-2 sm:grid-cols-2">
                <OutputOption
                  checked={form.result_mode === "new_session"}
                  onChange={() => onChange("result_mode", "new_session")}
                  title={t("schedule.editor.outputNew")}
                  description={t("schedule.editor.outputNewDescription")}
                />
                <OutputOption
                  checked={form.result_mode === "existing_session"}
                  onChange={() => onChange("result_mode", "existing_session")}
                  title={t("schedule.editor.outputExisting")}
                  description={t("schedule.editor.outputExistingDescription")}
                />
              </div>
            </fieldset>
            {form.result_mode === "existing_session" ? (
              <Field label={t("schedule.editor.targetSession")}>
                <select
                  value={form.target_session_id}
                  onChange={(event) =>
                    onChange("target_session_id", event.target.value)
                  }
                  className={inputClass}
                >
                  <option value="">{t("schedule.editor.chooseSession")}</option>
                  {sessions.map((session) => (
                    <option key={session.id} value={session.id}>
                      {session.title?.trim() || session.id}
                    </option>
                  ))}
                </select>
              </Field>
            ) : null}
            <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-background)] p-3.5">
              <div className="flex items-center justify-between gap-3">
                <span className="text-xs font-medium text-[var(--color-foreground-muted)]">
                  {t("schedule.editor.cronPreview")}
                </span>
                <code className="rounded bg-[var(--color-surface-muted)] px-2 py-1 font-mono text-xs">
                  {cronPreview || "—"}
                </code>
              </div>
              {form.customRawCron ? (
                <div className="mt-3 space-y-2">
                  <Field
                    label={t("schedule.editor.rawCron")}
                    hint={t("schedule.editor.rawCronHint")}
                  >
                    <input
                      value={form.customRawCron}
                      onChange={(event) =>
                        onChange("customRawCron", event.target.value)
                      }
                      className={`${inputClass} font-mono text-xs`}
                    />
                  </Field>
                </div>
              ) : null}
            </div>
          </div>
        </div>
        <div className="flex shrink-0 justify-end gap-2 border-t border-[var(--color-border)] px-5 py-4 sm:px-6">
          <Button variant="ghost" onClick={onClose}>
            {t("schedule.editor.cancel")}
          </Button>
          <Button variant="primary" disabled={saving} onClick={onSave}>
            {saving ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Check className="h-4 w-4" />
            )}
            {t(selected ? "schedule.editor.save" : "schedule.editor.create")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function CustomFrequency({
  form,
  locale,
  t,
  onChange,
  selectNumber,
}: {
  form: ScheduleForm;
  locale: "en" | "zh";
  t: (key: MessageKey, params?: Record<string, string | number>) => string;
  onChange: <K extends keyof ScheduleForm>(
    key: K,
    value: ScheduleForm[K],
  ) => void;
  selectNumber: (key: keyof ScheduleForm, value: string) => void;
}) {
  const weekdayLabels =
    locale === "zh"
      ? ["一", "二", "三", "四", "五", "六", "日"]
      : ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  return (
    <div className="space-y-4 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-background)] p-4">
      <Field label={t("schedule.editor.repeatType")}>
        <select
          value={form.customRepeat}
          onChange={(event) =>
            onChange("customRepeat", event.target.value as CustomRepeat)
          }
          className={inputClass}
        >
          <option value="hourly">{t("schedule.editor.hourly")}</option>
          <option value="daily">{t("schedule.editor.dailyRepeat")}</option>
          <option value="weekly">{t("schedule.editor.weeklyRepeat")}</option>
          <option value="monthly">{t("schedule.editor.monthly")}</option>
          <option value="yearly">{t("schedule.editor.yearly")}</option>
        </select>
      </Field>
      {form.customRepeat === "hourly" ? (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("schedule.editor.everyHours")}>
            <input
              type="number"
              min={1}
              max={23}
              value={form.customInterval}
              onChange={(event) =>
                selectNumber("customInterval", event.target.value)
              }
              className={inputClass}
            />
          </Field>
          <Field label={t("schedule.editor.minuteOfHour")}>
            <select
              value={form.customMinute}
              onChange={(event) =>
                selectNumber("customMinute", event.target.value)
              }
              className={inputClass}
            >
              {Array.from({ length: 60 }, (_, minute) => (
                <option key={minute} value={minute}>
                  {minute}
                </option>
              ))}
            </select>
          </Field>
        </div>
      ) : null}
      {form.customRepeat === "daily" ? (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("schedule.editor.everyDays")}>
            <input
              type="number"
              min={1}
              max={31}
              value={form.customInterval}
              onChange={(event) =>
                selectNumber("customInterval", event.target.value)
              }
              className={inputClass}
            />
          </Field>
          <Field label={t("schedule.editor.runTime")}>
            <TimeSelect
              value={form.time}
              onChange={(value) => onChange("time", value)}
            />
          </Field>
        </div>
      ) : null}
      {form.customRepeat === "weekly" ? (
        <div className="space-y-3">
          <Field label={t("schedule.editor.runDay")}>
            <div className="grid grid-cols-4 gap-2 sm:grid-cols-7">
              {WEEKDAYS.map((day, index) => {
                const checked = form.customWeekdays.includes(day.value);
                return (
                  <label
                    key={day.value}
                    className={cn(
                      "cursor-pointer rounded-[var(--radius)] border px-2 py-2 text-center text-sm transition-colors",
                      checked
                        ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)] font-medium"
                        : "border-[var(--color-border)] hover:border-[var(--color-border-strong)]",
                    )}
                  >
                    <input
                      type="checkbox"
                      className="sr-only"
                      checked={checked}
                      onChange={() => {
                        if (checked && form.customWeekdays.length === 1) return;
                        onChange(
                          "customWeekdays",
                          checked
                            ? form.customWeekdays.filter(
                                (value) => value !== day.value,
                              )
                            : [...form.customWeekdays, day.value],
                        );
                      }}
                    />
                    <span>
                      {locale === "zh"
                        ? `周${weekdayLabels[index]}`
                        : weekdayLabels[index]}
                    </span>
                  </label>
                );
              })}
            </div>
            <span className="mt-1.5 block text-xs leading-5 text-[var(--color-foreground-muted)]">
              {t("schedule.editor.atLeastOneDay")}
            </span>
          </Field>
          <Field label={t("schedule.editor.runTime")}>
            <TimeSelect
              value={form.time}
              onChange={(value) => onChange("time", value)}
            />
          </Field>
        </div>
      ) : null}
      {form.customRepeat === "monthly" ? (
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("schedule.editor.everyMonths")}>
            <input
              type="number"
              min={1}
              max={12}
              value={form.customInterval}
              onChange={(event) =>
                selectNumber("customInterval", event.target.value)
              }
              className={inputClass}
            />
          </Field>
          <Field label={t("schedule.editor.dayOfMonth")}>
            <select
              value={form.customDay}
              onChange={(event) =>
                selectNumber("customDay", event.target.value)
              }
              className={inputClass}
            >
              {DAYS.map((day) => (
                <option key={day.value} value={day.value}>
                  {t("schedule.dayName", { value: day.value })}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("schedule.editor.runTime")}>
            <TimeSelect
              value={form.time}
              onChange={(value) => onChange("time", value)}
            />
          </Field>
        </div>
      ) : null}
      {form.customRepeat === "yearly" ? (
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("schedule.editor.month")}>
            <select
              value={form.customMonth}
              onChange={(event) =>
                selectNumber("customMonth", event.target.value)
              }
              className={inputClass}
            >
              {MONTHS.map((month) => (
                <option key={month.value} value={month.value}>
                  {t("schedule.monthName", { value: month.value })}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("schedule.editor.day")}>
            <select
              value={form.customDay}
              onChange={(event) =>
                selectNumber("customDay", event.target.value)
              }
              className={inputClass}
            >
              {DAYS.map((day) => (
                <option key={day.value} value={day.value}>
                  {t("schedule.dayName", { value: day.value })}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("schedule.editor.runTime")}>
            <TimeSelect
              value={form.time}
              onChange={(value) => onChange("time", value)}
            />
          </Field>
        </div>
      ) : null}
      <p className="text-xs leading-5 text-[var(--color-foreground-muted)]">
        {t("schedule.editor.customHint")}
      </p>
    </div>
  );
}

function TimeSelect({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <select
      value={value}
      onChange={(event) => onChange(event.target.value)}
      className={inputClass}
    >
      {TIME_OPTIONS.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
}
function OutputOption({
  checked,
  onChange,
  title,
  description,
}: {
  checked: boolean;
  onChange: () => void;
  title: string;
  description: string;
}) {
  return (
    <label
      className={cn(
        "cursor-pointer rounded-[var(--radius-lg)] border p-3.5 transition-colors",
        checked
          ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]"
          : "border-[var(--color-border)] hover:border-[var(--color-border-strong)]",
      )}
    >
      <input
        type="radio"
        className="sr-only"
        checked={checked}
        onChange={onChange}
      />
      <span className="block text-sm font-medium">{title}</span>
      <span className="mt-1 block text-xs leading-5 text-[var(--color-foreground-muted)]">
        {description}
      </span>
    </label>
  );
}
const inputClass =
  "w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm outline-none transition-colors focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent-soft)]";
function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block font-medium">{label}</span>
      {children}
      {hint ? (
        <span className="mt-1.5 block text-xs leading-5 text-[var(--color-foreground-muted)]">
          {hint}
        </span>
      ) : null}
    </label>
  );
}
function Hint({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-[var(--radius)] bg-[var(--color-surface-muted)] px-3 py-2.5 text-xs leading-5 text-[var(--color-foreground-muted)]">
      {children}
    </div>
  );
}
function DetailRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 px-5 py-4 text-sm">
      <span className="text-[var(--color-foreground-muted)]">{label}</span>
      <span className="text-right font-medium">{value}</span>
    </div>
  );
}
