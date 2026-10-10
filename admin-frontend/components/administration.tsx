"use client";
import { useEffect, useState } from "react";
import { api, APIError } from "@/lib/api";
import type { Identity } from "@web/identity";
import { displayName } from "@web/brand";
export function Administration({ route }: { route: string[] }) {
  const [status, setStatus] = useState("loading");
  useEffect(() => {
    let active = true;
    void api<Identity>("/api/auth/session").then(account => {
      if (!active) return;
      if (account.must_change_password) { location.replace(`/?return_to=${encodeURIComponent(location.pathname + location.search)}`); return; }
      setStatus(account.role === "admin" ? "ready" : "denied");
    }).catch(error => {
      if (!active) return;
      if (error instanceof APIError && error.status === 401) { location.replace(`/?return_to=${encodeURIComponent(location.pathname + location.search)}`); return; }
      setStatus("unavailable");
    });
    return () => { active = false; };
  }, []);
  return <main><h1>{displayName} administration</h1>
    {status === "loading" && <p role="status">Opening administration…</p>}
    {status === "denied" && <><h2>Access denied</h2><p>A platform administrator role is required.</p><WorkbenchLink /></>}
    {status === "unavailable" && <p role="alert">Account access is unavailable. Reload to retry.</p>}
    {status === "ready" && <p>{route.length ? route.join(" / ") : "Administration"}</p>}
  </main>;
}

function WorkbenchLink() {
  return (
    // The workbench is a separate build; Next Link would add /admin to this URL.
    // eslint-disable-next-line @next/next/no-html-link-for-pages
    <a href="/">Return to workbench</a>
  );
}
