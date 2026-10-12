"use client";
import { CapabilityProvider, useCapabilities } from "./capability-provider";
import type { Identity } from "@web/identity";
import Link from "next/link";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import {
  Bell,
  ChevronDown,
  ChevronRight,
  Clock3,
  Folder,
  FolderOpen,
  Menu as MenuIcon,
  MessageSquare,
  Plus,
  Search,
  Settings2,
  SquarePen,
  X,
} from "lucide-react";
import {
  api,
  APIError,
  command,
  clearPreferenceMemory,
  newID,
  projectURL,
  recalled,
  remember,
  sessionURL,
  type AgentSession,
  type Project,
} from "@/lib/project-api";
import {
  readNavigation,
  normalizeNavigation,
  globalNavigation,
  readWorkspace,
  navigationURL,
  workspaceKey,
  type Navigation,
} from "@/lib/workspace-state";
import { useT } from "@/lib/i18n";
import { displayName, displayInitial } from "@web/brand";
import { ACTIVE_APPEARANCE_KEY, applyAppearance, DEFAULT_APPEARANCE, clearAppearanceBinding } from "@web/appearance";
import { adminReturnPath } from "@web/return-path";
import { useAccountAppearance } from "./account-appearance";
import { AccountSettings } from "./account-settings";
import {
  DialogProvider,
  IconButton,
  Menu,
  useDialog,
  useMediaQuery,
} from "./ui";
import { useWorkspaceData } from "./workspace-data";
import { ProjectPicker } from "./project-picker";
import { ProjectWorkspace } from "./workspace";
import { Inbox, SearchPanel } from "./adjacent-panels";
import { SharedConversations } from "./shared-conversations";
import { Schedules } from "./schedules";
import "./workbench.css";
import "./schedules.css";
import { ResourceProvider } from "./resource-provider";

