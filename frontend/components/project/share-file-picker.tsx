"use client";
import { useEffect, useState } from "react";
import { api, encode, projectURL, type FilePage } from "@/lib/project-api";
import { useT } from "@/lib/i18n";

export function ShareFilePicker({
  project,
  value,
  onChange,
  disabled = false,
  maxCount = 100,
}: {
  project: string;
  value: string[];
  onChange: (paths: string[]) => void;
  disabled?: boolean;
  maxCount?: number;
}) {
  const tr = useT();
  const [directory, setDirectory] = useState("");
  const [after, setAfter] = useState("");
  const [page, setPage] = useState<FilePage>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    void api<FilePage>(
      `${projectURL(project)}/files?path=${encode(directory)}&after=${encode(after)}&limit=50`,
      { signal: abort.signal },
    )
      .then((result) => {
        if (!abort.signal.aborted) setPage(result);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [project, directory, after, attempt]);
  const enter = (path: string) => {
    setPage(undefined);
    setError("");
    setDirectory(path);
    setAfter("");
  };
  return (
    <fieldset disabled={disabled} className="share-file-picker">
      <legend>
        {tr("Choose files")} · {value.length}/{maxCount}
      </legend>
      <div className="row-actions">
        <button
          type="button"
          disabled={!directory}
          onClick={() => enter(directory.split("/").slice(0, -1).join("/"))}
        >
          ↑
        </button>
        <span>{directory || "/"}</span>
      </div>
      {error && (
        <p role="alert">
          {tr(error)}
          <button type="button" onClick={() => setAttempt((n) => n + 1)}>
            {tr("Retry")}
          </button>
        </p>
      )}
      {!page && !error && <p role="status">{tr("Loading…")}</p>}
      {page?.entries.map((entry) => {
        const path = [directory, entry.name].filter(Boolean).join("/");
        return entry.kind === "directory" ? (
          <button type="button" key={path} onClick={() => enter(path)}>
            {entry.name}/
          </button>
        ) : entry.kind === "file" ? (
          <label key={path}>
            <input
              type="checkbox"
              disabled={!value.includes(path) && value.length >= maxCount}
              checked={value.includes(path)}
              onChange={(e) =>
                onChange(
                  e.target.checked
                    ? [...value, path]
                    : value.filter((p) => p !== path),
                )
              }
            />
            {entry.name}
          </label>
        ) : null;
      })}
      {page?.next && (
        <button
          type="button"
          onClick={() => {
            setAfter(page.next!);
            setPage(undefined);
            setError("");
          }}
        >
          {tr("Show more")}
        </button>
      )}
      {!!value.length && (
        <ul>
          {value.map((path) => (
            <li key={path}>
              {path}{" "}
              <button
                type="button"
                onClick={() => onChange(value.filter((p) => p !== path))}
                aria-label={`${tr("Close")} ${path}`}
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
    </fieldset>
  );
}
