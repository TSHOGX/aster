"use client";

import { useCallback, useMemo, useRef, useState } from "react";
import {
  Braces,
  ChevronRight,
  Code2,
  Download,
  File,
  FileText,
  Folder,
  FolderOpen,
  FolderUp,
  Image as ImageIcon,
  Search,
  Table2,
  Upload
} from "lucide-react";
import { toast } from "sonner";

import { useArtifacts, useSelectedSession } from "@/components/harness-provider";
import { Tabs } from "@/components/ui/tabs";
import { ArtifactViewer } from "./artifact-viewer";
import { buildArtifactHref, uploadArtifacts, type UploadEntry } from "@/lib/api";
import { buildArtifactTree, type ArtifactTreeDirectory, type ArtifactTreeNode } from "@/lib/artifact-tree";
import { entriesFromDataTransfer, entriesFromFileList, isSessionWritable } from "@/lib/upload-entries";
import { formatBytes, formatRelative } from "@/lib/format";
import { classifyArtifact } from "@/lib/mime";
import { cn } from "@/lib/cn";
import type { ApiArtifact, SessionStatus } from "@/lib/types";

const MAX_TABS = 6;
// Backend-enforced upload limits, mirrored client-side for fast friendly errors.
const MAX_UPLOAD_BYTES = 100 * 1024 * 1024; // 100 MiB per file
const MAX_UPLOAD_FILES = 2000;

export function ArtifactPane() {
  const session = useSelectedSession();
  const artifacts = useArtifacts(session?.id ?? null);

  if (!session) {
    return (
      <aside className="flex h-full min-h-0 min-w-0 flex-col overflow-hidden border-l border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3 text-xs text-[var(--color-foreground-muted)]">
        <span>Files will appear here once a session is running.</span>
      </aside>
    );
  }
  return (
    <ArtifactPaneInner
      key={session.id}
      sessionId={session.id}
      status={session.status}
      artifacts={artifacts}
    />
  );
}

type UploadState =
  | { phase: "idle" }
  | { phase: "uploading"; count: number; fraction: number };

