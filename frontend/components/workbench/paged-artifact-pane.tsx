"use client";
import dynamic from "next/dynamic";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Virtuoso } from "react-virtuoso";
import {
  useSelectedSession,
  useTranscriptState,
} from "@/components/harness-provider";
import { registerCache } from "@/lib/cache-lifecycle";
import { useT } from "@/lib/i18n";
import { request, uploadArtifacts, buildArtifactHref } from "@/lib/api";
import { subscribeArtifacts } from "@/lib/artifact-invalidation";
import { BoundedCache } from "@/lib/bounded-cache";
import {
  entriesFromDataTransfer,
  entriesFromFileList,
} from "@/lib/upload-entries";
import { formatBytes } from "@/lib/format";
import { uploadUiEnabled } from "@/lib/ui-config";
import type { ApiArtifact } from "@/lib/types";
import { toast } from "sonner";

const ArtifactViewer = dynamic(
  () => import("./artifact-viewer").then((m) => m.ArtifactViewer),
  { ssr: false },
);
type Entry = {
  kind: "directory" | "file";
  path: string;
  name: string;
  size: number;
  file_count: number;
  updated_at: string;
};
type Page = { entries: Entry[]; has_more: boolean; next_cursor: string };
const cache = new BoundedCache<Page>(2 * 1024 * 1024);
const expansion = new Map<string, string[]>();
registerCache(() => {
  cache.clear();
  expansion.clear();
});
export function clearArtifactPages() {
  cache.clear();
  expansion.clear();
}
export function PagedArtifactPane({
  onExpandedChange,
}: {
  onExpandedChange?: (v: boolean) => void;
}) {
  const session = useSelectedSession();
  const c = useTranscriptState(session?.id ?? null);
  return session ? (
    <Browser
      key={session.id}
      sessionId={session.id}
      ready={c.historyLoaded}
      latestFinalID={c.latestFinalID}
      readOnly={session.access === "reader"}
      status={session.status}
      onExpandedChange={onExpandedChange}
    />
  ) : (
    <aside />
  );
}
function Browser({
  sessionId,
  ready,
  latestFinalID,
  readOnly,
  status,
  onExpandedChange,
}: {
  sessionId: string;
  ready: boolean;
  latestFinalID: number;
  readOnly: boolean;
  status: string;
  onExpandedChange?: (v: boolean) => void;
}) {
  const t = useT();
  const [pages, setPages] = useState<Record<string, Page>>({});
  const [expanded, setExpanded] = useState(
    () => new Set(expansion.get(sessionId) ?? []),
  );
  const [pageHistory, setPageHistory] = useState<Record<string, string[]>>({});
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [error, setError] = useState("");
  const [active, setActive] = useState<Entry | null>(null);
  const [fullScreen, setFullScreen] = useState(false);
  const [mentioned, setMentioned] = useState<{ path: string; size: number }[]>(
    [],
  );
  const [revision, setRevision] = useState(0);
  const [uploading, setUploading] = useState(false);
  const requests = useRef(new Map<string, AbortController>());
  const base = `/api/sessions/${encodeURIComponent(sessionId)}`;
  useEffect(() => {
    const t = setTimeout(() => setSearch(query.trim()), 150);
    return () => clearTimeout(t);
  }, [query]);
  useEffect(() => {
    onExpandedChange?.(!!active && !fullScreen);
    return () => onExpandedChange?.(false);
  }, [active, fullScreen, onExpandedChange]);
  useEffect(() => {
    const refresh = () => {
      if (document.visibilityState !== "hidden") setRevision((v) => v + 1);
    };
    const stop = subscribeArtifacts(sessionId, refresh);
    const timer = setInterval(refresh, 15000);
    const held = requests.current;
    return () => {
      stop();
      clearInterval(timer);
      held.forEach((c) => c.abort());
      held.clear();
    };
  }, [sessionId]);
  const load = useCallback(
    async (directory: string, cursor = "") => {
      const key = search ? `search:${search}` : directory;
      if (requests.current.has(key)) return;
      const cached = cache.get(`${base}:${key}`);
      if (cached && !cursor)
        setPages((prev) => (prev[key] ? prev : { ...prev, [key]: cached }));
      const controller = new AbortController();
      requests.current.set(key, controller);
      const params = new URLSearchParams({ directory, cursor, q: search });
      const res = await request<Page>(
        `${base}/artifacts/${search ? "search" : "children"}?${params}`,
        { signal: controller.signal },
      );
      if (controller.signal.aborted) return;
      requests.current.delete(key);
      if (!res.ok) {
        if (res.status === 404 || res.status === 403) {
          setPages({});
          setMentioned([]);
          setActive(null);
        }
        setError(res.error);
        return;
      }
      setError("");
      setPages((prev) => {
        const page = { ...res.data, entries: res.data.entries };
        cache.set(`${base}:${key}`, page, JSON.stringify(page).length * 2);
        const next = { ...prev, [key]: page };
        let bytes = JSON.stringify(next).length * 2;
        for (const name of Object.keys(next)) {
          if (bytes <= 2 * 1024 * 1024) break;
          if (name === key) continue;
          bytes -= JSON.stringify(next[name]).length * 2;
          delete next[name];
        }
        return next;
      });
    },
    [base, search],
  );
  useEffect(() => {
    if (!ready) return;
    const c = new AbortController();
    const timer = setTimeout(() => {
      void load(
        "",
        pageHistory[search ? `search:${search}` : ""]?.at(-1) ?? "",
      );
      if (!search)
        expanded.forEach(
          (path) => void load(path, pageHistory[path]?.at(-1) ?? ""),
        );
    }, 0);
    if (latestFinalID)
      void request<{ files: { path: string; size: number }[] }>(
        `${base}/messages/${latestFinalID}/mentioned-files`,
        { signal: c.signal },
      ).then((res) => {
        if (!c.signal.aborted && res.ok) setMentioned(res.data.files);
      });
    const inFlight = requests.current;
    return () => {
      clearTimeout(timer);
      c.abort();
      inFlight.forEach((request) => request.abort());
      inFlight.clear();
    };
  }, [
    ready,
    load,
    expanded,
    search,
    base,
    latestFinalID,
    revision,
    pageHistory,
  ]);
  const rows = useMemo(() => {
    const result: {
      entry?: Entry;
      directory: string;
      depth: number;
      previous?: boolean;
    }[] = [];
    const walk = (dir: string, depth: number) => {
      const p = pages[search ? `search:${search}` : dir];
      if (pageHistory[search ? `search:${search}` : dir]?.length)
        result.push({ directory: dir, depth, previous: true });
      for (const e of p?.entries ?? []) {
        result.push({ entry: e, directory: dir, depth });
        if (!search && e.kind === "directory" && expanded.has(e.path))
          walk(e.path, depth + 1);
      }
      if (p?.has_more) result.push({ directory: dir, depth });
    };
    walk("", 0);
    return result;
  }, [pages, search, expanded, pageHistory]);
  const open = (e: Entry) => setActive(e);
  const writable = !readOnly && status !== "destroyed" && status !== "failed";
  const runUpload = async (entries: import("@/lib/api").UploadEntry[]) => {
    if (!uploadUiEnabled || !writable || uploading || !entries.length) return;
    if (
      entries.length > 2000 ||
      entries.some((e) => e.file.size > 100 * 1024 * 1024)
    ) {
      toast.error("Upload limit exceeded");
      return;
    }
    setUploading(true);
    try {
      const result = await uploadArtifacts(sessionId, entries);
      if (!result.ok) toast.error(result.error);
      setRevision((v) => v + 1);
    } finally {
      setUploading(false);
    }
  };
  return (
    <aside
      onDragOver={(e) => {
        if (uploadUiEnabled && writable) e.preventDefault();
      }}
      onDrop={(e) => {
        e.preventDefault();
        if (uploadUiEnabled && writable)
          void entriesFromDataTransfer(e.dataTransfer)
            .then(runUpload)
            .catch(() => toast.error("Cannot read dropped files"));
      }}
      className={
        fullScreen
          ? "fixed inset-0 z-40 flex flex-col bg-[var(--color-surface)]"
          : "flex h-full min-h-0 flex-col border-l bg-[var(--color-surface)]"
      }
    >
      <div className="p-3 text-sm">
        {t("workspace.title")}{" "}
        {uploadUiEnabled && writable && (
          <label className="ml-2 cursor-pointer text-xs">
            {uploading ? "Uploading…" : "Upload"}
            <input
              className="hidden"
              type="file"
              multiple
              disabled={uploading}
              onChange={(event) => {
                if (event.target.files)
                  void runUpload(entriesFromFileList(event.target.files));
                event.target.value = "";
              }}
            />
          </label>
        )}
        {uploadUiEnabled && writable && (
          <label className="ml-2 cursor-pointer text-xs">
            Folder
            <input
              className="hidden"
              type="file"
              multiple
              disabled={uploading}
              // @ts-expect-error directory selection is a browser extension
              webkitdirectory=""
              onChange={(e) => {
                if (e.target.files)
                  void runUpload(entriesFromFileList(e.target.files));
                e.target.value = "";
              }}
            />
          </label>
        )}
      </div>
      {error && (
        <button
          className="p-2 text-xs"
          onClick={() => setRevision((v) => v + 1)}
        >
          {error} · Retry
        </button>
      )}
      {active ? (
        <div className="min-h-0 flex-1">
          <ArtifactViewer
            key={active.path}
            sessionId={sessionId}
            path={active.path}
            artifact={
              {
                session_id: sessionId,
                path: active.path,
                size: active.size,
                updated_at: `${active.updated_at}:${revision}`,
                created_at: "",
                mod_time: "",
              } as ApiArtifact
            }
            readOnly={readOnly}
            onClose={() => setActive(null)}
            fullScreen={fullScreen}
            onToggleFullScreen={() => setFullScreen((v) => !v)}
          />
        </div>
      ) : (
        <>
          <input
            aria-label={t("workspace.searchFiles")}
            placeholder={t("workspace.searchFiles")}
            className="mx-3 mb-2 rounded border p-1 text-sm"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {!!mentioned.length && (
            <div className="max-h-36 overflow-auto border-b px-3 text-xs">
              {t("mentioned.title")}
              {mentioned.map((f) => (
                <button
                  className="block truncate"
                  key={f.path}
                  onClick={() =>
                    open({
                      ...f,
                      kind: "file",
                      name: f.path,
                      file_count: 1,
                      updated_at: "",
                    })
                  }
                >
                  {f.path}
                </button>
              ))}
            </div>
          )}
          <Virtuoso
            data={rows}
            fixedItemHeight={28}
            computeItemKey={(_, row) =>
              row.entry?.path ??
              `${row.previous ? "previous" : "more"}:${row.directory}`
            }
            itemContent={(_, row) => {
              const e = row.entry;
              return (
                <div
                  className="flex h-7 items-center gap-1 pr-2 text-xs"
                  style={{ paddingLeft: 8 + row.depth * 14 }}
                >
                  {e ? (
                    <>
                      <button
                        className="min-w-0 flex-1 truncate text-left"
                        onClick={() => {
                          if (e.kind === "file") open(e);
                          else
                            setExpanded((prev) => {
                              const next = new Set(prev);
                              if (next.has(e.path)) next.delete(e.path);
                              else {
                                if (next.size >= 50)
                                  next.delete(next.values().next().value!);
                                next.add(e.path);
                              }
                              expansion.set(sessionId, [...next]);
                              if (expansion.size > 5)
                                expansion.delete(
                                  expansion.keys().next().value!,
                                );
                              return next;
                            });
                        }}
                      >
                        {e.kind === "directory"
                          ? expanded.has(e.path)
                            ? "▾ "
                            : "▸ "
                          : ""}
                        {e.name}
                      </button>
                      <span>{formatBytes(e.size)}</span>
                      {e.kind === "file" && (
                        <a
                          href={buildArtifactHref(sessionId, e.path)}
                          download
                          aria-label="Download"
                        >
                          ↓
                        </a>
                      )}
                    </>
                  ) : (
                    <button
                      onClick={() => {
                        const key = search ? `search:${search}` : row.directory;
                        const history = pageHistory[key] ?? [];
                        const next = row.previous
                          ? history.slice(0, -1)
                          : [...history, pages[key]?.next_cursor ?? ""];
                        setPageHistory((prev) => ({ ...prev, [key]: next }));
                        void load(row.directory, next.at(-1) ?? "");
                      }}
                    >
                      {t(row.previous ? "perf.previous" : "perf.more")}
                    </button>
                  )}
                </div>
              );
            }}
          />
        </>
      )}
    </aside>
  );
}
