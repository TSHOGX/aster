"use client";

import { useEffect, useRef, useState } from "react";
import { useT } from "@/lib/i18n";
import { APIError } from "@/lib/project-api";
import { recalled, remember } from "@/lib/project-api";
import {
  columnName,
  openReading,
  parseCellAddress,
  parseOfficeBookmark,
  pauseReading,
  readingCopy,
  readingFind,
  readingLocate,
  readingStatus,
  readingWindow,
  releaseReading,
  type ReadingJob,
  type ReadingItem,
} from "@/lib/office-reading";
import { useResources } from "./resource-provider";
import { PreviewStatus } from "./preview-status";

const HEIGHT = 64;
const COLUMN = 160;

export function OfficeReadingPreview({
  project,
  path,
  href,
  kind,
  validation,
}: {
  project: string;
  path: string;
  href: string;
  kind: "docx" | "xlsx";
  validation: number;
}) {
  const tr = useT();
  const resources = useResources();
  const [job, setJob] = useState<ReadingJob | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [canceled, setCanceled] = useState(false);
  const cancel = useRef(() => {});
  const active = useRef<ReadingJob | null>(null);
  const bookmarkKey = `reading:${resources.actor}:${project}:${path}`;
  const [saved] = useState(() =>
    parseOfficeBookmark(recalled<unknown>(bookmarkKey, null)),
  );
  const bookmark = useRef(saved);
  const [sheet, setSheet] = useState(saved.sheet);
  const [top, setTop] = useState(saved.position * HEIGHT);
  const [left, setLeft] = useState(saved.left);
  const [windowItems, setWindowItems] = useState<ReadingItem[]>([]);
  const [target, setTarget] = useState("");
  const [query, setQuery] = useState("");
  const [notice, setNotice] = useState("");
  const [selected, setSelected] = useState("");
  const [selectionStart, setSelectionStart] = useState(1);
  const [selectionEnd, setSelectionEnd] = useState(1);
  const [selectionColumn, setSelectionColumn] = useState(1);
  const [selectionLastColumn, setSelectionLastColumn] = useState(1);
  const viewport = useRef<HTMLDivElement>(null);
  const request = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState(false);
  const count = job?.manifest?.sheets[sheet]?.count ?? 0;
  const first = Math.max(0, Math.floor(top / HEIGHT) - 5);
  const start = first;
  const columnStart = Math.max(0, Math.floor(left / COLUMN) - 1);
  useEffect(
    () => () => {
      request.current?.abort();
      if (active.current) void releaseReading(project, active.current.id);
    },
    [project],
  );
  useEffect(() => {
    if (canceled) return;
    const abort = new AbortController();
    let candidate: ReadingJob | undefined;
    let adopted = false;
    cancel.current = () => {
      abort.abort();
      setCanceled(true);
    };
    void (async () => {
      try {
        await Promise.resolve();
        if (abort.signal.aborted) return;
        setPending(true);
        candidate = await openReading(project, path, kind, abort.signal);
        while (candidate.state === "indexing") {
          await pauseReading(abort.signal);
          candidate = await readingStatus(project, candidate.id, abort.signal);
        }
        abort.signal.throwIfAborted();
        if (candidate.state === "failed" || !candidate.manifest)
          throw new Error(candidate.failure || "renderer_failed");
        if (active.current?.digest === candidate.digest) {
          setError("");
          return;
        }
        const selectedSheet = Math.max(
          0,
          Math.min(
            bookmark.current.sheet,
            candidate.manifest.sheets.length - 1,
          ),
        );
        const restore =
          !active.current && bookmark.current.digest === candidate.digest;
        const position =
          restore || active.current
            ? Math.min(
                candidate.manifest.sheets[selectedSheet].count - 1,
                bookmark.current.position,
              )
            : 0;
        const initial = await readingWindow(
          project,
          candidate.id,
          selectedSheet,
          Math.max(0, Math.floor(position / 50) * 50),
          abort.signal,
        );
        if (initial.digest !== candidate.digest)
          throw new Error("renderer_failed");
        abort.signal.throwIfAborted();
        const previous = active.current;
        active.current = candidate;
        adopted = true;
        if (previous) void releaseReading(project, previous.id);
        setJob(candidate);
        bookmark.current = {
          digest: candidate.digest,
          sheet: selectedSheet,
          position: Math.max(0, position),
          left: bookmark.current.left,
        };
        setWindowItems(initial.items);
        setError("");
        setSheet(selectedSheet);
        setTop(Math.max(0, position) * HEIGHT);
      } catch (e) {
        if (!abort.signal.aborted) {
          if (e instanceof APIError && [401, 403, 404].includes(e.status)) {
            if (active.current) void releaseReading(project, active.current.id);
            active.current = null;
            setJob(null);
            setWindowItems([]);
            setSelected("");
          }
          setError(
            e instanceof APIError && e.outcome?.code
              ? e.outcome.code
              : e instanceof Error
                ? e.message
                : "renderer_failed",
          );
        }
      } finally {
        if (candidate && !adopted) void releaseReading(project, candidate.id);
        if (!abort.signal.aborted) setPending(false);
      }
    })();
    return () => {
      abort.abort();
    };
  }, [project, path, kind, validation, attempt, canceled]);
  useEffect(() => {
    if (!job) return;
    const abort = new AbortController();
    request.current?.abort();
    request.current = abort;
    void Promise.resolve()
      .then(() => {
        if (!abort.signal.aborted) setBusy(true);
        return readingWindow(
          project,
          job.id,
          sheet,
          start,
          abort.signal,
          kind === "xlsx" ? columnStart : -1,
          kind === "xlsx" ? 14 : 0,
        );
      })
      .then((window) => {
        if (window.digest !== job.digest) throw new Error("renderer_failed");
        if (!abort.signal.aborted) {
          setWindowItems(window.items);
          setBusy(false);
        }
      })
      .catch((e) => {
        if (!abort.signal.aborted) {
          setBusy(false);
          if (e instanceof APIError && [401, 403, 404].includes(e.status)) {
            setJob(null);
            setWindowItems([]);
            setSelected("");
          }
          setError(
            e instanceof APIError && e.outcome?.code
              ? e.outcome.code
              : e instanceof Error
                ? e.message
                : "renderer_failed",
          );
        }
      });
    return () => abort.abort();
  }, [job, project, sheet, start, kind, columnStart]);
  useEffect(() => {
    if (!job || !viewport.current) return;
    viewport.current.scrollTop = bookmark.current.position * HEIGHT;
    viewport.current.scrollLeft = bookmark.current.left;
  }, [job]);
  const jump = (position: number) => {
    const value = Math.max(0, Math.min(count - 1, position));
    if (viewport.current) viewport.current.scrollTop = value * HEIGHT;
    setTop(value * HEIGHT);
  };
  const locate = async () => {
    if (kind !== "xlsx" || !job) return;
    const address = parseCellAddress(target);
    if (!address) return;
    const abort = new AbortController();
    request.current?.abort();
    request.current = abort;
    try {
      const result = await readingLocate(
        project,
        job.id,
        sheet,
        address.row,
        abort.signal,
      );
      if (result.position >= 0) {
        if (viewport.current)
          viewport.current.scrollLeft = address.column * COLUMN;
        setLeft(address.column * COLUMN);
        jump(result.position);
      }
    } catch (e) {
      if (!abort.signal.aborted)
        setNotice(e instanceof Error ? e.message : tr("Preview unavailable"));
    }
  };
  const find = async () => {
    if (!job || !query) return;
    const abort = new AbortController();
    request.current?.abort();
    request.current = abort;
    try {
      let result = await readingFind(
        project,
        job.id,
        sheet,
        Math.floor(top / HEIGHT) + 1,
        query,
        abort.signal,
      );
      if (result.position < 0)
        result = await readingFind(
          project,
          job.id,
          sheet,
          0,
          query,
          abort.signal,
        );
      setNotice(result.position < 0 ? tr("No matches in this file.") : "");
      if (result.position >= 0) jump(result.position);
    } catch (e) {
      if (!abort.signal.aborted)
        setError(e instanceof Error ? e.message : "renderer_failed");
    }
  };
  const retry = () => {
    setCanceled(false);
    setError("");
    setAttempt((n) => n + 1);
  };
  const copyRange = async () => {
    if (!job) return;
    const abort = new AbortController();
    request.current?.abort();
    request.current = abort;
    try {
      const result = await readingCopy(
        project,
        job.id,
        sheet,
        selectionStart - 1,
        selectionEnd - 1,
        selectionColumn - 1,
        selectionLastColumn - 1,
        abort.signal,
      );
      setSelected(result.text);
      await navigator.clipboard.writeText(result.text);
    } catch (e) {
      if (!abort.signal.aborted)
        setNotice(e instanceof Error ? e.message : tr("Preview unavailable"));
    }
  };
  const description =
    error === "source_too_large"
      ? tr(
          "This file is larger than the {size} preview limit. Download it to view the complete file.",
          { size: "100 MB" },
        )
      : error === "snapshot_expired"
        ? tr("This preview expired. Retry to read the current file.")
        : error === "renderer_failed"
          ? tr("The preview renderer stopped. Retry or download the file.")
          : error === "container_invalid"
            ? tr(
                "This file is damaged or has an invalid structure. Download the original or try another copy.",
              )
            : error === "content_limit"
              ? tr(
                  "This file could not be previewed within the supported content limits. Download it to view the complete file.",
                )
              : error === "unsupported_feature"
                ? tr(
                    "This file uses a feature this preview cannot display. Download it to open in another app.",
                  )
                : error === "parse_timeout"
                  ? tr(
                      "Preparing this preview took too long. Retry or download the file.",
                    )
                  : error ||
                    tr(
                      "Larger files may take a moment. You can keep working while the preview loads.",
                    );
  if (!job)
    return (
      <PreviewStatus
        path={path}
        href={href}
        title={tr(
          canceled
            ? "Preview canceled."
            : error
              ? "Preview unavailable"
              : "Preparing preview…",
        )}
        description={description}
        loading={!error && !canceled}
        onCancel={!error && !canceled ? () => cancel.current() : undefined}
        onRetry={error || canceled ? retry : undefined}
      />
    );
  const columns = job.manifest!.sheets[sheet]?.columns ?? 1;
  const colStart = Math.max(0, Math.floor(left / COLUMN) - 1);
  const colEnd = Math.min(columns, colStart + 14);
  const visible = windowItems.filter(
    (item) => item.position >= first && item.position < first + 28,
  );
  const changeSheet = (index: number) => {
    setSheet(index);
    setWindowItems([]);
    setTop(0);
    setLeft(0);
    if (viewport.current) {
      viewport.current.scrollTop = 0;
      viewport.current.scrollLeft = 0;
    }
  };
  return (
    <div
      className="office-reading-preview"
      data-preview-phase="ready"
      data-preview-kind={kind}
    >
      <div className="preview-reading-bar">
        <strong>
          {tr(
            kind === "docx" ? "Document reading view" : "Spreadsheet data view",
          )}
        </strong>
        <span>
          {tr("Complete text and data; layout and drawings are simplified.")}
        </span>
      </div>
      {pending && !canceled && (
        <p className="notice" role="status">
          {tr("Preparing preview…")}{" "}
          <button onClick={() => cancel.current()}>
            {tr("Cancel preview")}
          </button>
        </p>
      )}
      {canceled && (
        <p className="notice" role="status">
          {tr("Preview canceled.")}{" "}
          <button onClick={retry}>{tr("Retry")}</button>
        </p>
      )}
      {error && (
        <p className="notice error" role="alert">
          {description} <button onClick={retry}>{tr("Retry")}</button>
        </p>
      )}
      <div className="office-reading-tools">
        {kind === "xlsx" && (
          <label>
            {tr("Sheet")}{" "}
            <select
              value={sheet}
              onChange={(e) => changeSheet(Number(e.target.value))}
            >
              {job.manifest!.sheets.map((s, i) => (
                <option key={i} value={i}>
                  {s.name}
                  {s.hidden ? ` (${tr("Hidden")})` : ""}
                </option>
              ))}
            </select>
          </label>
        )}
        {kind === "docx" && job.manifest!.headings.length > 0 && (
          <label>
            {tr("Outline")}{" "}
            <select value="" onChange={(e) => jump(Number(e.target.value))}>
              <option value="">{tr("Choose a heading")}</option>
              {job.manifest!.headings.map((h) => (
                <option key={h.position} value={h.position}>
                  {h.text}
                </option>
              ))}
            </select>
          </label>
        )}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (kind === "xlsx") void locate();
            else {
              const n = Number(target);
              if (Number.isInteger(n) && n > 0 && n <= count) jump(n - 1);
            }
          }}
        >
          <label>
            {tr(kind === "xlsx" ? "Go to cell" : "Go to record")}{" "}
            <input
              type={kind === "xlsx" ? "text" : "number"}
              min={kind === "xlsx" ? undefined : 1}
              max={kind === "xlsx" ? undefined : count}
              placeholder={kind === "xlsx" ? "A1" : undefined}
              value={target}
              onChange={(e) => setTarget(e.target.value)}
            />
          </label>
          <button>{tr("Go")}</button>
        </form>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void find();
          }}
        >
          <label>
            {tr("Find in file")}{" "}
            <input
              type="search"
              value={query}
              maxLength={256}
              onChange={(e) => setQuery(e.target.value)}
            />
          </label>
          <button>{tr("Find next")}</button>
        </form>
        <span role="status">
          {tr("Record {current} of {total}", {
            current: Math.min(count, Math.floor(top / HEIGHT) + 1),
            total: count,
          })}
          {busy ? ` · ${tr("Loading…")}` : ""}
        </span>
      </div>
      {notice && <p role="status">{notice}</p>}
      {kind === "xlsx" && (
        <div className="office-reading-column-headings" aria-hidden="true">
          <div
            style={{
              transform: `translateX(${-left}px)`,
              width: columns * COLUMN + 56,
            }}
          >
            {Array.from({ length: colEnd - colStart }, (_, index) => (
              <span
                key={colStart + index}
                style={{
                  left: (colStart + index) * COLUMN + 56,
                  width: COLUMN,
                }}
              >
                {columnName(colStart + index)}
              </span>
            ))}
          </div>
        </div>
      )}
      <div
        className="office-reading-viewport"
        ref={viewport}
        tabIndex={0}
        aria-label={tr("Reading content")}
        aria-busy={busy}
        onScroll={(e) => {
          setTop(e.currentTarget.scrollTop);
          setLeft(e.currentTarget.scrollLeft);
          bookmark.current = {
            digest: job.digest,
            sheet,
            position: Math.floor(e.currentTarget.scrollTop / HEIGHT),
            left: e.currentTarget.scrollLeft,
          };
          remember(bookmarkKey, bookmark.current);
        }}
      >
        <div
          className="office-reading-spacer"
          style={{
            height: count * HEIGHT,
            width: kind === "xlsx" ? Math.max(640, columns * COLUMN) : "100%",
          }}
        >
          {visible.map((item) => (
            <div
              key={item.position}
              className={`office-reading-row ${item.heading ? "heading" : ""}`}
              style={{ top: item.position * HEIGHT, height: HEIGHT }}
            >
              <span className="office-reading-index">{item.row}</span>
              {item.cells ? (
                <div
                  className="office-reading-cells"
                  style={{ width: Math.max(columns * COLUMN, 640) }}
                >
                  {(kind === "xlsx"
                    ? Array.from({ length: colEnd - colStart }, (_, i) => ({
                        column: colStart + i,
                        text:
                          item.cells!.find((c) => c.column === colStart + i)
                            ?.text ?? "",
                      }))
                    : item.cells
                  ).map((cell) => (
                    <button
                      key={cell.column}
                      className="office-reading-cell"
                      style={{
                        left: cell.column * COLUMN,
                        width:
                          COLUMN *
                          ("span" in cell && typeof cell.span === "number"
                            ? cell.span
                            : 1),
                      }}
                      title={cell.text}
                      aria-label={`${columnName(cell.column)}${item.row}: ${cell.text}`}
                      onClick={() => {
                        setSelected(cell.text);
                        setSelectionStart(item.position + 1);
                        setSelectionEnd(item.position + 1);
                        setSelectionColumn(cell.column + 1);
                        setSelectionLastColumn(cell.column + 1);
                      }}
                    >
                      {cell.text || "\u00a0"}
                    </button>
                  ))}
                </div>
              ) : (
                <button
                  className="office-reading-paragraph"
                  onClick={() => {
                    setSelected(item.text ?? "");
                    setSelectionStart(item.position + 1);
                    setSelectionEnd(item.position + 1);
                  }}
                >
                  {item.text || "\u00a0"}
                </button>
              )}
            </div>
          ))}
        </div>
      </div>
      <div className="office-reading-footer">
        <button
          disabled={first === 0}
          onClick={() => jump(Math.max(0, first - 20))}
        >
          {tr("Previous")}
        </button>
        <button disabled={first + 20 >= count} onClick={() => jump(first + 20)}>
          {tr("Next")}
        </button>
        <label>
          {tr("Selected text")}{" "}
          <textarea
            readOnly
            value={selected}
            aria-label={tr("Selected text")}
          />
        </label>
        <button
          disabled={!selected}
          onClick={() => void navigator.clipboard.writeText(selected)}
        >
          {tr("Copy")}
        </button>
      </div>
      <details className="office-reading-selection">
        <summary>{tr("Copy range")}</summary>
        <div>
          <label>
            {tr("From record")}
            <input
              type="number"
              min={1}
              max={count}
              value={selectionStart}
              onChange={(e) => setSelectionStart(Number(e.target.value))}
            />
          </label>
          <label>
            {tr("To record")}
            <input
              type="number"
              min={selectionStart}
              max={Math.min(count, selectionStart + 999)}
              value={selectionEnd}
              onChange={(e) => setSelectionEnd(Number(e.target.value))}
            />
          </label>
          {kind === "xlsx" && (
            <>
              <label>
                {tr("From column")}
                <input
                  type="number"
                  min={1}
                  max={columns}
                  value={selectionColumn}
                  onChange={(e) => setSelectionColumn(Number(e.target.value))}
                />
              </label>
              <label>
                {tr("To column")}
                <input
                  type="number"
                  min={selectionColumn}
                  max={columns}
                  value={selectionLastColumn}
                  onChange={(e) =>
                    setSelectionLastColumn(Number(e.target.value))
                  }
                />
              </label>
            </>
          )}
          <button onClick={() => void copyRange()}>{tr("Copy range")}</button>
        </div>
      </details>
    </div>
  );
}
