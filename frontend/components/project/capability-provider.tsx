"use client";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api } from "@/lib/project-api";
import type { EffectiveAccess, ProductAction } from "@web/capabilities";

const CapabilityContext = createContext<EffectiveAccess | null>(null);

// This projection controls affordances only. Every request is independently
// authorized by the product service, including requests from stale browser tabs.
export function CapabilityProvider({ accountId, children }: { accountId: string; children: ReactNode }) {
  const [access, setAccess] = useState<EffectiveAccess | null>(null);
  useEffect(() => {
    let gone = false;
    let generation = 0;
    const refresh = async () => {
      const current = ++generation;
      try {
        const value = await api<EffectiveAccess>("/api/me/capabilities");
        if (!gone && current === generation) setAccess(value.account_id === accountId ? value : null);
      } catch { if (!gone && current === generation) setAccess(null); }
    };
    const onVisible = () => { if (document.visibilityState === "visible") void refresh(); };
    void refresh();
    const timer = window.setInterval(onVisible, 15000);
    window.addEventListener("focus", onVisible);
    window.addEventListener("project-permission-denied", onVisible);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      gone = true;
      window.clearInterval(timer);
      window.removeEventListener("focus", onVisible);
      window.removeEventListener("project-permission-denied", onVisible);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [accountId]);
  return <CapabilityContext value={access?.account_id === accountId ? access : null}>{children}</CapabilityContext>;
}
export function useCapabilities() {
  const access = useContext(CapabilityContext);
  return (action: ProductAction) => access?.account_status === "active" && access.actions[action]?.allowed === true;
}
