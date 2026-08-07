"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ChevronRight,
  Download,
  Folder,
  FolderOpen,
  FolderUp,
  Search,
  Upload
} from "lucide-react";
import { toast } from "sonner";

import { useArtifacts, useLastReply, useSelectedSession } from "@/components/harness-provider";
import { InputWithIcon } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ArtifactViewer } from "./artifact-viewer";
import { ArtifactIcon } from "./artifact-icon";
import { MentionedFiles, SectionHeader, useMentionedPaths } from "./mentioned-files";
import { buildArtifactHref, uploadArtifacts, type UploadEntry } from "@/lib/api";
import { buildArtifactTree, type ArtifactTreeDirectory, type ArtifactTreeNode } from "@/lib/artifact-tree";
import { entriesFromDataTransfer, entriesFromFileList, isSessionWritable } from "@/lib/upload-entries";
import { formatBytes, formatRelative } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n";
import { brandName, uploadUiEnabled } from "@/lib/ui-config";
import type { ApiArtifact, SessionStatus } from "@/lib/types";

// Backend-enforced upload limits, mirrored client-side for fast friendly errors.
const MAX_UPLOAD_BYTES = 100 * 1024 * 1024; // 100 MiB per file
const MAX_UPLOAD_FILES = 2000;
// Debounce the file-search filtering (mirrors the command palette) so the tree
// isn't refiltered + rebuilt on every keystroke.
const SEARCH_DEBOUNCE_MS = 150;

type ArtifactPaneProps = {
  onExpandedChange?: (expanded: boolean) => void;
};

export function ArtifactPane({ onExpandedChange }: ArtifactPaneProps) {
  const t = useT();
  const session = useSelectedSession();
  const artifacts = useArtifacts(session?.id ?? null);
  // Only the turn's final reply, so a streamed token does not re-render the tree.
  const lastReply = useLastReply(session?.id ?? null);

  if (!session) {
    return (
      <aside className="flex h-full min-h-0 min-w-0 flex-col items-center justify-center overflow-hidden border-l border-[var(--color-border)] bg-[var(--color-surface)]">
        <EmptyState
          icon={<Folder className="h-5 w-5" />}
          title={t("workspace.noFilesTitle")}
          description={t("workspace.noFilesBody")}
        />
      </aside>
    );
  }
  return (
    <ArtifactPaneInner
      key={session.id}
      sessionId={session.id}
      status={session.status}
      readOnly={session.access === "reader"}
      artifacts={artifacts}
      // A shell session has no reply: its entries are command output, written
      // with an empty stop_reason, which the structural fallback would read as a
      // final reply. Nothing there is prose that mentions a file.
      lastReplyContent={session.mode === "shell" ? "" : lastReply?.content ?? ""}
      onExpandedChange={onExpandedChange}
    />
  );
}

type UploadState =
  | { phase: "idle" }
  | { phase: "uploading"; count: number; fraction: number };

