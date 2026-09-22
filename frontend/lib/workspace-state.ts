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
    activeSessionId: id(d.activeSessionId) || ids(d.agents).at(-1) || "",
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
};
export function readNavigation(search: string): Navigation {
  const p = new URLSearchParams(search),
    view = p.get("view");
  return {
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
  };
}
export function navigationURL(n: Navigation) {
  const p = new URLSearchParams();
  if (n.view !== "project") p.set("view", n.view);
  if (n.project) p.set("project", n.project);
  if (n.session) p.set("session", n.session);
  if (n.message) p.set("message", n.message);
  if (n.draft) p.set("draft", "1");
  if (n.history) p.set("history", "1");
  return p.size ? `/?${p}` : "/";
}
