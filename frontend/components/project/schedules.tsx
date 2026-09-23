"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { Clock3, Plus, X } from "lucide-react";
import {
  api,
  command,
  encode,
  newID,
  projectURL,
  type AgentSession,
  type Project,
  type ScheduleOverview,
  type Schedule,
  type SchedulePage,
  type ScheduleRun,
} from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import { ProjectPicker } from "./project-picker";
import { IconButton, Menu, useDialog } from "./ui";
import { useResources } from "./resource-provider";
export function Schedules({
  projects,
  onOpen,
}: {
  projects: Project[];
  onOpen: (project: string, session: string) => void;
}) {
  const tr = useT(),
    ask = useDialog();
  const resources = useResources();
  const [filter, setFilter] = useState(""),
    [page, setPage] = useState<SchedulePage>({ schedules: [] }),
    [error, setError] = useState("");
  const [selected, setSelected] = useState<(Schedule & { project_title: string }) | null>(null),
    [creating, setCreating] = useState(false),
    [busy, setBusy] = useState(false);
  const serial = useRef(0);
  const request = useRef<AbortController | null>(null);
  const detail = useRef<AbortController | null>(null);
  const [loading, setLoading] = useState(true);
  const cursor = useRef("");
  const [hasPrevious, setHasPrevious] = useState(false);
  const load = useCallback(
    async (after = cursor.current) => {
      request.current?.abort();
      const abort = new AbortController();
      request.current = abort;
      const id = ++serial.current;
      setLoading(true);
      try {
        const url = `/api/schedules?project_id=${encode(filter)}&after=${encode(after)}&limit=50`;
        const next = await resources.read<SchedulePage>(
          { project: filter || "schedule-overview", kind: "index", identity: url },
          signal => api<SchedulePage>(url, { signal }), abort.signal,
        );
        if (abort.signal.aborted || id !== serial.current) return;
        if (next.schedules.length > 50 || JSON.stringify(next).length * 3 > 1024 * 1024)
          throw new Error("Schedule page exceeds its display budget.");
        cursor.current = after;
        setHasPrevious(!!after);
        setPage(next);
        setError("");
      } catch (e) {
        if (abort.signal.aborted || id !== serial.current) return;
        setError(String(e));
      } finally {
        if (!abort.signal.aborted && id === serial.current) setLoading(false);
      }
    }, [filter, resources],
  );
  useEffect(() => {
    let live = true;
    cursor.current = "";
    void Promise.resolve().then(() => { if (live) void load(""); });
    return () => { live = false; request.current?.abort(); detail.current?.abort(); };
  }, [load]);
  const fullSchedule = async (summary: ScheduleOverview) => {
    detail.current?.abort();
    const abort = new AbortController();
    detail.current = abort;
    const url = `${projectURL(summary.project_id)}/schedules/${encode(summary.id)}`;
    const schedule = await resources.read<Schedule>(
      { project: summary.project_id, kind: "index", identity: url },
      signal => api<Schedule>(url, { signal }), abort.signal,
    );
    abort.signal.throwIfAborted();
    return schedule;
  };
  const openSchedule = async (summary: ScheduleOverview) => {
    setBusy(true);
    try { const schedule = await fullSchedule(summary); setSelected({ ...schedule, project_title: summary.project_title }); setCreating(false); }
    catch (e) { if (!(e instanceof DOMException && e.name === "AbortError")) setError(String(e)); }
    finally { setBusy(false); }
  };
  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="adjacent-panel schedules-page" aria-busy={loading}>
      <header className="page-heading">
        <div>
          <h1>{tr("Scheduled")}</h1>
          <p>{tr("Tasks across your projects")}</p>
        </div>
        <button
          onClick={() => {
            detail.current?.abort();
            setSelected(null);
            setCreating(true);
          }}
        >
          <Plus size={17} />
          {tr("Create schedule")}
        </button>
      </header>
      <div className="schedule-filter">
        <button disabled={loading} onClick={() => void load()}>{tr("Refresh")}</button>
        <ProjectPicker
          label={tr("Filter by Project")}
          projects={projects}
          value={filter}
          emptyOption={tr("All Projects")}
          onChange={(id) => {
            request.current?.abort();
            detail.current?.abort();
            setPage({ schedules: [] });
            setLoading(true);
            setFilter(id);
            setSelected(null);
          }}
        />
      </div>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      <div className="schedule-list">
        {page.schedules.map((s) => (
          <div key={s.id} className="schedule-row">
            <Clock3 size={19} />
            <button
              className="schedule-description"
              disabled={busy}
              onClick={() => void openSchedule(s)}
            >
              <strong>{s.title}</strong>
              <small>
                {s.project_title} ·{" "}
                {s.session_id
                  ? tr("Existing conversation")
                  : tr("New conversation each run")}
              </small>
              <small>
                {s.next_at
                  ? new Date(s.next_at).toLocaleString()
                  : tr("Manual only")}{" "}
                · {s.timezone}
              </small>
            </button>
            <span className="quiet-status">
              {s.enabled ? tr("Enabled") : tr("Disabled")}
            </span>
            <Menu
              label={`${tr("Schedule actions")} ${s.title}`}
              items={[
                {
                  label: tr("Run now"),
                  disabled: busy || !s.enabled,
                  action: () =>
                    void act(async () => {
                      const run = await command<ScheduleRun>(
                        `${projectURL(s.project_id)}/schedules/${encode(s.id)}/run`,
                        { request_id: newID() },
                      );
                      onOpen(run.project_id, run.session_id);
                    }),
                },
                {
                  label: s.enabled ? tr("Disable") : tr("Enable"),
                  disabled: busy,
                  action: () =>
                    void act(async () => {
                      const full = await fullSchedule(s);
                      await command(
                        `${projectURL(s.project_id)}/schedules/${encode(s.id)}`,
                        { ...full, enabled: !s.enabled },
                        "PATCH",
                      );
                    }),
                },
                {
                  label: tr("Delete schedule"),
                  danger: true,
                  action: () =>
                    void (async () => {
                      if (
                        await ask({
                          title: tr("Delete schedule"),
                          description: tr(
                            "Delete this schedule? Admitted runs retain their own controls.",
                          ),
                          choices: [
                            { label: tr("Delete"), value: "yes", danger: true },
                          ],
                        })
                      )
                        void act(async () => {
                          await command(
                            `${projectURL(s.project_id)}/schedules/${encode(s.id)}?revision=${s.revision}`,
                            undefined,
                            "DELETE",
                          );
                          if (selected?.id === s.id) setSelected(null);
                        });
                    })(),
                },
              ]}
            />
          </div>
        ))}
      </div>
      {loading && <p role="status">{tr("Loading…")}</p>}
      {!page.schedules.length && !error && !loading && (
        <div className="empty-state">
          <Clock3 size={28} />
          <p>{tr("No scheduled tasks yet.")}</p>
        </div>
      )}
      <div className="schedule-pages">
        {hasPrevious && <button disabled={loading} onClick={() => void load("")}>{tr("First page")}</button>}
        {page.next && <button disabled={loading} onClick={() => void load(page.next)}>{tr("Next page")}</button>}
      </div>
      {(selected || creating) && (
        <ScheduleDetails
          key={selected?.id || "new"}
          schedule={selected}
          projects={projects}
          defaultProject={filter}
          onClose={() => {
            setSelected(null);
            setCreating(false);
          }}
          onSaved={() => {
            setSelected(null);
            setCreating(false);
            void load();
          }}
          onOpen={onOpen}
        />
      )}
    </section>
  );
}
function ScheduleDetails({
  schedule,
  projects,
  defaultProject,
  onClose,
  onSaved,
  onOpen,
}: {
  schedule: (Schedule & { project_title: string }) | null;
  projects: Project[];
  defaultProject: string;
  onClose: () => void;
  onSaved: () => void;
  onOpen: (project: string, session: string) => void;
}) {
  const tr = useT();
  const resources = useResources();
  const sessionRequest = useRef<AbortController | null>(null);
  const [sessionLoading, setSessionLoading] = useState(false);
  const [sessionAfter, setSessionAfter] = useState("");
  const historyRequest = useRef<AbortController | null>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [project, setProject] = useState(
      schedule?.project_id || defaultProject,
    ),
    [title, setTitle] = useState(schedule?.title || ""),
    [prompt, setPrompt] = useState(schedule?.prompt || ""),
    [cron, setCron] = useState(schedule?.cron || ""),
    [timezone, setTimezone] = useState(
      schedule?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone,
    ),
    [session, setSession] = useState(schedule?.session_id || "");
  const [sessions, setSessions] = useState<AgentSession[]>([]),
    [more, setMore] = useState(false),
    [runs, setRuns] = useState<ScheduleRun[] | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const requestId = useRef(newID());
  useEffect(() => {
    const previous = document.activeElement as HTMLElement;
    dialog.current?.showModal();
    return () => previous?.focus();
  }, []);
  const loadSessions = useCallback(async (after = "") => {
    sessionRequest.current?.abort();
    const abort = new AbortController();
    sessionRequest.current = abort;
    if (!project) return;
    setSessionLoading(true);
    try {
      const url = `${projectURL(project)}/sessions?limit=100&after=${encode(after)}`;
      const data = await resources.read<{ sessions: AgentSession[] }>(
        { project, kind: "index", identity: url }, signal => api(url, { signal }), abort.signal,
      );
      if (!abort.signal.aborted) {
        setSessions(data.sessions.filter(s => s.deletion === 0).slice(0,100));
        setMore(data.sessions.length === 100); setSessionAfter(after);
      }
    } catch (e) { if (!abort.signal.aborted) setError(String(e)); }
    finally { if (!abort.signal.aborted) setSessionLoading(false); }
  }, [project, resources]);
  useEffect(() => {
    let live = true;
    void Promise.resolve().then(() => { if (live) void loadSessions(); });
    return () => { live = false; sessionRequest.current?.abort(); historyRequest.current?.abort(); };
  }, [loadSessions]);
  const history = async (after = "") => {
    historyRequest.current?.abort();
    const abort = new AbortController();
    historyRequest.current = abort;
    setHistoryLoading(true);
    try {
      const url = `${projectURL(project)}/schedules/${encode(schedule!.id)}/runs?after=${encode(after)}&limit=50`;
      const data = await resources.read<{ runs: ScheduleRun[] }>(
        { project, kind: "index", identity: url }, signal => api(url, { signal }), abort.signal,
      );
      if (!abort.signal.aborted) setRuns(data.runs.slice(0, 50));
    } catch (e) { if (!abort.signal.aborted) setError(String(e)); }
    finally { if (!abort.signal.aborted) setHistoryLoading(false); }
  };
  return (
    <dialog
      className="app-dialog schedule-dialog"
      ref={dialog}
      aria-labelledby="schedule-detail-title"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (!project) return;
          setBusy(true);
          void command(
            `${projectURL(project)}/schedules${schedule ? `/${encode(schedule.id)}` : ""}`,
            {
              id: schedule?.id || requestId.current,
              title,
              prompt,
              cron,
              timezone,
              session_id: session,
              enabled: schedule?.enabled ?? true,
              ...(schedule ? { revision: schedule.revision } : {}),
            },
            schedule ? "PATCH" : "POST",
          )
            .then(onSaved)
            .catch((e) => setError(String(e)))
            .finally(() => setBusy(false));
        }}
      >
        <header>
          <h2 id="schedule-detail-title">
            {schedule ? tr("Edit schedule") : tr("Create schedule")}
          </h2>
          <IconButton label={tr("Close")} onClick={onClose}>
            <X size={18} />
          </IconButton>
        </header>
        {error && (
          <p className="notice error" role="alert">
            {error}
          </p>
        )}
        <div className="project-picker-field">
          <span>{tr("Project")}</span>
          <ProjectPicker
            projects={projects}
            value={project}
            disabled={!!schedule}
            fallbackTitle={schedule?.project_title}
            onChange={(id) => {
              setProject(id);
              setSession("");
              setSessions([]);
            }}
          />
        </div>
        <label>
          {tr("Schedule name")}
          <input
            required
            maxLength={256}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </label>
        <label>
          {tr("Prompt")}
          <textarea
            required
            maxLength={64000}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
        </label>
        <label>
          {tr("Target Session")}
          <select value={session} onChange={(e) => setSession(e.target.value)}>
            <option value="">{tr("Create a new Session per run")}</option>
            {session && !sessions.some((s) => s.id === session) && (
              <option value={session}>{session}</option>
            )}
            {sessions.map((s) => (
              <option key={s.id} value={s.id}>
                {s.title}
              </option>
            ))}
          </select>
        </label>
        <div>
          {sessionAfter && <button type="button" disabled={sessionLoading} onClick={() => void loadSessions()}>{tr("First page")}</button>}
          {more && <button type="button" disabled={sessionLoading} onClick={() => void loadSessions(sessions.at(-1)?.id || "")}>{tr("Next page")}</button>}
        </div>
        <label>
          {tr("Cron expression")}
          <input
            value={cron}
            onChange={(e) => setCron(e.target.value)}
            placeholder={tr("Leave empty for manual runs")}
          />
        </label>
        <label>
          {tr("Timezone")}
          <input
            required
            value={timezone}
            onChange={(e) => setTimezone(e.target.value)}
          />
        </label>
        <footer>
          {schedule && (
            <button type="button" disabled={historyLoading} onClick={() => void history()}>
              {tr("Run history")}
            </button>
          )}
          <button type="button" onClick={onClose}>
            {tr("Cancel")}
          </button>
          <button className="primary" disabled={busy || !project}>
            {tr("Save")}
          </button>
        </footer>
      </form>
      {runs && (
        <div className="run-history">
          {runs.map((run) => (
            <button
              key={run.id}
              onClick={() => {
                onClose();
                onOpen(run.project_id, run.session_id);
              }}
            >
              {run.due_at
                ? new Date(run.due_at).toLocaleString()
                : tr("Manual run")}{" "}
              · {run.state}
            </button>
          ))}
          {!runs.length && <p>{tr("No runs yet.")}</p>}
          {runs.length > 0 && runs.length % 50 === 0 && (
            <button disabled={historyLoading} onClick={() => void history(runs.at(-1)!.id)}>{tr("Next page")}</button>
          )}
        </div>
      )}
    </dialog>
  );
}
