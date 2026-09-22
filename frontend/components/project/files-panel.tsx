"use client";
import dynamic from "next/dynamic";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type RefObject,
} from "react";
import {
  ChevronDown,
  ChevronRight,
  File,
  Folder,
  FolderOpen,
  Maximize2,
  Minimize2,
  PanelRight,
  X,
} from "lucide-react";
import { useT } from "@/lib/i18n";
import { classifyArtifact } from "@/lib/mime";
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
import { fileImpact, type FileInvalidation } from "@/lib/file-invalidation";
import { useResources, useRealtime } from "./resource-provider";
import { FilePreview } from "./file-preview";
import { IconButton, Menu, Splitter, useDialog } from "./ui";
const CodeEditor = dynamic(
  () =>
    import("@/components/workbench/viewers/code-editor").then(
      (m) => m.CodeEditor,
    ),
  { ssr: false },
);
type Directory = FilePage & { error?: string; loading?: boolean };
export function FilesPanel({
  project,
  selected,
  onSelect,
  visible = true,
  fullscreen = false,
  onFullscreen = () => {},
  onClose = () => {},
  treeWidth = 240,
  onTreeWidth = () => {},
  directories = [""],
  onDirectories = () => {},
  guard,
  fullButton,
}: {
  project: Project;
  selected: string;
  onSelect: (path: string) => void;
  visible?: boolean;
  fullscreen?: boolean;
  onFullscreen?: (b: boolean) => void;
  onClose?: () => void;
  treeWidth?: number;
  onTreeWidth?: (n: number) => void;
  directories?: string[];
  onDirectories?: (d: string[]) => void;
  guard?: RefObject<() => Promise<boolean>>;
  fullButton?: RefObject<HTMLButtonElement | null>;
}) {
  const resources = useResources();
  const realtime = useRealtime();
  const reads = useRef(new AbortController());
  const loads = useRef(
    new Map<
      string,
      { after: string; promise: Promise<FilePage | undefined> }
    >(),
  );
  const treeKey = useMemo(
    () => ({
      project: project.id,
      kind: "directory" as const,
      identity: "directory-tree",
    }),
    [project.id],
  );
  const tr = useT(),
    ask = useDialog();
  const [tree, setTree] = useState<Record<string, Directory>>(
      () => resources.peek(treeKey) || {},
    ),
    [treeOpen, setTreeOpen] = useState(true),
    [filter, setFilter] = useState("");
  const [denied, setDenied] = useState(false);
  const [watchHealth, setWatchHealth] = useState<
    "live" | "reconnecting" | "stale"
  >("live");
  const originalRef = useRef("");
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [edit, setEdit] = useState<string | null>(null),
    [original, setOriginal] = useState(""),
    [version, setVersion] = useState(0),
    [changed, setChanged] = useState(false),
    [uncertain, setUncertain] = useState("");
  const fileInput = useRef<HTMLInputElement>(null),
    uploadDirectory = useRef(""),
    serial = useRef<Record<string, number>>({}),
    alive = useRef(true),
    editor = useRef(edit),
    expanded = useRef(directories),
    selectedRef = useRef(selected),
    treeRef = useRef(tree);
  const base = `${projectURL(project.id)}/files`,
    dirty = edit !== null && edit !== original;
  useEffect(() => {
    editor.current = edit;
    originalRef.current = original;
    expanded.current = directories;
    selectedRef.current = selected;
    treeRef.current = tree;
  }, [edit, original, directories, selected, tree]);
  useEffect(() => {
    alive.current = true;
    reads.current = new AbortController();
    return () => {
      alive.current = false;
      reads.current.abort();
    };
  }, []);
  const readDirectory = useCallback(
    (url: string) =>
      resources.read<FilePage>(
        { project: project.id, kind: "directory", identity: url },
        (signal) => api<FilePage>(url, { signal }),
        reads.current.signal,
      ),
    [resources, project.id],
  );
  useEffect(() => resources.pin(treeKey), [resources, treeKey]);
  useEffect(() => {
    resources.put(treeKey, tree);
  }, [resources, treeKey, tree]);
  const load = useCallback(
    async function load(
      path: string,
      after = "",
    ): Promise<FilePage | undefined> {
      const pending = loads.current.get(path);
      if (pending) {
        if (pending.after === after) return pending.promise;
        await pending.promise;
        if (!alive.current) return;
        return load(path, after);
      }
      const flight = (async () => {
        const id = (serial.current[path] = (serial.current[path] || 0) + 1);
        setTree((t) => ({
          ...t,
          [path]: {
            entries: t[path]?.entries || [],
            next: t[path]?.next,
            loading: true,
          },
        }));
        try {
          let append = !!after;
          let page: FilePage;
          try {
            page = await readDirectory(
              `${base}?path=${encode(path)}&after=${encode(after)}&limit=100`,
            );
          } catch (error) {
            if (!after || !(error instanceof APIError) || error.status !== 409)
              throw error;
            page = await readDirectory(
              `${base}?path=${encode(path)}&after=&limit=100`,
            );
            append = false;
          }
          if (!append) {
            const retain =
              (treeRef.current[path]?.entries.length || 0) + (after ? 100 : 0);
            while (page.next && page.entries.length < retain) {
              const next = await readDirectory(
                `${base}?path=${encode(path)}&after=${encode(page.next)}&limit=100`,
              );
              page = { ...next, entries: [...page.entries, ...next.entries] };
            }
          }
          if (!alive.current || serial.current[path] !== id) return;
          setTree((t) => ({
            ...t,
            [path]: {
              ...page,
              entries: append
                ? [
                    ...new Map(
                      [...(t[path]?.entries || []), ...page.entries].map(
                        (e) => [e.name, e],
                      ),
                    ).values(),
                  ]
                : page.entries,
            },
          }));
          return page;
        } catch (e) {
          if (alive.current && serial.current[path] === id)
            setTree((t) => ({
              ...t,
              [path]: { entries: t[path]?.entries || [], error: String(e) },
            }));
        }
      })();
      loads.current.set(path, { after, promise: flight });
      try {
        return await flight;
      } finally {
        if (loads.current.get(path)?.promise === flight)
          loads.current.delete(path);
      }
    },
    [base, readDirectory],
  );
  const dirsKey = directories.join("\n");
  useEffect(() => {
    if (!visible || project.state !== "active") return;
    for (const d of new Set(["", ...dirsKey.split("\n")]))
      if (!treeRef.current[d]) void load(d);
  }, [visible, project.state, dirsKey, load]);
  const validateSelected = useCallback(async () => {
    const path = selectedRef.current;
    if (!path) return true;
    if (editor.current === null) {
      setVersion((n) => n + 1);
      return true;
    }
    try {
      const blob = await resources.read(
        {
          project: project.id,
          kind: "file",
          identity: fileURL(project.id, path),
        },
        (signal) => readPreview(fileURL(project.id, path), signal, 1 << 20),
        reads.current.signal,
        0,
      );
      const text = await blob.text();
      if (
        alive.current &&
        selectedRef.current === path &&
        editor.current !== null
      )
        setChanged(text !== originalRef.current);
      return true;
    } catch {
      if (alive.current && selectedRef.current === path)
        setWatchHealth("stale");
      return false;
    }
  }, [resources, project.id]);
  useEffect(() => {
    if (!visible || project.state !== "active") return;
    let live = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let healthTimer: ReturnType<typeof setTimeout> | undefined;
    let delayedTimer: ReturnType<typeof setTimeout> | undefined;
    const dirtyDirectories = new Set<string>();
    let selectedDirty = false;
    const invalidate = (event: FileInvalidation) => {
      const impact = fileImpact(event, selectedRef.current, [
        "",
        ...expanded.current,
      ]);
      for (const dir of impact.directories) {
        if (dirtyDirectories.size >= 64) {
          dirtyDirectories.clear();
          dirtyDirectories.add("");
          break;
        }
        dirtyDirectories.add(dir);
      }
      selectedDirty ||= impact.selected;
      resources.invalidate(
        (k) =>
          k.project === project.id &&
          ((k.kind === "directory" &&
            impact.directories.some(
              (dir) =>
                new URL(k.identity, location.origin).searchParams.get(
                  "path",
                ) === dir,
            )) ||
            (k.kind === "file" &&
              impact.selected &&
              k.identity === fileURL(project.id, selectedRef.current))),
      );
      if (
        impact.gap &&
        event.reason !== "attachment" &&
        event.reason !== "initial"
      )
        setWatchHealth("stale");
      if (!timer)
        timer = setTimeout(() => {
          timer = undefined;
          const dirs = [...dirtyDirectories];
          dirtyDirectories.clear();
          const check = selectedDirty;
          selectedDirty = false;
          void Promise.all([
            ...dirs.map((dir) => load(dir)),
            ...(check ? [validateSelected()] : []),
          ]).then((results) => {
            if (alive.current && live)
              setWatchHealth(results.every(Boolean) ? "live" : "stale");
          });
        }, 100);
    };
    const unsubscribe = realtime.subscribe(
      project.id,
      (event) => {
        if (event.resource_kind === "files")
          invalidate(event.payload as FileInvalidation);
        else if (event.resource_kind === "resync")
          invalidate({ reset: true, reason: event.reason });
        else if (event.resource_kind === "closed") {
          setDenied(true);
          setTree({});
          setWatchHealth("stale");
        }
      },
      (state) => {
        live = state === "live";
        if (live) {
          clearTimeout(healthTimer);
          clearTimeout(delayedTimer);
          healthTimer = undefined;
          delayedTimer = undefined;
          setWatchHealth("live");
        } else {
          if (!healthTimer)
            healthTimer = setTimeout(
              () => setWatchHealth("reconnecting"),
              5000,
            );
          if (!delayedTimer)
            delayedTimer = setTimeout(() => setWatchHealth("stale"), 15000);
        }
      },
    );
    return () => {
      unsubscribe();
      clearTimeout(timer);
      clearTimeout(healthTimer);
      clearTimeout(delayedTimer);
    };
  }, [
    base,
    load,
    validateSelected,
    visible,
    project.state,
    project.id,
    resources,
    realtime,
  ]);
  // Reveal deep links without walking unrelated directories. Continue paging each ancestor until found.
  useEffect(() => {
    if (!selected || !visible || project.state !== "active") return;
    let gone = false;
    const parts = selected.split("/");
    const parents = [""];
    for (let i = 1; i < parts.length; i++)
      parents.push(parts.slice(0, i).join("/"));
    onDirectories([...new Set([...expanded.current, ...parents])]);
    void (async () => {
      for (let i = 0; i < parents.length && !gone; i++) {
        const dir = parents[i];
        let page: FilePage | undefined = treeRef.current[dir];
        if (!page) page = await load(dir);
        while (
          !gone &&
          page &&
          !page.entries.some((e) => e.name === parts[i]) &&
          page.next
        )
          page = await load(dir, page.next);
      }
      if (!gone)
        requestAnimationFrame(() =>
          document
            .querySelector(`[data-file-path="${CSS.escape(selected)}"]`)
            ?.scrollIntoView({ block: "nearest" }),
        );
    })();
    return () => {
      gone = true;
    };
    // onDirectories updates layout preferences; only a changed path should reveal again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected, visible, load, project.state]);
  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      for (const d of new Set(["", ...directories])) void load(d);
      return true;
    } catch (e) {
      setError(String(e));
      if (e instanceof APIError && e.outcome?.state === "unknown")
        setUncertain(e.outcome.id || "unknown");
      return false;
    } finally {
      setBusy(false);
    }
  };
  const save = async () => {
    if (edit === null) return true;
    const submitted = edit;
    let savedCurrentDraft = false;
    const success = await act(async () => {
      await upload(project.id, selected, new Blob([submitted]));
      setOriginal(submitted);
      savedCurrentDraft = editor.current === submitted;
      if (savedCurrentDraft) setEdit(null);
      setVersion((v) => v + 1);
      setChanged(false);
    });
    return success && savedCurrentDraft;
  };
  const canLeave = async () => {
    if (busy) return false;
    if (!dirty) {
      setEdit(null);
      return true;
    }
    const choice = await ask({
      title: tr("Unsaved changes"),
      description: selected,
      choices: [
        { label: tr("Discard"), value: "discard", danger: true },
        { label: tr("Save"), value: "save" },
      ],
    });
    if (choice === "save") return save();
    if (choice === "discard") {
      setEdit(null);
      setChanged(false);
      return true;
    }
    return false;
  };
  useEffect(() => {
    if (guard) guard.current = canLeave;
    return () => {
      if (guard) guard.current = async () => true;
    };
  });
  useEffect(() => {
    if (!dirty) return;
    const before = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener("beforeunload", before);
    return () => window.removeEventListener("beforeunload", before);
  }, [dirty]);
  const choose = async (path: string) => {
    if (busy || path === selected) return;
    if (!(await canLeave())) return;
    setEdit(null);
    setChanged(false);
    onSelect(path);
    if (window.matchMedia("(max-width: 767px)").matches) setTreeOpen(false);
  };
  const editFile = () =>
    void act(async () => {
      const blob = await readPreview(
        fileURL(project.id, selected),
        new AbortController().signal,
        1 << 20,
      );
      const text = new TextDecoder("utf-8", { fatal: true }).decode(
        await blob.arrayBuffer(),
      );
      if (text.includes("\0"))
        throw new Error(tr("This file contains binary data."));
      setOriginal(text);
      setEdit(text);
    });
  const refresh = () => {
    for (const d of new Set(["", ...directories])) void load(d);
    if (edit === null) setVersion((v) => v + 1);
    else void validateSelected();
  };
  const mkdir = async (dir: string) => {
    const name = await ask({
      title: tr("New folder name"),
      input: true,
      choices: [{ label: tr("New folder"), value: "create" }],
    });
    if (name)
      void act(async () => {
        await command(base, {
          id: newID(),
          operation: "mkdir",
          path: dir ? `${dir}/${name}` : name,
        });
      });
  };
  const treeActions = (dir: string) => [
    {
      label: tr("Upload"),
      disabled: busy,
      action: () => {
        uploadDirectory.current = dir;
        fileInput.current?.click();
      },
    },
    { label: tr("New folder"), disabled: busy, action: () => void mkdir(dir) },
    { label: tr("Refresh"), action: refresh },
  ];
  const entryActions = (path: string, entry: FileEntry) => [
    ...(entry.kind === "directory" ? treeActions(path) : []),
    {
      label: tr("Move"),
      disabled: busy,
      action: () =>
        void (async () => {
          if (selected === path && !(await canLeave())) return;
          const destination = await ask({
            title: tr("Move to Project path"),
            initial: path,
            input: true,
            choices: [{ label: tr("Move"), value: "move" }],
          });
          if (destination && destination !== path)
            void act(async () => {
              await command(base, {
                id: newID(),
                operation: "move",
                path,
                destination,
              });
              if (selected === path) {
                setEdit(null);
                onSelect(destination);
              }
            });
        })(),
    },
    {
      label: tr("Delete"),
      danger: true,
      disabled: busy,
      action: () =>
        void (async () => {
          if (selected === path && !(await canLeave())) return;
          if (
            await ask({
              title: tr("Delete file"),
              description: `${path} · ${tr("Directories must be empty.")}`,
              choices: [{ label: tr("Delete"), value: "delete", danger: true }],
            })
          )
            void act(async () => {
              await command(base, { id: newID(), operation: "remove", path });
              if (selected === path) {
                setEdit(null);
                onSelect("");
              }
            });
        })(),
    },
  ];
  const renderDirectory = (dir: string, depth = 0) => {
    const page = tree[dir];
    if (!page) return <p className="list-hint">{tr("Loading…")}</p>;
    const includes = (path: string, name: string) =>
      !filter ||
      name.toLowerCase().includes(filter.toLowerCase()) ||
      Object.entries(tree).some(
        ([parent, p]) =>
          parent.startsWith(path + "/") &&
          p.entries.some((e) =>
            e.name.toLowerCase().includes(filter.toLowerCase()),
          ),
      ) ||
      tree[path]?.entries.some((e) =>
        e.name.toLowerCase().includes(filter.toLowerCase()),
      );
    return (
      <>
        {page.entries
          .filter((e) => includes(dir ? `${dir}/${e.name}` : e.name, e.name))
          .map((entry) => {
            const path = dir ? `${dir}/${entry.name}` : entry.name;
            const isDir = entry.kind === "directory",
              open = directories.includes(path);
            return (
              <div key={path}>
                <div
                  className={`file-row ${selected === path ? "selected" : ""}`}
                  data-file-path={path}
                  style={{ paddingLeft: 8 + depth * 16 }}
                >
                  <button
                    className="file-name"
                    title={
                      entry.kind === "symlink"
                        ? tr("Symlinks are not followed")
                        : path
                    }
                    disabled={entry.kind === "symlink"}
                    aria-expanded={isDir ? open : undefined}
                    onClick={() => {
                      if (isDir)
                        onDirectories(
                          open
                            ? directories.filter((p) => p !== path)
                            : [...directories, path],
                        );
                      else void choose(path);
                    }}
                  >
                    {isDir ? (
                      open ? (
                        <ChevronDown size={14} />
                      ) : (
                        <ChevronRight size={14} />
                      )
                    ) : (
                      <span className="tree-indent" />
                    )}
                    {isDir ? <Folder size={16} /> : <File size={16} />}
                    <span>{entry.name}</span>
                  </button>
                  <Menu
                    label={`${tr("File actions")} ${entry.name}`}
                    items={entryActions(path, entry)}
                  />
                </div>
                {isDir && open && renderDirectory(path, depth + 1)}
              </div>
            );
          })}
        {page.loading && <small className="list-hint">{tr("Loading…")}</small>}
        {page.error && (
          <div className="notice error">
            {page.error}
            <button onClick={() => void load(dir)}>{tr("Retry")}</button>
          </div>
        )}
        {!page.loading && !page.error && !page.entries.length && (
          <small className="list-hint">{tr("This directory is empty.")}</small>
        )}
        {page.next && (
          <button
            className="list-hint"
            onClick={() => void load(dir, page.next)}
          >
            {tr("Show more")}
          </button>
        )}
      </>
    );
  };
  if (denied)
    return (
      <aside className="files-panel">
        <p role="alert">{tr("This conversation is unavailable.")}</p>
      </aside>
    );
  return (
    <aside
      className={`files-panel ${treeOpen ? "tree-open" : ""} ${fullscreen ? "fullscreen" : ""}`}
      aria-label={tr("Project Files")}
      style={{ "--tree-width": `${treeWidth}px` } as CSSProperties}
    >
      <header className="file-toolbar">
        <nav className="file-breadcrumb" aria-label={tr("File path")}>
          <span>{project.title}</span>
          {selected
            .split("/")
            .filter(Boolean)
            .map((part, i, parts) => (
              <span key={i}>
                <ChevronRight size={13} />
                {i === parts.length - 1 ? (
                  <strong>{part}</strong>
                ) : (
                  <button
                    onClick={() => {
                      onDirectories([
                        ...new Set([
                          ...directories,
                          parts.slice(0, i + 1).join("/"),
                        ]),
                      ]);
                      setTreeOpen(true);
                    }}
                  >
                    {part}
                  </button>
                )}
              </span>
            ))}
        </nav>
        <div className="row-actions">
          {selected && (
            <Menu
              label={tr("File actions")}
              items={[
                {
                  label: tr("Download"),
                  action: () => {
                    const a = document.createElement("a");
                    a.href = fileURL(project.id, selected);
                    a.download = selected.split("/").pop() || "";
                    a.click();
                  },
                },
                {
                  label: tr("Edit text"),
                  disabled:
                    busy ||
                    project.state !== "active" ||
                    !["code", "text", "markdown", "json", "table"].includes(
                      classifyArtifact(selected).kind,
                    ),
                  action: editFile,
                },
                { label: tr("Close file"), action: () => void choose("") },
              ]}
            />
          )}
          <IconButton
            label={tr("Toggle file tree")}
            aria-pressed={treeOpen}
            onClick={() => setTreeOpen(!treeOpen)}
          >
            <PanelRight size={17} />
          </IconButton>
          <button
            type="button"
            ref={fullButton}
            className="icon-button"
            aria-label={
              fullscreen ? tr("Exit Full Screen") : tr("Enter Full Screen")
            }
            title={
              fullscreen ? tr("Exit Full Screen") : tr("Enter Full Screen")
            }
            onClick={() => onFullscreen(!fullscreen)}
          >
            {fullscreen ? <Minimize2 size={17} /> : <Maximize2 size={17} />}
          </button>
          <IconButton label={tr("Close file preview")} onClick={onClose}>
            <X size={18} />
          </IconButton>
        </div>
      </header>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      {uncertain && (
        <p className="notice warning">
          {tr("Operation")}: {uncertain} ·{" "}
          {tr(
            "has an uncertain outcome. Inspect current file contents before a new write.",
          )}
        </p>
      )}
      {watchHealth !== "live" && (
        <p className="notice" role="status">
          {tr("File updates may be delayed. Refresh to check.")}
        </p>
      )}
      {changed && (
        <p className="notice warning">
          {edit !== null
            ? tr("Files changed. Your unsaved text has been kept.")
            : tr("File updates may be delayed. Refresh to check.")}
        </p>
      )}
      <input
        hidden
        ref={fileInput}
        type="file"
        multiple
        onChange={(e) => {
          const files = Array.from(e.target.files || []);
          e.target.value = "";
          void act(async () => {
            for (const file of files)
              await upload(
                project.id,
                uploadDirectory.current
                  ? `${uploadDirectory.current}/${file.name}`
                  : file.name,
                file,
              );
            setVersion((v) => v + 1);
          });
        }}
      />
      {project.state !== "active" ? (
        <div className="empty-state">
          <p>
            {tr("The workspace is")} {project.state}.
          </p>
          <button
            disabled={busy}
            onClick={() =>
              void act(async () => {
                await command(`${projectURL(project.id)}/resume`);
              })
            }
          >
            {tr("Resume Project")}
          </button>
        </div>
      ) : (
        <div className="file-columns">
          <section
            className="selected-file"
            aria-label={selected ? `File ${selected}` : tr("File preview")}
          >
            {!selected ? (
              <div className="empty-state">
                <FolderOpen size={30} />
                <h2>{tr("Open file")}</h2>
                <p>{tr("Select a file from the workspace tree")}</p>
              </div>
            ) : edit !== null ? (
              <>
                <div className="editing-toolbar">
                  <span>{dirty ? tr("Unsaved changes") : tr("Edit text")}</span>
                  <button
                    disabled={busy}
                    onClick={() =>
                      void (async () => {
                        if (await canLeave()) setEdit(null);
                      })()
                    }
                  >
                    {tr("Cancel edit")}
                  </button>
                  <button
                    className="primary"
                    disabled={busy}
                    onClick={() => void save()}
                  >
                    {tr("Save file")}
                  </button>
                </div>
                <div className="file-editor">
                  <CodeEditor
                    path={selected}
                    initialText={edit}
                    onChange={(text) => {
                      editor.current = text;
                      setEdit(text);
                    }}
                  />
                </div>
              </>
            ) : (
              <FilePreview
                key={`${project.id}:${selected}`}
                project={project.id}
                validation={version}
                path={selected}
                href={fileURL(project.id, selected)}
              />
            )}
          </section>
          {treeOpen && (
            <>
              <Splitter
                label={tr("Resize file tree")}
                reverse
                value={treeWidth}
                min={200}
                max={320}
                onChange={onTreeWidth}
              />
              <section className="file-tree" aria-label={tr("File tree")}>
                <div className="tree-toolbar">
                  <input
                    aria-label={tr("Filter loaded files")}
                    placeholder={tr("Filter loaded files")}
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                  />
                  <Menu label={tr("Tree actions")} items={treeActions("")} />
                </div>
                {filter && (
                  <small className="list-hint">
                    {tr("Only loaded entries are filtered.")}
                  </small>
                )}
                <div className="file-tree-scroll">{renderDirectory("")}</div>
              </section>
            </>
          )}
        </div>
      )}
    </aside>
  );
}
