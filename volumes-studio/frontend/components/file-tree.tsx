/* eslint-disable react-hooks/set-state-in-effect -- Hydrate browser navigation and synchronize external workspace snapshots. */
"use client";
import { useT } from "../lib/i18n";

import { useCallback, useEffect, useRef, useState, useMemo } from "react";
import { ChevronDown, ChevronRight, File, Folder, Search } from "lucide-react";
import { api, Entry } from "../lib/api";
import { storageRead, storageWrite } from "../lib/workbench";
import { Page, parentPath, query } from "../lib/workbench";
import { ContextMenu, Menu, MenuAction } from "./ui";
type Row =
  | { entry: Entry; depth: number }
  | { parent: string; depth: number; next: string };
export function FileTree({
  library,
  branch,
  version,
  revision,
  selected,
  onOpen,
  onDirectory,
  actions,
  onRename,
  searchRef,
}: {
  library: string;
  branch: string;
  version: string;
  revision: number;
  selected: string;
  onOpen: (entry: Entry) => void;
  onDirectory: (path: string) => void;
  actions: (entry: Entry) => MenuAction[];
  onRename: (entry: Entry, to: string) => Promise<void>;
  searchRef?: React.RefObject<HTMLInputElement | null>;
}) {
  const tr = useT();
  const [expanded, setExpanded] = useState<Set<string>>(new Set([""])),
    [pages, setPages] = useState<Record<string, Page<Entry>>>({}),
    [needle, setNeedle] = useState(""),
    [results, setResults] = useState<Page<Entry> | null>(null),
    [error, setError] = useState(""),
    [viewport, setViewport] = useState({ top: 0, height: 600 }),
    [renaming, setRenaming] = useState(""),
    [name, setName] = useState("");
  const host = useRef<HTMLDivElement>(null),
    pendingReveal = useRef(selected),
    generation = useRef(0),
    searchGeneration = useRef(0),
    pending = useRef(new Set<string>());
  const root = `/libraries/${library}/${version ? `versions/${version}` : `branches/${encodeURIComponent(branch)}/draft`}`;
  const load = useCallback(
    async (parent: string, cursor = "") => {
      const key = parent + "\n" + cursor;
      if (pending.current.has(key)) return;
      pending.current.add(key);
      const turn = generation.current;
      try {
        if (!version && !branch) return;
        const p: Page<Entry> = version ? await api<Page<Entry>>(
          `${root}/tree?${query({ mode: "workbench", parent, cursor })}`,
        ) : await api<{entries: Entry[]; next?: string; revision: number; descendant_count: number}>(
          `${root}/tree?${query({ parent, revision, after: cursor })}`,
        ).then(page => ({items:page.entries,next:page.next ?? "",revision:page.revision,total:page.descendant_count}));
        if (turn === generation.current)
          setPages((old) => ({
            ...old,
            [parent]: {
              ...p,
              items: cursor
                ? [...(old[parent]?.items || []), ...p.items]
                : p.items,
            },
          }));
      } catch (e) {
        if (turn === generation.current) setError(String(e));
      } finally {
        if (turn === generation.current) pending.current.delete(key);
      }
    },
    [root, revision, version, branch],
  );
  useEffect(() => {
    const raw = storageRead("session", `studio-tree:${library}:${branch}:${version}`);
    try {
      setExpanded(new Set(raw ? JSON.parse(raw) : [""]));
    } catch {
      setExpanded(new Set([""]));
    }
  }, [library, branch, version]);
  useEffect(() => {
    const turn = ++generation.current;
    pending.current.clear();
    setPages({});
    setError("");
    for (const p of expanded) void load(p);
    const pendingRequests = pending.current;
    return () => {
      generation.current = turn + 1;
      pendingRequests.clear();
    }; /* expansion itself loads below */
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load]);
  useEffect(() => {
    storageWrite(
      "session",
      `studio-tree:${library}:${branch}:${version}`,
      JSON.stringify([...expanded]),
    );
    for (const p of expanded) if (!pages[p]) void load(p);
  }, [expanded, pages, load, library, branch, version]);
  useEffect(() => {
    if (!selected || needle) return;
    const parts = selected.split("/");
    for (let i = 0; i < parts.length; i++) {
      const parent = parts.slice(0, i).join("/");
      const target = parts.slice(0, i + 1).join("/");
      const p = pages[parent];
      if (p?.next && !p.items.some((e) => e.path === target))
        void load(parent, p.next);
    }
  }, [selected, needle, pages, load]);
  useEffect(() => {
    if (!selected) return;
    const parts = parentPath(selected).split("/").filter(Boolean);
    setExpanded(
      (old) =>
        new Set([
          ...old,
          ...parts.map((_, i) => parts.slice(0, i + 1).join("/")),
        ]),
    );
  }, [selected]);
  useEffect(() => {
    const turn = ++searchGeneration.current;
    setResults(null);
    if (!needle) {
      return;
    }
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Page<Entry>>(
        `${root}/search?${query({ q: needle, revision: version ? undefined : revision })}`,
        { signal: abort.signal },
      )
        .then((page) => {
          if (turn === searchGeneration.current) {
            setResults(page);
            setError("");
          }
        })
        .catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      abort.abort();
      searchGeneration.current = turn + 1;
    };
  }, [needle, root, revision, version]);
  useEffect(() => {
    const node = host.current;
    if (!node) return;
    const observer = new ResizeObserver(() =>
      setViewport((v) => ({ ...v, height: node.clientHeight })),
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  const rows = useMemo(() => {
    const rows: Row[] = [];
    const walk = (parent: string, depth: number) => {
      const p = pages[parent];
      for (const entry of p?.items || []) {
        rows.push({ entry, depth });
        if (entry.kind === "directory" && expanded.has(entry.path))
          walk(entry.path, depth + 1);
      }
      if (p?.next) rows.push({ parent, depth, next: p.next });
    };
    if (needle) {
      for (const entry of results?.items || []) rows.push({ entry, depth: 0 });
    } else walk("", 0);
    return rows;
  }, [pages, expanded, needle, results]);
  const toggle = (entry: Entry) => {
    onDirectory(entry.path);
    setExpanded((old) => {
      const next = new Set(old);
      if (next.has(entry.path)) next.delete(entry.path);
      else next.add(entry.path);
      return next;
    });
  };
  const reveal = (entry: Entry) => {
    if (entry.kind === "directory") {
      toggle(entry);
      setNeedle("");
    } else {
      onOpen(entry);
      if (needle) setNeedle("");
    }
  };
  const rowHeight = 36;
  const first = Math.max(0, Math.floor(viewport.top / rowHeight) - 8),
    end = Math.min(
      rows.length,
      Math.ceil((viewport.top + viewport.height) / rowHeight) + 8,
    );
  useEffect(() => {
    pendingReveal.current = selected;
  }, [selected, root]);
  useEffect(() => {
    // Reveal an explicit selection once, after its ancestors have loaded.
    // Expanding unrelated folders must not pull a scrolled tree back to it.
    if (!selected || pendingReveal.current !== selected || needle) return;
    const index = rows.findIndex(
      (r) => "entry" in r && r.entry.path === selected,
    );
    if (index >= 0 && host.current) {
      pendingReveal.current = "";
      const top = index * 36;
      if (
        top < host.current.scrollTop ||
        top > host.current.scrollTop + host.current.clientHeight - 36
      )
        host.current.scrollTop = top;
    }
  }, [selected, rows, needle]);
  return (
    <>
      <label className="search tree-search">
        <Search size={15} />
        <input
          ref={searchRef}
          value={needle}
          placeholder={tr("Go to file…")}
          aria-label={tr("Find files")}
          onChange={(e) => setNeedle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && results?.items[0])
              reveal(results.items[0]);
          }}
        />
        <kbd>{tr("⌘P")}</kbd>
      </label>
      {error && (
        <div className="error tree-error" role="alert">
          {tr(error)}
          <button
            onClick={() => {
              setError("");
              void load("");
            }}
          >
             {tr("Retry")} </button>
        </div>
      )}
      <div
        ref={host}
        className="tree-scroll"
        role="tree"
        aria-label={tr("Files")}
        onScroll={(e) =>
          setViewport((v) => ({ ...v, top: e.currentTarget.scrollTop }))
        }
      >
        <div style={{ height: rows.length * rowHeight, position: "relative" }}>
          {rows.slice(first, end).map((row, i) => {
            const index = first + i;
            if (!("entry" in row))
              return (
                <button
                  style={{
                    position: "absolute",
                    top: index * rowHeight,
                    height: rowHeight,
                  }}
                  key={`more:${row.parent}`}
                  onClick={() => void load(row.parent, row.next)}
                >
                   {tr("Load more entries")} </button>
              );
            const { entry, depth } = row;
            const items = actions(entry).map((a) =>
              a.id === "rename"
                ? {
                    ...a,
                    action: () => {
                      setRenaming(entry.path);
                      setName(entry.path.split("/").at(-1)!);
                    },
                  }
                : a,
            );
            return (
              <ContextMenu key={entry.path} items={items}>
                <div
                  role="treeitem"
                  aria-selected={selected === entry.path}
                  aria-expanded={
                    entry.kind === "directory" ? expanded.has(entry.path) : undefined
                  }
                  aria-level={depth + 1}
                  className={`tree-row ${selected === entry.path ? "selected" : ""}`}
                  style={{
                    position: "absolute",
                    top: index * rowHeight,
                    height: rowHeight,
                    paddingLeft: 8 + depth * 16,
                  }}
                >
                  <button
                    className="file-name"
                    data-tree-index={index}
                    aria-label={tr("Open {value0}", { value0: entry.path })}
                    onClick={() => reveal(entry)}
                    onKeyDown={(e) => {
                      if (
                        e.key === "ArrowRight" &&
                        entry.kind === "directory" &&
                        !expanded.has(entry.path)
                      ) {
                        e.preventDefault();
                        toggle(entry);
                      } else if (
                        e.key === "ArrowLeft" &&
                        entry.kind === "directory" &&
                        expanded.has(entry.path)
                      ) {
                        e.preventDefault();
                        toggle(entry);
                      } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                        e.preventDefault();
                        const next = Math.max(
                          0,
                          Math.min(
                            rows.length - 1,
                            index + (e.key === "ArrowDown" ? 1 : -1),
                          ),
                        );
                        host.current!.scrollTop = Math.max(
                          0,
                          next * rowHeight - viewport.height / 2,
                        );
                        requestAnimationFrame(() =>
                          host.current
                            ?.querySelector<HTMLButtonElement>(
                              `[data-tree-index="${next}"]`,
                            )
                            ?.focus(),
                        );
                      }
                    }}
                  >
                    {entry.kind === "directory" ? (
                      expanded.has(entry.path) ? (
                        <ChevronDown size={13} />
                      ) : (
                        <ChevronRight size={13} />
                      )
                    ) : (
                      <span className="tree-spacer" />
                    )}
                    {entry.kind === "directory" ? (
                      <Folder size={15} />
                    ) : (
                      <File size={15} />
                    )}
                    <span title={entry.path}>
                      {needle ? entry.path : entry.path.split("/").at(-1)}
                    </span>
                  </button>
                  {renaming === entry.path ? (
                    <form
                      className="inline-rename"
                      onSubmit={async (e) => {
                        e.preventDefault();
                        try {
                          await onRename(
                            entry,
                            [parentPath(entry.path), name]
                              .filter(Boolean)
                              .join("/"),
                          );
                          setRenaming("");
                        } catch (e) {
                          setError(String(e));
                        }
                      }}
                    >
                      <input
                        autoFocus
                        aria-label={tr("New file name")}
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === "Escape") setRenaming("");
                        }}
                      />
                    </form>
                  ) : (
                    items.length > 0 && (
                      <Menu label={tr("Actions for {value0}", { value0: entry.path })} items={items} />
                    )
                  )}
                </div>
              </ContextMenu>
            );
          })}
        </div>
        {rows.length === 0 && (
          <p className="muted tree-empty">
            {needle ? results ? tr("No matching files.") : tr("Searching…") : pages[""] ? tr("This directory is empty.") : tr("Loading files…")}
          </p>
        )}
        {needle && results?.next && (
          <button
            onClick={() => {
              const turn = searchGeneration.current;
              void api<Page<Entry>>(
                `${root}/search?${query({ q: needle, revision: version ? undefined : revision, cursor: results.next })}`,
              )
                .then(
                  (p) =>
                    turn === searchGeneration.current &&
                    setResults({ ...p, items: [...results.items, ...p.items] }),
                )
                .catch((e) => {
                  if (turn === searchGeneration.current) setError(String(e));
                });
            }}
          >
             {tr("Load more matches")} </button>
        )}
      </div>
    </>
  );
}
