"use client";

import { useEffect, useState } from "react";
import { Download, ExternalLink, Loader2, Pencil, RotateCw, Save, X } from "lucide-react";
import { toast } from "sonner";

import { buildArtifactHref, fetchArtifactText, writeArtifact } from "@/lib/api";
import { formatBytes, formatRelative } from "@/lib/format";
import { classifyArtifact, type ArtifactClassification } from "@/lib/mime";
import { MarkdownView } from "./markdown-view";
import { CodeView } from "./viewers/code-view";
import { CodeEditor } from "./viewers/code-editor";
import type { ApiArtifact } from "@/lib/types";

type Props = { sessionId: string; path: string; artifact?: ApiArtifact | null };

// Files at or below this size may be edited inline. Larger files stay read-only
// to keep the editor responsive (the read path already caps text fetch at 2MB).
const MAX_EDIT_BYTES = 1024 * 1024;
const EDITABLE_KINDS = new Set(["text", "code", "json", "markdown"]);

type LoadState =
  | { phase: "skip" }
  | { phase: "loading" }
  | { phase: "ok"; text: string; lastModified: string | null }
  | { phase: "error"; error: string };

type EditState =
  | { phase: "off" }
  | { phase: "editing"; text: string; dirty: boolean }
  | { phase: "saving"; text: string }
  | { phase: "stale"; text: string };

export function ArtifactViewer({ sessionId, path, artifact }: Props) {
  const meta = classifyArtifact(path);
  const href = buildArtifactHref(sessionId, path);
  const skip = meta.kind === "binary" || meta.kind === "image" || meta.kind === "pdf";
  const [load, setLoad] = useState<LoadState>(skip ? { phase: "skip" } : { phase: "loading" });
  const [edit, setEdit] = useState<EditState>({ phase: "off" });

  // Fetch text on mount / when the file identity changes. The parent remounts
  // this viewer on path + updated_at change (its key), so this effect fetches
  // once per mount; state is only written from the async callback.
  useEffect(() => {
    if (skip) return;
    let cancelled = false;
    void fetchArtifactText(sessionId, path).then((res) => {
      if (cancelled) return;
      setLoad(
        res.ok
          ? { phase: "ok", text: res.text, lastModified: res.lastModified ?? null }
          : { phase: "error", error: res.error }
      );
    });
    return () => {
      cancelled = true;
    };
  }, [sessionId, path, artifact?.updated_at, skip]);

  // User-triggered re-fetch (stale-edit reload); safe to set loading here since
  // it runs from an event handler, not during an effect body.
  const loadText = () => {
    setLoad({ phase: "loading" });
    void fetchArtifactText(sessionId, path).then((res) => {
      setLoad(
        res.ok
          ? { phase: "ok", text: res.text, lastModified: res.lastModified ?? null }
          : { phase: "error", error: res.error }
      );
    });
  };

  const editable =
    EDITABLE_KINDS.has(meta.kind) &&
    load.phase === "ok" &&
    (!artifact || artifact.size <= MAX_EDIT_BYTES);

  const startEditing = () => {
    if (load.phase !== "ok") return;
    setEdit({ phase: "editing", text: load.text, dirty: false });
  };

  const cancelEditing = () => setEdit({ phase: "off" });

  const reloadStale = () => {
    setEdit({ phase: "off" });
    loadText();
  };

  const save = async () => {
    if (edit.phase !== "editing") return;
    const text = edit.text;
    setEdit({ phase: "saving", text });
    // The optimistic-concurrency token is the Last-Modified header the download
    // emitted, echoed back as If-Unmodified-Since.
    const lastModified = load.phase === "ok" ? load.lastModified ?? undefined : undefined;
    const result = await writeArtifact(sessionId, path, text, lastModified);
    if (result.ok) {
      toast.success(`Saved ${path.split("/").pop()}`);
      // The file re-renders via the artifact.updated SSE (key includes
      // updated_at); leave edit mode so the fresh read shows.
      setEdit({ phase: "off" });
      return;
    }
    if (result.stale) {
      setEdit({ phase: "stale", text });
      return;
    }
    if (result.busy) {
      toast.error("Can't save while the agent is working. Try again once the turn finishes.");
      setEdit({ phase: "editing", text, dirty: true });
      return;
    }
    if (result.status === 413) {
      toast.error("File is too large to save (limit 1 MiB).");
      setEdit({ phase: "editing", text, dirty: true });
      return;
    }
    toast.error(`Save failed: ${result.error}`);
    setEdit({ phase: "editing", text, dirty: true });
  };

  const editorText = edit.phase !== "off" ? edit.text : null;
  const saving = edit.phase === "saving";

  return (
    <div className="flex h-full min-h-0 flex-col bg-[var(--color-background)]">
      <ViewerHeader
        path={path}
        href={href}
        artifact={artifact}
        editable={editable}
        editState={edit}
        onEdit={startEditing}
        onCancel={cancelEditing}
        onSave={save}
      />
      {edit.phase === "stale" ? (
        <div className="flex items-center justify-between gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 text-xs text-[var(--color-danger)]">
          <span>File changed by the agent — reload to get the latest.</span>
          <button
            type="button"
            onClick={reloadStale}
            className="flex shrink-0 items-center gap-1 rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1 text-[var(--color-foreground)] hover:bg-[var(--color-surface)]"
          >
            <RotateCw className="h-3 w-3" /> Reload
          </button>
        </div>
      ) : null}
      <div className="min-h-0 flex-1 overflow-hidden">
        {editorText !== null ? (
          <CodeEditor
            path={path}
            initialText={editorText}
            onChange={(text) =>
              setEdit((prev) =>
                prev.phase === "editing" ? { phase: "editing", text, dirty: true } : prev
              )
            }
          />
        ) : (
          <ViewerBody path={path} href={href} meta={meta} load={load} />
        )}
      </div>
      {saving ? (
        <div className="border-t border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-[10px] text-[var(--color-foreground-muted)]">
          Saving…
        </div>
      ) : null}
    </div>
  );
}

