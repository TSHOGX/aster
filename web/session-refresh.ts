"use client";
import { useEffect, useEffectEvent } from "react";
import { ACTIVE_APPEARANCE_KEY } from "./appearance";

// All applications revalidate with their own authority; browser events never
// grant access. Polling also covers expired cookies and storage-disabled tabs.
export function useSessionRefresh(refresh: () => void) {
  const revalidate = useEffectEvent(refresh);
  useEffect(() => {
    const changed = () => { if (!document.hidden) revalidate(); };
    const storage = (event: StorageEvent) => {
      if (event.key === ACTIVE_APPEARANCE_KEY || event.key === null) revalidate();
    };
    window.addEventListener("focus", changed);
    window.addEventListener("storage", storage);
    document.addEventListener("visibilitychange", changed);
    const timer = window.setInterval(changed, 30000);
    return () => {
      window.removeEventListener("focus", changed);
      window.removeEventListener("storage", storage);
      document.removeEventListener("visibilitychange", changed);
      window.clearInterval(timer);
    };
  }, []);
}
