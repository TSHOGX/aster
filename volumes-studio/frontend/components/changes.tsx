/* eslint-disable react-hooks/set-state-in-effect -- Hydrate browser navigation and synchronize external workspace snapshots. */
"use client";
import { useT } from "../lib/i18n";

import { useEffect, useRef, useState } from "react";
import { EditorView } from "@codemirror/view";
import { EditorState } from "@codemirror/state";
import { MergeView, unifiedMergeView } from "@codemirror/merge";
import { api, base, bytes, type Entry, type Library } from "../lib/api";
import { query } from "../lib/workbench";
import { useStudio } from "./studio-provider";
type Change = { path: string; change: string; before?: Entry; after?: Entry };
export function TextDiff({ before, after }: { before: string; after: string }) {
  const tr = useT();
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const node = host.current;
    if (!node) return;
    const media = matchMedia("(max-width: 767px)");
    let destroy = () => {};
    const render = () => {
      destroy();
      const extensions = [
        EditorState.readOnly.of(true),
        EditorView.editable.of(false),
        EditorView.lineWrapping,
        EditorView.theme({
          "&": {
            height: "100%",
            backgroundColor: "var(--surface)",
            color: "var(--foreground)",
          },
          ".cm-scroller": { fontFamily: "var(--font-mono)", overflow: "auto" },
          ".cm-gutters": {
            backgroundColor: "var(--surface)",
            color: "var(--foreground-muted)",
          },
        }),
      ];
      if (media.matches) {
        const v = new EditorView({
          parent: node,
          doc: after,
          extensions: [
            ...extensions,
            unifiedMergeView({ original: before, mergeControls: false }),
          ],
        });
        destroy = () => v.destroy();
      } else {
        const v = new MergeView({
          parent: node,
          a: { doc: before, extensions },
          b: { doc: after, extensions },
          collapseUnchanged: { margin: 3, minSize: 8 },
        });
        destroy = () => v.destroy();
      }
    };
    render();
    media.addEventListener("change", render);
    return () => {
      destroy();
      media.removeEventListener("change", render);
    };
  }, [before, after]);
  return <div className="diff-host" ref={host} role="region" tabIndex={0} aria-label={tr("File differences")} />;
}
type ChangePage = { changes: Change[]; next: string; total: number; revision: number; head: string };
type Side = { entry?: Entry; text?: string; preview: string; download?: string };
export function Changes({ library }: { library: Library }) {
  const tr = useT(), studio = useStudio();
  const branch = studio.model.branches[library.id];
  const branchID = branch?.id, head = branch?.head, revision = branch?.draft_revision;
  const root = `/libraries/${encodeURIComponent(library.id)}/branches/${encodeURIComponent(branchID ?? "")}`;
  const params = query({ head, revision });
  const [page, setPage] = useState<ChangePage>();
  const [selected, setSelected] = useState<Change>();
  const [sides, setSides] = useState<{ before: Side; after: Side }>();
  const [error, setError] = useState(""), [loading, setLoading] = useState(false), [loadingFile, setLoadingFile] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const listRequest = useRef<AbortController>(undefined);
  const selectedPath = useRef("");
  const morePending = useRef(false);
  useEffect(() => {
    listRequest.current?.abort(); const abort = new AbortController(); listRequest.current = abort;
    setPage(undefined); setSelected(undefined); setSides(undefined); setError("");
    morePending.current = false;
    if (!branchID || !head || !revision) { setLoading(false); return; }
    setLoading(true);
    void api<ChangePage>(`${root}/draft/diff?${params}`, { signal: abort.signal }).then(value => {
      if (abort.signal.aborted) return;
      setPage(value);
      const path = selectedPath.current || new URL(location.href).searchParams.get("change_path");
      setSelected(value.changes.find(item => item.path === path));
    }).catch(() => {
      if (!abort.signal.aborted) setError("Comparison is unavailable. Reload or check Volume access and the current branch revision.");
    }).finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [root, params, branchID, head, revision, refresh]);
  useEffect(() => {
    const abort = new AbortController(); setSides(undefined);
    if (!selected || !head || !revision) { setLoadingFile(false); return; }
    setLoadingFile(true); setError("");
    const readBefore = async (): Promise<Side> => {
      if (!selected.before) return { preview: "absent" };
      const value = await api<{ entry: Entry; text?: string; preview: string }>(`${root}/head-file?${query({ head, path: selected.path })}`, { signal: abort.signal });
      return { ...value, download: value.entry.kind === "file" ? `${base}/api/v1/libraries/${encodeURIComponent(library.id)}/commits/${encodeURIComponent(head)}/content?${query({ path: selected.path })}` : undefined };
    };
    const readAfter = async (): Promise<Side> => {
      // Recheck this exact draft revision even when the selected path is absent.
      const snapshot = await api<{ entry: Entry | null }>(`${root}/draft/entry?${query({ path: selected.path, revision })}`, { signal: abort.signal });
      const entry = snapshot.entry ?? undefined;
      if (!entry) return { preview: "absent" };
      if (entry.kind === "directory") return { entry, preview: "directory" };
      const download = `${base}/api/v1${root}/draft/content?${query({ path: selected.path, revision })}`;
      if (Number(entry.size) > 1 << 20) return { entry, preview: "too_large", download };
      const response = await fetch(download, { signal: abort.signal, cache: "no-store" });
      if (!response.ok) throw new Error("unavailable content");
      const raw = await response.arrayBuffer();
      if (raw.byteLength > 1 << 20) throw new Error("oversized content");
      try {
        const text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(raw);
        if (text.includes("\0")) return { entry, preview: "binary", download };
        return { entry, text, preview: "text", download };
      } catch { return { entry, preview: "binary", download }; }
    };
    void Promise.all([readBefore(), readAfter()]).then(([before, after]) => {
      if (!abort.signal.aborted) setSides({ before, after });
    }).catch(() => {
      if (!abort.signal.aborted) {
        setPage(undefined); setSelected(undefined); setSides(undefined);
        setError("Comparison is unavailable. Reload or check Volume access and the current branch revision.");
      }
    }).finally(() => { if (!abort.signal.aborted) setLoadingFile(false); });
    return () => abort.abort();
  }, [selected, root, head, revision, library.id]);
  async function more() {
    if (!page?.next || morePending.current || !listRequest.current) return;
    const abort = listRequest.current; morePending.current = true; setLoading(true);
    try {
      const value = await api<ChangePage>(`${root}/draft/diff?${params}&after=${encodeURIComponent(page.next)}`, { signal: abort.signal });
      if (!abort.signal.aborted) setPage(previous => previous ? { ...value, changes: [...previous.changes, ...value.changes] } : undefined);
    } catch {
      if (!abort.signal.aborted) {
        setPage(undefined); setSelected(undefined); setSides(undefined);
        setError("Comparison is unavailable. Reload or check Volume access and the current branch revision.");
      }
    } finally { if (!abort.signal.aborted) { morePending.current = false; setLoading(false); } }
  }
  const select = (change: Change) => {
    selectedPath.current = change.path; setSelected(change);
    const url = new URL(location.href); url.searchParams.set("change_path", change.path); history.replaceState(null, "", url);
  };
  const dirty = studio.model.buffers.filter(buffer => buffer.library === library.id && buffer.branch === branchID && buffer.text !== buffer.saved).length;
  const previewLabel = (side: Side) => side.preview === "absent" ? "File absent" : side.preview === "binary" ? "Binary file: text preview unavailable." : side.preview === "too_large" ? "File exceeds the 1 MiB text preview limit." : side.preview === "directory" ? "Directory" : "File";
  const text = sides && [sides.before, sides.after].every(side => side.preview === "text" || side.preview === "absent");
  return <section className="changes-view" aria-label={tr("Draft comparison")}>
    <div className="view-heading"><h2>{tr("Changes")} <span className="count">{page?.total ?? "…"}</span></h2>
      <span className="muted">{tr("Saved draft vs. committed head")}{dirty ? tr(" · {value0} unsaved file(s) excluded", { value0: dirty }) : ""}</span>
      <button disabled={loading} onClick={() => setRefresh(value => value + 1)}>{tr("Reload comparison")}</button>
    </div>
    {error && <p role="alert" className="notice error">{tr(error)}</p>}
    {loading && <p role="status">{tr("Loading…")}</p>}
    <div className="changes-workspace"><aside className="change-list">
      {page?.changes.map(change => <button key={change.path} className={selected?.path === change.path ? "selected" : ""} aria-pressed={selected?.path === change.path} onClick={() => select(change)}>
        <span className={`change-mark ${change.change}`} aria-hidden="true">{change.change === "added" ? "+" : change.change === "removed" ? "−" : "~"}</span><span>{change.path}</span><small>{tr(change.change)}</small>
      </button>)}
      {page?.changes.length === 0 && <p className="empty-state">{tr("No saved changes.")}</p>}
      {page?.next && <button disabled={loading} onClick={() => void more()}>{tr("Load more changes")}</button>}
    </aside><div className="comparison">
      <div className="editor-toolbar"><strong>{selected?.path || tr("Select a change")}</strong></div>
      {loadingFile && <p role="status">{tr("Loading comparison…")}</p>}
      {sides && <>
        <div className="snapshot-metadata">{(["before", "after"] as const).map(key => {
          const side = sides[key];
          return <section key={key}><h3>{tr(key === "before" ? "Committed head" : "Saved draft")}</h3>
            <p><code>{key === "before" ? head : `${branchID} · ${revision}`}</code></p>
            <p>{tr(previewLabel(side))}{side.entry && <> · {bytes(side.entry.size)}</>}</p>
            {side.entry?.digest && <code>{side.entry.digest}</code>}
            {side.download && <p><a href={side.download}>{tr(key === "before" ? "Download committed file" : "Download saved draft file")}</a></p>}
          </section>;
        })}</div>
        {text ? <TextDiff before={sides.before.text ?? ""} after={sides.after.text ?? ""} /> : <p>{tr("Binary, directory or large file: text preview unavailable.")}</p>}
      </>}
      {!selected && !error && <p className="empty-state">{tr("Select a file to compare.")}</p>}
    </div></div>
  </section>;
}
