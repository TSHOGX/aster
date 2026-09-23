import { recalled } from "./project-api";
export type WorkspacePreferences = {
  version: 2;
  activeSessionId: string;
  filesOpen: boolean;
  terminalOpen: boolean;
  terminals: string[];
  activeTerminal: string;
  file: string;
  fileWidth: number;
  treeWidth: number;
  terminalHeight: number;
  directories: string[];
};
export const workspaceKey = (actor: string, project: string) =>
  `project-view:${actor}:${project}:views`;
export function parsePreferences(value: unknown): WorkspacePreferences {
  const d =
    value && typeof value === "object"
      ? (value as Record<string, unknown>)
      : {};
  const id = (v: unknown) =>
    typeof v === "string" && /^[a-zA-Z0-9_-]{1,128}$/.test(v) ? v : "";
  const ids = (v: unknown) =>
    Array.isArray(v) ? [...new Set(v.map(id).filter(Boolean))] : [];
  const size = (v: unknown, fallback: number, min: number, max: number) =>
    typeof v === "number" && Number.isFinite(v)
      ? Math.max(min, Math.min(max, v))
      : fallback;
  return {
    version: 2,
    activeSessionId: id(d.activeSessionId),
    filesOpen: d.version === 2 && d.filesOpen === true,
    terminalOpen: d.version === 2 && d.terminalOpen === true,
    terminals: ids(d.terminals),
    activeTerminal: id(d.activeTerminal) || ids(d.terminals)[0] || "",
    file: typeof d.file === "string" ? d.file.slice(0, 4096) : "",
    fileWidth: size(d.fileWidth, 58, 25, 80),
    treeWidth: size(d.treeWidth, 240, 200, 320),
    terminalHeight: size(d.terminalHeight, 260, 160, 1000),
    directories: Array.isArray(d.directories)
      ? d.directories
          .filter((x): x is string => typeof x === "string")
          .slice(0, 200)
      : [""],
  };
}
export const readWorkspace = (actor: string, project: string) =>
  parsePreferences(recalled(workspaceKey(actor, project), null));
export function mergePage<T extends { id: string; revision: number }>(
  old: T[],
  next: T[],
): T[] {
  const byId = new Map(old.map((x) => [x.id, x]));
  for (const x of next)
    if (!byId.has(x.id) || byId.get(x.id)!.revision <= x.revision)
      byId.set(x.id, x);
  return [...byId.values()].sort((a, b) => a.id.localeCompare(b.id));
}
export type Navigation = {
  view: "project" | "search" | "inbox" | "shared" | "schedules" | "share";
  project: string;
  session: string;
  message: string;
  draft: boolean;
  history: boolean;
  conversation?: string;
  schedule?: string;
  scheduleProject?: string;
  scheduleMode?: "create" | "edit";
  scheduleFilter?: string;
  scheduleQuery?: string;
  scheduleStatus?: "enabled" | "paused";
  scheduleAfter?: string;
};
export function normalizeNavigation(n: Navigation): Navigation {
  const base: Navigation = {
    view: n.view,
    project: "",
    session: "",
    message: "",
    draft: false,
    history: false,
  };
  if (n.view === "project" || n.view === "share") {
    base.project = n.project;
    base.session = n.project && !n.draft ? n.session : "";
    if (n.view === "project") {
      base.draft = n.draft;
      base.message = base.session ? n.message : "";
      base.history = !!base.session && n.history;
    }
  } else if (n.view === "shared" && n.conversation) {
    base.conversation = n.conversation;
  } else if (n.view === "schedules") {
    if (n.scheduleMode === "create") base.scheduleMode = "create";
    else if (n.schedule && n.scheduleProject) {
      base.schedule = n.schedule;
      base.scheduleProject = n.scheduleProject;
      if (n.scheduleMode === "edit") base.scheduleMode = "edit";
    }
    if (n.scheduleFilter) base.scheduleFilter = n.scheduleFilter;
    if (n.scheduleQuery) base.scheduleQuery = n.scheduleQuery.slice(0, 256);
    if (n.scheduleStatus === "enabled" || n.scheduleStatus === "paused")
      base.scheduleStatus = n.scheduleStatus;
    if (n.scheduleAfter) base.scheduleAfter = n.scheduleAfter;
  }
  return base;
}
export function globalNavigation(
  view: "search" | "inbox" | "shared" | "schedules",
  conversation = "",
): Navigation {
  return normalizeNavigation({
    view,
    project: "",
    session: "",
    message: "",
    draft: false,
    history: false,
    conversation,
  });
}
export function readNavigation(search: string): Navigation {
  const p = new URLSearchParams(search),
    view = p.get("view");
  return normalizeNavigation({
    view: ["search", "inbox", "shared", "schedules", "share"].includes(
      view || "",
    )
      ? (view as Navigation["view"])
      : "project",
    project: p.get("project") || "",
    session: p.get("session") || "",
    message: p.get("message") || "",
    draft: p.get("draft") === "1",
    history: p.get("history") === "1",
    conversation: p.get("conversation") || "",
    schedule: p.get("schedule") || "",
    scheduleProject: p.get("schedule_project") || "",
    scheduleMode:
      p.get("mode") === "create"
        ? "create"
        : p.get("mode") === "edit"
          ? "edit"
          : undefined,
    scheduleFilter: p.get("filter_project") || "",
    scheduleQuery: p.get("q") || "",
    scheduleStatus:
      p.get("status") === "enabled"
        ? "enabled"
        : p.get("status") === "paused"
          ? "paused"
          : undefined,
    scheduleAfter: p.get("after") || "",
  });
}
export function navigationURL(input: Navigation) {
  const n = normalizeNavigation(input);
  const p = new URLSearchParams();
  if (n.view !== "project") p.set("view", n.view);
  if (n.project) p.set("project", n.project);
  if (n.session) p.set("session", n.session);
  if (n.message) p.set("message", n.message);
  if (n.draft) p.set("draft", "1");
  if (n.history) p.set("history", "1");
  if (n.conversation) p.set("conversation", n.conversation);
  if (n.schedule) p.set("schedule", n.schedule);
  if (n.scheduleProject) p.set("schedule_project", n.scheduleProject);
  if (n.scheduleMode) p.set("mode", n.scheduleMode);
  if (n.scheduleFilter) p.set("filter_project", n.scheduleFilter);
  if (n.scheduleQuery) p.set("q", n.scheduleQuery);
  if (n.scheduleStatus) p.set("status", n.scheduleStatus);
  if (n.scheduleAfter) p.set("after", n.scheduleAfter);
  return p.size ? `/?${p}` : "/";
}
