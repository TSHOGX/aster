"use client";
import { intlLocale } from "@web/i18n/locale";

import { useEffect, useState } from "react";
import { api } from "@/lib/project-api";
import { useT } from "@/lib/i18n";

type Status = {
  labels?: Record<string, { version_label: string }>;
  state: string;
  updated_at: string;
  current: {
    key: string;
    destination: string;
    source: { version: string; artifact_id?: string };
  }[];
};

// Mounted only inside Settings > Advanced, never on a daily project surface.
export function EnvironmentStatus({ project }: { project: string }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [response, setResponse] = useState<{
    project: string;
    value?: Status;
    error?: boolean;
  }>();
  const value = response?.project === project ? response.value : undefined;
  const error = response?.project === project && response.error;
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    void api<Status>(
      `/api/me/project-configurations/${encodeURIComponent(project)}/environment`,
      { signal: controller.signal },
    )
      .then((value) => {
        if (!controller.signal.aborted) setResponse({ project, value });
      })
      .catch(() => {
        if (!controller.signal.aborted) setResponse({ project, error: true });
      });
    return () => controller.abort();
  }, [project, open, attempt]);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>{tr("Environment")}</summary>
      {open &&
        (error ? (
          <p role="alert">
            {tr("Environment details are unavailable.")}{" "}
            <button
              onClick={() => {
                setResponse(undefined);
                setAttempt((v) => v + 1);
              }}
            >
              {tr("Retry")}
            </button>
          </p>
        ) : !value ? (
          <p role="status">{tr("Loading…")}</p>
        ) : (
          <>
            <p>
              {tr(
                value.state === "current"
                  ? "Current environment is up to date."
                  : value.state === "fixed"
                    ? "This project retains the versions selected by your administrator."
                    : "This project keeps its current content until an update can be applied on a future start.",
              )}
            </p>
            <ul>
              {value.current.map((m) => (
                <li key={m.key}>
                  {m.destination} ·{" "}
                  {value.labels?.[`${m.source.artifact_id}@${m.source.version}`]
                    ?.version_label || m.source.version}
                </li>
              ))}
            </ul>
            {value.updated_at && (
              <p>
                {tr("Last checked")}:{" "}
                {new Date(value.updated_at).toLocaleString(intlLocale())}
              </p>
            )}
          </>
        ))}
    </details>
  );
}
