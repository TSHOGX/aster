"use client";
import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { ResourceStore } from "@/lib/resource-store";
import { ProjectRealtime } from "@/lib/project-realtime";
const Resources = createContext<ResourceStore | null>(null);
const Realtime = createContext<ProjectRealtime | null>(null);
export function ResourceProvider({
  actor,
  children,
}: {
  actor: string;
  children: ReactNode;
}) {
  const [store] = useState(() => new ResourceStore(actor));
  const [realtime] = useState(() => new ProjectRealtime(store));
  useEffect(() => {
    const clear = () => {
      realtime.clear();
      store.clear();
    };
    window.addEventListener("project-auth-expired", clear);
    return () => {
      window.removeEventListener("project-auth-expired", clear);
      clear();
    };
  }, [store, realtime]);
  return (
    <Resources.Provider value={store}>
      <Realtime.Provider value={realtime}>{children}</Realtime.Provider>
    </Resources.Provider>
  );
}
export function useRealtime() {
  const realtime = useContext(Realtime);
  if (!realtime) throw new Error("Resource events are unavailable");
  return realtime;
}
export function useResources() {
  const store = useContext(Resources);
  if (!store) throw new Error("Authenticated resources are unavailable");
  return store;
}