function ViewerHeader({
  path,
  href,
  artifact,
  editable,
  editState,
  onEdit,
  onCancel,
  onSave
}: {
  path: string;
  href: string;
  artifact?: ApiArtifact | null;
  editable: boolean;
  editState: EditState;
  onEdit: () => void;
  onCancel: () => void;
  onSave: () => void;
}) {
  const editing = editState.phase === "editing" || editState.phase === "saving" || editState.phase === "stale";
  const saving = editState.phase === "saving";
  return (
    <div className="flex min-h-[42px] items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="truncate font-mono text-xs">{path}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-[var(--color-foreground-muted)]">
          {artifact ? (
            <>
              <span>{formatBytes(artifact.size)}</span>
              <span aria-hidden>·</span>
              <span>{formatRelative(artifact.updated_at)}</span>
            </>
          ) : (
            <span>Artifact metadata unavailable</span>
          )}
        </div>
      </div>
      {editing ? (
        <>
          <button
            type="button"
            onClick={onSave}
            disabled={saving || editState.phase === "stale"}
            className="flex h-8 items-center gap-1.5 rounded-[var(--radius)] bg-[var(--color-accent,var(--color-foreground))] px-2.5 text-xs text-[var(--color-background)] hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"
            title="Save"
          >
            <Save className="h-3.5 w-3.5" /> Save
          </button>
          <button
            type="button"
            onClick={onCancel}
            disabled={saving}
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)] disabled:opacity-40"
            title="Cancel edit"
          >
            <X className="h-4 w-4" />
          </button>
        </>
      ) : (
        <>
          {editable ? (
            <button
              type="button"
              onClick={onEdit}
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
              title="Edit"
            >
              <Pencil className="h-4 w-4" />
            </button>
          ) : null}
          <a
            href={href}
            target="_blank"
            rel="noreferrer"
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
            title="Open raw"
          >
            <ExternalLink className="h-4 w-4" />
          </a>
          <a
            href={href}
            download
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius)] text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-foreground)]"
            title="Download"
          >
            <Download className="h-4 w-4" />
          </a>
        </>
      )}
    </div>
  );
}

