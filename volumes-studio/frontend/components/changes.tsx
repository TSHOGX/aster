/* eslint-disable react-hooks/set-state-in-effect -- Hydrate browser navigation and synchronize external workspace snapshots. */
"use client";
import { message, useT } from "../lib/i18n";
import { formatQuantity, type Message } from "../../../web/i18n/locale";

import { useEffect, useRef, useState } from "react";
import { EditorView } from "@codemirror/view";
import { EditorState } from "@codemirror/state";
import { MergeView, unifiedMergeView } from "@codemirror/merge";
import { api, Entry, Library } from "../lib/api";
import { decodeText, Page, query } from "../lib/workbench";
import { contentURL } from "./shell";
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
  return <div className="diff-host" ref={host} aria-label={tr("File differences")} />;
}
export function Changes({ library }: { library: Library }) {
  const tr = useT();
  const studio = useStudio();
  const [page, setPage] = useState<Page<Change> | null>(null),
    [selected, setSelected] = useState<Change | null>(null),
    [content, setContent] = useState<{ before: string; after: string } | null>(
      null,
    ),
    [comparison, setComparison] = useState<Message>(""),
    [error, setError] = useState("");
  const branch = studio.model.branches[library.id];
  const root = `/libraries/${library.id}/branches/${branch?.id ?? ""}`;
  const params = query({
    head: branch?.head,
    revision: branch?.draft_revision,

  });
  useEffect(() => {
    const abort = new AbortController();
    if (!branch) return;
    void api<{changes: Change[];next:string;total:number}>(`${root}/draft/diff?${params}`, {
      signal: abort.signal,
    })
      .then((p) => {
        setPage({items:p.changes,next:p.next,total:p.total,revision:branch.draft_revision});
        setError("");
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [root, params, branch, branch?.draft_revision]);
  useEffect(() => {
    if (!selected) return;
    const abort = new AbortController();
    setContent(null);
    setComparison("Loading comparison…");
    const load = async (version: string, path: string) => {
      if (version) {
        const result = await api<{text?:string;preview:string}>(`${root}/head-file?${query({head:version,path})}`,{signal:abort.signal});
        if (result.preview !== "text") throw new Error("This file is unavailable for text comparison.");
        return result.text ?? "";
      }
      const url = contentURL(library.id, version, path, branch?.draft_revision,studio.model.branches[library.id]?.id);
      const response = await fetch(url, { signal: abort.signal });
      if (!response.ok)
        throw new Error(
          version
            ? "Original content is unavailable. The base version may have been retired."
            : "The saved draft changed. Refresh the comparison.",
        );
      if (Number(response.headers.get("Content-Length")) > 1 << 20)
        throw new Error("This file is too large for text comparison.");
      const data = await response.arrayBuffer();
      if (data.byteLength > 1 << 20)
        throw new Error("This file is too large for text comparison.");
      return decodeText(data).text;
    };
    void (async () => {
      try {
        const entry = selected.after || selected.before;
        if (entry?.kind === "directory") {
          setComparison(message("Directory {change}", { change: selected.change }));
          return;
        }
        if (
          entry &&
          (Number(entry.size) > 1 << 20 ||
            /\.(png|jpe?g|gif|webp|pdf|docx?|xlsx?|pptx?|zip|bin)$/i.test(
              entry.path,
            ))
        ) {
          setComparison(
            message("{change} · {before} → {after} bytes. Download to inspect this file.", {
              change: selected.change, before: selected.before?.size || "0", after: selected.after?.size || "0",
            }),
          );
          return;
        }
        const before =
          selected.change === "added"
            ? ""
            : await load(branch?.head ?? "", selected.path);
        const after =
          selected.change === "removed" ? "" : await load("", selected.path);
        if (!abort.signal.aborted) {
          setContent({ before, after });
          setComparison("");
        }
      } catch (e) {
        if (!abort.signal.aborted) setComparison(String(e));
      }
    })();
    return () => abort.abort();
  }, [
    studio.model.branches,
    branch?.head,
    selected,
    library.id,
    branch?.draft_revision,
    root,
  ]);
  const dirty = studio.model.buffers.filter(
    (b) => b.library === library.id && b.branch === branch?.id && b.text !== b.saved,
  ).length;
  const comparisonMessage = typeof comparison === "object" ? {
    ...comparison, params: {
      ...comparison.params,
      change: tr(String(comparison.params?.change ?? "")),
      ...(comparison.params?.before !== undefined ? { before: formatQuantity(comparison.params.before), after: formatQuantity(comparison.params.after) } : {}),
    },
  } : comparison;
  return (
    <section className="changes-view">
      <div className="view-heading">
        <h2>
           {tr("Changes")} <span className="count">{page?.total ?? "…"}</span>
        </h2>
        <span className="muted">
           {tr("Saved draft vs. committed head")} {dirty ? tr(" · {value0} unsaved file(s) excluded", { value0: dirty }) : ""}
        </span>
      </div>
      {error && (
        <p role="alert" className="notice error">
          {tr(error)}
        </p>
      )}
      <div className="changes-workspace">
        <aside className="change-list">
          {page?.items.map((c) => (
            <button
              key={c.path}
              className={selected?.path === c.path ? "selected" : ""}
              onClick={() => setSelected(c)}
            >
              <span className={`change-mark ${c.change}`}>
                {c.change === "added" ? "+" : c.change === "removed" ? "−" : "~"}
              </span>
              <span>{c.path}</span>
              <small>{tr(c.change)}</small>
            </button>
          ))}
          {page?.items.length === 0 && (
            <p className="empty-state">{tr("No saved changes.")}</p>
          )}
          {page?.next && (
            <button
              onClick={() =>
                void api<{changes:Change[];next:string;total:number}>(
                  `${root}/draft/diff?${params}&after=${encodeURIComponent(page.next)}`,
                )
                  .then((p) =>
                    setPage({ ...page,next:p.next,total:p.total,items: [...page.items, ...p.changes] }),
                  )
                  .catch((e) => setError(String(e)))
              }
            >
               {tr("Load more changes")} </button>
          )}
        </aside>
        <div className="comparison">
          <div className="editor-toolbar">
            <strong>{selected?.path || tr("Select a change")}</strong>
            {selected && <span className="muted">{tr("Base → saved draft")}</span>}
          </div>
          {content ? (
            <TextDiff {...content} />
          ) : (
            <p className="empty-state">
              {tr(comparisonMessage) || tr("Select a file to compare.")}
            </p>
          )}
        </div>
      </div>
    </section>
  );
}
