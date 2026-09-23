"use client";
import { useEffect, useRef, useState, type RefObject } from "react";
import { Clock3, Pause, Plus } from "lucide-react";
import {
  api,
  APIError,
  command,
  encode,
  newID,
  projectURL,
  sessionURL,
  type AgentSession,
  type Project,
  type Schedule,
  type SchedulePage,
  type ScheduleRun,
  type ScheduleRunPage,
} from "@/lib/project-api";
import type { Navigation } from "@/lib/workspace-state";
import { useLocale, useT } from "@/lib/i18n";
import { scheduleTime } from "@/lib/schedule-recurrence";
import { ProjectPicker } from "./project-picker";
import { Menu, useDialog } from "./ui";
import { RecurrenceSummary, UpcomingOccurrences } from "./schedule-recurrence";
import { ScheduleEditor } from "./schedule-editor";

export function Schedules({
  projects,
  onOpen,
  nav,
  onNavigate,
  guardRef,
  runRequests,
  listMemoryRef,
}: {
  projects: Project[];
  onOpen: (project: string, session: string) => void;
  nav: Navigation;
  onNavigate: (n: Navigation, replace?: boolean) => void;
  guardRef: RefObject<() => Promise<boolean>>;
  runRequests: RefObject<Map<string, string>>;
  listMemoryRef: RefObject<{ key: string; top: number }>;
}) {
  const tr = useT(),
    locale = useLocale();
  const [refresh, setRefresh] = useState(0),
    [notice, setNotice] = useState("");
  const [query, setQuery] = useState(nav.scheduleQuery || ""),
    [composing, setComposing] = useState(false);
  const [page, setPage] = useState<{
    key: string;
    value?: SchedulePage;
    error?: string;
  }>();
  const [detail, setDetail] = useState<{
    key: string;
    value?: Schedule;
    error?: string;
  }>();
  const [detailRefresh, setDetailRefresh] = useState(0);
  const navRef = useRef(nav),
    navigateRef = useRef(onNavigate);
  useEffect(() => {
    navRef.current = nav;
    navigateRef.current = onNavigate;
  }, [nav, onNavigate]);
  useEffect(() => {
    void Promise.resolve().then(() => setQuery(nav.scheduleQuery || ""));
  }, [nav.scheduleQuery]);
  useEffect(() => {
    if (composing || query === (nav.scheduleQuery || "")) return;
    const timer = setTimeout(
      () =>
        navigateRef.current(
          { ...navRef.current, scheduleQuery: query, scheduleAfter: undefined },
          true,
        ),
      250,
    );
    return () => clearTimeout(timer);
  }, [query, composing, nav.scheduleQuery]);
  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (
        event.key !== "Escape" ||
        event.defaultPrevented ||
        document.querySelector('dialog[open],[role="menu"],[role="listbox"]')
      )
        return;
      const current = navRef.current;
      if (current.schedule || current.scheduleMode === "create") {
        event.preventDefault();
        navigateRef.current({
          ...current,
          schedule: undefined,
          scheduleProject: undefined,
          scheduleMode: undefined,
        });
      }
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, []);
  const filter = nav.scheduleFilter || "",
    status = nav.scheduleStatus || "",
    after = nav.scheduleAfter || "";
  const listKey = JSON.stringify([
    filter,
    status,
    nav.scheduleQuery || "",
    after,
    refresh,
  ]);
  useEffect(() => {
    const controller = new AbortController();
    const url = `/api/schedules?project_id=${encode(filter)}&q=${encode(nav.scheduleQuery || "")}&status=${status}&after=${encode(after)}&limit=50`;
    void api<SchedulePage>(url, { signal: controller.signal })
      .then((value) => {
        if (controller.signal.aborted) return;
        if (
          value.schedules.length > 50 ||
          JSON.stringify(value).length * 3 > 1024 * 1024
        )
          throw new Error("Schedule page exceeds its display budget.");
        setPage({ key: listKey, value });
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 409 && after) {
          setNotice(tr("Tasks changed. Showing a fresh first page."));
          navigateRef.current(
            { ...navRef.current, scheduleAfter: undefined },
            true,
          );
          return;
        }
        setPage({ key: listKey, error: String(e) });
      });
    return () => controller.abort();
  }, [filter, status, nav.scheduleQuery, after, refresh, listKey, tr]);
  const selectedKey =
    nav.schedule && nav.scheduleProject
      ? `${nav.scheduleProject}:${nav.schedule}`
      : "";
  useEffect(() => {
    if (!selectedKey) return;
    const controller = new AbortController();
    void api<Schedule>(
      `${projectURL(nav.scheduleProject!)}/schedules/${encode(nav.schedule!)}`,
      { signal: controller.signal },
    )
      .then((value) => {
        if (!controller.signal.aborted) {
          if (value.deleted) throw new Error("This task was deleted.");
          setDetail({ key: selectedKey, value });
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted)
          setDetail({ key: selectedKey, error: String(e) });
      });
    return () => controller.abort();
  }, [selectedKey, nav.scheduleProject, nav.schedule, detailRefresh]);
  const listRef = useRef<HTMLDivElement>(null);
  const memoryKey = JSON.stringify([
    filter,
    status,
    nav.scheduleQuery || "",
    after,
  ]);
  useEffect(() => {
    if (page?.key !== listKey || !listRef.current) return;
    listRef.current.scrollTop =
      listMemoryRef.current.key === memoryKey ? listMemoryRef.current.top : 0;
  }, [page, listKey, memoryKey, listMemoryRef]);
  const current = page?.key === listKey ? page : undefined;
  const selected = detail?.key === selectedKey ? detail : undefined;
  const loading = !current;
  const creating = nav.scheduleMode === "create";
  const hasDetail = creating || !!selectedKey;
  const close = () =>
    onNavigate({
      ...nav,
      schedule: undefined,
      scheduleProject: undefined,
      scheduleMode: undefined,
    });
  const saved = (value: Schedule) => {
    setDetail({ key: `${value.project_id}:${value.id}`, value });
    setNotice(tr("Task saved."));
    setRefresh((n) => n + 1);
    onNavigate(
      {
        ...nav,
        schedule: value.id,
        scheduleProject: value.project_id,
        scheduleMode: undefined,
      },
      true,
    );
  };
  const setFilter = (next: Partial<Navigation>) =>
    onNavigate({ ...nav, ...next, scheduleAfter: undefined }, true);
  return (
    <section
      className={`schedules-page ${hasDetail ? "has-detail" : ""}`}
      aria-busy={loading}
    >
      <div className="schedules-layout">
        <section
          className="schedule-list-pane"
          aria-label={tr("Scheduled tasks")}
        >
          <header className="page-heading">
            <h1>{tr("Scheduled")}</h1>
            <button
              onClick={() =>
                onNavigate({
                  ...nav,
                  schedule: undefined,
                  scheduleProject: undefined,
                  scheduleMode: "create",
                })
              }
            >
              <Plus size={17} />
              {tr("Create task")}
            </button>
          </header>
          <div className="schedule-filters">
            <div className="row-actions" aria-label={tr("Task status")}>
              {([undefined, "enabled", "paused"] as const).map((value) => (
                <button
                  key={value || "all"}
                  aria-pressed={nav.scheduleStatus === value}
                  onClick={() => setFilter({ scheduleStatus: value })}
                >
                  {value === "enabled"
                    ? tr("Enabled")
                    : value === "paused"
                      ? tr("Paused")
                      : tr("All")}
                </button>
              ))}
            </div>
            <input
              aria-label={tr("Search task names")}
              placeholder={tr("Search task names")}
              maxLength={256}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onCompositionStart={() => setComposing(true)}
              onCompositionEnd={() => setComposing(false)}
            />
            <div className="schedule-filter">
              <ProjectPicker
                label={tr("Filter by Project")}
                projects={projects}
                value={filter}
                emptyOption={tr("All Projects")}
                onChange={(id) => setFilter({ scheduleFilter: id })}
              />
              <button
                disabled={loading}
                onClick={() => setRefresh((n) => n + 1)}
              >
                {tr("Refresh")}
              </button>
            </div>
          </div>
          {notice && (
            <p role="status" className="notice">
              {notice}
            </p>
          )}
          {notice === tr("Task saved.") &&
            current?.value &&
            selected?.value &&
            !current.value.schedules.some(
              (s) => s.id === selected.value!.id,
            ) && (
              <p role="status">
                {tr("Saved task is outside this page or filter.")}{" "}
                <button
                  onClick={() =>
                    setFilter({
                      scheduleFilter: undefined,
                      scheduleStatus: undefined,
                      scheduleQuery: undefined,
                    })
                  }
                >
                  {tr("Show all tasks")}
                </button>
              </p>
            )}
          {current?.error && (
            <p role="alert" className="notice error">
              {current.error}{" "}
              <button onClick={() => setRefresh((n) => n + 1)}>
                {tr("Retry")}
              </button>
            </p>
          )}
          <div
            className="schedule-list"
            ref={listRef}
            onScroll={(event) => {
              listMemoryRef.current = {
                key: memoryKey,
                top: event.currentTarget.scrollTop,
              };
            }}
          >
            {loading && <p role="status">{tr("Loading…")}</p>}
            {current?.value?.schedules.map((s) => (
              <div
                key={s.id}
                className={`schedule-row ${nav.schedule === s.id ? "selected" : ""}`}
              >
                {s.enabled ? <Clock3 size={19} /> : <Pause size={19} />}
                <button
                  className="schedule-description"
                  aria-current={nav.schedule === s.id ? "page" : undefined}
                  onClick={() =>
                    onNavigate({
                      ...nav,
                      schedule: s.id,
                      scheduleProject: s.project_id,
                      scheduleMode: undefined,
                    })
                  }
                >
                  <strong>{s.title}</strong>
                  <small>
                    {!s.enabled && `${tr("Paused")} · `}
                    <RecurrenceSummary cron={s.cron} timezone={s.timezone} />
                  </small>
                  {s.enabled && s.cron && s.next_at && (
                    <small>
                      {tr("Next run")}:{" "}
                      {scheduleTime(s.next_at, s.timezone, locale)}
                    </small>
                  )}
                  <small>
                    {s.project_title} ·{" "}
                    {s.session_id
                      ? tr("Existing conversation")
                      : tr("New conversation each run")}
                  </small>
                </button>
                <Menu
                  label={`${tr("Schedule actions")} ${s.title}`}
                  items={[
                    {
                      label: tr("Edit task"),
                      action: () =>
                        onNavigate({
                          ...nav,
                          schedule: s.id,
                          scheduleProject: s.project_id,
                          scheduleMode: "edit",
                        }),
                    },
                  ]}
                />
              </div>
            ))}
            {current?.value?.schedules.length === 0 && (
              <div className="empty-state">
                <Clock3 size={28} />
                <p>
                  {filter || status || nav.scheduleQuery
                    ? tr("No matching tasks.")
                    : tr("No scheduled tasks yet.")}
                </p>
                {(filter || status || nav.scheduleQuery) && (
                  <button
                    onClick={() =>
                      setFilter({
                        scheduleFilter: undefined,
                        scheduleQuery: undefined,
                        scheduleStatus: undefined,
                      })
                    }
                  >
                    {tr("Clear filters")}
                  </button>
                )}
              </div>
            )}
          </div>
          <footer className="schedule-pages">
            {after && (
              <button
                disabled={loading}
                onClick={() =>
                  onNavigate({ ...nav, scheduleAfter: undefined }, true)
                }
              >
                {tr("First page")}
              </button>
            )}
            {current?.value?.next && (
              <button
                disabled={loading}
                onClick={() =>
                  onNavigate(
                    { ...nav, scheduleAfter: current.value!.next },
                    true,
                  )
                }
              >
                {tr("Next page")}
              </button>
            )}
          </footer>
        </section>
        <section
          className="schedule-detail-pane"
          aria-label={tr("Task details")}
        >
          {creating ? (
            <ScheduleEditor
              key="create"
              projects={projects}
              defaultProject={filter}
              guardRef={guardRef}
              onClose={close}
              onSaved={saved}
            />
          ) : selectedKey ? (
            !selected ? (
              <p role="status">{tr("Loading…")}</p>
            ) : selected.error ? (
              <div>
                <button onClick={close}>{tr("Back to tasks")}</button>
                <p role="alert">
                  {tr("This task is unavailable.")} {selected.error}
                </p>
                <button onClick={() => setDetailRefresh((n) => n + 1)}>
                  {tr("Retry")}
                </button>
              </div>
            ) : selected.value ? (
              <SelectedSchedule
                key={selectedKey}
                schedule={selected.value}
                editing={nav.scheduleMode === "edit"}
                projects={projects}
                guardRef={guardRef}
                runRequests={runRequests}
                onEdit={() => onNavigate({ ...nav, scheduleMode: "edit" })}
                onClose={close}
                onCancel={() => onNavigate({ ...nav, scheduleMode: undefined })}
                onSaved={saved}
                onChanged={(value) => {
                  if (
                    navRef.current.schedule === value.id &&
                    navRef.current.scheduleProject === value.project_id
                  )
                    setDetail({ key: selectedKey, value });
                  setRefresh((n) => n + 1);
                }}
                onDeleted={() => {
                  if (
                    navRef.current.schedule === nav.schedule &&
                    navRef.current.scheduleProject === nav.scheduleProject
                  )
                    navigateRef.current({
                      ...navRef.current,
                      schedule: undefined,
                      scheduleProject: undefined,
                      scheduleMode: undefined,
                    });
                  setRefresh((n) => n + 1);
                }}
                onOpen={onOpen}
              />
            ) : null
          ) : (
            <p className="schedule-select-prompt">
              {tr("Select a task to see its details.")}
            </p>
          )}
        </section>
      </div>
    </section>
  );
}

