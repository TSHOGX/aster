/* eslint-disable react-hooks/immutability -- Workspace buffers are imperative controller objects; mutations publish through the Studio provider. */
/* eslint-disable react-hooks/set-state-in-effect -- Hydrate browser navigation and synchronize external workspace snapshots. */
"use client";
import { message, useT } from "../lib/i18n";
import { IntegrationReview } from "./integration-review";
import { BranchCheckpoint } from "./checkpoint";
import { BranchSelector } from "./branch-selector";
import { branchAPI, branchRoot, type Branch } from "../lib/branches";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  PanelLeft,
  Plus,
  Save,
  X,
  FileText,
  List,
  UploadCloud,
} from "lucide-react";
import {
  api,
  bytes,
  Entry,
  Library,
  requestID,
  Upload,
  Version,
} from "../lib/api";
import { storageRead, storageWrite } from "../lib/workbench";
import { Page, parentPath, query, encodeText } from "../lib/workbench";
import { deleteRecovery } from "../lib/recovery";
import { SharedAssetsValidation } from "./shared-assets-validation";
import { ShellHeader, LibraryDetails, contentURL } from "./shell";
import { BufferFile, useStudio, workspaceKey } from "./studio-provider";
import { Menu, Modal, TextDialog } from "./ui";
import { FileTree } from "./file-tree";
import { CodeEditor } from "./code-editor";
import { ImagePreview, MarkdownPreview } from "./preview";
import { Changes, TextDiff } from "./changes";
import { VersionPicker, Versions } from "./versions";
type Form = {
  title: string;
  label: string;
  initial?: string;
  submit?: string;
  action: (v: string) => Promise<void>;
};
export function LibraryEditor({ id }: { id: string }) {
  const tr = useT();
  const studio = useStudio();
  const [library, setLibrary] = useState<Library | null>(null),
    [version, setVersion] = useState(""),
    [versionLabel, setVersionLabel] = useState("Draft"),
    [view, setView] = useState("files"),
    [selected, setSelected] = useState(""),
    [directory, setDirectory] = useState(""),
    [error, setError] = useState(""),
    [form, setForm] = useState<Form | null>(null),
    [details, setDetails] = useState(false),
    [sidebar, setSidebar] = useState(true),
    [mobile, setMobile] = useState(false),
    [width, setWidth] = useState(260),
    [wrap, setWrap] = useState(true),
    [mode, setMode] = useState("edit"),
    [position, setPosition] = useState([1, 1]),
    [ready, setReady] = useState(false),
    [drag, setDrag] = useState(false),
    [activePublication, setActivePublication] = useState(false),
    [opened, setOpened] = useState(false),
    [moveEntry, setMoveEntry] = useState<Entry | null>(null);
  const branchID = studio.model.branches[id]?.id ?? "";
  const scope = workspaceKey(id, version ? "" : branchID, version);
  const input = useRef<HTMLInputElement>(null),
    folderInput = useRef<HTMLInputElement>(null),
    search = useRef<HTMLInputElement>(null),
    root = `/libraries/${id}`;
  const [refs] = useState(() => ({ current: studio }));
  refs.current = studio;
  const run = async (action: () => Promise<unknown>) => {
    try {
      setError("");
      await action();
    } catch (e) {
      setError(String(e));
    }
  };
  const refreshGeneration = useRef(0);
  const openGeneration = useRef(0);
  const refresh = useCallback(
    // eslint-disable-next-line react-hooks/preserve-manual-memoization -- Stable imperative controller bridge; reading its latest value must not retrigger refresh on each notification.
    async (signal?: AbortSignal) => {
      const controller = refs.current;
      const turn = ++refreshGeneration.current;
      const [l, u, v] = await Promise.all([
        api<Library>(`${root}?mode=workbench`, { signal }),
        controller.model.branches[id] ? api<{ uploads: Upload[] }>(`${root}/uploads?branch_id=${encodeURIComponent(controller.model.branches[id].id)}`, { signal }) : Promise.resolve({ uploads: [] as Upload[] }),
        api<Page<Version>>(`${root}/versions?mode=workbench&limit=50`, {
          signal,
        }),
      ]);
      if (signal?.aborted || turn !== refreshGeneration.current) return;
      if (controller.model.branches[id]) {
        const branch = await branchAPI.read(id, controller.model.branches[id].id, signal);
        if (signal?.aborted || turn !== refreshGeneration.current) return;
        controller.model.branches[id] = branch;
        l.draft_revision = branch.draft_revision;
      }
      setLibrary(l);
      setActivePublication(
        v.items.some((x) => x.state === "pending" || x.state === "validating"),
      );
      controller.model.libraries[id] = l;
      controller.model.uploads[id] = u.uploads;
      controller.notify();
    },
    [id, root, refs],
  );
  useEffect(() => {
    const media = matchMedia("(max-width: 767px)");
    const resize = () => {
      setMobile(media.matches);
      setSidebar(!media.matches);
    };
    resize();
    media.addEventListener("change", resize);
    return () => media.removeEventListener("change", resize);
  }, []);
  useEffect(() => {
    const p = new URLSearchParams(location.search);
    setVersion(p.get("version") || "");
    setView(
      ["files", "changes", "versions"].includes(p.get("view") || "")
        ? p.get("view")!
        : "files",
    );
    setSelected(
      p.get("path") ||
        refs.current.model.buffers.find(
          (b) => b.key === refs.current.model.active[scope],
        )?.entry.path ||
        "",
    );
    setWidth(
      Math.max(
        220,
        Math.min(
          420,
          Number(storageRead("local", "studio-sidebar-width") || 260),
        ),
      ),
    );
    setSidebar(innerWidth >= 768);
    setReady(true);
    setOpened(false);
  }, [id, refs, scope]);
  useEffect(() => {
    const abort = new AbortController();
    const update = () => {
      if (!document.hidden && !refs.current.model.expired)
        void refresh(abort.signal).catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        });
    };
    update();
    const timer = setInterval(update, activePublication ? 2000 : 5000);
    document.addEventListener("visibilitychange", update);
    return () => {
      abort.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [refresh, activePublication, studio.model.epoch, refs]);
  useEffect(() => {
    if (!ready) return;
    const params = new URLSearchParams(location.search);
    params.set("view", view);
    if (version) params.set("version",version); else params.delete("version");
    if (selected) params.set("path",selected); else params.delete("path");
    if (branchID) params.set("branch",branchID);
    history.replaceState(null,"",`?${params}`);
  }, [ready, view, version, selected, branchID]);
  useEffect(() => {
    if (!version) {
      setVersionLabel("Draft");
      return;
    }
    const abort = new AbortController();
    void api<Version>(`${root}/versions/${version}`, { signal: abort.signal })
      .then((v) => setVersionLabel(v.label))
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [root, version]);
  const active = studio.model.buffers.find(
    (b) => b.key === studio.model.active[scope] && b.version === version,
  );
  const buffers = studio.model.buffers.filter(
    (b) => b.library === id && b.version === version && (version || b.branch === branchID),
  );
  const open = async (entry: Entry) => {
    const turn = ++openGeneration.current;
    if (entry.kind === "directory") {
      setDirectory(entry.path);
      return;
    }
    await studio.open(id, version, entry, library?.draft_revision);
    if (turn !== openGeneration.current) return;
    setSelected(entry.path);
    setDirectory(parentPath(entry.path));
    setView("files");
    if (innerWidth < 768) setSidebar(false);
  };
  const openPath = async (path: string) => {
    if (!version) {
      const s = await studio.snapshot(id, path);
      if (!s.entry) throw new Error("File no longer exists.");
      await open(s.entry);
    } else {
      const p = await api<Page<Entry>>(
        `${root}/versions/${version}/search?${query({ q: path })}`,
      );
      const entry = p.items.find((e) => e.path === path);
      if (!entry)
        throw new Error("This path does not exist in the selected version.");
      await open(entry);
    }
  };
  useEffect(() => {
    if (!ready || !library || opened) return;
    setOpened(true);
    if (selected) void openPath(selected).catch((e) => setError(String(e)));
    // Load the initial deep link once. Further navigation is explicit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, library, opened]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "p") {
        e.preventDefault();
        setSidebar(true);
        setTimeout(() => search.current?.focus(), 0);
      }
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        if (active && !active.version) void run(() => studio.save(active));
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  });
  const switchVersion = (next: string) => {
    openGeneration.current++;
    studio.model.selectionGeneration[id] =
      (studio.model.selectionGeneration[id] || 0) + 1;
    void studio.persist();
    setVersion(next);
    setView("files");
    setDirectory("");
    setOpened(false);
    setError("");
  };
  const create = (kind: "text" | "mkdir") =>
    setForm({
      title: kind === "text" ? "New file" : "New folder",
      label: "Name",
      submit: "Create",
      action: async (name) => {
        const path = [directory, name].filter(Boolean).join("/");
        await studio.queue.run(`${id}:${branchID}`, async () => {
          const target = await studio.snapshot(id, path);
          if (target.entry)
            throw new Error("An entry already exists at this path.");
          await studio.write(`${branchRoot(id,branchID)}/draft/${kind === "text" ? "text" : "structure"}`, {
            request_id: requestID(),
            expected_revision: target.revision,
            expected_head:target.head,
            ...(kind === "mkdir" ? {action:kind} : {}),
            path,
            ...(kind === "text" ? { text: "" } : {}),
          });
        });
        studio.changed();
        await refresh();
        if (kind === "text") await openPath(path);
      },
    });
  const move = async (entry: Entry, to: string) => {
    if (!(await studio.settle(id, entry.path))) return;
    const before = await studio.snapshot(id, entry.path);
    await studio.mutate(
      id,
      { action: "move", path: entry.path, to },
      before.revision,
    );
    for (const b of studio.model.buffers) {
      if (
        b.library === id &&
        b.branch === branchID &&
        !b.version &&
        (b.entry.path === entry.path ||
          b.entry.path.startsWith(entry.path + "/"))
      ) {
        const next = to + b.entry.path.slice(entry.path.length);
        await studio.persist();
        const old = b.key;
        await deleteRecovery(b.recoveryID);
        const recoveryIdentity = JSON.parse(b.recoveryID) as string[];
        recoveryIdentity[recoveryIdentity.length - 1] = next;
        b.recoveryID = JSON.stringify(recoveryIdentity);
        b.entry = { ...b.entry, path: next };
        b.key = JSON.stringify([id, b.branch, "", next]);
        b.editorState = undefined;
        if (studio.model.active[scope] === old) studio.model.active[scope] = b.key;
      }
    }
    studio.changed();
    if (selected === entry.path || selected.startsWith(entry.path + "/"))
      setSelected(to + selected.slice(entry.path.length));
    await refresh();
  };
  const remove = async (entry: Entry) => {
    if (!(await studio.settle(id, entry.path))) return;
    const state = await studio.snapshot(id, entry.path);
    let count = 1;
    if (entry.kind === "directory") {
      const tree = await api<{ descendant_count: number }>(
        `${branchRoot(id,branchID)}/draft/tree?${query({ parent: entry.path, revision: state.revision })}`,
      );
      count += tree.descendant_count;
    }
    if (
      (await studio.ask(
        "Delete from draft",
        count > 1 ? message("Delete {path} and {count} contained entries? Published versions are retained.", { path: entry.path, count: count - 1 }) : message("Delete {path}? Published versions are retained.", { path: entry.path }),
        ["Cancel", "Delete"],
      )) !== "Delete"
    )
      return;
    await studio.mutate(
      id,
      { action: "remove", path: entry.path, recursive: true },
      state.revision,
    );
    for (const b of [...studio.model.buffers])
      if (
        b.library === id &&
        b.branch === branchID &&
        !b.version &&
        (b.entry.path === entry.path ||
          b.entry.path.startsWith(entry.path + "/"))
      )
        await studio.close(b);
    setSelected("");
    await refresh();
  };
  const fileActions = (entry: Entry) => [
    {
      label: message("Copy path"),
      action: () =>
        void navigator.clipboard
          .writeText(entry.path)
          .catch((e) => setError(String(e))),
    },
    ...(entry.kind === "file"
      ? [
          {
            label: message("Download"),
            action: () => {
              window.location.assign(contentURL(id, version, entry.path,library?.draft_revision,branchID));
            },
          },
        ]
      : []),
    ...(!version && !library?.archived
      ? [
          { id: "rename", label: message("Rename"), action: () => {} },
          { label: message("Move"), action: () => setMoveEntry(entry) },
          {
            label: message("Delete"),
            danger: true,
            action: () => void run(() => remove(entry)),
          },
        ]
      : []),
  ];
  const tabsLabel = (b: BufferFile) => {
    const same = buffers.filter(
      (x) => x.entry.path.split("/").at(-1) === b.entry.path.split("/").at(-1),
    );
    if (same.length === 1) return b.entry.path.split("/").at(-1);
    const parts = b.entry.path.split("/");
    for (let n = 2; n < parts.length; n++) {
      const suffix = parts.slice(-n).join("/");
      if (
        same.filter(
          (x) => x.entry.path.endsWith("/" + suffix) || x.entry.path === suffix,
        ).length === 1
      )
        return suffix;
    }
    return b.entry.path;
  };
  const fileSidebar = (
    <aside className="files-sidebar">
      <div className="sidebar-heading">
        <strong>{tr("Files")}</strong>
        <div className="actions">
          {!version && !library?.archived && (
            <Menu
              label={tr("Add files")}
              items={[
                { label: message("New file"), action: () => create("text") },
                { label: message("New folder"), action: () => create("mkdir") },
                {
                  label: message("Upload files"),
                  action: () => input.current?.click(),
                },
                {
                  label: message("Upload folder"),
                  action: () => folderInput.current?.click(),
                },
              ]}
            >
              <Plus size={16} />
            </Menu>
          )}
          <button
            className="icon-button"
            aria-label={tr("Hide files")}
            onClick={() => setSidebar(false)}
          >
            <PanelLeft size={16} />
          </button>
        </div>
      </div>
      <VersionPicker
        library={id}
        value={version}
        label={versionLabel}
        onChange={switchVersion}
      />
      {library && (
        <FileTree
          key={`${id}:${studio.model.branches[id]?.id ?? ""}:${version}`}
          branch={studio.model.branches[id]?.id ?? ""}
          library={id}
          version={version}
          revision={library.draft_revision}
          selected={selected}
          onOpen={(entry) => void run(() => open(entry))}
          onDirectory={setDirectory}
          actions={fileActions}
          onRename={move}
          searchRef={search}
        />
      )}
      <div className="sidebar-footer">
        {version ? tr("Read-only version") : tr("Draft · revision {value0}", { value0: library?.draft_revision ?? "…" })}
      </div>
    </aside>
  );
  return (
    <main className="studio-shell">
      <ShellHeader library={library} />
      <div className="branch-controls">
      <BranchSelector library={id} onSelect={() => { setVersion(""); setSelected(""); setDirectory(""); void run(() => refresh()); }} />
      {!version && studio.model.branches[id] && <BranchCheckpoint key={`${id}:${branchID}`} branch={studio.model.branches[id]} onCommitted={() => { void run(() => refresh()); }} />}
      {!version && studio.model.branches[id] && <IntegrationReview key={`${id}:${branchID}`} branch={studio.model.branches[id]} />}
      </div>
      <div
        className="workbench"
        style={{ "--sidebar-width": `${width}px` } as React.CSSProperties}
        onDragOver={(e) => {
          if (
            !version &&
            !library?.archived &&
            e.dataTransfer.types.includes("Files")
          ) {
            e.preventDefault();
            setDrag(true);
          }
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node))
            setDrag(false);
        }}
        onDrop={(e) => {
          e.preventDefault();
          setDrag(false);
          if (!version && !library?.archived)
            void studio.upload(id, directory, Array.from(e.dataTransfer.files));
        }}
      >
        {sidebar && (
          <>
            {mobile ? (
              <Modal
                title={tr("Files")}
                open
                onClose={() => setSidebar(false)}
                className="sidebar-dialog"
              >
                {fileSidebar}
              </Modal>
            ) : (
              fileSidebar
            )}
            <div
              className="split-handle"
              role="separator"
              aria-label={tr("Resize files sidebar")}
              aria-orientation="vertical"
              aria-valuenow={width}
              aria-valuemin={220}
              aria-valuemax={420}
              tabIndex={0}
              onKeyDown={(e) => {
                if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                  e.preventDefault();
                  const n = Math.max(
                    220,
                    Math.min(420, width + (e.key === "ArrowRight" ? 10 : -10)),
                  );
                  setWidth(n);
                  storageWrite("local", "studio-sidebar-width", String(n));
                }
              }}
              onPointerDown={(e) => {
                e.currentTarget.setPointerCapture(e.pointerId);
              }}
              onPointerMove={(e) => {
                if (e.currentTarget.hasPointerCapture(e.pointerId)) {
                  const n = Math.max(220, Math.min(420, e.clientX));
                  setWidth(n);
                  storageWrite("local", "studio-sidebar-width", String(n));
                }
              }}
            />
          </>
        )}
        <section className="workspace-main">
 {library?.profile === "shared-assets-v1" && (version || branchID) && <SharedAssetsValidation key={`${id}:${version}:${library.draft_revision}:${studio.model.epoch}`} library={id} version={version} branch={branchID} revision={library.draft_revision} onOpen={()=>void run(()=>openPath("AGENTS.md"))} />}
          <div className="workspace-nav">
            {(!sidebar || mobile) && (
              <button
                aria-label={tr("Show files")}
                className="icon-button"
                onClick={() => setSidebar(true)}
              >
                <PanelLeft size={17} />
              </button>
            )}
            <nav aria-label={tr("Library views")}>
              {["files", "changes", "versions"].map((v) => (
                <button
                  key={v}
                  aria-current={view === v ? "page" : undefined}
                  onClick={() => setView(v)}
                >
                  {tr(v[0].toUpperCase() + v.slice(1))}
                  {v === "changes" && !!library?.draft_change_count && (
                    <span className="count">{library.draft_change_count}</span>
                  )}
                </button>
              ))}
            </nav>
            <div className="actions">
              <Menu
                label={tr("Library actions")}
                items={[
                  {
                    label: message("Edit library details"),
                    action: () => setDetails(true),
                  },
                  { label: message("Refresh"), action: () => void run(() => refresh()) },
                ]}
              />

            </div>
          </div>
          {error && (
            <div className="notice error" role="alert">
              <span>{tr(error)}</span>
              <button
                onClick={() => {
                  setError("");
                  void run(() => refresh());
                }}
              >
                 {tr("Refresh saved state")} </button>
            </div>
          )}
          {view === "files" && (
            <>
              <div className="file-tabs" role="tablist" aria-label={tr("Open files")}>
                {buffers.map((b) => (
                  <div
                    className={`file-tab ${active === b ? "active" : ""}`}
                    key={b.key}
                  >
                    <button
                      role="tab"
                      aria-selected={active === b}
                      onClick={() => {
                        studio.model.active[scope] = b.key;
                        studio.notify();
                        setSelected(b.entry.path);
                        setDirectory(parentPath(b.entry.path));
                      }}
                      title={b.entry.path}
                    >
                      {tabsLabel(b)}
                      {b.text !== b.saved && (
                        <span aria-label={tr("Unsaved changes")}>●</span>
                      )}
                    </button>
                    <button
                      aria-label={tr("Close {value0}", { value0: b.entry.path })}
                      className="tab-close"
                      onClick={() => void run(() => studio.close(b))}
                    >
                      <X size={12} />
                    </button>
                  </div>
                ))}
                {buffers.length > 0 && (
                  <Menu
                    label={tr("Open files")}
                    items={[
                      ...buffers.map((b) => ({
                        label: b.entry.path,
                        action: () => {
                          studio.model.active[scope] = b.key;
                          studio.notify();
                          setSelected(b.entry.path);
                        },
                      })),
                      {
                        label: message("Close all files"),
                        action: () =>
                          void run(async () => {
                            if (await studio.settle(id))
                              for (const b of [...buffers])
                                await studio.close(b);
                          }),
                      },
                    ]}
                  >
                    <List size={16} />
                  </Menu>
                )}
              </div>
              {active ? (
                <>
                  <div className="editor-toolbar">
                    <span className="file-path" title={active.entry.path}>
                      {active.entry.path}
                    </span>
                    <div className="actions">
                      <span className={active.error ? "error" : "muted"}>
                        {active.version ? tr("Read only") : active.text !== active.saved &&
                              active.status === "Saved" ? tr("Unsaved") : tr(active.status)}
                      </span>
                      {active.editable && /\.md$/i.test(active.entry.path) && (
                        <div className="segmented" aria-label={tr("Markdown mode")}>
                          {["edit", "preview", "split"].map((m) => (
                            <button
                              key={m}
                              aria-pressed={mode === m}
                              onClick={() => setMode(m)}
                            >
                              {tr(m)}
                            </button>
                          ))}
                        </div>
                      )}
                      <Menu
                        label={tr("File editor actions")}
                        items={[
                          {
                            label: wrap ? message("Disable line wrapping") : message("Enable line wrapping"),
                            action: () => setWrap(!wrap),
                          },
                          {
                            label: message("Download"),
                            action: () =>
                              window.location.assign(
                                contentURL(
                                  id,
                                  active.version,
                                  active.entry.path,
                                  active.revision, active.branch,
                                ),
                              ),
                          },
                          {
                            label: message("Reload file"),
                            action: () =>
                              void run(async () => {
                                await studio.close(active);
                                if (!studio.model.buffers.includes(active))
                                  await openPath(active.entry.path);
                              }),
                          },
                          {
                            label: message("Save all"),
                            disabled: !!version || !!library?.archived,
                            action: () => void run(() => studio.saveAll(id)),
                          },
                        ]}
                      />
                      {active.editable && !version && (
                        <button
                          className="primary"
                          disabled={
                            !!library?.archived ||
                            (active.text === active.saved && !active.pending) ||
                            active.status === "Saving"
                          }
                          onClick={() => void run(() => studio.save(active))}
                        >
                          <Save size={14} />
                          <span>{tr("Save file")}</span>
                        </button>
                      )}
                    </div>
                  </div>
                  {active.error && (
                    <div className="notice error" role="alert">
                      {tr(active.error)}
                    </div>
                  )}
                  {active.format.mixed && active.text !== active.saved && (
                    <div className="inline-status">
                       {tr("Mixed line endings will be saved as LF.")} </div>
                  )}
                  <div
                    className={`editor-content ${active.editable && /\.md$/i.test(active.entry.path) ? `markdown-mode-${mode}` : ""}`}
                  >
                    {active.image ? (
                      <ImagePreview buffer={active} />
                    ) : active.editable ? (
                      <>
                        {(!/\.md$/i.test(active.entry.path) ||
                          mode !== "preview") && (
                          <CodeEditor
                            key={`${active.key}:${active.reset || 0}`}
                            buffer={active}
                            wrap={wrap}
                            onChange={(text) => {
                              active.text = text;
                              active.status =
                                text === active.saved ? "Saved" : "Unsaved";
                              studio.notify();
                            }}
                            onSave={() => void run(() => studio.save(active))}
                            onPosition={(line, col) => setPosition([line, col])}
                          />
                        )}{" "}
                        {/\.md$/i.test(active.entry.path) &&
                          mode !== "edit" && (
                            <MarkdownPreview
                              buffer={active}
                              onOpen={(path) => void run(() => openPath(path))}
                            />
                          )}
                      </>
                    ) : (
                      <div className="empty-state">
                        <FileText size={32} />
                        <h2>{tr("File preview unavailable")}</h2>
                        <p>
                          {bytes(active.entry.size)}  {tr("· Download to inspect this file.")} </p>
                        <a
                          className="button"
                          href={contentURL(
                            id,
                            active.version,
                            active.entry.path,
                            active.revision, active.branch,
                          )}
                        >
                           {tr("Download")} </a>
                      </div>
                    )}
                  </div>
                  <div className="editor-status">
                    <span>{active.version ? tr("Read only") : tr("Draft")}</span>
                    <span>
                      {active.editable ? tr("Ln {value0}, Col {value1} · UTF-8{value2} · {value3}", { value0: position[0], value1: position[1], value2: active.format.bom ? " BOM" : "", value3: active.format.separator === "\r\n" ? "CRLF" : "LF" }) : bytes(active.entry.size)}
                    </span>
                  </div>
                </>
              ) : (
                <div className="editor-empty">
                  <FileText size={30} />
                  <h2>{version ? tr("Browse version files") : tr("Select a file")}</h2>
                  <p>{tr("Open a file from the sidebar, or use Go to file.")}</p>
                  <div className="actions">
                    <button
                      onClick={() => {
                        setSidebar(true);
                        setTimeout(() => search.current?.focus(), 0);
                      }}
                    >
                       {tr("Go to file")} </button>
                    {!version && !library?.archived && (
                      <button onClick={() => input.current?.click()}>
                        <UploadCloud size={16} />
                         {tr("Upload files")} </button>
                    )}
                  </div>
                </div>
              )}
            </>
          )}
          {view === "changes" && library && <Changes library={library} />}{" "}
          {view === "versions" && library && (
            <Versions
              library={library}
              onInspect={switchVersion}
              onChanged={() => void run(() => refresh())}
            />
          )}
        </section>
        {drag && <div className="drop-target">{tr("Upload to /")}{directory}</div>}
      </div>
      <input
        className="visually-hidden"
        ref={input}
        type="file"
        multiple
        aria-label={tr("Select files")}
        onChange={(e) => {
          void studio.upload(id, directory, Array.from(e.target.files || []));
          e.target.value = "";
        }}
      />
      <input
        className="visually-hidden"
        ref={folderInput}
        type="file"
        multiple
        {...{ webkitdirectory: "" }}
        aria-label={tr("Select directory")}
        onChange={(e) => {
          void studio.upload(id, directory, Array.from(e.target.files || []));
          e.target.value = "";
        }}
      />
      {form && (
        <TextDialog
          title={form.title}
          label={form.label}
          initial={form.initial}
          submit={form.submit}
          onClose={() => setForm(null)}
          onSubmit={form.action}
        />
      )}{" "}
      {details && library && (
        <LibraryDetails
          library={library}
          onClose={() => setDetails(false)}
          onSaved={setLibrary}
        />
      )}
      {active?.conflict && <Conflict buffer={active} onError={setError} />}{" "}
      {moveEntry && studio.model.branches[id] && (
        <MoveDialog
          branch={studio.model.branches[id]}
          entry={moveEntry}
          onClose={() => setMoveEntry(null)}
          onMove={(to) => move(moveEntry, to)}
        />
      )}
    </main>
  );
}
function Conflict({
  buffer,
  onError,
}: {
  buffer: BufferFile;
  onError: (e: string) => void;
}) {
  const tr = useT();
  const studio = useStudio();
  const [open, setOpen] = useState(true),
    [copy, setCopy] = useState(false);
  const conflict = buffer.conflict!;
  return (
    <>
      <button className="conflict-reopen" onClick={() => setOpen(true)}>
         {tr("Review conflict")} </button>
      <Modal
        title={tr("Resolve conflict · {value0}", { value0: buffer.entry.path })}
        open={open}
        onClose={() => setOpen(false)}
        drawer
      >
        <p>
           {tr("The left side is the saved file; the right side is your unsaved content.")} </p>
        <div className="conflict-diff">
          <TextDiff before={conflict.text} after={buffer.text} />
        </div>
        <div className="dialog-actions">
          <button
            onClick={() => {
              if (!conflict.entry || !conflict.readable) {
                onError(
                  "The file was deleted. Save your content under a new name.",
                );
                return;
              }
              buffer.entry = conflict.entry;
              if (conflict.format) buffer.format = conflict.format;
              buffer.revision = conflict.revision;
              buffer.text = buffer.saved = conflict.text;
              buffer.editorState = undefined;
              buffer.reset = (buffer.reset || 0) + 1;
              buffer.conflict = undefined;
              buffer.error = "";
              buffer.status = "Saved";
              studio.notify();
              void studio.persist();
            }}
          >
             {tr("Use saved version")} </button>
          <button onClick={() => setCopy(true)}>{tr("Save as new file")}</button>
          <button
            className="primary"
            onClick={async () => {
              try {
                const current = await studio.snapshot(
                  buffer.library,
                  buffer.entry.path,
                );
                if (
                  JSON.stringify(current.entry) !==
                    JSON.stringify(conflict.entry) ||
                  current.revision !== conflict.revision
                )
                  throw new Error(
                    "The target changed again. Retry saving to review its latest content.",
                  );
                if (!current.entry)
                  throw new Error("The file was deleted. Save as a new file.");
                buffer.entry = current.entry;
                buffer.revision = current.revision;
                buffer.conflict = undefined;
                await studio.save(buffer);
              } catch (e) {
                onError(String(e));
                setOpen(false);
              }
            }}
          >
             {tr("Overwrite reviewed version")} </button>
        </div>
      </Modal>
      {copy && (
        <TextDialog
          title={tr("Save as new file")}
          label={tr("Path relative to library root")}
          initial={buffer.entry.path + ".copy"}
          onClose={() => setCopy(false)}
          onSubmit={async (path) => {
            await studio.queue.run(`${buffer.library}:${buffer.branch}`, async () => {
              const target = await studio.snapshot(buffer.library, path,buffer.branch);
              if (target.entry) throw new Error("That path already exists.");
              await studio.write(
                `${branchRoot(buffer.library,buffer.branch)}/draft/text`,
                {
                  request_id: requestID(),
                  expected_revision: target.revision,
                  expected_head:target.head,
                  path,
                  text: encodeText(buffer.text, buffer.format),
                },
              );
            });
            buffer.saved = buffer.text;
            buffer.conflict = undefined;
            buffer.error = "";
            await studio.close(buffer);
            studio.changed();
          }}
        />
      )}
    </>
  );
}
function MoveDialog({
  branch,
  entry,
  onClose,
  onMove,
}: {
  branch: Branch;
  entry: Entry;
  onClose: () => void;
  onMove: (to: string) => Promise<void>;
}) {
  const tr = useT();
  const [parent, setParent] = useState(""),
    [page, setPage] = useState<Page<Entry> | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    void branchAPI.tree(branch, parent, "", abort.signal)
      .then(value => ({items:value.entries,next:value.next ?? "",revision:value.revision,total:value.descendant_count}))
      .then(setPage)
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [branch, parent]);
  return (
    <Modal title={tr("Move {value0}", { value0: entry.path })} open onClose={onClose}>
      <p>{tr("Destination: /")}{parent}</p>
      {parent && (
        <button onClick={() => setParent(parentPath(parent))}>
           {tr("Parent directory")} </button>
      )}
      <div className="picker-results">
        {page?.items
          .filter(
            (e) =>
              e.kind === "directory" &&
              e.path !== entry.path &&
              !e.path.startsWith(entry.path + "/"),
          )
          .map((e) => (
            <button key={e.path} onClick={() => setParent(e.path)}>
              ▸ {e.path.split("/").at(-1)}
            </button>
          ))}
        {page?.next && (
          <button
            onClick={() =>
              void branchAPI.tree({...branch,draft_revision:page.revision}, parent, page.next)
                .then(value => ({items:value.entries,next:value.next ?? "",revision:value.revision,total:value.descendant_count}))
                .then((p) =>
                  setPage({ ...p, items: [...page.items, ...p.items] }),
                )
                .catch((e) => setError(String(e)))
            }
          >
             {tr("Load more folders")} </button>
        )}
      </div>
      {error && (
        <p role="alert" className="error">
          {tr(error)}
        </p>
      )}
      <div className="dialog-actions">
        <button onClick={onClose}>{tr("Cancel")}</button>
        <button
          className="primary"
          onClick={async () => {
            try {
              await onMove(
                [parent, entry.path.split("/").at(-1)]
                  .filter(Boolean)
                  .join("/"),
              );
              onClose();
            } catch (e) {
              setError(String(e));
            }
          }}
        >
           {tr("Move here")} </button>
      </div>
    </Modal>
  );
}
