"use client";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { ResourceStore } from "@/lib/resource-store";
const Resources = createContext<ResourceStore | null>(null);
export function ResourceProvider({actor,children}:{actor:string;children:ReactNode}) {
  const [store] = useState(()=>new ResourceStore(actor));
  useEffect(()=> {
    const clear = () => store.clear();
    window.addEventListener("project-auth-expired",clear);
    return ()=> {window.removeEventListener("project-auth-expired",clear);store.clear();};
  },[store]);
  return <Resources.Provider value={store}>{children}</Resources.Provider>;
}
export function useResources() {
  const store=useContext(Resources);
  if(!store) throw new Error("Authenticated resources are unavailable");
  return store;
}