function SelectedSchedule({
  schedule,
  editing,
  projects,
  guardRef,
  runRequests,
  onEdit,
  onClose,
  onCancel,
  onSaved,
  onChanged,
  onDeleted,
  onOpen,
}: {
  schedule: Schedule;
  editing: boolean;
  projects: Project[];
  guardRef: RefObject<() => Promise<boolean>>;
  runRequests: RefObject<Map<string, string>>;
  onEdit: () => void;
  onClose: () => void;
  onCancel: () => void;
  onSaved: (s: Schedule) => void;
  onChanged: (s: Schedule) => void;
  onDeleted: () => void;
  onOpen: (p: string, s: string) => void;
}) {
  const tr = useT(),
    locale = useLocale(),
    ask = useDialog();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [historyVersion, setHistoryVersion] = useState(0),
    [lastRun, setLastRun] = useState<ScheduleRun>();
  const [target, setTarget] = useState<AgentSession>(),
    [projectTitle, setProjectTitle] = useState(
      projects.find((p) => p.id === schedule.project_id)?.title || "",
    );
  const [targetError, setTargetError] = useState(false);
  const path = `${projectURL(schedule.project_id)}/schedules/${encode(schedule.id)}`;
  useEffect(() => {
    const controller = new AbortController();
    void api<Project>(projectURL(schedule.project_id), {
      signal: controller.signal,
    })
      .then((p) => {
        if (!controller.signal.aborted) setProjectTitle(p.title);
      })
      .catch(() => {});
    if (schedule.session_id)
      void api<AgentSession>(
        sessionURL(schedule.project_id, schedule.session_id),
        { signal: controller.signal },
      )
        .then((s) => {
          if (!controller.signal.aborted) {
            setTarget(s);
            setTargetError(s.deletion !== 0);
          }
        })
        .catch(() => {
          if (!controller.signal.aborted) setTargetError(true);
        });
    return () => controller.abort();
  }, [schedule.project_id, schedule.session_id]);
  const act = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const run = () =>
    act(async () => {
      const id = runRequests.current.get(path) || newID();
      runRequests.current.set(path, id);
      const result = await command<ScheduleRun>(`${path}/run`, {
        request_id: id,
      });
      runRequests.current.delete(path);
      setLastRun(result);
      setHistoryVersion((n) => n + 1);
      setNotice(tr("Run submitted."));
    });
  const toggle = () =>
    act(async () => {
      const latest = await api<Schedule>(path);
      const changed = await command<Schedule>(
        path,
        { ...latest, enabled: !schedule.enabled },
        "PATCH",
      );
      onChanged(changed);
      setNotice(
        changed.enabled
          ? tr("Task enabled.")
          : tr("Paused. Already submitted runs are unaffected."),
      );
    });
  const remove = async () => {
    if (
      await ask({
        title: tr("Delete task"),
        description: `${schedule.title}\n${tr("No new runs will start. Already submitted runs will not stop.")}`,
        choices: [{ label: tr("Delete"), value: "delete", danger: true }],
      })
    )
      void act(async () => {
        await command(
          `${path}?revision=${schedule.revision}`,
          undefined,
          "DELETE",
        );
        onDeleted();
      });
  };
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!schedule.next_at) return;
    const timer = setTimeout(
      () => setNow(Date.now()),
      Math.min(
        2147483647,
        Math.max(0, Date.parse(schedule.next_at) - Date.now()),
      ),
    );
    return () => clearTimeout(timer);
  }, [schedule.next_at]);

  return (
    <>
      {editing ? (
        <ScheduleEditor
          schedule={schedule}
          projects={projects}
          defaultProject=""
          guardRef={guardRef}
          onClose={onCancel}
          onSaved={onSaved}
        />
      ) : (
        <div className="schedule-reading">
          <header className="schedule-detail-heading">
            <span>{schedule.enabled ? tr("Enabled") : tr("Paused")}</span>
            <div className="row-actions">
              <button disabled={busy} onClick={onEdit}>
                {tr("Edit")}
              </button>
              <button disabled={busy} onClick={() => void toggle()}>
                {schedule.enabled ? tr("Pause") : tr("Resume")}
              </button>
              <Menu
                label={tr("Task actions")}
                items={[
                  {
                    label: tr("Delete task"),
                    danger: true,
                    disabled: busy,
                    action: () => void remove(),
                  },
                ]}
              />
              <button onClick={onClose}>{tr("Back to tasks")}</button>
            </div>
          </header>
          <h2>{schedule.title}</h2>
          <Instructions text={schedule.prompt} />
          <section className="schedule-settings">
            <h3>{tr("Runs in")}</h3>
            <dl>
              <dt>{tr("Project")}</dt>
              <dd>{projectTitle || tr("Loading…")}</dd>
              <dt>{tr("Conversation")}</dt>
              <dd>
                {schedule.session_id ? (
                  targetError ? (
                    tr("This conversation is unavailable. Choose another.")
                  ) : target?.id === schedule.session_id ? (
                    <button
                      onClick={() =>
                        onOpen(schedule.project_id, schedule.session_id!)
                      }
                    >
                      {target.title}
                    </button>
                  ) : (
                    tr("Loading…")
                  )
                ) : (
                  tr("New conversation each run")
                )}
              </dd>
            </dl>
          </section>
          <section className="schedule-settings">
            <h3>{tr("Schedule")}</h3>
            <RecurrenceSummary
              cron={schedule.cron}
              timezone={schedule.timezone}
            />
            <UpcomingOccurrences
              cron={schedule.cron}
              timezone={schedule.timezone}
              enabled={schedule.enabled}
            />
            <p>
              {!schedule.enabled
                ? tr("Paused. Resume to continue on this schedule.")
                : !schedule.cron
                  ? tr("Runs only when you choose Run now.")
                  : schedule.next_at
                    ? `${tr("Next run")}: ${scheduleTime(schedule.next_at, schedule.timezone, locale)}`
                    : tr("Upcoming time is unavailable. Refresh the task.")}
            </p>
            {schedule.enabled &&
              schedule.next_at &&
              Date.parse(schedule.next_at) <= now && (
                <p>
                  {tr("The planned time has passed. Refresh for an update.")}
                </p>
              )}
          </section>
          {notice && <p role="status">{notice}</p>}
          {lastRun && (
            <button
              onClick={() => onOpen(lastRun.project_id, lastRun.session_id)}
            >
              {tr("Open conversation")}
            </button>
          )}
          {error && (
            <p className="notice error" role="alert">
              {error}{" "}
              {runRequests.current.has(path) &&
                tr("Retry Run now to confirm the same request.")}
            </p>
          )}
          <footer className="schedule-footer">
            <button
              disabled={busy}
              onClick={() =>
                void act(async () => onChanged(await api<Schedule>(path)))
              }
            >
              {tr("Refresh task")}
            </button>
            <button
              className="primary"
              disabled={
                busy ||
                (!schedule.enabled && !runRequests.current.has(path)) ||
                targetError
              }
              onClick={() => void run()}
            >
              {tr("Run now")}
            </button>
            {!schedule.enabled && (
              <small>{tr("Resume this task to run it now.")}</small>
            )}
          </footer>
        </div>
      )}
      <RunHistory
        key={historyVersion}
        schedule={schedule}
        version={historyVersion}
        editing={editing}
        onOpen={onOpen}
      />
    </>
  );
}
function Instructions({ text }: { text: string }) {
  const tr = useT();
  const [expanded, setExpanded] = useState(false);
  return (
    <div>
      <p className="schedule-instructions">
        {expanded ? text : text.slice(0, 2000)}
      </p>
      {text.length > 2000 && (
        <button onClick={() => setExpanded((v) => !v)}>
          {expanded ? tr("Show less") : tr("Show all instructions")}
        </button>
      )}
    </div>
  );
}
function RunHistory({
  schedule,
  version,
  editing,
  onOpen,
}: {
  schedule: Schedule;
  version: number;
  editing: boolean;
  onOpen: (p: string, s: string) => void;
}) {
  const tr = useT(),
    locale = useLocale();
  const [opened, setOpened] = useState(!editing),
    [after, setAfter] = useState(""),
    [refresh, setRefresh] = useState(0),
    [expanded, setExpanded] = useState(false);
  const [data, setData] = useState<{
    key: string;
    value?: ScheduleRunPage;
    error?: string;
  }>();
  const key = JSON.stringify([after, refresh, version]);
  useEffect(() => {
    if (!editing) void Promise.resolve().then(() => setOpened(true));
  }, [editing]);
  useEffect(() => {
    if (!opened) return;
    const controller = new AbortController();
    void api<ScheduleRunPage>(
      `${projectURL(schedule.project_id)}/schedules/${encode(schedule.id)}/runs?after=${encode(after)}&limit=50`,
      { signal: controller.signal },
    )
      .then((value) => {
        if (!controller.signal.aborted)
          setData({ key, value: { ...value, runs: value.runs.slice(0, 50) } });
      })
      .catch((e) => {
        if (!controller.signal.aborted) setData({ key, error: String(e) });
      });
    return () => controller.abort();
  }, [opened, schedule.project_id, schedule.id, after, refresh, version, key]);
  const current = data?.key === key ? data : undefined;
  const label = (state: string) => {
    switch (state) {
      case "queued":
        return tr("Queued");
      case "running":
        return tr("Running");
      case "completed":
        return tr("Completed");
      case "failed":
        return tr("Failed");
      case "canceled":
        return tr("Canceled");
      case "interrupted":
        return tr("Interrupted");
      case "held":
        return tr("Held");
      default:
        return tr("Outcome unknown");
    }
  };
  return (
    <section className="run-history" aria-label={tr("Recent runs")}>
      <header>
        <h3>{tr("Recent runs")}</h3>
        <button
          onClick={() => {
            setOpened(true);
            setAfter("");
            setRefresh((n) => n + 1);
          }}
          disabled={opened && !current}
        >
          {opened ? tr("Refresh history") : tr("Show history")}
        </button>
      </header>
      {opened && (
        <>
          {!current && <p role="status">{tr("Loading…")}</p>}
          {current?.error && (
            <p role="alert">
              {tr("Could not load run history.")}{" "}
              <button onClick={() => setRefresh((n) => n + 1)}>
                {tr("Retry")}
              </button>
            </p>
          )}
          {current?.value?.runs.length === 0 && <p>{tr("No runs yet.")}</p>}
          {current?.value?.runs.slice(0, expanded ? 50 : 5).map((run) => (
            <article key={run.id}>
              <span>{label(run.state)}</span>
              <time>
                {run.due_at
                  ? `${tr("Scheduled trigger")}: ${scheduleTime(run.due_at, schedule.timezone, locale)}`
                  : tr("Manual run")}
              </time>
              <button onClick={() => onOpen(run.project_id, run.session_id)}>
                {tr("Open conversation")}
              </button>
            </article>
          ))}
          {(current?.value?.runs.length || 0) > 5 && (
            <button onClick={() => setExpanded((v) => !v)}>
              {expanded ? tr("Show less") : tr("Show this page’s runs")}
            </button>
          )}
          <div className="row-actions">
            {after && (
              <button
                onClick={() => {
                  setAfter("");
                  setExpanded(false);
                }}
              >
                {tr("First page")}
              </button>
            )}
            {current?.value?.next && (
              <button
                onClick={() => {
                  setAfter(current.value!.next!);
                  setExpanded(false);
                }}
              >
                {tr("Next page")}
              </button>
            )}
          </div>
        </>
      )}
    </section>
  );
}
