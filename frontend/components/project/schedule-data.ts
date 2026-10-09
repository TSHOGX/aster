"use client";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import {
  ScheduleWindow,
  windowRows,
  type WindowPage,
} from "@/lib/schedule-window";
import { useResources } from "./resource-provider";
import { api } from "@/lib/project-api";

export function useScheduleWindow<
  T extends { id: string; revision?: number },
  P,
>(path: string, decode: (page: P) => WindowPage<T>) {
  const resources = useResources();
  const controller = useMemo(
    () =>
      new ScheduleWindow<T>(
        (params, signal) => {
          const join = path.includes("?") ? "&" : "?";
          return resources
            .read(
              {
                project: "",
                kind: "index",
                identity: `${path}${join}${params}`,
              },
              (signal) =>
                api<P>(`${path}${join}${params}&limit=50`, { signal }),
              signal,
            )
            .then(decode);
        },
        resources.peek<WindowPage<T>[]>({
          project: "",
          kind: "index",
          identity: `schedule-window:${path}`,
        }),
      ),
    [path, decode, resources],
  );
  const snapshot = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
    controller.getSnapshot,
  );
  useEffect(() => {
    const key = {
      project: "",
      kind: "index" as const,
      identity: `schedule-window:${path}`,
    };
    const release = resources.pin(key);
    const unsubscribe = controller.subscribe(() => {
      const current = controller.getSnapshot();
      if (current.pages.length) resources.put(key, current.pages);
      else resources.forget(key);
    });
    controller.resume();
    void controller.refresh();
    return () => {
      controller.dispose();
      unsubscribe();
      release();
    };
  }, [controller, path, resources]);
  return {
    ...snapshot,
    rows: windowRows(snapshot.pages),
    controller,
    next: snapshot.pages.at(-1)?.next,
    previous: snapshot.pages[0]?.previous,
  };
}
