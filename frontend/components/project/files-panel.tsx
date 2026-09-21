"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  command,
  encode,
  fileURL,
  newID,
  projectURL,
  readPreview,
  upload,
  type FileEntry,
  type FilePage,
  type Project,
} from "@/lib/project-api";
import { FilePreview } from "./file-preview";

export function FilesPanel({
  project,
  selected,
  onSelect,
}: {
  project: Project;
  selected: string;
  onSelect: (path: string) => void;
}) {
  const [directory, setDirectory] = useState("");
  const [after, setAfter] = useState("");
  const [page, setPage] = useState<FilePage>({ entries: [] });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [edit, setEdit] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const [uncertain, setUncertain] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);
  const serial = useRef(0);
  const base = `${projectURL(project.id)}/files`;
  const invalidateRequest = useCallback(() => {
    serial.current++;
  }, []);
  const refresh = useCallback(async () => {
    const id = ++serial.current;
    try {
      if (project.state !== "active") return;
      const page = await api<FilePage>(
        `${base}?path=${encode(directory)}&after=${encode(after)}&limit=100`,
      );
      if (id === serial.current) {
        setPage(page);
        setError("");
      }
    } catch (e) {
      if (id === serial.current)
        setError(e instanceof Error ? e.message : "Files are unavailable.");
    }
  }, [base, directory, after, project.state]);
  useEffect(() => {
    void Promise.resolve().then(refresh);
    return invalidateRequest;
  }, [refresh, invalidateRequest]);
  useEffect(() => {
    if (project.state !== "active") return;
    const source = new EventSource(`${base}/watch`);
    let timer: ReturnType<typeof setTimeout> | null = null;
    const invalidate = () => {
      if (!timer)
        timer = setTimeout(() => {
          timer = null;
          void refresh();
        }, 400);
    };
    source.addEventListener("invalidate", invalidate);
    source.onopen = invalidate;
    source.onerror = invalidate;
    return () => {
      source.close();
      if (timer) clearTimeout(timer);
    };
  }, [base, project.state, refresh]);
  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      setVersion((n) => n + 1);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "File operation failed.");
      if (e instanceof APIError && e.outcome?.state === "unknown")
        setUncertain(e.outcome.id || "unknown");
    } finally {
      setBusy(false);
    }
  };
  const move = (name: string) => {
    const destination = window.prompt("Move to Project path", name);
    if (destination && destination !== name)
      void act(() =>
        command(base, {
          id: newID(),
          operation: "move",
          path: name,
          destination,
        }),
      );
  };
  const remove = (name: string) => {
    if (window.confirm(`Remove ${name}? Directories must be empty.`))
      void act(async () => {
        await command(base, { id: newID(), operation: "remove", path: name });
        if (selected === name) onSelect("");
      });
  };
  const open = (entry: FileEntry) => {
    const path = directory ? `${directory}/${entry.name}` : entry.name;
    if (entry.kind === "directory") {
      setDirectory(path);
      setAfter("");
    } else if (entry.kind === "file") {
      setEdit(null);
      onSelect(path);
    }
  };
  const editFile = () =>
    void act(async () => {
      const abort = new AbortController();
      const blob = await readPreview(
        fileURL(project.id, selected),
        abort.signal,
        1 << 20,
      );
      setEdit(await blob.text());
    });
  return (
    <aside
      className={`files-panel ${selected ? "previewing" : ""}`}
      aria-label="Project Files"
    >
      <header className="panel-heading">
        <strong>Project Files</strong>
        <div className="row-actions">
          <button
            disabled={busy || project.state !== "active"}
            onClick={() => void refresh()}
          >
            Refresh
          </button>
          <button
            disabled={busy || project.state !== "active"}
            onClick={() => fileInput.current?.click()}
          >
            Upload
          </button>
          <button
            disabled={busy || project.state !== "active"}
            onClick={() => {
              const name = window.prompt("New folder name");
              if (name)
                void act(() =>
                  command(base, {
                    id: newID(),
                    operation: "mkdir",
                    path: directory ? `${directory}/${name}` : name,
                  }),
                );
            }}
          >
            New folder
          </button>
        </div>
      </header>
      <input
        hidden
        multiple
        type="file"
        ref={fileInput}
        onChange={(e) => {
          const files = Array.from(e.target.files || []);
          e.target.value = "";
          void act(async () => {
            for (const file of files)
              await upload(
                project.id,
                directory ? `${directory}/${file.name}` : file.name,
                file,
              );
          });
        }}
      />
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      {uncertain && (
        <div className="notice warning">
          Operation {uncertain} has an uncertain outcome. Inspect current file
          contents before a new write.{" "}
          <button
            onClick={() => {
              setUncertain("");
              void refresh();
            }}
          >
            Inspect files
          </button>
        </div>
      )}
      {project.state !== "active" ? (
        <div className="empty-state">
          <p>The workspace is {project.state}.</p>
          <button
            className="primary"
            disabled={busy}
            onClick={() =>
              void act(() => command(`${projectURL(project.id)}/resume`))
            }
          >
            Resume Project
          </button>
        </div>
      ) : (
        <>
          <nav className="file-breadcrumb">
            <button
              onClick={() => {
                setDirectory("");
                setAfter("");
              }}
            >
              workspace
            </button>
            {directory
              .split("/")
              .filter(Boolean)
              .map((part, i, parts) => (
                <button
                  key={i}
                  onClick={() => {
                    setDirectory(parts.slice(0, i + 1).join("/"));
                    setAfter("");
                  }}
                >
                  {part}
                </button>
              ))}
          </nav>
          <div className="file-list">
            {page.entries.map((entry) => {
              const name = directory
                ? `${directory}/${entry.name}`
                : entry.name;
              return (
                <div className="file-row" key={name}>
                  <button
                    className="file-name"
                    onClick={() => open(entry)}
                    title={
                      entry.kind === "symlink"
                        ? "Symlinks are not followed"
                        : name
                    }
                  >
                    <span>
                      {entry.kind === "directory"
                        ? "▸"
                        : entry.kind === "symlink"
                          ? "↗"
                          : "·"}
                    </span>
                    {entry.name}
                  </button>
                  <small>
                    {entry.kind === "file"
                      ? `${Math.ceil(entry.size / 1024)} KB`
                      : entry.kind}
                  </small>
                  <button
                    aria-label={`Move ${entry.name}`}
                    onClick={() => move(name)}
                  >
                    Move
                  </button>
                  <button
                    aria-label={`Remove ${entry.name}`}
                    onClick={() => remove(name)}
                  >
                    ×
                  </button>
                </div>
              );
            })}
            {!page.entries.length && (
              <p className="notice">This directory is empty.</p>
            )}
            {after && <button onClick={() => setAfter("")}>First page</button>}
            {page.next && (
              <button onClick={() => setAfter(page.next || "")}>
                Next files
              </button>
            )}
          </div>
        </>
      )}
      {selected && (
        <section className="selected-file" aria-label={`File ${selected}`}>
          <header className="panel-heading">
            <strong title={selected}>{selected}</strong>
            <div className="row-actions">
              <a href={fileURL(project.id, selected)} download>
                Download
              </a>
              <button
                disabled={busy || project.state !== "active"}
                onClick={editFile}
              >
                Edit text
              </button>
              <button
                onClick={() => {
                  onSelect("");
                  setEdit(null);
                }}
              >
                Close file
              </button>
            </div>
          </header>
          {edit !== null ? (
            <>
              <textarea
                className="file-editor"
                aria-label={`Edit ${selected}`}
                value={edit}
                onChange={(e) => setEdit(e.target.value)}
                maxLength={1 << 20}
                spellCheck={false}
              />
              <div className="row-actions">
                <button onClick={() => setEdit(null)}>Cancel edit</button>
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() =>
                    void act(async () => {
                      await upload(project.id, selected, new Blob([edit]));
                      setEdit(null);
                    })
                  }
                >
                  Save file
                </button>
              </div>
            </>
          ) : (
            <FilePreview
              key={`${selected}:${version}`}
              path={selected}
              href={fileURL(project.id, selected)}
            />
          )}
        </section>
      )}
    </aside>
  );
}
