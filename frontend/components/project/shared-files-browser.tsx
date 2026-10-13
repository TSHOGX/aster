"use client";
import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, File, Folder } from "lucide-react";
import {
  flattenDirectoryTree,
  type DirectoryTree,
} from "@/lib/directory-window";
import { FileTreeViewport } from "./file-tree-viewport";

// The same bounded tree viewport and keyboard interactions as Project files,
// populated exclusively from the source's explicit file allowlist.
export function SharedFilesBrowser({
  paths,
  selected,
  onSelect,
}: {
  paths: string[];
  selected: string;
  onSelect: (path: string) => void;
}) {
  const [expanded, setExpanded] = useState<string[]>([]);
  const [collapsed, setCollapsed] = useState<{
    selected: string;
    paths: string[];
  }>({ selected: "", paths: [] });
  const tree = useMemo(() => {
    const out: DirectoryTree = { "": { entries: [] } };
    for (const path of paths) {
      const parts = path.split("/");
      let directory = "";
      parts.forEach((name, index) => {
        const leaf = index === parts.length - 1;
        if (!out[directory].entries.some((e) => e.name === name))
          out[directory].entries.push({
            name,
            kind: leaf ? "file" : "directory",
            size: 0,
            modified: 0,
          });
        directory = [directory, name].filter(Boolean).join("/");
        if (!leaf) out[directory] ||= { entries: [] };
      });
    }
    for (const directory of Object.values(out))
      directory.entries.sort((a, b) =>
        a.kind === b.kind
          ? a.name.localeCompare(b.name)
          : a.kind === "directory"
            ? -1
            : 1,
      );
    return out;
  }, [paths]);
  const parents = selected
    .split("/")
    .slice(0, -1)
    .map((_, index, array) => array.slice(0, index + 1).join("/"));
  const closed = collapsed.selected === selected ? collapsed.paths : [];
  const open = [...new Set([...expanded, ...parents])].filter(
    (path) => !closed.includes(path),
  );
  const rows = flattenDirectoryTree(tree, open);
  return (
    <FileTreeViewport
      rows={rows}
      selected={selected}
      load={() => {}}
      renderRow={(row, focusable) =>
        row.kind === "entry" ? (
          <div
            className={`file-row ${selected === row.path ? "selected" : ""}`}
            role="treeitem"
            aria-level={row.depth + 1}
            aria-expanded={
              row.entry.kind === "directory" ? row.expanded : undefined
            }
            aria-selected={row.path === selected}
            data-file-path={row.path}
            style={{ paddingLeft: 8 + row.depth * 16 }}
          >
            <button
              className="file-name"
              tabIndex={focusable ? 0 : -1}
              title={row.path}
              onClick={() => {
                if (row.entry.kind !== "directory") {
                  onSelect(row.path);
                  return;
                }
                if (row.expanded) {
                  setExpanded((old) => old.filter((path) => path !== row.path));
                  setCollapsed({ selected, paths: [...closed, row.path] });
                } else {
                  setExpanded((old) => [...new Set([...old, row.path])]);
                  setCollapsed({
                    selected,
                    paths: closed.filter((path) => path !== row.path),
                  });
                }
              }}
            >
              {row.entry.kind === "directory" ? (
                <>
                  {row.expanded ? (
                    <ChevronDown size={14} />
                  ) : (
                    <ChevronRight size={14} />
                  )}
                  <Folder size={16} />
                </>
              ) : (
                <File size={16} />
              )}
              <span>{row.entry.name}</span>
            </button>
          </div>
        ) : null
      }
    />
  );
}