export function ProjectWorkbench() {
  const tr = useT();
  const [identity, setIdentity] = useState<Identity | null>(null),
    [booted, setBooted] = useState(false),
    [error, setError] = useState("");
  const [username, setUsername] = useState(""),
    [password, setPassword] = useState(""),
    [busy, setBusy] = useState(false);
  const authGeneration = useRef(0);
  const acceptIdentity = useCallback((value: Identity | null) => {
    authGeneration.current++;
    setIdentity(current => current && value && current.id === value.id && current.revision > value.revision ? current : value);
    if (value) setError("");
  }, []);
  useEffect(() => {
    let gone = false;
    const generation = ++authGeneration.current;
    void api<Identity>("/api/auth/session")
      .then((user) => {
        if (!gone && generation === authGeneration.current) { acceptIdentity(user); if (user.must_change_password) clearAppearanceBinding(); }
      })
      .catch((e) => {
        if (!gone && generation === authGeneration.current) {
          if (e instanceof APIError && e.status === 401) clearAppearanceBinding();
          else setError(String(e));
        }
      })
      .finally(() => {
        if (!gone) setBooted(true);
      });
    return () => {
      gone = true;
    };
  }, [acceptIdentity]);
  useEffect(() => {
    let alive = true;
    const revalidate = () => {
      const generation = ++authGeneration.current;
      void api<Identity>("/api/auth/session").then(user => {
        if (!alive || generation !== authGeneration.current) return;
        acceptIdentity(user);
        if (user.must_change_password) clearAppearanceBinding();
      }).catch(() => {
        if (!alive || generation !== authGeneration.current) return;
        clearPreferenceMemory(); clearAppearanceBinding(); acceptIdentity(null);
        setError(tr("Your login expired. Sign in again."));
      });
    };
    const storage = (event: StorageEvent) => { if (event.key === ACTIVE_APPEARANCE_KEY || event.key === null) revalidate(); };
    window.addEventListener("project-auth-expired", revalidate);
    window.addEventListener("storage", storage);
    return () => { alive = false; window.removeEventListener("project-auth-expired", revalidate); window.removeEventListener("storage", storage); };
  }, [tr, acceptIdentity]);
  useEffect(() => {
    if (!booted || identity && !identity.must_change_password) return;
    const system = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => applyAppearance(DEFAULT_APPEARANCE);
    update(); system.addEventListener("change", update);
    return () => system.removeEventListener("change", update);
  }, [booted, identity]);
  const adminReturn = identity && !identity.must_change_password
    ? adminReturnPath(new URLSearchParams(window.location.search).get("return_to"), window.location.origin) : null;
  useEffect(() => { if (adminReturn) window.location.replace(adminReturn); }, [adminReturn]);
  if (!booted)
    return (
      <main className="project-loading">{tr("Opening your workbench…")}</main>
    );
  if (!identity)
    return (
      <main className="project-login">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            void command<Identity>("/api/auth/login", { username, password })
              .then((user) => {
                acceptIdentity(user);
                setPassword("");
              })
              .catch((e) => setError(String(e)))
              .finally(() => setBusy(false));
          }}
        >
          <span className="brand-mark" aria-hidden="true">{displayInitial}</span>
          <h1>{displayName}</h1>
          <p>{tr("Your research, agents, and workspace.")}</p>
          {error && (
            <p className="notice error" role="alert">
              {error}
            </p>
          )}
          <label>
            {tr("Username")}
            <input
              required
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </label>
          <label>
            {tr("Password")}
            <input
              required
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <button className="primary" disabled={busy}>
            {busy ? tr("Signing in…") : tr("Sign in")}
          </button>
        </form>
      </main>
    );
  if (identity.must_change_password)
    return (
      <main className="project-login">
        <form onSubmit={(e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          void command<Identity>("/api/auth/password/setup", { new_password: password })
            .then((user) => { acceptIdentity(user); setPassword(""); })
            .catch((e) => setError(String(e)))
            .finally(() => setBusy(false));
        }}>
          <h1>{tr("Set your password")}</h1>
          <p>{tr("Choose a permanent password with 12 to 128 characters.")}</p>
          {error && <p className="notice error" role="alert">{error}</p>}
          <label>{tr("New password")}
            <input required type="password" autoComplete="new-password" value={password}
              onChange={(e) => setPassword(e.target.value)} />
          </label>
          <button className="primary" disabled={busy}>{tr("Save password")}</button>
          <button type="button" disabled={busy} onClick={() => {
            setBusy(true);
            void command("/api/auth/logout", {})
              .then(() => { clearAppearanceBinding(); acceptIdentity(null); setPassword(""); setError(""); })
              .catch((e) => setError(String(e)))
              .finally(() => setBusy(false));
          }}>{tr("Sign out")}</button>
        </form>
      </main>
    );
  if (adminReturn) return <main className="project-loading">{tr("Opening administration…")}</main>;
  return (
    <DialogProvider key={identity.id}>
      <ResourceProvider actor={identity.id}>
      <CapabilityProvider accountId={identity.id}>
        <Application
          identity={identity}
          onIdentity={acceptIdentity}
          onProfile={value => setIdentity(current => current?.id === value.id ? { ...current, ...value } : current)}
          onLogout={() => {
            clearPreferenceMemory();
            clearAppearanceBinding();
            acceptIdentity(null);
          }}
        />
      </CapabilityProvider>
      </ResourceProvider>
    </DialogProvider>
  );
}
function Application({
  identity,
  onIdentity,
  onProfile,
  onLogout,
}: {
  identity: Identity;
  onIdentity: (value: Identity) => void;
  onProfile: (value: Pick<Identity, "id" | "username" | "display_name" | "revision">) => void;
  onLogout: () => void;
}) {
  const can = useCapabilities();
  const canManage = can("projects.manage"), canCreate = can("projects.create"), canRun = can("agents.run");
  const tr = useT(),
    ask = useDialog(),
    actor = identity.id;
  const appearance = useAccountAppearance(actor);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [nav, dispatch] = useReducer(
    (_: Navigation, n: Navigation) => n,
    undefined,
    () => {
      const n = readNavigation(location.search);
      if (n.view === "project" && n.project && !n.session && !n.draft)
        n.session = readWorkspace(actor, n.project).activeSessionId;
      return n;
    },
  );
  const [expanded, setExpanded] = useState<string[]>(() => {
    const v = recalled<unknown>(`project-view:${actor}:expanded`, []);
    return Array.isArray(v)
      ? v.filter((x): x is string => typeof x === "string")
      : [];
  });
  const narrow = useMediaQuery("(max-width: 767px)");
  const sidebar = useRef<HTMLElement>(null);
  const [drawer, setDrawer] = useState(false),
    [fullscreen, setFullscreen] = useState(false),
    [storageError, setStorageError] = useState(false);
  useEffect(() => {
    if (!drawer) return;
    const previous = document.activeElement as HTMLElement;
    sidebar.current?.querySelector<HTMLElement>("a,button")?.focus();
    const keys = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.target instanceof Element && e.target.closest("dialog[open], [role='dialog']")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        setDrawer(false);
        previous?.focus();
      }
      if (e.key === "Tab") {
        const nodes = [
          ...(sidebar.current?.querySelectorAll<HTMLElement>(
            "a,button:not(:disabled),input,select",
          ) || []),
        ];
        const first = nodes[0],
          last = nodes.at(-1);
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last?.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", keys);
    return () => document.removeEventListener("keydown", keys);
  }, [drawer]);
  const forgetExpandedProject = useCallback((project: string) => {
    setExpanded((old) =>
      old.includes(project) ? old.filter((id) => id !== project) : old,
    );
  }, []);
  const data = useWorkspaceData(
    nav.project,
    expanded,
    nav.session,
    forgetExpandedProject,
  );
  const { ensureTarget } = data;
  const guard = useRef<() => Promise<boolean>>(async () => true);
  const runRequests = useRef(new Map<string, string>());
  const scheduleListMemory = useRef({ key: "", top: 0 });
  const navRef = useRef(nav);
  useEffect(() => {
    navRef.current = nav;
  }, [nav]);
  const navigate = useCallback(async (next: Navigation, replace = false) => {
    next = normalizeNavigation(next);
    if (
      (next.project !== navRef.current.project ||
        next.view !== navRef.current.view ||
        next.schedule !== navRef.current.schedule ||
        next.scheduleProject !== navRef.current.scheduleProject ||
        next.scheduleMode !== navRef.current.scheduleMode) &&
      !(await guard.current())
    )
      return;
    navRef.current = next;
    dispatch(next);
    setDrawer(false);
    setFullscreen(false);
    history[replace ? "replaceState" : "pushState"](
      null,
      "",
      navigationURL(next),
    );
  }, []);
  useEffect(() => {
    history.replaceState(null, "", navigationURL(navRef.current));
    const pop = () => {
      const next = readNavigation(location.search);
      void (async () => {
        if (
          (next.project !== navRef.current.project ||
            next.view !== navRef.current.view ||
            next.schedule !== navRef.current.schedule ||
            next.scheduleProject !== navRef.current.scheduleProject ||
            next.scheduleMode !== navRef.current.scheduleMode) &&
          !(await guard.current())
        ) {
          history.pushState(null, "", navigationURL(navRef.current));
          return;
        }
        navRef.current = next;
        history.replaceState(null, "", navigationURL(next));
        dispatch(next);
        setDrawer(false);
        setFullscreen(false);
      })();
    };
    window.addEventListener("popstate", pop);
    return () => window.removeEventListener("popstate", pop);
  }, []);
  useEffect(() => {
    remember(`project-view:${actor}:expanded`, expanded);
  }, [actor, expanded]);
  useEffect(() => {
    const fail = () => setStorageError(true);
    window.addEventListener("project-storage-unavailable", fail);
    return () =>
      window.removeEventListener("project-storage-unavailable", fail);
  }, []);
  useEffect(() => {
    if (!["project", "share"].includes(nav.view) || !nav.project) return;
    let gone = false;
    void ensureTarget(nav.project, nav.session)
      .then(() => {
        if (!gone)
          setExpanded((old) =>
            old.includes(nav.project) ? old : [...old, nav.project],
          );
      })
      .catch((e) => {
        if (
          !gone &&
          navRef.current === nav &&
          e instanceof APIError &&
          e.status === 404
        )
          void navigate(
            nav.session
              ? globalNavigation("shared", nav.session)
              : readNavigation(""),
            true,
          );
      });
    return () => {
      gone = true;
    };
  }, [nav, ensureTarget, navigate]);
  const selected = data.sessions[nav.project]?.find(
    (s) => s.id === nav.session,
  );
  const open = (project: string, session = "", message = "") => {
    const pref = readWorkspace(actor, project);
    const target = session || pref.activeSessionId;
    if (target) {
      remember(workspaceKey(actor, project), {
        ...pref,
        activeSessionId: target,
      });
    }
    void navigate({
      view: "project",
      project,
      session: target,
      message,
      draft: !target,
      history: false,
    });
  };
  const act = async (fn: () => Promise<unknown>) => {
    data.setError("");
    try {
      await fn();
      await data.refresh();
    } catch (e) {
      data.setError(String(e));
    }
  };
  const createProject = async () => {
    if (!canCreate) return;
    const title = await ask({
      title: tr("Project name"),
      initial: "",
      input: true,
      choices: [{ label: tr("Create a Project"), value: "create" }],
    });
    if (!title) return;
    await act(async () => {
      const p = await command<Project>("/api/projects", { id: newID(), title });
      void navigate({
        view: "project",
        project: p.id,
        session: "",
        message: "",
        draft: true,
        history: false,
      });
    });
  };
  const newChat = (project = nav.project) => {
    if (!canRun) return;
    void navigate({
      view: "project",
      project,
      session: "",
      message: "",
      draft: true,
      history: false,
    });
  };
  const projectActions = (p: Project) => [
    {
      label: tr("Rename"),
      disabled: !canManage,
      action: () =>
        void (async () => {
          const title = await ask({
            title: tr("Project name"),
            initial: p.title,
            input: true,
            choices: [{ label: tr("Save"), value: "save" }],
          });
          if (title)
            void act(() =>
              command(
                projectURL(p.id),
                { title, revision: p.revision },
                "PATCH",
              ),
            );
        })(),
    },
    {
      label: p.execution_suspended ? tr("Resume suspended Project") : p.state === "active" ? tr("Pause") : tr("Resume Project"),
      disabled: ["deleting", "deleted"].includes(p.state) || (p.state !== "active" && !canManage),
      action: () =>
        void act(() =>
          command(
            `${projectURL(p.id)}/${p.state === "active" && !p.execution_suspended ? "pause" : "resume"}?revision=${p.revision}`,
          ),
        ),
    },
    ...[false, true].map((force) => ({
      label: force ? tr("Force restart") : tr("Restart"),
      danger: force,
      disabled: !!p.execution_suspended || p.state !== "active" || !canManage,
      action: () =>
        void (async () => {
          if (
            await ask({
              title: force ? tr("Force restart") : tr("Restart"),
              description: force
                ? tr(
                    "Force a cold Project restart? This ends every running Agent and terminal, holds queued work, and leaves unresolved effects unknown.",
                  )
                : tr(
                    "Cold restart this idle Project? Live terminals and admitted work must end first.",
                  ),
              choices: [{ label: tr("Restart"), value: "yes", danger: force }],
            })
          )
            void act(() =>
              command(
                `${projectURL(p.id)}/restart?revision=${p.revision}${force ? "&force=1" : ""}`,
              ),
            );
        })(),
    })),
    {
      label: tr("Delete Project"),
      danger: true,
      action: () =>
        void (async () => {
          if (p.id === nav.project && !(await guard.current())) return;
          if (
            await ask({
              title: tr("Delete Project"),
              description: tr(
                "Delete this Project, all its Sessions, terminals, workspace files and shares?",
              ),
              choices: [{ label: tr("Delete"), value: "yes", danger: true }],
            })
          )
            await act(async () => {
              await command(
                `${projectURL(p.id)}?revision=${p.revision}`,
                undefined,
                "DELETE",
              );
              if (nav.project === p.id)
                void navigate({
                  view: "project",
                  project: "",
                  session: "",
                  message: "",
                  draft: true,
                  history: false,
                });
            });
        })(),
    },
  ];
  const sessionActions = (s: AgentSession) => [
    {
      label: tr("Rename"),
      disabled: !canManage,
      action: () =>
        void (async () => {
          const title = await ask({
            title: tr("Session title"),
            initial: s.title,
            input: true,
            choices: [{ label: tr("Save"), value: "save" }],
          });
          if (title)
            void act(() =>
              command(
                sessionURL(s.project_id, s.id),
                { title, revision: s.revision },
                "PATCH",
              ),
            );
        })(),
    },
    {
      label: tr("Sharing"),
      action: () =>
        void navigate({
          view: "share",
          project: s.project_id,
          session: s.id,
          message: "",
          draft: false,
          history: false,
        }),
    },
    {
      label: tr("Turn history"),
      action: () =>
        void navigate({
          view: "project",
          project: s.project_id,
          session: s.id,
          message: "",
          draft: false,
          history: true,
        }),
    },
    {
      label: tr("Delete"),
      danger: true,
      action: () =>
        void (async () => {
          if (
            await ask({
              title: tr("Delete"),
              description: tr(
                "Delete this Agent Session and its conversation? Project files and other Sessions remain.",
              ),
              choices: [{ label: tr("Delete"), value: "yes", danger: true }],
            })
          )
            void act(async () => {
              await command(
                `${sessionURL(s.project_id, s.id)}?revision=${s.revision}`,
                undefined,
                "DELETE",
              );
              remember(
                `project-view:${actor}:${s.project_id}:draft:${s.id}`,
                "",
              );
              const pref = readWorkspace(actor, s.project_id);
              if (pref.activeSessionId === s.id)
                remember(workspaceKey(actor, s.project_id), {
                  ...pref,
                  activeSessionId: "",
                });
              if (nav.session === s.id) newChat();
              await data.refreshChildren(s.project_id);
            });
        })(),
    },
  ];
  const page = (view: "search" | "inbox" | "shared" | "schedules") =>
    void navigate(globalNavigation(view));
  const project = data.projects.find((p) => p.id === nav.project);
  return (
    <main
      className={`project-shell ${drawer ? "navigation-open" : ""} ${fullscreen ? "file-fullscreen" : ""}`}
    >
      {settingsOpen && <AccountSettings identity={identity} appearance={appearance} onIdentity={onIdentity} onProfile={onProfile}
        onClose={() => setSettingsOpen(false)} onLogoutAll={async () => {
          if (!await guard.current()) return;
          await command("/api/me/sessions", undefined, "DELETE");
          onLogout();
        }} />}
      {drawer && (
        <button
          className="navigation-backdrop"
          aria-label={tr("Close navigation")}
          onClick={() => setDrawer(false)}
        />
      )}
      <aside
        ref={sidebar}
        className="project-sidebar"
        inert={fullscreen || (narrow && !drawer)}
        aria-label={tr("Projects")}
      >
        <div className="sidebar-top">
          <Link
            className="project-brand"
            href="/"
            onClick={(e) => {
              e.preventDefault();
              void navigate(readNavigation(""));
            }}
          >
            <span className="brand-mark" aria-hidden="true">{displayInitial}</span>
            <strong>{displayName}</strong>
          </Link>
          <IconButton label={tr("Search")} onClick={() => page("search")}>
            <Search size={18} />
          </IconButton>
          <IconButton label={tr("Inbox")} onClick={() => page("inbox")}>
            <Bell size={18} />
          </IconButton>
        </div>
        <nav className="global-nav">
          <button onClick={() => newChat()}>
            <SquarePen size={18} />
            {tr("New Chat")}
          </button>
          <button
            aria-current={nav.view === "schedules" ? "page" : undefined}
            onClick={() => page("schedules")}
          >
            <Clock3 size={18} />
            {tr("Scheduled")}
          </button>
          <button
            aria-current={nav.view === "shared" ? "page" : undefined}
            onClick={() => page("shared")}
          >
            <MessageSquare size={18} />
            {tr("Shared with me")}
          </button>
        </nav>
        <div className="sidebar-label">
          <span>{tr("Projects")}</span>
          <IconButton
            label={tr("New Project")}
            disabled={!canCreate}
            onClick={() => void createProject()}
          >
            <Plus size={16} />
          </IconButton>
        </div>
        <nav className="project-list">
          {data.projects
            .filter((p) => p.state !== "deleted")
            .map((p) => (
              <div className="project-group" key={p.id}>
                <div className="navigation-row project-row">
                  <button
                    className="navigation-title project-toggle"
                    aria-expanded={expanded.includes(p.id)}
                    onClick={() =>
                      setExpanded((old) =>
                        old.includes(p.id)
                          ? old.filter((id) => id !== p.id)
                          : [...old, p.id],
                      )
                    }
                  >
                    {expanded.includes(p.id) ? (
                      <ChevronDown size={14} />
                    ) : (
                      <ChevronRight size={14} />
                    )}
                    {expanded.includes(p.id) ? (
                      <FolderOpen size={17} />
                    ) : (
                      <Folder size={17} />
                    )}
                    <span>{p.title}</span>
                  </button>
                  <IconButton
                    className="icon-button project-new-chat"
                    label={tr("New chat in {project}", { project: p.title })}
                    disabled={!canRun || !!p.execution_suspended || ["deleting", "deleted"].includes(p.state)}
                    onClick={() => newChat(p.id)}
                  >
                    <Plus size={16} />
                  </IconButton>
                  <Menu
                    label={`${tr("Project actions")} ${p.title}`}
                    items={projectActions(p)}
                  />
                </div>
                {expanded.includes(p.id) && (
                  <div className="session-list">
                    {data.sessions[p.id] === undefined ? (
                      <small className="list-hint">{tr("Loading…")}</small>
                    ) : (
                      data.sessions[p.id]
                        .filter((s) => s.deletion === 0)
                        .map((s) => (
                          <div
                            className={`navigation-row session-row ${["project", "share"].includes(nav.view) && nav.session === s.id && nav.project === p.id ? "selected" : ""}`}
                            key={s.id}
                          >
                            <button
                              className="navigation-title"
                              aria-current={
                                ["project", "share"].includes(nav.view) &&
                                nav.session === s.id &&
                                nav.project === p.id
                                  ? "page"
                                  : undefined
                              }
                              title={s.title}
                              data-session-link={s.id}
                              onClick={() => open(p.id, s.id)}
                            >
                              <span>{s.title || tr("New Chat")}</span>
                              {s.state !== "idle" && (
                                <span
                                  className={`state-dot ${s.state}`}
                                  aria-label={s.state}
                                  title={s.state}
                                />
                              )}
                            </button>
                            <Menu
                              label={`${tr("Session actions")} ${s.title}`}
                              items={sessionActions(s)}
                            />
                          </div>
                        ))
                    )}
                    {data.more[p.id] && (
                      <button
                        className="list-hint"
                        onClick={() => void data.refreshChildren(p.id, true)}
                      >
                        {tr("Show more")}
                      </button>
                    )}
                    {data.sessions[p.id]?.filter((s) => s.deletion === 0)
                      .length === 0 && (
                      <button
                        className="list-hint"
                        onClick={() =>
                          void navigate({
                            view: "project",
                            project: p.id,
                            session: "",
                            message: "",
                            draft: true,
                            history: false,
                          })
                        }
                      >
                        {tr("New Chat")}
                      </button>
                    )}
                  </div>
                )}
              </div>
            ))}
          {data.more.projects && (
            <button onClick={() => void data.refreshProjects(true)}>
              {tr("More Projects")}
            </button>
          )}
        </nav>
        <footer>
          <Menu
            label={tr("Account")}
            items={[
              {
                label: tr("Settings"),
                action: () => setSettingsOpen(true),
              },
              ...(identity.role === "admin" ? [{
                label: tr("Administration"),
                action: () => void (async () => {
                  // The independent admin build needs a full navigation through the edge.
                  if (await guard.current()) window.location.assign(new URL("/admin", window.location.origin).href);
                })(),
              }] : []),
              {
                label: tr("Sign out"),
                action: () =>
                  void (async () => {
                    if (await guard.current())
                      void act(async () => {
                        await command("/api/auth/logout");
                        onLogout();
                      });
                  })(),
              },
            ]}
          >
            <Settings2 size={18} />
            <span>{identity.display_name}{Object.keys(appearance.changes).length > 0 && <small className="account-status">{tr("Unsaved appearance")}</small>}</span>
          </Menu>
        </footer>
      </aside>
      <div className="project-main">
        <div className="mobile-navigation">
          <IconButton
            label={tr("Open navigation")}
            onClick={() => setDrawer(true)}
          >
            <MenuIcon size={20} />
          </IconButton>
          <span>{displayName}</span>
        </div>
        {data.error && (
          <p className="notice error" role="alert">
            {data.error}
            <IconButton label={tr("Dismiss")} onClick={() => data.setError("")}>
              <X size={16} />
            </IconButton>
          </p>
        )}
        {storageError && (
          <p className="notice warning">
            {tr(
              "Browser storage is unavailable. Drafts are kept here until you leave this page.",
            )}
          </p>
        )}
        {project && (
          <div
            className="workspace-host"
            hidden={!["project", "share"].includes(nav.view)}
          >
            <ProjectWorkspace
              key={`${actor}:${project.id}`}
              actor={actor}
              project={project}
              projects={data.projects}
              session={selected}
              sessionId={nav.session}
              sessions={data.sessions[project.id] || []}
              terminals={data.terminals[project.id] || []}
              nav={nav}
              onNavigate={(next) => void navigate(next)}
              onRefresh={data.refresh}
              onError={data.setError}
              guard={guard}
              fullscreen={fullscreen}
              onFullscreen={setFullscreen}
            />
          </div>
        )}
        {nav.view === "search" ? (
          <SearchPanel onOpen={open} />
        ) : nav.view === "inbox" ? (
          <Inbox onOpen={open} />
        ) : nav.view === "shared" ? (
          <SharedConversations
            selected={nav.conversation || ""}
            onSelect={(id) => void navigate(globalNavigation("shared", id))}
          />
        ) : nav.view === "schedules" ? (
          <Schedules
            projects={data.projects}
            onOpen={open}
            nav={nav}
            onNavigate={(next, replace) => void navigate(next, replace)}
            guardRef={guard}
            runRequests={runRequests}
            listMemoryRef={scheduleListMemory}
          />
        ) : !project ? (
          <div className="project-welcome">
            <h1>{tr("What would you like to work on?")}</h1>
            <p>{tr("Choose a Project to start a conversation.")}</p>
            {data.projects.length > 0 && (
              <ProjectPicker
                projects={data.projects}
                value=""
                onChange={(id) => newChat(id)}
              />
            )}
            <button disabled={!canCreate} onClick={() => void createProject()}>
              <Plus size={17} />
              {tr("Create a Project")}
            </button>
          </div>
        ) : null}
      </div>
    </main>
  );
}