function ArtifactPaneInner({
  sessionId,
  status,
  artifacts
}: {
  sessionId: string;
  status: SessionStatus;
  artifacts: ApiArtifact[];
}) {
  const [openTabs, setOpenTabs] = useState<string[]>([]);
  const [activeTab, setActiveTab] = useState<string | null>(null);
  const [collapsedDirs, setCollapsedDirs] = useState<Set<string>>(() => new Set());
  const [query, setQuery] = useState("");
  const [upload, setUpload] = useState<UploadState>({ phase: "idle" });
  const [dragging, setDragging] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);

  const writable = isSessionWritable(status);
  const busy = upload.phase === "uploading";
  const canUpload = writable && !busy;

  const runUpload = useCallback(
    async (entries: UploadEntry[]) => {
      if (entries.length === 0) return;
      if (!isSessionWritable(status)) {
        toast.error("Can't upload while the agent is working. Try again once the turn finishes.");
        return;
      }
      // Client-side pre-checks mirroring backend limits (100 MiB/file, 2000 files).
      if (entries.length > MAX_UPLOAD_FILES) {
        toast.error(`Too many files (limit ${MAX_UPLOAD_FILES}). Select fewer files.`);
        return;
      }
      const oversize = entries.find((e) => e.file.size > MAX_UPLOAD_BYTES);
      if (oversize) {
        toast.error(`${oversize.relPath.split("/").pop()} is larger than 100 MiB.`);
        return;
      }
      setUpload({ phase: "uploading", count: entries.length, fraction: 0 });
      const label = entries.length === 1 ? entries[0].relPath.split("/").pop() : `${entries.length} files`;
      const result = await uploadArtifacts(sessionId, entries, (fraction) => {
        setUpload((prev) => (prev.phase === "uploading" ? { ...prev, fraction } : prev));
      });
      setUpload({ phase: "idle" });
      if (result.ok) {
        const n = result.uploaded.length || entries.length;
        toast.success(`Uploaded ${n === 1 ? label : `${n} files`}`);
        return;
      }
      if (result.errorClass === "turn_in_progress") {
        toast.error("Can't upload while the agent is working. Try again once the turn finishes.");
      } else if (result.status === 413) {
        toast.error("Upload exceeds the size or file-count limit.");
      } else {
        toast.error(`Upload failed: ${result.error}`);
      }
    },
    [sessionId, status]
  );

  const onDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
      if (!isSessionWritable(status)) {
        toast.error("Can't upload while the agent is working. Try again once the turn finishes.");
        return;
      }
      void entriesFromDataTransfer(event.dataTransfer)
        .then((entries) => runUpload(entries))
        .catch(() => toast.error("Couldn't read the dropped files."));
    },
    [runUpload, status]
  );

  const onDragEnter = useCallback(
    (event: React.DragEvent) => {
      if (!Array.from(event.dataTransfer.types).includes("Files")) return;
      dragDepth.current += 1;
      if (canUpload) setDragging(true);
    },
    [canUpload]
  );

  const onDragLeave = useCallback(() => {
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  }, []);

  const artifactPaths = useMemo(() => new Set(artifacts.map((a) => a.path)), [artifacts]);
  const liveOpenTabs = useMemo(
    () => openTabs.filter((path) => artifactPaths.has(path)),
    [artifactPaths, openTabs]
  );
  const liveActiveTab = activeTab && artifactPaths.has(activeTab) ? activeTab : null;
  const visibleArtifacts = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return artifacts;
    return artifacts.filter((a) => a.path.toLowerCase().includes(needle));
  }, [artifacts, query]);
  const tree = useMemo(() => buildArtifactTree(visibleArtifacts), [visibleArtifacts]);
  const activeArtifact = liveActiveTab ? artifacts.find((a) => a.path === liveActiveTab) ?? null : null;

  const openArtifact = (path: string) => {
    setOpenTabs((prev) => {
      if (prev.includes(path)) return prev;
      const next = [...prev, path];
      return next.length > MAX_TABS ? next.slice(next.length - MAX_TABS) : next;
    });
    setActiveTab(path);
  };

  const closeTab = (path: string) => {
    const idx = liveOpenTabs.indexOf(path);
    const remaining = liveOpenTabs.filter((p) => p !== path);
    setOpenTabs(remaining);
    setActiveTab((prev) => {
      if (prev !== path) return prev;
      return remaining[Math.min(idx, remaining.length - 1)] ?? null;
    });
  };

  const toggleDirectory = (path: string) => {
    setCollapsedDirs((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  return (
    <aside
      className="relative flex h-full min-h-0 min-w-0 flex-col overflow-hidden border-l border-[var(--color-border)] bg-[var(--color-surface)]"
      onDragEnter={onDragEnter}
      onDragOver={(e) => {
        if (Array.from(e.dataTransfer.types).includes("Files")) e.preventDefault();
      }}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => {
          if (e.target.files) void runUpload(entriesFromFileList(e.target.files));
          e.target.value = "";
        }}
      />
      <input
        ref={folderInputRef}
        type="file"
        multiple
        // @ts-expect-error non-standard directory-picker attributes
        webkitdirectory=""
        directory=""
        className="hidden"
        onChange={(e) => {
          if (e.target.files) void runUpload(entriesFromFileList(e.target.files));
          e.target.value = "";
        }}
      />
      <div className="shrink-0 border-b border-[var(--color-border)] px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <span className="text-xs uppercase tracking-wider text-[var(--color-foreground-muted)]">
            Files {artifacts.length > 0 ? `(${artifacts.length})` : ""}
          </span>
          <div className="flex shrink-0 items-center gap-1.5">
            {artifacts.length > 0 ? (
              <span className="text-[11px] text-[var(--color-foreground-muted)]">
                {formatBytes(tree.size)}
              </span>
            ) : null}
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={!canUpload}
              title={writable ? "Upload files" : "Uploads are paused while the agent is working"}
              className="flex h-7 w-7 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)] disabled:cursor-not-allowed disabled:opacity-40"
            >
              <Upload className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              onClick={() => folderInputRef.current?.click()}
              disabled={!canUpload}
              title={writable ? "Upload folder" : "Uploads are paused while the agent is working"}
              className="flex h-7 w-7 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)] disabled:cursor-not-allowed disabled:opacity-40"
            >
              <FolderUp className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
        {busy ? (
          <div className="mt-2">
            <div className="mb-1 flex items-center justify-between text-[10px] text-[var(--color-foreground-muted)]">
              <span>Uploading {upload.count === 1 ? "1 file" : `${upload.count} files`}…</span>
              <span>{Math.round(upload.fraction * 100)}%</span>
            </div>
            <div className="h-1 overflow-hidden rounded-full bg-[var(--color-surface-muted)]">
              <div
                className="h-full bg-[var(--color-accent,var(--color-foreground))] transition-[width]"
                style={{ width: `${Math.round(upload.fraction * 100)}%` }}
              />
            </div>
          </div>
        ) : !writable ? (
          <p className="mt-2 text-[10px] text-[var(--color-foreground-muted)]">
            Uploads paused while the agent is working.
          </p>
        ) : null}
        {artifacts.length > 0 ? (
          <label className="mt-2 flex h-8 items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-2 text-xs">
            <Search className="h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)]" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search files"
              className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-[var(--color-foreground-muted)]"
            />
          </label>
        ) : null}
      </div>

      <div className="min-h-[144px] max-h-[42%] shrink-0 overflow-y-auto border-b border-[var(--color-border)]">
        {artifacts.length === 0 ? (
          <p className="px-4 py-4 text-xs text-[var(--color-foreground-muted)]">
            No artifacts yet. The agent writes files to <code className="font-mono">/workspace</code>.
          </p>
        ) : visibleArtifacts.length === 0 ? (
          <p className="px-4 py-4 text-xs text-[var(--color-foreground-muted)]">No matching files.</p>
        ) : (
          <ArtifactTreeView
            root={tree}
            collapsedDirs={collapsedDirs}
            forceExpanded={query.trim().length > 0}
            activePath={liveActiveTab}
            onToggleDirectory={toggleDirectory}
            onOpenArtifact={openArtifact}
          />
        )}
      </div>

      <div className="flex flex-1 min-h-0 flex-col">
        {liveOpenTabs.length > 0 ? (
          <Tabs
            value={liveActiveTab}
            onValueChange={setActiveTab}
            items={liveOpenTabs.map((path) => ({
              value: path,
              label: path.split("/").pop() ?? path,
              onClose: () => closeTab(path)
            }))}
          />
        ) : null}
        <div className="flex-1 min-h-0 overflow-hidden">
          {liveActiveTab && activeArtifact ? (
            <ArtifactViewer
              key={`${liveActiveTab}:${activeArtifact.updated_at}`}
              sessionId={sessionId}
              path={liveActiveTab}
              artifact={activeArtifact}
            />
          ) : (
            <div className="flex h-full items-center justify-center text-center text-xs text-[var(--color-foreground-muted)]">
              <span>Select a file from the tree above to preview it here.</span>
            </div>
          )}
        </div>
      </div>

      {dragging ? (
        <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center border-2 border-dashed border-[var(--color-accent,var(--color-foreground))] bg-[var(--color-background)]/85">
          <div className="flex flex-col items-center gap-2 text-xs text-[var(--color-foreground)]">
            <Upload className="h-5 w-5" />
            <span>Drop files or folders to upload</span>
          </div>
        </div>
      ) : null}
    </aside>
  );
}

function ArtifactTreeView({
  root,
  collapsedDirs,
  forceExpanded,
  activePath,
  onToggleDirectory,
  onOpenArtifact
}: {
  root: ArtifactTreeDirectory;
  collapsedDirs: Set<string>;
  forceExpanded: boolean;
  activePath: string | null;
  onToggleDirectory: (path: string) => void;
  onOpenArtifact: (path: string) => void;
}) {
  return (
    <ul className="py-1">
      {root.children.map((node) => (
        <TreeNode
          key={`${node.kind}:${node.path}`}
          node={node}
          depth={0}
          collapsedDirs={collapsedDirs}
          forceExpanded={forceExpanded}
          activePath={activePath}
          onToggleDirectory={onToggleDirectory}
          onOpenArtifact={onOpenArtifact}
        />
      ))}
    </ul>
  );
}

function TreeNode({
  node,
  depth,
  collapsedDirs,
  forceExpanded,
  activePath,
  onToggleDirectory,
  onOpenArtifact
}: {
  node: ArtifactTreeNode;
  depth: number;
  collapsedDirs: Set<string>;
  forceExpanded: boolean;
  activePath: string | null;
  onToggleDirectory: (path: string) => void;
  onOpenArtifact: (path: string) => void;
}) {
  const indent = 8 + depth * 14;

  if (node.kind === "directory") {
    const expanded = forceExpanded || !collapsedDirs.has(node.path);
    return (
      <li>
        <button
          className="flex h-7 w-full items-center gap-1.5 px-2 pr-3 text-left text-xs hover:bg-[var(--color-surface-muted)]"
          style={{ paddingLeft: indent }}
          onClick={() => onToggleDirectory(node.path)}
          aria-expanded={expanded}
        >
          <ChevronRight
            className={cn(
              "h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)] transition-transform",
              expanded && "rotate-90"
            )}
          />
          {expanded ? (
            <FolderOpen className="h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)]" />
          ) : (
            <Folder className="h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)]" />
          )}
          <span className="min-w-0 flex-1 truncate font-mono">{node.name}</span>
          <span className="shrink-0 text-[10px] text-[var(--color-foreground-muted)]">
            {node.fileCount} · {formatBytes(node.size)}
          </span>
        </button>
        {expanded ? (
          <ul>
            {node.children.map((child) => (
              <TreeNode
                key={`${child.kind}:${child.path}`}
                node={child}
                depth={depth + 1}
                collapsedDirs={collapsedDirs}
                forceExpanded={forceExpanded}
                activePath={activePath}
                onToggleDirectory={onToggleDirectory}
                onOpenArtifact={onOpenArtifact}
              />
            ))}
          </ul>
        ) : null}
      </li>
    );
  }

  const active = activePath === node.path;
  return (
    <li>
      <div
        className={cn(
          "group flex h-7 items-center gap-1.5 px-2 pr-2 text-xs hover:bg-[var(--color-surface-muted)]",
          active && "bg-[var(--color-surface-muted)]"
        )}
        style={{ paddingLeft: indent + 21 }}
      >
        <button
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
          onClick={() => onOpenArtifact(node.path)}
          title={node.path}
        >
          <ArtifactIcon path={node.path} />
          <span className="min-w-0 flex-1 truncate font-mono">{node.name}</span>
          <span className="shrink-0 text-[10px] text-[var(--color-foreground-muted)]">
            {formatBytes(node.artifact.size)} · {formatRelative(node.artifact.updated_at)}
          </span>
        </button>
        <a
          href={buildArtifactHref(node.artifact.session_id, node.path)}
          download
          className="shrink-0 text-[var(--color-foreground-muted)] opacity-70 hover:text-[var(--color-foreground)] group-hover:opacity-100"
          title="Download"
        >
          <Download className="h-3.5 w-3.5" />
        </a>
      </div>
    </li>
  );
}

function ArtifactIcon({ path }: { path: string }) {
  const kind = classifyArtifact(path).kind;
  const className = "h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)]";
  switch (kind) {
    case "image":
      return <ImageIcon className={className} />;
    case "code":
      return <Code2 className={className} />;
    case "json":
      return <Braces className={className} />;
    case "table":
      return <Table2 className={className} />;
    case "markdown":
    case "text":
      return <FileText className={className} />;
    default:
      return <File className={className} />;
  }
}
