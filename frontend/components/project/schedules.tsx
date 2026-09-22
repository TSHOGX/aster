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
  type SchedulePage,
  type ScheduleRun,
} from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import { ProjectPicker } from "./project-picker";
import { IconButton, Menu, useDialog } from "./ui";
import { mergePage } from "@/lib/workspace-state";
export function Schedules({
  projects,
  onOpen,
}: {
  projects: Project[];
  onOpen: (project: string, session: string) => void;
}) {
  const tr = useT(),
    ask = useDialog();
  const [filter, setFilter] = useState(""),
    [page, setPage] = useState<SchedulePage>({ schedules: [] }),
    [error, setError] = useState("");
  const [selected, setSelected] = useState<ScheduleOverview | null>(null),
    [creating, setCreating] = useState(false),
    [busy, setBusy] = useState(false);
  const serial = useRef(0);
  const retainedCount = useRef(0);
  const load = useCallback(
    async (after = "") => {
      const id = ++serial.current;
      try {
        let next = await api<SchedulePage>(
          `/api/schedules?project_id=${encode(filter)}&after=${encode(after)}&limit=50`,
        );
        if (!after)
          while (next.next && next.schedules.length < retainedCount.current) {
            const additional = await api<SchedulePage>(
              `/api/schedules?project_id=${encode(filter)}&after=${encode(next.next)}&limit=50`,
            );
            next = {
              ...additional,
              schedules: [...next.schedules, ...additional.schedules],
            };
          }
        if (id !== serial.current) return;
        retainedCount.current = after
          ? retainedCount.current + next.schedules.length
          : next.schedules.length;
        setPage((old) => ({
          ...next,
          schedules: after
            ? mergePage(old.schedules, next.schedules)
            : next.schedules,
        }));
        setError("");
      } catch (e) {
        if (id === serial.current) setError(String(e));
      }
    },
    [filter],
  );
  useEffect(() => {
    void Promise.resolve().then(() => load());
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, 10000);
    return () => {
      clearInterval(timer);
    };
  }, [load]);
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
    <section className="adjacent-panel schedules-page">
      <header className="page-heading">
        <div>
          <h1>{tr("Scheduled")}</h1>
          <p>{tr("Tasks across your projects")}</p>
        </div>
        <button
          onClick={() => {
            setSelected(null);
            setCreating(true);
          }}
        >
          <Plus size={17} />
          {tr("Create schedule")}
        </button>
      </header>
      <div className="schedule-filter">
        <ProjectPicker
          label={tr("Filter by Project")}
          projects={projects}
          value={filter}
          emptyOption={tr("All Projects")}
          onChange={(id) => {
            retainedCount.current = 0;
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
              onClick={() => {
                setSelected(s);
                setCreating(false);
              }}
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
                      await command(
                        `${projectURL(s.project_id)}/schedules/${encode(s.id)}`,
                        { ...s, enabled: !s.enabled },
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
      {!page.schedules.length && !error && (
        <div className="empty-state">
          <Clock3 size={28} />
          <p>{tr("No scheduled tasks yet.")}</p>
        </div>
      )}
      {page.next && (
        <button onClick={() => void load(page.next)}>{tr("Show more")}</button>
      )}
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
  schedule: ScheduleOverview | null;
  projects: Project[];
  defaultProject: string;
  onClose: () => void;
  onSaved: () => void;
  onOpen: (project: string, session: string) => void;
}) {
  const tr = useT();
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
  useEffect(() => {
    let gone = false;
    if (project)
      void api<{ sessions: AgentSession[] }>(
        `${projectURL(project)}/sessions?limit=100`,
      )
        .then((data) => {
          if (!gone) {
            setSessions(data.sessions.filter((s) => s.deletion === 0));
            setMore(data.sessions.length === 100);
          }
        })
        .catch((e) => {
          if (!gone) setError(String(e));
        });
    return () => {
      gone = true;
    };
  }, [project]);
  const history = async () => {
    try {
      const data = await api<{ runs: ScheduleRun[] }>(
        `${projectURL(project)}/schedules/${encode(schedule!.id)}/runs?after=${encode(runs?.at(-1)?.id || "")}&limit=50`,
      );
      setRuns((old) => [...(old || []), ...data.runs]);
    } catch (e) {
      setError(String(e));
    }
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
        {more && (
          <button
            type="button"
            onClick={() =>
              void api<{ sessions: AgentSession[] }>(
                `${projectURL(project)}/sessions?after=${encode(sessions.at(-1)?.id || "")}&limit=100`,
              )
                .then((data) => {
                  setSessions((old) => mergePage(old, data.sessions));
                  setMore(data.sessions.length === 100);
                })
                .catch((e) => setError(String(e)))
            }
          >
            {tr("Show more")}
          </button>
        )}
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
            <button type="button" onClick={() => void history()}>
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
            <button onClick={() => void history()}>{tr("Show more")}</button>
          )}
        </div>
      )}
    </dialog>
  );
}
