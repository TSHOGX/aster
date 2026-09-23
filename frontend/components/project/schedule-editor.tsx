"use client";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type RefObject,
} from "react";
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
} from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import {
  parseRecurrence,
  recurrenceCron,
  scheduleConfiguration,
} from "@/lib/schedule-recurrence";
import { ProjectPicker } from "./project-picker";
import { RecurrenceFields } from "./schedule-recurrence";
import { useDialog } from "./ui";

export function ScheduleEditor({
  schedule,
  projects,
  defaultProject,
  guardRef,
  onClose,
  onSaved,
}: {
  schedule?: Schedule;
  projects: Project[];
  defaultProject: string;
  guardRef: RefObject<() => Promise<boolean>>;
  onClose: () => void;
  onSaved: (s: Schedule) => void;
}) {
  const tr = useT(),
    ask = useDialog();
  const [baseline, setBaseline] = useState(schedule);
  const [project, setProject] = useState(
    schedule?.project_id ||
      defaultProject ||
      (projects.filter((p) => !["deleted", "deleting"].includes(p.state))
        .length === 1
        ? projects.filter((p) => !["deleted", "deleting"].includes(p.state))[0]
            .id
        : ""),
  );
  const [title, setTitle] = useState(schedule?.title || ""),
    [prompt, setPrompt] = useState(schedule?.prompt || "");
  const detectedZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [timezone, setTimezone] = useState(
    schedule?.timezone || detectedZone || "UTC",
  );
  const [rule, setRule] = useState(() =>
    parseRecurrence(schedule?.cron ?? "0 9 * * *"),
  );
  const [ruleChanged, setRuleChanged] = useState(false);
  const [existing, setExisting] = useState(!!schedule?.session_id),
    [session, setSession] = useState(schedule?.session_id || "");
  const [options, setOptions] = useState<AgentSession[]>([]),
    [after, setAfter] = useState(""),
    [more, setMore] = useState(false),
    [sessionLoading, setSessionLoading] = useState(false),
    [sessionError, setSessionError] = useState("");
  const [selectedTarget, setSelectedTarget] = useState<AgentSession>();
  const [busy, setBusy] = useState(false),
    [uncertain, setUncertain] = useState(false),
    [error, setError] = useState("");
  const [latest, setLatest] = useState<Schedule>();
  const [validation, setValidation] = useState<{
    key: string;
    valid: boolean;
  }>();
  const setValidity = useCallback(
    (key: string, valid: boolean) => setValidation({ key, valid }),
    [],
  );
  const [requestId] = useState(newID);
  const pending = useRef<Schedule | null>(null);
  const cron = ruleChanged || !schedule ? recurrenceCron(rule) : schedule.cron;
  const value: Schedule = {
    id: schedule?.id || requestId,
    project_id: project,
    title,
    prompt,
    cron: cron ?? "",
    timezone,
    session_id: existing ? session : "",
    enabled: baseline?.enabled ?? true,
    deleted: false,
    revision: baseline?.revision ?? 0,
  };
  const [initial] = useState(() =>
    schedule ? scheduleConfiguration(schedule) : scheduleConfiguration(value),
  );
  const dirty = scheduleConfiguration(value) !== initial;
  const recurrenceUnchanged =
    !!baseline && cron === baseline.cron && timezone === baseline.timezone;
  const previewValid =
    validation?.key === JSON.stringify([cron, timezone]) && validation.valid;
  const targetValid =
    !existing ||
    (!!session &&
      selectedTarget?.id === session &&
      selectedTarget.project_id === project &&
      selectedTarget.deletion === 0);
  const valid =
    !!project &&
    !!title.trim() &&
    !!prompt.trim() &&
    cron !== null &&
    targetValid &&
    (recurrenceUnchanged || previewValid);
  const canLeave = useCallback(async () => {
    if (busy || uncertain) {
      setError(tr("Resolve the pending save before leaving."));
      return false;
    }
    return (
      !dirty ||
      !!(await ask({
        title: tr("Unsaved changes"),
        description: tr("Leaving will discard your unsaved changes."),
        choices: [
          { label: tr("Discard changes"), value: "discard", danger: true },
        ],
      }))
    );
  }, [busy, uncertain, dirty, ask, tr]);
  useEffect(() => {
    guardRef.current = canLeave;
    return () => {
      guardRef.current = async () => true;
    };
  }, [guardRef, canLeave]);
  useEffect(() => {
    const unload = (event: BeforeUnloadEvent) => {
      if (dirty || busy || uncertain) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", unload);
    return () => window.removeEventListener("beforeunload", unload);
  }, [dirty, busy, uncertain]);
  useEffect(() => {
    if (!existing || !project) return;
    const controller = new AbortController();
    void Promise.resolve().then(() => {
      if (!controller.signal.aborted) {
        setSessionLoading(true);
        setSessionError("");
      }
    });
    void api<{ sessions: AgentSession[] }>(
      `${projectURL(project)}/sessions?limit=100&after=${encode(after)}`,
      { signal: controller.signal },
    )
      .then((data) => {
        if (controller.signal.aborted) return;
        setOptions(data.sessions.filter((s) => s.deletion === 0).slice(0, 100));
        setMore(data.sessions.length === 100);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setSessionError(String(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setSessionLoading(false);
      });
    return () => controller.abort();
  }, [project, existing, after]);
  useEffect(() => {
    if (!existing || !session || !project) return;
    const controller = new AbortController();
    void api<AgentSession>(sessionURL(project, session), {
      signal: controller.signal,
    })
      .then((s) => {
        if (!controller.signal.aborted) setSelectedTarget(s);
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setSelectedTarget(undefined);
          setSessionError(
            tr("This conversation is unavailable. Choose another."),
          );
        }
      });
    return () => controller.abort();
  }, [project, session, existing, tr]);
  const save = async () => {
    if (busy || (!valid && !uncertain)) return;
    setBusy(true);
    setError("");
    const submitted = pending.current || { ...value };
    const path = `${projectURL(submitted.project_id)}/schedules/${encode(submitted.id)}`;
    try {
      let result: Schedule;
      if (!schedule) {
        pending.current = submitted;
        try {
          result = await command<Schedule>(
            `${projectURL(project)}/schedules`,
            submitted,
          );
        } catch (e) {
          if (e instanceof APIError && e.status >= 400 && e.status < 500) {
            pending.current = null;
            setUncertain(false);
            throw e;
          }
          setUncertain(true);
          const found = await api<Schedule>(path).catch(() => null);
          if (
            !found ||
            scheduleConfiguration(found) !== scheduleConfiguration(submitted)
          )
            throw e;
          result = found;
        }
      } else {
        try {
          result = await command<Schedule>(path, submitted, "PATCH");
        } catch (e) {
          const found = await api<Schedule>(path).catch(() => null);
          if (
            found &&
            scheduleConfiguration(found) === scheduleConfiguration(submitted)
          )
            result = found;
          else if (
            e instanceof APIError &&
            e.status === 409 &&
            found &&
            baseline &&
            scheduleConfiguration(found) === scheduleConfiguration(baseline)
          ) {
            result = await command<Schedule>(
              path,
              { ...submitted, revision: found.revision },
              "PATCH",
            );
          } else {
            if (found && e instanceof APIError && e.status === 409) {
              setLatest(found);
              throw new Error(
                tr("The task changed. Your edits have been kept."),
              );
            }
            throw e;
          }
        }
      }
      pending.current = null;
      setUncertain(false);
      guardRef.current = async () => true;
      onSaved(result);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="schedule-editor"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      onKeyDown={(e) => {
        if (
          (e.ctrlKey || e.metaKey) &&
          e.key === "Enter" &&
          !e.nativeEvent.isComposing
        ) {
          e.preventDefault();
          if (valid && !busy) void save();
        }
      }}
    >
      <header className="schedule-detail-heading">
        <h2>{schedule ? tr("Edit task") : tr("Create task")}</h2>
        <button type="button" onClick={onClose}>
          {schedule ? tr("Back to task") : tr("Back to tasks")}
        </button>
      </header>
      <fieldset disabled={busy || uncertain} className="schedule-form-fields">
        <label>
          {tr("Task name")}
          <input
            required
            maxLength={256}
            aria-label={tr("Task name")}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
          {!title.trim() && <small>{tr("Enter a task name.")}</small>}
        </label>
        <label>
          {tr("Instructions")}
          <textarea
            required
            maxLength={Math.max(64000, schedule?.prompt.length || 0)}
            aria-label={tr("Instructions")}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
          <small>{tr("Describe what should happen on each run.")}</small>
          {!prompt.trim() && (
            <small>{tr("Enter instructions for each run.")}</small>
          )}
        </label>
        <fieldset className="schedule-settings">
          <legend>{tr("Runs in")}</legend>
          <div className="project-picker-field">
            <span>{tr("Project")}</span>
            <ProjectPicker
              projects={projects}
              value={project}
              disabled={!!schedule}
              fallbackTitle={project}
              onChange={(id) => {
                setProject(id);
                setSession("");
                setSelectedTarget(undefined);
                setOptions([]);
                setAfter("");
              }}
            />
            {!project && (
              <small>{tr("Choose a Project before creating this task.")}</small>
            )}
          </div>
          <label>
            {tr("Conversation")}
            <select
              aria-label={tr("Conversation")}
              value={existing ? "existing" : "new"}
              onChange={(e) => setExisting(e.target.value === "existing")}
            >
              <option value="new">{tr("New conversation each run")}</option>
              <option value="existing">{tr("Existing conversation")}</option>
            </select>
            <small>
              {existing
                ? tr("Continues this conversation using its existing context.")
                : tr(
                    "Each run creates a conversation using this Project’s files and environment.",
                  )}
            </small>
          </label>
          {existing && (
            <>
              <label>
                {tr("Choose a conversation")}
                <select
                  required
                  value={session}
                  onChange={(e) => {
                    setSession(e.target.value);
                    setSelectedTarget(undefined);
                  }}
                >
                  <option value="">{tr("Choose a conversation")}</option>
                  {session && !options.some((s) => s.id === session) && (
                    <option value={session}>
                      {selectedTarget?.id === session
                        ? selectedTarget.title
                        : tr("Loading…")}
                    </option>
                  )}
                  {options.map((s) => (
                    <option value={s.id} key={s.id}>
                      {s.title}
                    </option>
                  ))}
                </select>
              </label>
              {sessionLoading && <p>{tr("Loading…")}</p>}
              {sessionError && <p role="alert">{sessionError}</p>}
              <div className="row-actions">
                {after && (
                  <button
                    type="button"
                    disabled={sessionLoading}
                    onClick={() => setAfter("")}
                  >
                    {tr("First page")}
                  </button>
                )}
                {more && (
                  <button
                    type="button"
                    disabled={sessionLoading}
                    onClick={() => setAfter(options.at(-1)?.id || "")}
                  >
                    {tr("Next page")}
                  </button>
                )}
              </div>
            </>
          )}
        </fieldset>
        {!schedule && !detectedZone && (
          <p role="status">
            {tr("Your timezone could not be detected. UTC is selected.")}
          </p>
        )}
        <RecurrenceFields
          rule={rule}
          cron={cron}
          timezone={timezone}
          onRule={(next) => {
            setRule(next);
            setRuleChanged(true);
          }}
          onTimezone={setTimezone}
          onValidity={setValidity}
        />
      </fieldset>
      {latest && (
        <div className="notice warning">
          <p>{tr("The task changed. Your edits have been kept.")}</p>
          <details>
            <summary>{tr("Review latest saved task")}</summary>
            <h3>{latest.title}</h3>
            <p className="schedule-instructions">{latest.prompt}</p>
            <p>
              {latest.cron} · {latest.timezone}
            </p>
          </details>
          <button
            type="button"
            onClick={() =>
              void (async () => {
                if (
                  await ask({
                    title: tr("Replace latest task settings?"),
                    description: tr(
                      "Your next save will replace the latest saved configuration with these edits.",
                    ),
                    choices: [
                      { label: tr("Use latest revision"), value: "yes" },
                    ],
                  })
                ) {
                  setBaseline(latest);
                  setLatest(undefined);
                  setError("");
                }
              })()
            }
          >
            {tr("Use latest revision")}
          </button>
        </div>
      )}
      {error && (
        <p role="alert" className="notice error">
          {error}
        </p>
      )}
      {uncertain && (
        <p role="status">
          {tr(
            "Save outcome is unconfirmed. Retry uses the same task and original settings.",
          )}
        </p>
      )}
      <footer className="schedule-footer">
        <button type="button" onClick={onClose}>
          {tr("Cancel")}
        </button>
        <button
          className="primary"
          disabled={busy || (!uncertain && (!valid || (!!schedule && !dirty)))}
        >
          {busy
            ? tr("Saving…")
            : uncertain
              ? tr("Retry")
              : schedule
                ? tr("Save changes")
                : tr("Create task")}
        </button>
      </footer>
    </form>
  );
}
