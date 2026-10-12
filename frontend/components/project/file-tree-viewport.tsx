"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import type { TreeRow } from "@/lib/directory-window";
import { useT } from "@/lib/i18n";

const ROW_HEIGHT = 36;
const OVERSCAN = 8;

export function FileTreeViewport({ rows, renderRow, load, onAnchor, selected, busy = false }: {
  rows: TreeRow[];
  renderRow: (row: TreeRow, focusable: boolean) => ReactNode;
  load: (directory: string, cursor?: string) => void;
  onAnchor?: (directory: string, name: string) => void;
  selected: string;
  busy?: boolean;
}) {
  const tr = useT();
  const host = useRef<HTMLDivElement>(null);
  const [viewport, setViewport] = useState({ top: 0, height: 600 });
  const [focused, setFocused] = useState("");
  const anchor = useRef<{ id: string; offset: number } | null>(null);
  const revealed = useRef("");
  useEffect(() => {
    const node = host.current;
    if (!node) return;
    const observer = new ResizeObserver(() => setViewport(old => ({ ...old, height: node.clientHeight })));
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (selected && revealed.current !== selected) {
      const index = rows.findIndex(row => row.kind === "entry" && row.path === selected);
      const node = host.current;
      if (index >= 0 && node) {
        revealed.current = selected;
        const top = Math.max(0, index * ROW_HEIGHT - node.clientHeight / 2);
        node.scrollTop = top;
        anchor.current = { id: rows[Math.floor(top / ROW_HEIGHT)]?.id ?? selected, offset: top % ROW_HEIGHT };
        setViewport(old => ({ ...old, top }));
        return;
      }
    }
    const saved = anchor.current, node = host.current;
    if (!node || !saved) return;
    const index = rows.findIndex(row => row.id === saved.id);
    if (index < 0) return;
    const top = index * ROW_HEIGHT + saved.offset;
    if (Math.abs(node.scrollTop - top) > 1) {
      node.scrollTop = top;
      setViewport(old => ({ ...old, top }));
    }
  }, [rows, selected]);
  const first = Math.max(0, Math.floor(viewport.top / ROW_HEIGHT) - OVERSCAN);
  const end = Math.min(rows.length, Math.ceil((viewport.top + viewport.height) / ROW_HEIGHT) + OVERSCAN);
  const visible = rows.slice(first, end);
  useEffect(() => {
    for (const row of visible) {
      if (viewport.top > 0 && row.kind === "edge" && !row.loading && (row.direction === "next" || viewport.top < ROW_HEIGHT * 3)) load(row.directory, row.cursor);
      if (row.kind === "status" && row.message === "loading" && !rows.some(other => other.directory === row.directory && other.kind === "entry"))
        load(row.directory);
    }
    // The visible interval is the demand; duplicate requests are deduplicated by the controller.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [first, end, rows]);
  const focusRow = (index: number) => {
    const row = rows[index];
    if (!row || row.kind !== "entry") return;
    setFocused(row.path);
    const node = host.current;
    if (!node) return;
    if (index * ROW_HEIGHT < node.scrollTop || (index + 1) * ROW_HEIGHT > node.scrollTop + node.clientHeight) {
      node.scrollTop = Math.max(0, index * ROW_HEIGHT - node.clientHeight / 2);
      setViewport(old => ({ ...old, top: node.scrollTop }));
    }
    requestAnimationFrame(() => node.querySelector<HTMLElement>(`[data-file-path="${CSS.escape(row.path)}"] .file-name`)?.focus());
  };
  const focusedIndex = rows.findIndex(row => row.kind === "entry" && row.path === focused);
  const extra = focusedIndex >= 0 && (focusedIndex < first || focusedIndex >= end) ? [{ row: rows[focusedIndex], index: focusedIndex }] : [];
  return <div className="file-tree-scroll virtual-file-tree" ref={host} role="tree" aria-busy={busy} aria-label={tr("File tree")}
    onScroll={event => {
      const node = event.currentTarget;
      const index = Math.min(rows.length - 1, Math.floor(node.scrollTop / ROW_HEIGHT));
      if (rows[index]) anchor.current = { id: rows[index].id, offset: node.scrollTop - index * ROW_HEIGHT };
      const row = rows[index];
      if (row?.kind === "entry") onAnchor?.(row.directory, row.entry.name);
      setViewport(old => ({ ...old, top: node.scrollTop }));
    }}
    onFocus={event => {
      const row = (event.target as HTMLElement).closest<HTMLElement>("[data-file-path]");
      if (row?.dataset.filePath) setFocused(row.dataset.filePath);
    }}
    onKeyDown={event => {
      if (!(event.target as HTMLElement).classList.contains("file-name")) return;
      const rowElement = (event.target as HTMLElement).closest<HTMLElement>("[data-tree-index]");
      let index = Number(rowElement?.dataset.treeIndex ?? -1);
      if (index < 0) return;
      const row = rows[index];
      if (row.kind !== "entry") return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        do { index += step; } while (index >= 0 && index < rows.length && rows[index].kind !== "entry");
        focusRow(index);
      } else if (event.key === "Home" || event.key === "End") {
        event.preventDefault();
        const indexes = rows.flatMap((item, i) => item.kind === "entry" ? [i] : []);
        focusRow(event.key === "Home" ? indexes[0] : indexes.at(-1)!);
      } else if (event.key === "ArrowRight" && row.entry.kind === "directory" && !row.expanded ||
        event.key === "ArrowLeft" && row.entry.kind === "directory" && row.expanded) {
        event.preventDefault(); (event.target as HTMLButtonElement).click();
      } else if (event.key === "ArrowLeft" && row.directory) {
        event.preventDefault(); focusRow(rows.findIndex(item => item.kind === "entry" && item.path === row.directory));
      } else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
        const match = [...rows.slice(index + 1), ...rows.slice(0, index + 1)].find(item =>
          item.kind === "entry" && item.entry.name.toLocaleLowerCase().startsWith(event.key.toLocaleLowerCase()));
        if (match) { event.preventDefault(); focusRow(rows.indexOf(match)); }
      }
    }}>
    <div className="virtual-tree-content" style={{ height: rows.length * ROW_HEIGHT }}>
      {[...visible.map((row, i) => ({ row, index: first + i })), ...extra].map(({ row, index }) =>
        <div key={row.id} data-tree-index={index} className="virtual-tree-row" style={{ top: index * ROW_HEIGHT, height: ROW_HEIGHT }}>
          {renderRow(row, row.kind === "entry" && (focused === row.path || !focused && index === 0))}
        </div>)}
    </div>
  </div>;
}
