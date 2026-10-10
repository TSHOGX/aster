"use client";
import { scheduleWrite } from "@/lib/schedule-recurrence";
import {
  useEffect,
  useRef,
  useState,
  type RefObject,
  type ComponentProps,
} from "react";
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
  type ScheduleOverview,
  type ScheduleRun,
  type ScheduleRunPage,
} from "@/lib/project-api";
import type { WatchHealth } from "@/lib/project-realtime";
import { useRealtime } from "./resource-provider";
import { useScheduleWindow } from "./schedule-data";
import { ScheduleViewport } from "./schedule-viewport";
import { ScheduleLoadEdge } from "./schedule-load-edge";
import { changedRuns, ScheduleHintGate } from "@/lib/schedule-window";
import type { Navigation } from "@/lib/workspace-state";
import { useLocale, useT } from "@/lib/i18n";
import { scheduleTime } from "@/lib/schedule-recurrence";
import { ProjectPicker } from "./project-picker";
import { Menu, useDialog } from "./ui";
import { RecurrenceSummary, UpcomingOccurrences } from "./schedule-recurrence";
import { ScheduleEditor } from "./schedule-editor";

const overviewPage = (page: SchedulePage) => ({
  rows: page.schedules,
  next: page.next,
  previous: page.previous,
});
const runsPage = (page: ScheduleRunPage) => ({
  rows: page.runs,
  next: page.next,
  previous: page.previous,
});

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
  const [syncHealth, setSyncHealth] = useState<WatchHealth>("connecting"),
    [notice, setNotice] = useState("");
  const [query, setQuery] = useState(nav.scheduleQuery || ""),
    [composing, setComposing] = useState(false);
  const [detail, setDetail] = useState<{
    key: string;
    value?: Schedule;
    error?: string;
    unavailable?: boolean;
  }>();
  const [detailRefresh, setDetailRefresh] = useState(0),
    [historyRefresh, setHistoryRefresh] = useState(0);
  const realtime = useRealtime();
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
    status = nav.scheduleStatus || "";
  const list = useScheduleWindow<ScheduleOverview, SchedulePage>(
    `/api/schedules?project_id=${encode(filter)}&q=${encode(nav.scheduleQuery || "")}&status=${status}`,
    overviewPage,
  );
  useEffect(() => {
    if (!list.resyncs) return;
    const message = tr("Tasks changed. Syncing around your current position.");
    const notify = setTimeout(() => setNotice(message), 0);
    const clear = setTimeout(
      () => setNotice((current) => (current === message ? "" : current)),
      5000,
    );
    return () => {
      clearTimeout(notify);
      clearTimeout(clear);
    };
  }, [list.resyncs, tr]);
  const listKey = JSON.stringify([filter, status, nav.scheduleQuery || ""]);
  const loadMore = () => {
    void list.controller.extend("next");
  };
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let catalog = false,
      detail = false,
      history = false;
    const flush = () => {
      timer = undefined;
      if (catalog) void list.controller.refresh();
      if (detail) setDetailRefresh((n) => n + 1);
      if (history) setHistoryRefresh((n) => n + 1);
      catalog = detail = history = false;
    };
    const gate = new ScheduleHintGate();
    const unsubscribe = realtime.subscribe(
      "",
      (event) => {
        if (event.resource_kind === "ready" || !gate.accept(event)) return;
        if (event.resource_kind === "closed") {
          setDetail(undefined);
          void list.controller.refresh();
          return;
        }
        const payload = event.payload as
          | { project_id?: string; schedule_id?: string }
          | undefined;
        const selected = navRef.current;
        const reset = event.resource_kind === "resync";
        catalog ||= reset || event.resource_kind.startsWith("schedule.");
        const relevant =
          reset ||
          ((!payload?.project_id ||
            payload.project_id === selected.scheduleProject) &&
            (!payload?.schedule_id ||
              payload.schedule_id === selected.schedule));
        detail ||=
          relevant && (reset || event.resource_kind.startsWith("schedule."));
        history ||=
          relevant && (reset || event.resource_kind.startsWith("run."));
        if (!timer) timer = setTimeout(flush, 200);
      },
      setSyncHealth,
    );
    return () => {
      unsubscribe();
      clearTimeout(timer);
    };
  }, [realtime, list.controller]);
  useEffect(() => {
    if (syncHealth === "live" || syncHealth === "closed") return;
    let attempt = 0;
    let timer: ReturnType<typeof setTimeout>;
    const retry = () => {
      if (!document.hidden) {
        void list.controller.refresh();
        setDetailRefresh((n) => n + 1);
        setHistoryRefresh((n) => n + 1);
      }
      timer = setTimeout(retry, Math.min(30000, 5000 * 2 ** attempt++));
    };
    timer = setTimeout(retry, 5000);
    return () => clearTimeout(timer);
  }, [syncHealth, list.controller]);
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
          if (value.state === "deleted") {
            setDetail({
              key: selectedKey,
              value,
              error: tr("This task was deleted."),
            });
            return;
          }
          setDetail((old) =>
            old?.key === selectedKey &&
            (old.value?.revision || 0) > value.revision
              ? old
              : { key: selectedKey, value },
          );
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted)
          setDetail((old) => ({
            key: selectedKey,
            value:
              e instanceof APIError &&
              [401, 403, 404].includes(e.status) &&
              navRef.current.scheduleMode !== "edit"
                ? undefined
                : old?.key === selectedKey
                  ? old.value
                  : undefined,
            unavailable:
              e instanceof APIError && [401, 403, 404].includes(e.status),
            error: String(e),
          }));
      });
    return () => controller.abort();
  }, [selectedKey, nav.scheduleProject, nav.schedule, detailRefresh, tr]);
  const listRef = useRef<HTMLDivElement>(null);
  const memoryKey = listKey;
  useEffect(() => {
    if (!listRef.current) return;
    listRef.current.scrollTop =
      listMemoryRef.current.key === memoryKey ? listMemoryRef.current.top : 0;
  }, [memoryKey, listMemoryRef]);
  const current = {
    value: { schedules: list.rows, next: list.next },
    error: list.error ? String(list.error) : undefined,
  };
  const selected = detail?.key === selectedKey ? detail : undefined;
  const loading = list.loading;
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
    void list.controller.refresh();
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
          <p
            className={`schedule-sync schedule-sync-${syncHealth}`}
            role="status"
          >
            {syncHealth === "live"
              ? list.updating
                ? tr("Syncing…")
                : tr("Live")
              : syncHealth === "stale"
                ? tr("Offline. Updates will resume when you return.")
                : syncHealth === "closed"
                  ? tr("Updates may be delayed.")
                  : syncHealth === "reconnecting"
                    ? tr("Reconnecting…")
                    : tr("Connecting…")}
            {list.syncedAt && (
              <span className="schedule-sync-time">
                {tr("Last synced: {time}", {
                  time: new Date(list.syncedAt).toLocaleTimeString(
                    locale === "zh" ? "zh-CN" : "en-US",
                  ),
                })}
              </span>
            )}
          </p>
          <div className="schedule-filters">
            <div className="row-actions" aria-label={tr("Task status")}>
              {([undefined, "active", "paused"] as const).map((value) => (
                <button
                  key={value || "all"}
                  aria-pressed={nav.scheduleStatus === value}
                  onClick={() => setFilter({ scheduleStatus: value })}
                >
                  {value === "active"
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
              <button onClick={() => void list.controller.refresh()}>
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
            <ScheduleLoadEdge
              enabled={!!list.previous && !list.error}
              busy={list.updating}
              onLoad={() => void list.controller.extend("previous")}
              label={list.updating ? tr("Loading…") : tr("Load previous tasks")}
            />
            <ScheduleViewport
              estimate={150}
              rows={list.rows.map((s) => ({
                id: s.id,
                node: (
                  <div
                    key={s.id}
                    className={`schedule-row ${nav.schedule === s.id ? "selected" : ""}`}
                  >
                    {s.state === "active" ? <Clock3 size={19} /> : <Pause size={19} />}
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
                        {s.state !== "active" && `${tr("Paused")} · `}
                        <RecurrenceSummary
                          cron={s.cron}
                          timezone={s.timezone}
                        />
                      </small>
                      {s.state === "active" && s.cron && s.next_at && (
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
                ),
              }))}
            />
            <ScheduleLoadEdge
              enabled={!!list.next && !list.error}
              busy={list.updating}
              onLoad={loadMore}
              label={list.updating ? tr("Loading…") : tr("Load more tasks")}
            />
            {!loading &&
              !list.error &&
              current?.value?.schedules.length === 0 && (
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
            ) : selected.error &&
              (!selected.value ||
                ((selected.value.state === "deleted" || selected.unavailable) &&
                  nav.scheduleMode !== "edit")) ? (
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
                readError={selected.error}
                onRetry={() => setDetailRefresh((n) => n + 1)}
                editing={nav.scheduleMode === "edit"}
                projects={projects}
                guardRef={guardRef}
                runRequests={runRequests}
                liveVersion={historyRefresh}
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
                  void list.controller.refresh();
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
                  void list.controller.refresh();
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

// Automatic status reads must never change any configuration used by an
// in-progress edit. The editor's existing explicit conflict reconciliation owns
// replacement of this baseline when saving.
function ScheduleDraft(props: ComponentProps<typeof ScheduleEditor>) {
  const [baseline] = useState(props.schedule);
  return <ScheduleEditor {...props} schedule={baseline} />;
}

function SelectedSchedule({
  schedule,
  editing,
  projects,
  guardRef,
  runRequests,
  liveVersion,
  readError,
  onRetry,
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
  liveVersion: number;
  readError?: string;
  onRetry: () => void;
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
        scheduleWrite({ ...latest, state: schedule.state === "active" ? "paused" : "active" }, newID()),
        "PATCH",
      );
      onChanged(changed);
      setNotice(
        changed.state === "active"
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
          `${path}?revision=${schedule.revision}&request_id=${newID()}`,
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
      {readError && (
        <p role="alert" className="notice error">
          {tr("Updates may be delayed.")}{" "}
          <button onClick={onRetry}>{tr("Retry")}</button>
        </p>
      )}
      {editing ? (
        <ScheduleDraft
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
            <span>{schedule.state === "active" ? tr("Enabled") : tr("Paused")}</span>
            <div className="row-actions">
              <button disabled={busy} onClick={onEdit}>
                {tr("Edit")}
              </button>
              <button disabled={busy} onClick={() => void toggle()}>
                {schedule.state === "active" ? tr("Pause") : tr("Resume")}
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
              enabled={schedule.state === "active"}
              nextAt={schedule.next_at}
            />
            <p>
              {schedule.state !== "active"
                ? tr("Paused. Resume to continue on this schedule.")
                : !schedule.cron
                  ? tr("Runs only when you choose Run now.")
                  : schedule.next_at
                    ? `${tr("Next run")}: ${scheduleTime(schedule.next_at, schedule.timezone, locale)}`
                    : tr("Upcoming time is unavailable. Reconnecting…")}
            </p>
            {schedule.state === "active" &&
              schedule.next_at &&
              Date.parse(schedule.next_at) <= now && (
                <p>
                  {tr("Waiting for the scheduler to confirm the next run…")}
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
              className="primary"
              disabled={
                busy ||
                (schedule.state !== "active" && !runRequests.current.has(path)) ||
                targetError
              }
              onClick={() => void run()}
            >
              {tr("Run now")}
            </button>
            {schedule.state !== "active" && (
              <small>{tr("Resume this task to run it now.")}</small>
            )}
          </footer>
        </div>
      )}
      <RunHistory
        key={schedule.id}
        schedule={schedule}
        version={historyVersion}
        liveVersion={liveVersion}
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
  liveVersion,
  editing,
  onOpen,
}: {
  schedule: Schedule;
  version: number;
  liveVersion: number;
  editing: boolean;
  onOpen: (p: string, s: string) => void;
}) {
  const tr = useT(),
    locale = useLocale();
  const [expanded, setExpanded] = useState(false);
  const [pending, setPending] = useState<{
    value: ScheduleRunPage;
    count: number;
  }>();
  const [latest, setLatest] = useState<ScheduleRun[]>([]);
  const [headError, setHeadError] = useState("");
  const panel = useRef<HTMLDivElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const knownHead = useRef<ScheduleRun[]>([]);
  const history = useScheduleWindow<ScheduleRun, ScheduleRunPage>(
    `${projectURL(schedule.project_id)}/schedules/${encode(schedule.id)}/runs`,
    runsPage,
  );
  const change = `${version}:${liveVersion}`;
  const applied = useRef(change);
  useEffect(() => {
    if (!history.previous && history.pages.length) {
      knownHead.current = history.pages[0].rows;
      void Promise.resolve().then(() => setLatest(history.pages[0].rows));
    } else if (!expanded && history.previous)
      void history.controller.refresh("");
  }, [history.previous, history.pages, expanded, history.controller]);
  useEffect(() => {
    if (change === applied.current) return;
    applied.current = change;
    if (!expanded || (panel.current?.scrollTop || 0) <= 48) {
      void history.controller.refresh();
      return;
    }
    const controller = new AbortController();
    void api<ScheduleRunPage>(
      `${projectURL(schedule.project_id)}/schedules/${encode(schedule.id)}/runs?limit=50`,
      { signal: controller.signal },
    )
      .then((value) => {
        if (controller.signal.aborted) return;
        if (
          value.runs.length > 50 ||
          JSON.stringify(value).length * 3 > 1024 * 1024
        )
          throw new Error("Run history exceeds its display budget.");
        setPending({
          value,
          count: changedRuns(knownHead.current, value.runs),
        });
        setHeadError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setHeadError(String(e));
      });
    return () => controller.abort();
  }, [change, expanded, history.controller, schedule.project_id, schedule.id]);
  const close = () => {
    setExpanded(false);
    toggleRef.current?.focus();
  };
  useEffect(() => {
    if (!expanded) return;
    const escape = (event: KeyboardEvent) => {
      if (
        event.key !== "Escape" ||
        event.defaultPrevented ||
        document.querySelector('dialog[open],[role="menu"],[role="listbox"]')
      )
        return;
      event.preventDefault();
      event.stopImmediatePropagation();
      setExpanded(false);
      toggleRef.current?.focus();
    };
    window.addEventListener("keydown", escape, true);
    panel.current?.focus();
    return () => window.removeEventListener("keydown", escape, true);
  }, [expanded]);
  const label = (state: string) => {
    switch (state) {
      case "queued":
        return tr("Queued");
      case "running":
        return tr("Running");
      case "starting":
        return tr("Starting");
      case "stopping":
        return tr("Stopping");
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
  const changes = pending?.count || 0;
  const rows = expanded
    ? history.rows
    : (pending?.value.runs || latest).slice(0, 5);
  const row = (run: ScheduleRun) => (
    <article key={run.id}>
      <span className={`schedule-run-state schedule-run-${run.state}`}>
        {label(run.state)}
      </span>
      <time>
        {run.due_at
          ? `${tr("Scheduled trigger")}: ${scheduleTime(run.due_at, schedule.timezone, locale)}`
          : tr("Manual run")}
      </time>
      <button onClick={() => onOpen(run.project_id, run.session_id)}>
        {tr("Open conversation")}
      </button>
    </article>
  );
  return (
    <section
      className={`run-history ${expanded ? "history-expanded" : ""}`}
      aria-label={tr("Recent runs")}
    >
      <header>
        <h3>{tr("Recent runs")}</h3>
        <button
          ref={toggleRef}
          aria-expanded={expanded}
          onClick={() => {
            if (expanded) close();
            else {
              setExpanded(true);
              if (pending) {
                history.controller.replace(runsPage(pending.value));
                setPending(undefined);
              }
            }
          }}
        >
          {expanded ? tr("Close history") : tr("View full history")}
        </button>
      </header>
      {expanded && (
        <div className="history-panel">
          {!!changes && (
            <button
              className="history-updates"
              onClick={() => {
                if (pending) {
                  knownHead.current = pending.value.runs;
                  history.controller.replace(runsPage(pending.value));
                }
                setPending(undefined);
                requestAnimationFrame(() => {
                  if (panel.current) panel.current.scrollTop = 0;
                });
              }}
            >
              {tr("{count} updates · Back to latest", { count: changes })}
            </button>
          )}
          {!!(history.error || headError) && (
            <p role="alert">
              {tr("Could not load run history.")}{" "}
              <button
                onClick={() => {
                  applied.current = "";
                  setHeadError("");
                  void history.controller.refresh();
                }}
              >
                {tr("Retry")}
              </button>
            </p>
          )}
          <div
            className="run-history-scroll"
            ref={panel}
            tabIndex={0}
            aria-label={tr("Run history")}
          >
            <ScheduleLoadEdge
              enabled={!!history.previous && !history.error}
              busy={history.updating}
              onLoad={() => void history.controller.extend("previous")}
              label={history.updating ? tr("Loading…") : tr("Load newer runs")}
            />
            <ScheduleViewport
              estimate={64}
              rows={rows.map((run) => ({ id: run.id, node: row(run) }))}
            />
            <ScheduleLoadEdge
              enabled={!!history.next && !history.error}
              busy={history.updating}
              onLoad={() => void history.controller.extend("next")}
              label={history.updating ? tr("Loading…") : tr("Load more runs")}
            />
          </div>
        </div>
      )}
      {!expanded && rows.map(row)}
      {history.loading && <p role="status">{tr("Loading…")}</p>}
      {!history.loading && !history.error && history.rows.length === 0 && (
        <p>{tr("No runs yet.")}</p>
      )}
      {!expanded && !!history.error && (
        <p role="alert">
          {tr("Could not load run history.")}{" "}
          <button onClick={() => void history.controller.refresh()}>
            {tr("Retry")}
          </button>
        </p>
      )}
      {editing && expanded && (
        <small>{tr("History updates do not change your draft.")}</small>
      )}
    </section>
  );
}