function ArtifactPaneInner({
  sessionId,
  status,
  readOnly = false,
  artifacts,
  lastReplyContent,
  onExpandedChange
}: {
  sessionId: string;
  status: SessionStatus;
  // True for a session shared with the caller. Reads work (the read gate admits
  // a grant holder); uploads and edits are owner-only and would 404, so the
  // entry points are withheld rather than left to fail.
  readOnly?: boolean;
  artifacts: ApiArtifact[];
  /** Raw markdown of the last committed final reply; "" when there is none. */
  lastReplyContent: string;
  onExpandedChange?: (expanded: boolean) => void;
}) {
  const t = useT();
  // Artifacts-style: a single open file drives the Preview panel. Null → list view.
  const [openPath, setOpenPath] = useState<string | null>(null);
  // When true, the preview overlays the whole viewport instead of docking in the
  // right rail. Persists across file switches; reset once the preview closes.
  const [fullScreen, setFullScreen] = useState(false);
  const [collapsedDirs, setCollapsedDirs] = useState<Set<string>>(() => new Set());
  const [query, setQuery] = useState("");
  // `query` drives the input (updates immediately for a responsive field);
  // `debouncedQuery` drives the derived filtering + tree rebuild + force-expand
  // so typing a character doesn't re-filter all artifacts and rebuild the tree
  // on every keystroke. Mirrors the 150ms debounce in the command palette.
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [upload, setUpload] = useState<UploadState>({ phase: "idle" });
  const [dragging, setDragging] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);

  const writable = !readOnly && isSessionWritable(status);
  const busy = upload.phase === "uploading";
  // uploadUiEnabled is a build-time flag (default off): with it off there are no
  // upload buttons, no drop target, and no upload status text at all.
  const canUpload = uploadUiEnabled && writable && !busy;

  const runUpload = useCallback(
    async (entries: UploadEntry[]) => {
      if (entries.length === 0) return;
      if (!isSessionWritable(status)) {
        toast.error(t("workspace.uploadBusy"));
        return;
      }
      // Client-side pre-checks mirroring backend limits (100 MiB/file, 2000 files).
      if (entries.length > MAX_UPLOAD_FILES) {
        toast.error(t("workspace.tooManyFiles", { limit: MAX_UPLOAD_FILES }));
        return;
      }
      const oversize = entries.find((e) => e.file.size > MAX_UPLOAD_BYTES);
      if (oversize) {
        toast.error(t("workspace.fileTooLarge", { name: oversize.relPath.split("/").pop() ?? "" }));
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
        toast.success(n === 1 ? t("workspace.uploadedOne", { name: label ?? "" }) : t("workspace.uploadedMany", { count: n }));
        return;
      }
      if (result.errorClass === "turn_in_progress") {
        toast.error(t("workspace.uploadBusy"));
      } else if (result.status === 413) {
        toast.error(t("workspace.uploadLimit"));
      } else {
        toast.error(t("workspace.uploadFailedReason", { error: result.error }));
      }
    },
    [sessionId, status, t]
  );

  const onDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
      // Flag off: the drop is swallowed silently rather than uploading through a
      // path whose UI affordances were withheld.
      if (!uploadUiEnabled) return;
      if (readOnly) {
        toast.error(t("workspace.readOnlyShared"));
        return;
      }
      if (!isSessionWritable(status)) {
        toast.error(t("workspace.uploadBusy"));
        return;
      }
      void entriesFromDataTransfer(event.dataTransfer)
        .then((entries) => runUpload(entries))
        .catch(() => toast.error(t("workspace.dropReadFailed")));
    },
    [runUpload, status, readOnly, t]
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

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedQuery(query), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [query]);

  const artifactPaths = useMemo(() => new Set(artifacts.map((a) => a.path)), [artifacts]);
  const livePath = openPath && artifactPaths.has(openPath) ? openPath : null;
  const visibleArtifacts = useMemo(() => {
    const needle = debouncedQuery.trim().toLowerCase();
    if (!needle) return artifacts;
    return artifacts.filter((a) => a.path.toLowerCase().includes(needle));
  }, [artifacts, debouncedQuery]);
  const tree = useMemo(() => buildArtifactTree(visibleArtifacts), [visibleArtifacts]);
  // Whether the mentioned section will render, so the tree knows if it needs its
  // own group label. Asking the same hook the section uses keeps the two from
  // ever disagreeing about the count.
  const mentionedCount = useMentionedPaths(lastReplyContent, artifacts).length;
  const activeArtifact = livePath ? artifacts.find((a) => a.path === livePath) ?? null : null;

  // Keep the parent grid in sync: the docked panel widens whenever a live file
  // is open, but not while full-screen (the overlay covers the grid anyway, so
  // leaving the column at its normal width avoids a reflow behind the overlay).
  useEffect(() => {
    onExpandedChange?.(livePath !== null && !fullScreen);
  }, [livePath, fullScreen, onExpandedChange]);

  const openArtifact = (path: string) => setOpenPath(path);
  const closePreview = () => {
    setOpenPath(null);
    setFullScreen(false);
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

  const overlay = fullScreen && livePath !== null && activeArtifact !== null;

  return (
    <aside
      className={cn(
        "relative flex min-h-0 min-w-0 flex-col overflow-hidden border-l border-[var(--color-border)] bg-[var(--color-surface)]",
        overlay ? "fixed inset-0 z-40 h-dvh border-l-0" : "h-full"
      )}
      onDragEnter={onDragEnter}
      onDragOver={(e) => {
        if (Array.from(e.dataTransfer.types).includes("Files")) e.preventDefault();
      }}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {uploadUiEnabled ? (
        <>
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
        </>
      ) : null}
      {/* Rendered in both modes, but from two places rather than hoisted above
          the branch: in list mode it belongs *under* the Workspace header (it is
          a section of the file index, not a banner over it), and in preview mode
          the header is gone so it goes on top. Both call sites are in DOM order,
          so focus and screen-reader order match what is on screen — a CSS `order`
          on one shared node would not. Alongside an open file it doubles as a
          switcher between the turn's outputs; hidden in the full-screen overlay,
          which is a deliberate reading mode. */}
      {livePath && activeArtifact ? (
        // ── Preview mode: the panel becomes a full-height Artifact Preview. ──
        // The viewer sizes itself with h-full, so it needs a bounded flex track
        // rather than the aside directly: with the mentioned-files section as a
        // sibling above it, 100% of the aside would overflow by that section's
        // height and push the viewer's own footer out of view.
        <>
          {overlay ? null : (
            <MentionedFiles
              content={lastReplyContent}
              files={artifacts}
              activePath={livePath}
              onOpen={openArtifact}
            />
          )}
          <div className="flex min-h-0 flex-1 flex-col">
            <ArtifactViewer
              // Key on file identity only, NOT updated_at. Including updated_at
              // made every `artifact.updated` SSE for the open file change the
              // key → a full unmount/remount + redundant (up to 2MB) refetch on
              // each write while an agent streams the file, causing flicker.
              // ArtifactViewer's own effect (deps include artifact.updated_at)
              // already refetches the content on change, so the component can
              // stay mounted across updates.
              key={livePath}
              sessionId={sessionId}
              path={livePath}
              artifact={activeArtifact}
              readOnly={readOnly}
              fullScreen={fullScreen}
              onToggleFullScreen={() => setFullScreen((v) => !v)}
              onClose={closePreview}
            />
          </div>
        </>
      ) : (
        // ── List mode: workspace file browser. ──
        <>
          <div className="shrink-0 border-b border-[var(--color-border)] px-4 py-3">
            <div className="flex items-center justify-between gap-3">
              <div className="flex min-w-0 items-baseline gap-2">
                <span className="text-sm font-semibold">{t("workspace.title")}</span>
                {artifacts.length > 0 ? (
                  <span className="text-2xs text-[var(--color-foreground-muted)]">
                    {artifacts.length} · {formatBytes(tree.size)}
                  </span>
                ) : null}
              </div>
              <div className={readOnly || !uploadUiEnabled ? "hidden" : "flex shrink-0 items-center gap-1.5"}>
                <button
                  type="button"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={!canUpload}
                  title={writable ? t("workspace.uploadFiles") : t("workspace.uploadPaused")}
                  className="flex h-7 w-7 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)] disabled:cursor-not-allowed disabled:opacity-40"
                >
                  <Upload className="h-3.5 w-3.5" />
                </button>
                <button
                  type="button"
                  onClick={() => folderInputRef.current?.click()}
                  disabled={!canUpload}
                  title={writable ? t("workspace.uploadFolder") : t("workspace.uploadPaused")}
                  className="flex h-7 w-7 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)] disabled:cursor-not-allowed disabled:opacity-40"
                >
                  <FolderUp className="h-3.5 w-3.5" />
                </button>
              </div>
            </div>
            {busy ? (
              <div className="mt-2">
                <div className="mb-1 flex items-center justify-between text-2xs text-[var(--color-foreground-muted)]">
                  <span>{upload.count === 1 ? t("workspace.uploadingOne") : t("workspace.uploading", { count: upload.count })}</span>
                  <span>{Math.round(upload.fraction * 100)}%</span>
                </div>
                <div className="h-1 overflow-hidden rounded-full bg-[var(--color-surface-muted)]">
                  <div
                    className="h-full bg-[var(--color-accent,var(--color-foreground))] transition-[width]"
                    style={{ width: `${Math.round(upload.fraction * 100)}%` }}
                  />
                </div>
              </div>
            ) : readOnly ? (
              <p className="mt-2 text-2xs text-[var(--color-foreground-muted)]">
                Shared with you — read-only.
              </p>
            ) : uploadUiEnabled && !writable ? (
              <p className="mt-2 text-2xs text-[var(--color-foreground-muted)]">
                {t("workspace.uploadsPausedNote")}
              </p>
            ) : null}
            {artifacts.length > 0 ? (
              <InputWithIcon
                icon={<Search className="h-3.5 w-3.5" />}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t("workspace.searchFiles")}
                aria-label={t("workspace.searchFiles")}
                className="mt-2.5"
              />
            ) : null}
          </div>

          <MentionedFiles
            content={lastReplyContent}
            files={artifacts}
            activePath={livePath}
            onOpen={openArtifact}
          />

          <div className="min-h-0 flex-1 overflow-y-auto">
            {/* The tree only gets a label when something sits above it. Alone in
                the panel it is self-evidently "the files" and the Workspace
                header already says so; a lone group label on a single list is
                noise. With the mentioned section above, the pair needs to read as
                two groups of one list rather than two lists divided by a hairline
                that says nothing about what changed across it. */}
            {mentionedCount > 0 && visibleArtifacts.length > 0 ? (
              <SectionHeader labelKey="mentioned.allFiles" count={artifacts.length} />
            ) : null}
            {artifacts.length === 0 ? (
              <p className="px-4 py-4 text-xs text-[var(--color-foreground-muted)]">
                {t("workspace.empty", { brand: brandName })}
              </p>
            ) : visibleArtifacts.length === 0 ? (
              <p className="px-4 py-4 text-xs text-[var(--color-foreground-muted)]">{t("workspace.noMatches")}</p>
            ) : (
              <ArtifactTreeView
                root={tree}
                collapsedDirs={collapsedDirs}
                forceExpanded={debouncedQuery.trim().length > 0}
                activePath={null}
                onToggleDirectory={toggleDirectory}
                onOpenArtifact={openArtifact}
              />
            )}
          </div>
        </>
      )}

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
  const t = useT();
  const indent = 8 + depth * 14;

  if (node.kind === "directory") {
    const expanded = forceExpanded || !collapsedDirs.has(node.path);
    return (
      <li>
        <button
          className="flex h-7 w-full items-center gap-1.5 px-2 pr-3 text-left text-[13px] hover:bg-[var(--color-surface-muted)]"
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
          <span className="shrink-0 text-[11px] text-[var(--color-foreground-muted)]">
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
          // 13px rather than --text-xs (15px): the tree is a dense index of
          // names, and the mono face reads a size larger than the UI sans at the
          // same nominal size. The trailing size/mtime column follows it down.
          "group flex h-7 items-center gap-1.5 px-2 pr-2 text-[13px] hover:bg-[var(--color-surface-muted)]",
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
          <span className="shrink-0 text-[11px] text-[var(--color-foreground-muted)]">
            {formatBytes(node.artifact.size)} · {formatRelative(node.artifact.updated_at)}
          </span>
        </button>
        <a
          href={buildArtifactHref(node.artifact.session_id, node.path)}
          download
          className="shrink-0 text-[var(--color-foreground-muted)] transition-opacity hover:text-[var(--color-foreground)] focus-visible:opacity-100 group-hover:opacity-100 sm:opacity-0"
          title={t("workspace.download")}
        >
          <Download className="h-3.5 w-3.5" />
        </a>
      </div>
    </li>
  );
}

