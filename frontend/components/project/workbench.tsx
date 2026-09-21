"use client";
import { useT } from "@/lib/i18n";
import dynamic from "next/dynamic";
import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Bell,
  FolderOpen,
  MessageSquare,
  Plus,
  Search,
  TerminalSquare,
} from "lucide-react";
import {
  api,
  APIError,
  command,
  encode,
  newID,
  projectURL,
  viewPreferences,
  remember,
  type AgentSession,
  type Identity,
  type Project,
  type ProjectTerminal,
  type Projection,
} from "@/lib/project-api";
import { brandName } from "@/lib/ui-config";
import { setTheme } from "@/lib/theme";
import { SessionPanel } from "./session-panel";
import { FilesPanel } from "./files-panel";
import {
  Inbox,
  SchedulePanel,
  SearchPanel,
  SharePanel,
  SharedConversations,
} from "./adjacent-panels";
import "./workbench.css";
const TerminalPanel = dynamic(
  () =>
    import("@/components/workbench/terminal-panel").then(
      (module) => module.TerminalPanel,
    ),
  { ssr: false },
);
type View = "project" | "search" | "inbox" | "shared";
export function ProjectWorkbench() {
  const tr = useT();
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [booted, setBooted] = useState(false);
  const [projects, setProjects] = useState<Project[]>([]);
  const [selected, setSelected] = useState("");
  const [targetSession, setTargetSession] = useState("");
  const [targetMessage, setTargetMessage] = useState("");
  const [view, setView] = useState<View>("project");
  const [error, setError] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [loginBusy, setLoginBusy] = useState(false);
  const refresh = useCallback(async () => {
    try {
      setProjects(
        (
          await api<{
            projects: Project[];
          }>("/api/projects?limit=100")
        ).projects,
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : "Projects are unavailable.");
    }
  }, []);
  useEffect(() => {
    let gone = false;
    void api<Identity>("/api/auth/session")
      .then((user) => {
        if (!gone) {
          setIdentity(user);
          const params = new URLSearchParams(location.search);
          setSelected(params.get("project") || "");
          setTargetSession(params.get("session") || "");
          setTargetMessage(params.get("message") || "");
        }
      })
      .catch((e) => {
        if (!gone && !(e instanceof APIError && e.status === 401))
          setError(String(e));
      })
      .finally(() => {
        if (!gone) setBooted(true);
      });
    return () => {
      gone = true;
    };
  }, []);
  useEffect(() => {
    if (!identity) return;
    void Promise.resolve().then(refresh);
    const timer = setInterval(() => void refresh(), 10000);
    return () => clearInterval(timer);
  }, [identity, refresh]);
  useEffect(() => {
    const expired = () => {
      setIdentity(null);
      setProjects([]);
      setSelected("");
      setError("Your login expired. Sign in again.");
    };
    window.addEventListener("project-auth-expired", expired);
    return () => window.removeEventListener("project-auth-expired", expired);
  }, []);
  const navigate = (project: string, session = "", message = "") => {
    const select = () => {
      setSelected(project);
      setTargetSession(session);
      setTargetMessage(message);
      setView("project");
      const params = new URLSearchParams({ project });
      if (session) params.set("session", session);
      if (message) params.set("message", message);
      history.replaceState(null, "", `/?${params}`);
    };
    if (projects.some((item) => item.id === project)) {
      select();
      return;
    }
    void api<Project>(projectURL(project))
      .then(select)
      .catch((e) => {
        if (e instanceof APIError && e.status === 404 && session) {
          setTargetSession(session);
          setView("shared");
        } else
          setError(
            e instanceof Error ? e.message : "This Project is unavailable.",
          );
      });
  };
  const create = async () => {
    const title = window.prompt(tr("Project name"), "New Project");
    if (!title?.trim()) return;
    try {
      const project = await command<Project>("/api/projects", {
        id: newID(),
        title,
      });
      await refresh();
      navigate(project.id);
    } catch (e) {
      setError(String(e));
    }
  };
  if (!booted)
    return (
      <main className="project-loading">
        {tr("Opening your workbench\u2026")}
      </main>
    );
  if (!identity)
    return (
      <main className="project-login">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            setLoginBusy(true);
            setError("");
            void command<Identity>("/api/auth/login", { username, password })
              .then((user) => {
                setIdentity(user);
                setPassword("");
              })
              .catch((e) =>
                setError(e instanceof Error ? e.message : "Login failed."),
              )
              .finally(() => setLoginBusy(false));
          }}
        >
          <div className="brand-mark">B</div>
          <h1>{brandName}</h1>
          <p>{tr("Your research, agents, and workspace.")}</p>
          {error && (
            <p className="notice error" role="alert">
              {error}
            </p>
          )}
          <label>
            {tr("Username")}
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              required
            />
          </label>
          <label>
            {tr("Password")}
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              required
            />
          </label>
          <button className="primary" disabled={loginBusy}>
            {loginBusy ? tr("Signing in…") : tr("Sign in")}
          </button>
        </form>
      </main>
    );
  return (
    <main className="project-shell" key={identity.username}>
      <aside className="project-sidebar">
        <Link
          className="project-brand"
          href="/"
          onClick={(e) => {
            e.preventDefault();
            setView("project");
          }}
        >
          <span className="brand-mark">B</span>
          <strong>{brandName}</strong>
        </Link>
        <button className="new-project primary" onClick={() => void create()}>
          <Plus size={16} />
          {tr("New Project")}
        </button>
        <nav className="global-nav">
          <button
            aria-current={view === "search" ? "page" : undefined}
            onClick={() => setView("search")}
          >
            <Search size={16} />
            {tr("Search")}
          </button>
          <button
            aria-current={view === "inbox" ? "page" : undefined}
            onClick={() => setView("inbox")}
          >
            <Bell size={16} />
            {tr("Inbox")}
          </button>
          <button
            aria-current={view === "shared" ? "page" : undefined}
            onClick={() => setView("shared")}
          >
            <MessageSquare size={16} />
            {tr("Shared with me")}
          </button>
        </nav>
        <div className="sidebar-label">
          {tr("PROJECTS")}{" "}
          <button
            aria-label={tr("Refresh Projects")}
            onClick={() => void refresh()}
          >
            ↻
          </button>
        </div>
        <nav className="project-list">
          {projects.map((project) => (
            <button
              key={project.id}
              aria-current={
                selected === project.id && view === "project"
                  ? "page"
                  : undefined
              }
              onClick={() => navigate(project.id)}
            >
              <FolderOpen size={16} />
              <span>
                {project.title}
                <small>{project.state}</small>
              </span>
            </button>
          ))}
          {projects.length === 100 && (
            <button
              onClick={() =>
                void api<{
                  projects: Project[];
                }>(
                  `/api/projects?after=${encode(projects.at(-1)?.id || "")}&limit=100`,
                ).then((data) => setProjects(data.projects))
              }
            >
              {tr("More Projects")}
            </button>
          )}
        </nav>
        <footer>
          <span>
            {identity.username}
            {identity.development ? " · local" : ""}
          </span>
          <div className="row-actions">
            <button
              onClick={() =>
                setTheme(
                  document.documentElement.dataset.theme === "dark"
                    ? "light"
                    : "dark",
                )
              }
            >
              {tr("Theme")}
            </button>
            <button
              onClick={() =>
                void command("/api/auth/logout").then(() => {
                  setIdentity(null);
                  setProjects([]);
                  setSelected("");
                })
              }
            >
              {tr("Sign out")}
            </button>
          </div>
        </footer>
      </aside>
      <div className="project-main">
        {error && (
          <p className="notice error" role="alert">
            {error}
            <button onClick={() => setError("")}>{tr("Dismiss")}</button>
          </p>
        )}
        {view === "search" ? (
          <SearchPanel onOpen={navigate} />
        ) : view === "inbox" ? (
          <Inbox onOpen={navigate} />
        ) : view === "shared" ? (
          <SharedConversations
            key={targetSession}
            initialSession={targetSession}
          />
        ) : selected ? (
          <ProjectSpace
            key={`${identity.username}:${selected}`}
            actor={identity.username}
            projectId={selected}
            targetSession={targetSession}
            targetMessage={targetMessage}
            onChanged={refresh}
            onDeleted={() => {
              setSelected("");
              void refresh();
            }}
          />
        ) : (
          <div className="project-welcome">
            <span className="eyebrow">
              {tr("A WORKSPACE FOR YOUR RESEARCH")}
            </span>
            <h1>
              {tr("One Project.")}
              <br />
              {tr("Room for every idea.")}
            </h1>
            <p>
              {tr(
                "Run agents side by side, open a terminal, and work with shared files.",
              )}
            </p>
            <button className="primary" onClick={() => void create()}>
              <Plus size={16} />
              {tr("Create a Project")}
            </button>
          </div>
        )}
      </div>
    </main>
  );
}
function ProjectSpace({
  actor,
  projectId,
  targetSession,
  targetMessage,
  onChanged,
  onDeleted,
}: {
  actor: string;
  projectId: string;
  targetSession: string;
  targetMessage: string;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const tr = useT();
  const key = `project-view:${actor}:${projectId}:views`;
  const [project, setProject] = useState<Project | null>(null);
  const [sessions, setSessions] = useState<AgentSession[]>([]);
  const [terminals, setTerminals] = useState<ProjectTerminal[]>([]);
  const [openAgents, setOpenAgents] = useState<string[]>(() =>
    [
      ...new Set([
        ...viewPreferences(key).agents,
        ...(targetSession ? [targetSession] : []),
      ]),
    ].slice(-4),
  );
  const [openTerminals, setOpenTerminals] = useState<string[]>(
    () => viewPreferences(key).terminals,
  );
  const [selectedFile, setSelectedFile] = useState(
    () => viewPreferences(key).file,
  );
  const [tab, setTab] = useState<"work" | "schedules" | "share">("work");
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const [busy, setBusy] = useState(false);
  const base = projectURL(projectId);
  const refreshSerial = useRef(0);
  const refresh = useCallback(async () => {
    const serial = ++refreshSerial.current;
    try {
      const [p, s, t] = await Promise.all([
        api<Project>(base),
        api<{
          sessions: AgentSession[];
        }>(`${base}/sessions?limit=100`),
        api<{
          terminals: ProjectTerminal[];
        }>(`${base}/terminals?limit=100`),
      ]);
      if (serial !== refreshSerial.current) return;
      setProject((prior) => (prior && prior.revision > p.revision ? prior : p));
      setSessions((prior) => mergeEntities(prior, s.sessions));
      setTerminals((prior) => mergeEntities(prior, t.terminals));
    } catch (e) {
      if (serial !== refreshSerial.current) return;
      setError(e instanceof Error ? e.message : "Project unavailable.");
      if (e instanceof APIError && [401, 404].includes(e.status)) {
        setProject(null);
        setSessions([]);
        setTerminals([]);
      }
    }
  }, [base]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
    const timer = setInterval(() => {
      void refresh();
      setTick((n) => n + 1);
    }, 2500);
    return () => clearInterval(timer);
  }, [refresh]);
  useEffect(() => {
    remember(key, {
      agents: openAgents,
      terminals: openTerminals,
      file: selectedFile,
    });
  }, [key, openAgents, openTerminals, selectedFile]);
  useEffect(() => {
    if (!targetSession) return;
    const task = setTimeout(() => {
      setOpenAgents((open) => [...new Set([...open, targetSession])].slice(-4));
      setTab("work");
    }, 0);
    return () => clearTimeout(task);
  }, [targetSession]);
  useEffect(() => {
    const source = new EventSource(`${base}/events`);
    let timer: ReturnType<typeof setTimeout> | null = null;
    const invalidate = () => {
      if (!timer)
        timer = setTimeout(() => {
          timer = null;
          void refresh();
          setTick((n) => n + 1);
        }, 100);
    };
    source.addEventListener("projection", (event) => {
      const data = JSON.parse((event as MessageEvent).data) as Projection;
      if (data.project_id === projectId) invalidate();
    });
    source.addEventListener("snapshot", invalidate);
    source.addEventListener("gap", invalidate);
    source.onopen = invalidate;
    source.onerror = invalidate;
    source.addEventListener("closed", () => {
      source.close();
      void refresh();
    });
    return () => {
      source.close();
      if (timer) clearTimeout(timer);
    };
  }, [base, projectId, refresh]);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      await refresh();
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Action failed.");
    } finally {
      setBusy(false);
    }
  };
  const openAgent = (id: string) => {
    setOpenAgents((open) => [...new Set([...open, id])].slice(-4));
    setTab("work");
    void refresh();
  };
  const startTerminal = async (terminal: ProjectTerminal) => {
    if (openTerminals.length >= 4 && !openTerminals.includes(terminal.id))
      throw new Error(
        "Close a terminal view before opening another. Its process will keep running.",
      );
    const url = `${base}/terminals/${encode(terminal.id)}/start`;
    for (let attempt = 0; attempt < 100; attempt++) {
      const result = await command<{
        state?: string;
      }>(url, {
        rows: terminal.rows,
        cols: terminal.cols,
      });
      if (result.state === "running") {
        setOpenTerminals((open) => [...new Set([...open, terminal.id])]);
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(
      "Terminal start is still pending. Its durable state can be inspected and retried.",
    );
  };
  if (!project)
    return (
      <div className="empty-state">
        <h2>{tr("Opening Project\u2026")}</h2>
        {error && <p className="notice error">{error}</p>}
      </div>
    );
  if (project.state === "deleted")
    return (
      <div className="empty-state">
        <h2>{tr("Project deleted")}</h2>
        <p>{tr("Its owned runtime resources have been reclaimed.")}</p>
        <button onClick={onDeleted}>{tr("Back to Projects")}</button>
      </div>
    );
  return (
    <>
      <header className="project-heading">
        <div>
          <div className="eyebrow">{tr("PROJECT WORKSPACE")}</div>
          <h1>
            {project.title}
            <span className={`project-state ${project.state}`}>
              {project.state}
            </span>
          </h1>
        </div>
        <div className="row-actions">
          <button
            disabled={busy}
            onClick={() => {
              const title = window.prompt(tr("Project name"), project.title);
              if (title?.trim())
                void act(() =>
                  command(base, { title, revision: project.revision }, "PATCH"),
                );
            }}
          >
            {tr("Rename")}
          </button>
          <button
            disabled={busy || project.state !== "active"}
            onClick={() =>
              void act(() =>
                command(`${base}/pause?revision=${project.revision}`),
              )
            }
          >
            {tr("Pause")}
          </button>
          <button
            disabled={busy || project.state !== "active"}
            onClick={() => {
              if (
                window.confirm(
                  tr(
                    "Cold restart this idle Project? Live terminals and admitted work must end first.",
                  ),
                )
              )
                void act(() =>
                  command(`${base}/restart?revision=${project.revision}`),
                );
            }}
          >
            {tr("Restart")}
          </button>
          <button
            className="danger"
            disabled={busy || project.state !== "active"}
            onClick={() => {
              if (
                window.confirm(
                  tr(
                    "Force a cold Project restart? This ends every running Agent and terminal, holds queued work, and leaves unresolved effects unknown.",
                  ),
                )
              )
                void act(() =>
                  command(
                    `${base}/restart?revision=${project.revision}&force=1`,
                  ),
                );
            }}
          >
            {tr("Force restart")}
          </button>
          <button
            className="danger"
            disabled={busy || project.state === "deleting"}
            onClick={() => {
              if (
                window.confirm(
                  tr(
                    "Delete this Project, all its Sessions, terminals, workspace files and shares?",
                  ),
                )
              )
                void act(() =>
                  command(
                    `${base}?revision=${project.revision}`,
                    undefined,
                    "DELETE",
                  ),
                );
            }}
          >
            {tr("Delete Project")}
          </button>
        </div>
      </header>
      {error || project.failure ? (
        <p role="alert" className="notice error">
          {error || project.failure}
        </p>
      ) : null}
      <div className="project-toolbar">
        <nav className="work-tabs">
          <button
            aria-current={tab === "work" ? "page" : undefined}
            onClick={() => setTab("work")}
          >
            {tr("Workbench")}
          </button>
          <button
            aria-current={tab === "schedules" ? "page" : undefined}
            onClick={() => setTab("schedules")}
          >
            {tr("Schedules")}
          </button>
          <button
            aria-current={tab === "share" ? "page" : undefined}
            onClick={() => setTab("share")}
          >
            {tr("Sharing")}
          </button>
        </nav>
        <div className="row-actions">
          <button
            disabled={busy || project.state === "deleting"}
            onClick={() =>
              void act(async () => {
                const session = await command<AgentSession>(
                  `${base}/sessions`,
                  { id: newID(), title: "" },
                );
                openAgent(session.id);
              })
            }
          >
            <Plus size={14} />
            {tr("New agent")}
          </button>
          <button
            disabled={busy || project.state === "deleting"}
            onClick={() =>
              void act(async () => {
                const terminal = await command<ProjectTerminal>(
                  `${base}/terminals`,
                  {
                    id: newID(),
                    title: `Terminal ${terminals.length + 1}`,
                    rows: 24,
                    cols: 80,
                  },
                );
                await startTerminal(terminal);
              })
            }
          >
            <TerminalSquare size={14} />
            {tr("New terminal")}
          </button>
        </div>
      </div>
      <div className="child-strip">
        <span>{tr("Agents")}</span>
        {sessions
          .filter((s) => s.deletion === 0)
          .map((s) => (
            <button
              key={s.id}
              aria-pressed={openAgents.includes(s.id)}
              onClick={() => openAgent(s.id)}
            >
              {s.title}
              <small>{s.state}</small>
            </button>
          ))}
        {terminals
          .filter((t) => t.deletion === 0)
          .map((t) => (
            <div className="terminal-chip" key={t.id}>
              <button
                disabled={busy}
                onClick={() => void act(() => startTerminal(t))}
              >
                {t.title} · {t.state}
              </button>
              {["running", "starting"].includes(t.state) && (
                <button
                  disabled={busy}
                  onClick={() =>
                    void act(() =>
                      command(`${base}/terminals/${encode(t.id)}/stop`),
                    )
                  }
                >
                  {tr("Terminate")}
                </button>
              )}
              <button
                disabled={busy}
                aria-label={`Delete ${t.title}`}
                onClick={() => {
                  if (
                    window.confirm(
                      `Delete ${t.title} and terminate its process?`,
                    )
                  )
                    void act(() =>
                      command(
                        `${base}/terminals/${encode(t.id)}?revision=${t.revision}`,
                        undefined,
                        "DELETE",
                      ),
                    );
                }}
              >
                ×
              </button>
            </div>
          ))}
      </div>
      <div className="workspace-layout">
        <div className="execution-area">
          {tab === "schedules" ? (
            <SchedulePanel
              project={projectId}
              sessions={sessions}
              onOpen={openAgent}
            />
          ) : tab === "share" ? (
            <SharePanel project={projectId} sessions={sessions} />
          ) : (
            <div className={`agent-grid panels-${openAgents.length}`}>
              {openAgents.map((id) => {
                const session = sessions.find(
                  (s) => s.id === id && s.deletion === 0,
                );
                return session ? (
                  <SessionPanel
                    key={id}
                    actor={actor}
                    session={session}
                    tick={tick}
                    onClose={() =>
                      setOpenAgents((open) =>
                        open.filter((value) => value !== id),
                      )
                    }
                    onChanged={() => void refresh()}
                    onFile={setSelectedFile}
                    targetMessage={targetSession === id ? targetMessage : ""}
                  />
                ) : null;
              })}
              {!openAgents.some((id) =>
                sessions.some((s) => s.id === id && s.deletion === 0),
              ) && (
                <div className="empty-state">
                  <MessageSquare size={30} />
                  <h2>{tr("Open an agent to begin")}</h2>
                  <p>
                    {tr(
                      "Agent views run independently in this Project\u2019s shared workspace.",
                    )}
                  </p>
                </div>
              )}
            </div>
          )}
          {openTerminals.length > 0 && (
            <div className="terminal-grid">
              {openTerminals.map((id) => {
                const terminal = terminals.find(
                  (t) => t.id === id && t.deletion === 0,
                );
                return terminal ? (
                  <TerminalPanel
                    key={`${id}:${terminal.incarnation || "created"}`}
                    projectId={projectId}
                    terminalId={id}
                    title={terminal.title}
                    processState={terminal.state}
                    processExitCode={terminal.exit_code}
                    onClose={() =>
                      setOpenTerminals((open) =>
                        open.filter((value) => value !== id),
                      )
                    }
                  />
                ) : null;
              })}
            </div>
          )}
        </div>
        <FilesPanel
          project={project}
          selected={selectedFile}
          onSelect={setSelectedFile}
        />
      </div>
    </>
  );
}
function mergeEntities<
  T extends {
    id: string;
    revision: number;
  },
>(current: T[], next: T[]): T[] {
  return next.map((item) => {
    const old = current.find((prior) => prior.id === item.id);
    return old && old.revision > item.revision ? old : item;
  });
}
