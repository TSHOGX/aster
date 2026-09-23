"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  encode,
  projectURL,
  type AgentSession,
  type Project,
  type ProjectTerminal,
} from "@/lib/project-api";
import { useResources, useRealtime } from "./resource-provider";
import { mergePage } from "@/lib/workspace-state";

export function useWorkspaceData(
  active: string,
  expanded: string[],
  targetSession: string,
  onUnavailableProject: (project: string) => void,
) {
  const resources = useResources(),
    realtime = useRealtime();
  const abort = useRef(new AbortController());
  const read = useCallback(
    <T>(url: string) =>
      resources.read<T>(
        {
          project: decodeURIComponent(
            new URL(url, "http://resource").pathname.split("/")[3] || "",
          ),
          kind: "index",
          identity: url,
        },
        (signal) => api<T>(url, { signal }),
        abort.current.signal,
      ),
    [resources],
  );
  const [projects, setProjects] = useState<Project[]>([]);
  const [sessions, setSessions] = useState<Record<string, AgentSession[]>>({});
  const [terminals, setTerminals] = useState<Record<string, ProjectTerminal[]>>(
    {},
  );
  const [more, setMore] = useState<Record<string, boolean>>({});
  const [error, setError] = useState("");
  const selection = useRef({ active, targetSession });
  useEffect(() => {
    selection.current = { active, targetSession };
  }, [active, targetSession]);
  const projectDirty = useRef(0),
    childDirty = useRef<Record<string, number>>({});
  const projectFlight = useRef<Promise<void> | null>(null);
  const childFlights = useRef(
    new Map<string, { promise: Promise<void>; terminals: boolean }>(),
  );
  const pages = useRef<Record<string, number>>({});
  const serial = useRef<Record<string, number>>({});
  const unavailable = useRef(new Set<string>());
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    abort.current = new AbortController();
    return () => {
      alive.current = false;
      abort.current.abort();
    };
  }, []);
  const forgetProject = useCallback((project: string) => {
    unavailable.current.add(project);
    serial.current[project] = (serial.current[project] || 0) + 1;
    delete pages.current[project];
    delete childDirty.current[project];
    resources.clear(project);
    setProjects(old => old.filter(item => item.id !== project));
    setSessions(old => { const next = {...old}; delete next[project]; return next; });
    setTerminals(old => { const next = {...old}; delete next[project]; return next; });
    setMore(old => { const next = {...old}; delete next[project]; return next; });
    onUnavailableProject(project);
  }, [resources, onUnavailableProject]);
  const refreshProjects = useCallback(
    async function refreshProjects(loadMore = false): Promise<void> {
      if (projectFlight.current) {
        if (!loadMore) projectDirty.current++;
        await projectFlight.current;
        if (loadMore) return refreshProjects(true);
        return;
      }
      const generation = projectDirty.current;
      const flight = (async () => {
        const id = (serial.current.projects =
          (serial.current.projects || 0) + 1);
        const count = Math.max(
          1,
          (pages.current.projects || 1) + (loadMore ? 1 : 0),
        );
        try {
          let all: Project[] = [];
          let after = "";
          let hasMore = false;
          for (let n = 0; n < count; n++) {
            const data = await read<{ projects: Project[] }>(
              `/api/projects?limit=100&after=${encode(after)}`,
            );
            all = mergePage(all, data.projects);
            hasMore = data.projects.length === 100;
            after = data.projects.at(-1)?.id || "";
            if (!hasMore) break;
          }
          if (!alive.current || serial.current.projects !== id) return;
          if (
            selection.current.active &&
            !all.some((p) => p.id === selection.current.active)
          ) {
            const selectedProject = selection.current.active;
            try {
              const p = await read<Project>(projectURL(selectedProject));
              if (p.state !== "deleted") all = mergePage(all, [p]);
              else forgetProject(selectedProject);
            } catch (e) {
              if (alive.current && e instanceof APIError && e.status === 404)
                forgetProject(selectedProject);
            }
          }
          if (!alive.current || serial.current.projects !== id) return;
          for (const project of all) unavailable.current.delete(project.id);
          pages.current.projects = count;
          setProjects((prior) =>
            all.map(
              (p) =>
                prior.find((x) => x.id === p.id && x.revision > p.revision) ||
                p,
            ),
          );
          setMore((m) => ({ ...m, projects: hasMore }));
        } catch (e) {
          if (alive.current) setError(String(e));
        }
      })();
      projectFlight.current = flight;
      try {
        await flight;
      } finally {
        if (projectFlight.current === flight) projectFlight.current = null;
      }
      if (alive.current && generation !== projectDirty.current)
        return refreshProjects();
    },
    [read, forgetProject],
  );
  const refreshChildren = useCallback(
    async function refreshChildren(
      project: string,
      loadMore = false,
      includeTerminals = false,
    ): Promise<void> {
      if (unavailable.current.has(project)) return;
      const pending = childFlights.current.get(project);
      if (pending) {
        if (!loadMore)
          childDirty.current[project] = (childDirty.current[project] || 0) + 1;
        await pending.promise;
        if (loadMore || (includeTerminals && !pending.terminals))
          return refreshChildren(project, loadMore, includeTerminals);
        return;
      }
      const generation = childDirty.current[project] || 0;
      const flight = (async () => {
        const id = (serial.current[project] =
          (serial.current[project] || 0) + 1);
        const count = Math.max(
          1,
          (pages.current[project] || 1) + (loadMore ? 1 : 0),
        );
        try {
          let all: AgentSession[] = [];
          let after = "";
          let hasMore = false;
          for (let n = 0; n < count; n++) {
            const data = await read<{ sessions: AgentSession[] }>(
              `${projectURL(project)}/sessions?limit=100&after=${encode(after)}`,
            );
            all = mergePage(all, data.sessions);
            hasMore = data.sessions.length === 100;
            after = data.sessions.at(-1)?.id || "";
            if (!hasMore) break;
          }
          if (!alive.current || serial.current[project] !== id) return;
          if (
            selection.current.active === project &&
            selection.current.targetSession &&
            !all.some((s) => s.id === selection.current.targetSession)
          ) {
            try {
              const s = await read<AgentSession>(
                `${projectURL(project)}/sessions/${encode(selection.current.targetSession)}`,
              );
              all = mergePage(all, [s]);
            } catch {
              /* Deleted selection stays unavailable. */
            }
          }
          if (!alive.current || serial.current[project] !== id) return;
          pages.current[project] = count;
          setSessions((s) => ({
            ...s,
            [project]: all.map(
              (x) =>
                s[project]?.find(
                  (y) => y.id === x.id && y.revision > x.revision,
                ) || x,
            ),
          }));
          setMore((m) => ({ ...m, [project]: hasMore }));
          if (includeTerminals) {
            let list: ProjectTerminal[] = [];
            let cursor = "";
            for (;;) {
              const data = await read<{ terminals: ProjectTerminal[] }>(
                `${projectURL(project)}/terminals?limit=100&after=${encode(cursor)}`,
              );
              list = mergePage(list, data.terminals);
              if (data.terminals.length < 100) break;
              cursor = data.terminals.at(-1)!.id;
            }
            if (alive.current && serial.current[project] === id)
              setTerminals((t) => ({ ...t, [project]: list }));
          }
        } catch (e) {
          if (!alive.current) return;
          if (e instanceof APIError && e.status === 404) forgetProject(project);
          else if (!unavailable.current.has(project)) setError(String(e));
        }
      })();
      childFlights.current.set(project, {
        promise: flight,
        terminals: includeTerminals,
      });
      try {
        await flight;
      } finally {
        if (childFlights.current.get(project)?.promise === flight)
          childFlights.current.delete(project);
      }
      if (alive.current && generation !== (childDirty.current[project] || 0))
        return refreshChildren(project, false, includeTerminals);
    },
    [read, forgetProject],
  );
  const ensureTarget = useCallback(
    async (project: string, session = "") => {
      try {
        const p = await read<Project>(projectURL(project));
        if (!alive.current) return;
        if (p.state === "deleted") throw new APIError(404, "Project unavailable");
        unavailable.current.delete(project);
        setProjects((old) => mergePage(old, [p]));
        if (session) {
          try {
            const s = await read<AgentSession>(
              `${projectURL(project)}/sessions/${encode(session)}`,
            );
            if (alive.current)
              setSessions((old) => ({
                ...old,
                [project]: mergePage(old[project] || [], [s]),
              }));
          } catch (e) {
            if (!(e instanceof APIError && e.status === 404)) throw e;
            // An owned Project with a missing child is not a shared conversation.
            if (alive.current)
              setSessions((old) => ({
                ...old,
                [project]: (old[project] || []).filter((s) => s.id !== session),
              }));
          }
        }
      } catch (e) {
        if (!alive.current) throw e;
        if (e instanceof APIError && e.status === 404) {
          forgetProject(project);
        } else if (unavailable.current.has(project)) {
          // A concurrent index read already confirmed this Project is gone
          // and canceled its other reads. Keep that navigation result a 404.
          throw new APIError(404, "Project unavailable");
        } else setError(String(e));
        throw e;
      }
    },
    [read, forgetProject],
  );
  const refresh = useCallback(async () => {
    await Promise.all([
      refreshProjects(),
      active ? refreshChildren(active, false, true) : Promise.resolve(),
    ]);
  }, [active, refreshChildren, refreshProjects]);
  useEffect(() => {
    void refreshProjects();
  }, [refreshProjects]);
  const expandedKey = expanded
    .filter((p) => p !== active)
    .sort()
    .join(",");
  useEffect(() => {
    for (const id of expandedKey.split(",").filter(Boolean))
      void refreshChildren(id);
  }, [expandedKey, refreshChildren]);
  useEffect(() => {
    if (!active) return;
    void refreshChildren(active, false, true);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let live = false;
    const pending = new Map<string, { kind: string; id: string }>();
    const updateSession = async (id: string) => {
      try {
        const child = await read<AgentSession>(
          `${projectURL(active)}/sessions/${encode(id)}`,
        );
        if (alive.current)
          setSessions((old) => ({
            ...old,
            [active]: mergePage(old[active] || [], [child]),
          }));
      } catch (e) {
        if (e instanceof APIError && e.status === 404 && alive.current)
          setSessions((old) => ({
            ...old,
            [active]: (old[active] || []).filter((s) => s.id !== id),
          }));
        else if (alive.current) setError(String(e));
      }
    };
    const updateTerminal = async (id: string) => {
      try {
        const terminal = await read<ProjectTerminal>(
          `${projectURL(active)}/terminals/${encode(id)}`,
        );
        if (alive.current)
          setTerminals((old) => ({
            ...old,
            [active]: mergePage(old[active] || [], [terminal]),
          }));
      } catch (e) {
        if (e instanceof APIError && e.status === 404 && alive.current)
          setTerminals((old) => ({
            ...old,
            [active]: (old[active] || []).filter((t) => t.id !== id),
          }));
        else if (alive.current) setError(String(e));
      }
    };
    const flush = () => {
      timer = undefined;
      const events = [...pending.values()];
      pending.clear();
      if (events.some((e) => e.kind === "resync")) {
        void refreshProjects();
        void refreshChildren(active, false, true);
        return;
      }
      for (const e of events) {
        if (["sessions", "control"].includes(e.kind) && e.id)
          void updateSession(e.id);
        else if (e.kind === "terminals" && e.id) void updateTerminal(e.id);
        else if (e.kind === "project") void refreshProjects();
      }
    };
    const unsubscribe = realtime.subscribe(
      active,
      (event) => {
        if (event.resource_kind === "closed") {
          resources.clear(active);
          setSessions((old) => ({ ...old, [active]: [] }));
          setTerminals((old) => ({ ...old, [active]: [] }));
          void refreshProjects();
          return;
        }
        if (
          !["resync", "sessions", "control", "terminals", "project"].includes(
            event.resource_kind,
          )
        )
          return;
        if (pending.size >= 128) {
          pending.clear();
          pending.set("resync", { kind: "resync", id: "" });
        } else
          pending.set(event.resource_kind + "/" + (event.resource_id || ""), {
            kind: event.resource_kind,
            id: event.resource_id || "",
          });
        if (!timer) timer = setTimeout(flush, 100);
      },
      (state) => {
        live = state === "live";
      },
    );
    // Light fallback exists only while disconnected. Healthy idle does no list polling.
    const fallback = setInterval(() => {
      if (!live && !document.hidden) {
        void refreshProjects();
        void refreshChildren(active, false, true);
      }
    }, 30000);
    return () => {
      unsubscribe();
      clearTimeout(timer);
      clearInterval(fallback);
    };
  }, [active, realtime, resources, read, refreshChildren, refreshProjects]);
  return {
    projects,
    sessions,
    terminals,
    more,
    error,
    setError,
    refresh,
    refreshProjects,
    refreshChildren,
    ensureTarget,
  };
}
