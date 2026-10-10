"use client";
import { useEffect, useRef, useState } from "react";
import { ArrowUpRight, CalendarClock, Pencil } from "lucide-react";
import { ScheduleStatus } from "./schedule-status";
import {
  api,
  command,
  encode,
  newID,
  projectURL,
  type ScheduleOverview,
} from "@/lib/project-api";
import { useLocale, useT } from "@/lib/i18n";
import { scheduleTime } from "@/lib/schedule-recurrence";
import { navigationURL } from "@/lib/workspace-state";
import { ScheduleHintGate } from "@/lib/schedule-window";
import { useRealtime, useResources } from "./resource-provider";

export function ScheduleTaskCard({
  project,
  id,
}: {
  project: string;
  id: string;
}) {
  const tr = useT(),
    locale = useLocale(),
    realtime = useRealtime(),
    resources = useResources();
  const root = useRef<HTMLElement>(null);
  const [visible, setVisible] = useState(false),
    [task, setTask] = useState<ScheduleOverview>(),
    [error, setError] = useState(false),
    [refresh, setRefresh] = useState(0),
    [busy, setBusy] = useState(false);
  const pending = useRef<{ request_id: string; revision: number } | undefined>(
    undefined,
  );
  const path = `${projectURL(project)}/schedules/${encode(id)}`;
  useEffect(() => {
    const observer = new IntersectionObserver(
      (entries) => setVisible(entries.some((entry) => entry.isIntersecting)),
      { rootMargin: "100px" },
    );
    if (root.current) observer.observe(root.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const key = {
      project: "",
      kind: "index" as const,
      identity: `schedule-card:${project}:${id}`,
    };
    const load = () => {
      resources.invalidate(
        (candidate) =>
          candidate.project === key.project &&
          candidate.identity === key.identity,
      );
      void resources
        .read(
          key,
          (signal) => api<ScheduleOverview>(`${path}/metadata`, { signal }),
          controller.signal,
        )
        .then((value) => {
          if (!controller.signal.aborted) {
            setTask(value);
            setError(false);
          }
        })
        .catch(() => {
          if (!controller.signal.aborted) setError(true);
        });
    };
    load();
    const gate = new ScheduleHintGate();
    const unsubscribe = realtime.subscribe("", (event) => {
      if (event.resource_kind === "ready" || !gate.accept(event)) return;
      if (event.resource_kind === "closed") {
        controller.abort();
        clearTimeout(timer);
        setTask(undefined);
        setError(true);
        return;
      }
      const payload = event.payload as
        | { project_id?: string; schedule_id?: string }
        | undefined;
      if (
        event.resource_kind === "resync" ||
        (payload?.project_id === project &&
          (!payload.schedule_id || payload.schedule_id === id))
      ) {
        clearTimeout(timer);
        timer = setTimeout(load, 150);
      }
    });
    return () => {
      controller.abort();
      clearTimeout(timer);
      unsubscribe();
    };
  }, [visible, project, id, path, realtime, resources, refresh]);
  const cancel = async () => {
    if (!task || busy) return;
    pending.current ||= { request_id: newID(), revision: task.revision };
    setBusy(true);
    try {
      await command(`${path}/cancel`, pending.current);
      pending.current = undefined;
      setRefresh((n) => n + 1);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  };
  const taskURL = navigationURL({
    view: "schedules",
    project: "",
    session: "",
    message: "",
    draft: false,
    history: false,
    schedule: id,
    scheduleProject: project,
    scheduleStatus: task?.state,
  });
  const resultURL = task?.last_session_id
    ? navigationURL({
        view: "project",
        project,
        session: task.last_session_id,
        message: "",
        draft: false,
        history: false,
      })
    : "";
  return (
    <article
      ref={root}
      className="schedule-tool-card"
      data-schedule-id={id}
    >
      <header className="schedule-tool-heading">
        <span className="schedule-tool-icon">
          <CalendarClock size={20} aria-hidden="true" />
        </span>
        <div>
          <span className="schedule-eyebrow">{tr("Scheduled task")}</span>
          <strong>{task?.title || tr("Schedule")}</strong>
        </div>
      </header>
      {error ? (
        <p role="status">
          {tr("Task status is unavailable.")}{" "}
          <button onClick={() => setRefresh((n) => n + 1)}>
            {tr("Retry")}
          </button>
        </p>
      ) : task ? (
        <>
          <div className="schedule-tool-status">
            <ScheduleStatus state={task.state} />
          </div>
          {task.last_state && (
            <div className="schedule-latest-result">
              <span>{tr("Latest result")}</span>
              <ScheduleStatus state={task.last_state} run />
            </div>
          )}
          {task.next_at && (
            <dl className="schedule-tool-time">
              <dt>{tr("Next run")}</dt>
              <dd>{scheduleTime(task.next_at, task.timezone, locale)}</dd>
            </dl>
          )}
          {task.state === "deleted" && <p className="schedule-tool-note">{tr("This task was deleted.")}</p>}
        </>
      ) : (
        <p>{tr("Loading…")}</p>
      )}
      <div className="schedule-card-actions">
        <a href={taskURL}>
          {tr("View task")}
          <ArrowUpRight size={15} aria-hidden="true" />
        </a>
        {resultURL && <a href={resultURL}>{tr("Open conversation")}</a>}
        {task && ["active", "paused"].includes(task.state) && (
          <>
            <a href={taskURL + "&mode=edit"}>
              <Pencil size={14} aria-hidden="true" />
              {tr("Edit")}
            </a>
            <button
              className="schedule-card-cancel"
              disabled={busy}
              onClick={() => void cancel()}
            >
              {tr("Cancel task")}
            </button>
          </>
        )}
      </div>
    </article>
  );
}
