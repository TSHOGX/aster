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
  type Projection,
} from "@/lib/project-api";
import { mergePage } from "@/lib/workspace-state";

// Bound list traffic across all expanded projects, including slow responses.
function limiter() {
  let running = 0;
  const queue: (() => void)[] = [];
  return async <T>(fn: () => Promise<T>) => {
    if (running >= 4) await new Promise<void>((r) => queue.push(r));
    else running++;
    try {
      return await fn();
    } finally {
      const next = queue.shift();
      if (next) next();
      else running--;
    }
  };
}
export function useWorkspaceData(
  active: string,
  expanded: string[],
  targetSession: string,
) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [sessions, setSessions] = useState<Record<string, AgentSession[]>>({});
  const [terminals, setTerminals] = useState<Record<string, ProjectTerminal[]>>(
    {},
  );
  const [more, setMore] = useState<Record<string, boolean>>({});
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const selection = useRef({ active, targetSession });
  useEffect(() => {
    selection.current = { active, targetSession };
  }, [active, targetSession]);
  const projectFlight = useRef<Promise<void> | null>(null);
  const childFlights = useRef(
    new Map<string, { promise: Promise<void>; terminals: boolean }>(),
  );
  const pages = useRef<Record<string, number>>({});
  const serial = useRef<Record<string, number>>({});
  const limited = useRef(limiter());
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const refreshProjects = useCallback(async function refreshProjects(
    loadMore = false,
  ): Promise<void> {
    if (projectFlight.current) {
      await projectFlight.current;
      if (loadMore) return refreshProjects(true);
      return;
    }
    const flight = (async () => {
      const id = (serial.current.projects = (serial.current.projects || 0) + 1);
      const count = Math.max(
        1,
        (pages.current.projects || 1) + (loadMore ? 1 : 0),
      );
      try {
        let all: Project[] = [];
        let after = "";
        let hasMore = false;
        for (let n = 0; n < count; n++) {
          const data = await limited.current(() =>
            api<{ projects: Project[] }>(
              `/api/projects?limit=100&after=${encode(after)}`,
            ),
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
          try {
            const p = await limited.current(() =>
              api<Project>(projectURL(selection.current.active)),
            );
            if (p.state !== "deleted") all = mergePage(all, [p]);
          } catch {
            /* Missing selection stays unavailable. */
          }
        }
        if (!alive.current || serial.current.projects !== id) return;
        pages.current.projects = count;
        setProjects((prior) =>
          all.map(
            (p) =>
              prior.find((x) => x.id === p.id && x.revision > p.revision) || p,
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
  }, []);
  const refreshChildren = useCallback(async function refreshChildren(
    project: string,
    loadMore = false,
    includeTerminals = false,
  ): Promise<void> {
    const pending = childFlights.current.get(project);
    if (pending) {
      await pending.promise;
      if (loadMore || (includeTerminals && !pending.terminals))
        return refreshChildren(project, loadMore, includeTerminals);
      return;
    }
    const flight = (async () => {
      const id = (serial.current[project] = (serial.current[project] || 0) + 1);
      const count = Math.max(
        1,
        (pages.current[project] || 1) + (loadMore ? 1 : 0),
      );
      try {
        let all: AgentSession[] = [];
        let after = "";
        let hasMore = false;
        for (let n = 0; n < count; n++) {
          const data = await limited.current(() =>
            api<{ sessions: AgentSession[] }>(
              `${projectURL(project)}/sessions?limit=100&after=${encode(after)}`,
            ),
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
            const s = await limited.current(() =>
              api<AgentSession>(
                `${projectURL(project)}/sessions/${encode(selection.current.targetSession)}`,
              ),
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
            const data = await limited.current(() =>
              api<{ terminals: ProjectTerminal[] }>(
                `${projectURL(project)}/terminals?limit=100&after=${encode(cursor)}`,
              ),
            );
            list = mergePage(list, data.terminals);
            if (data.terminals.length < 100) break;
            cursor = data.terminals.at(-1)!.id;
          }
          if (alive.current && serial.current[project] === id)
            setTerminals((t) => ({ ...t, [project]: list }));
        }
      } catch (e) {
        if (alive.current) setError(String(e));
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
  }, []);
  const ensureTarget = useCallback(async (project: string, session = "") => {
    try {
      const p = await api<Project>(projectURL(project));
      if (!alive.current) return;
      setProjects((old) => mergePage(old, [p]));
      if (session) {
        try {
          const s = await api<AgentSession>(
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
      if (alive.current) setError(String(e));
      throw e;
    }
  }, []);
  const refresh = useCallback(async () => {
    await Promise.all([
      refreshProjects(),
      active ? refreshChildren(active, false, true) : Promise.resolve(),
    ]);
    if (alive.current) setTick((n) => n + 1);
  }, [active, refreshChildren, refreshProjects]);
  useEffect(() => {
    void refreshProjects();
    const timer = setInterval(() => {
      if (!document.hidden) void refreshProjects();
    }, 10000);
    return () => clearInterval(timer);
  }, [refreshProjects]);
  const expandedKey = expanded
    .filter((p) => p !== active)
    .sort()
    .join(",");
  useEffect(() => {
    const ids = expandedKey.split(",").filter(Boolean);
    const run = () => {
      if (!document.hidden) for (const id of ids) void refreshChildren(id);
    };
    run();
    const timer = setInterval(run, 10000);
    return () => clearInterval(timer);
  }, [expandedKey, refreshChildren]);
  useEffect(() => {
    if (!active) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const update = () => {
      if (!document.hidden)
        void refreshChildren(active, false, true).then(() => {
          if (alive.current) setTick((n) => n + 1);
        });
    };
    update();
    const interval = setInterval(update, 2500);
    const source = new EventSource(`${projectURL(active)}/events`);
    const invalidate = () => {
      if (!timer)
        timer = setTimeout(() => {
          timer = undefined;
          update();
          void refreshProjects();
        }, 100);
    };
    source.addEventListener("projection", (event) => {
      const data = JSON.parse((event as MessageEvent).data) as Projection;
      if (data.project_id === active) invalidate();
    });
    source.addEventListener("snapshot", invalidate);
    source.addEventListener("gap", invalidate);
    source.onopen = invalidate;
    source.addEventListener("closed", () => {
      source.close();
      invalidate();
    });
    document.addEventListener("visibilitychange", update);
    return () => {
      source.close();
      clearInterval(interval);
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [active, refreshChildren, refreshProjects]);
  return {
    projects,
    sessions,
    terminals,
    more,
    error,
    setError,
    tick,
    refresh,
    refreshProjects,
    refreshChildren,
    ensureTarget,
  };
}