function ViewerBody({
  path,
  href,
  meta,
  load
}: {
  path: string;
  href: string;
  meta: ArtifactClassification;
  load: LoadState;
}) {
  if (meta.kind === "image") {
    return (
      <div className="flex h-full items-center justify-center overflow-auto bg-[var(--color-surface)] p-4">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src={href}
          alt={path}
          className="max-h-full max-w-full rounded-[var(--radius)] border border-[var(--color-border)]"
        />
      </div>
    );
  }

  if (meta.kind === "pdf") {
    return (
      <iframe
        src={href}
        title={path}
        className="h-full w-full border-0 bg-[var(--color-background)]"
      />
    );
  }

  if (meta.kind === "binary") {
    return (
      <div className="flex h-full items-center justify-center px-6 text-center">
        <div>
          <p className="text-sm">Binary file preview is not supported.</p>
          <p className="mt-1 text-xs text-[var(--color-foreground-muted)]">
            Use the header actions to open or download the raw bytes.
          </p>
        </div>
      </div>
    );
  }

  if (load.phase === "loading") {
    return (
      <div className="flex h-full items-center justify-center text-[var(--color-foreground-muted)]">
        <Loader2 className="h-4 w-4 animate-spin" />
      </div>
    );
  }
  if (load.phase === "error") {
    return <div className="px-6 py-4 text-sm text-[var(--color-danger)]">{load.error}</div>;
  }
  if (load.phase !== "ok") return null;

  if (meta.kind === "markdown") {
    return (
      <div className="h-full overflow-auto px-6 py-4">
        <MarkdownView content={load.text} />
      </div>
    );
  }
  if (meta.kind === "json") {
    return <JsonPreview text={load.text} />;
  }
  if (meta.kind === "table") {
    return <DelimitedTable text={load.text} delimiter={meta.delimiter ?? ","} />;
  }
  if (meta.kind === "code") {
    return <CodeView code={load.text} language={meta.language} />;
  }
  return (
    <pre className="h-full overflow-auto whitespace-pre-wrap break-words bg-[var(--color-code-bg)] px-6 py-4 font-mono text-xs leading-relaxed text-[var(--color-code-ink)]">
      {load.text}
    </pre>
  );
}

function JsonPreview({ text }: { text: string }) {
  let formatted: string | null = null;
  try {
    formatted = JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    formatted = null;
  }
  if (formatted !== null) {
    return <CodeView code={formatted} language="json" />;
  }
  return (
    <div className="h-full min-h-0">
      <div className="border-b border-[var(--color-border)] bg-[var(--color-surface-muted)] px-4 py-2 text-xs text-[var(--color-danger)]">
        Invalid JSON. Showing raw text.
      </div>
      <pre className="h-[calc(100%-37px)] overflow-auto whitespace-pre-wrap break-words bg-[var(--color-code-bg)] px-6 py-4 font-mono text-xs leading-relaxed text-[var(--color-code-ink)]">
        {text}
      </pre>
    </div>
  );
}

function DelimitedTable({ text, delimiter }: { text: string; delimiter: "," | "\t" }) {
  const rows = parseDelimited(text, delimiter);
  if (rows.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-[var(--color-foreground-muted)]">
        No rows to preview.
      </div>
    );
  }

  const [head, ...body] = rows;
  const previewRows = body.slice(0, 200);

  return (
    <div className="h-full overflow-auto">
      <table className="min-w-full border-collapse text-left text-xs">
        <thead className="sticky top-0 z-[1] bg-[var(--color-surface)]">
          <tr>
            {head.map((cell, index) => (
              <th
                key={`${index}:${cell}`}
                className="max-w-[220px] border-b border-r border-[var(--color-border)] px-3 py-2 font-medium"
              >
                <span className="block truncate" title={cell || `Column ${index + 1}`}>
                  {cell || `Column ${index + 1}`}
                </span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {previewRows.map((row, rowIndex) => (
            <tr key={rowIndex} className="odd:bg-[var(--color-background)] even:bg-[var(--color-surface)]">
              {head.map((_, cellIndex) => {
                const cell = row[cellIndex] ?? "";
                return (
                  <td
                    key={cellIndex}
                    className="max-w-[260px] border-b border-r border-[var(--color-border)] px-3 py-1.5 align-top"
                  >
                    <span className="block truncate" title={cell}>
                      {cell}
                    </span>
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {body.length > previewRows.length ? (
        <div className="border-t border-[var(--color-border)] px-4 py-2 text-xs text-[var(--color-foreground-muted)]">
          Showing first {previewRows.length} rows of {body.length}.
        </div>
      ) : null}
    </div>
  );
}

function parseDelimited(text: string, delimiter: "," | "\t") {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;

  const pushField = () => {
    row.push(field);
    field = "";
  };
  const pushRow = () => {
    pushField();
    rows.push(row);
    row = [];
  };

  for (let i = 0; i < text.length; i += 1) {
    const char = text[i];
    if (quoted) {
      if (char === '"' && text[i + 1] === '"') {
        field += '"';
        i += 1;
      } else if (char === '"') {
        quoted = false;
      } else {
        field += char;
      }
      continue;
    }

    if (char === '"') {
      quoted = true;
    } else if (char === delimiter) {
      pushField();
    } else if (char === "\n") {
      pushRow();
    } else if (char !== "\r") {
      field += char;
    }
  }

  if (field !== "" || row.length > 0) {
    pushRow();
  }

  return rows;
}
