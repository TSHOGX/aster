"use client";
import { cacheablePreview, rememberPreview } from "@/lib/preview-cache";
import { useT } from "@/lib/i18n";
import Image from "next/image";
import { useEffect, useRef, useState } from "react";
import { decodeTextFile, isTextFile, TEXT_FILE_LIMIT } from "@/lib/file-view";
import { TextPreview } from "./text-preview";
import { classifyArtifact } from "@/lib/mime";
import {
  contentDigest,
  PreviewRepresentation,
} from "@/lib/preview-representation";
import { useResources } from "./resource-provider";
import { APIError, readPreview } from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { CodeView } from "@/components/workbench/viewers/code-view";
import {
  acquirePreviewParse,
  PREVIEW_LIMITS,
  PreviewLimitError,
  reservePreview,
} from "@/lib/preview-budget";
import { OfficePreview } from "./office-preview";
import { validatePreview } from "@/lib/preview-preflight-client";
export function FilePreview({
  path,
  href,
  project,
  validation = 0,
  mode = "preview",
  onTextReady,
}: {
  path: string;
  href: string;
  project: string;
  validation?: number;
  mode?: "preview" | "source";
  onTextReady?: (path: string, editable: boolean) => void;
}) {
  const tr = useT();
  const resources = useResources();
  const [loaded, setLoaded] = useState<PreviewRepresentation | null>(null);
  useEffect(() => { onTextReady?.(path, loaded?.textReadable ?? false); }, [path, loaded, onTextReady]);
  const current = useRef<PreviewRepresentation | null>(null);
  const owned = useRef(new Set<PreviewRepresentation>());
  useEffect(() => {
    for (const item of owned.current)
      if (item !== loaded) {
        item.release();
        owned.current.delete(item);
      }
  }, [loaded]);
  useEffect(() => {
    const retained = owned.current;
    return () => {
      for (const item of retained) item.release();
      retained.clear();
      current.current = null;
    };
  }, []);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const [retryable, setRetryable] = useState(false);
  const [validated, setValidated] = useState<number | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    const key = { project, kind: "file" as const, identity: href };
    const prepare = async (blob: Blob) => {
      const digest = await contentDigest(blob);
      if (abort.signal.aborted) return;
      if (
        current.current?.path === path &&
        current.current.digest === digest
      ) {
        setError("");
        return;
      }
      const type = classifyArtifact(path).kind;
      const decoded = await decodeTextFile(path, blob);
      if (isTextFile(path) && blob.size <= TEXT_FILE_LIMIT && decoded === null)
        throw new Error(tr("This file is not readable as UTF-8 text. Download it to open in another app."));
      const text = decoded ?? "";
      const mime =
        type === "pdf"
          ? "application/pdf"
          : type === "image"
            ? `image/${path.toLowerCase().endsWith(".svg") ? "svg+xml" : path.split(".").pop()}`
            : "application/octet-stream";
      if (abort.signal.aborted) return;
      let plan;
      if (
        ["image", "pdf", "spreadsheet", "document", "presentation"].includes(
          type,
        )
      ) {
        const releaseParse = await acquirePreviewParse(abort.signal);
        let releaseBuffers = () => {};
        try {
          abort.signal.throwIfAborted();
          releaseBuffers = reservePreview(
            blob.size + PREVIEW_LIMITS.entryBytes * 8,
          );
          plan = await validatePreview(blob, path, abort.signal);
        } finally {
          releaseBuffers();
          releaseParse();
        }
      }
      if (abort.signal.aborted) return;
      const representation = new PreviewRepresentation(
        blob,
        digest,
        text,
        path,
        mime,
        plan,
        decoded !== null,
      );
      owned.current.add(representation);
      current.current = representation;
      setLoaded(representation);
      setError("");
    };
    void (async () => {
      try {
        const cached = resources.peek<Blob>(key);
        if (!current.current && cached instanceof Blob && cacheablePreview(path, cached)) {
          try {
            await prepare(cached);
          } catch {
            // An evicted/budget-limited cache is optional; fresh authority and
            // content must still be checked. The active editor is separately owned.
          }
        }
        if (abort.signal.aborted) return;
        const blob = await resources.read(key, signal => readPreview(href, signal), abort.signal, 0);
        if (abort.signal.aborted) return;
        rememberPreview(resources, key, path, blob);
        await prepare(blob);
        if (!abort.signal.aborted) setValidated(validation);
      } catch (e) {
        if (!abort.signal.aborted) {
          if (e instanceof APIError && [401, 403, 404].includes(e.status)) {
            resources.clear(project);
            current.current = null;
            setLoaded(null);
          }
          setRetryable(e instanceof TypeError || e instanceof APIError && [202, 429, 500, 502, 503, 504].includes(e.status));
          setError(
            e instanceof PreviewLimitError
              ? tr(
                  "This file exceeds the supported preview limits. Download it to view the complete file.",
                )
              : e instanceof Error
                ? e.message
                : "Preview failed.",
          );
        }
      }
    })();
    return () => {
      abort.abort();
    };
  }, [href, path, validation, resources, project, tr, retry]);
  if (error && !loaded)
    return (
      <div className="notice" role="status">
        <p>{error}</p>
        {retryable && <button onClick={() => { setError(""); setRetry(n => n + 1); }}>{tr("Retry")}</button>}
        <a href={href} download>
          {tr("Download")} {path}
        </a>
      </div>
    );
  if (!loaded) return <p className="notice">{tr("Loading preview\u2026")}</p>;
  return (
    <>
      {!error && validated !== validation && cacheablePreview(path, loaded.blob) && (
        <span className="preview-validation" role="status">{tr("Checking for file changes…")}</span>
      )}
      {error && (
        <p className="notice error" role="alert">
          {error}{" "}
          {retryable && <button onClick={() => { setError(""); setRetry(n => n + 1); }}>{tr("Retry")}</button>}
          <a href={href} download>
            {tr("Download")}
          </a>
        </p>
      )}
      <PreviewContent loaded={loaded} path={path} href={href} mode={mode} />
    </>
  );
}
function PreviewContent({
  loaded,
  path,
  href,
  mode,
}: {
  loaded: PreviewRepresentation;
  path: string;
  href: string;
  mode: "preview" | "source";
}) {
  const tr = useT();
  const kind = classifyArtifact(path);
  if (loaded.textReadable && mode === "source")
    return <CodeView code={loaded.text} language={kind.language} />;
  if (kind.kind === "image")
    return (
      <div className="image-preview">
        <Image
          src={loaded.url}
          alt={path}
          width={1200}
          height={900}
          unoptimized
        />
      </div>
    );
  if (
    kind.kind === "spreadsheet" ||
    kind.kind === "document" ||
    kind.kind === "presentation"
  )
    return (
      <OfficePreview
        representation={loaded}
        kind={kind.kind}
        path={path}
        href={href}
      />
    );
  if (loaded.textReadable && ["html", "table", "json"].includes(kind.kind))
    return <TextPreview key={loaded.digest} path={path} text={loaded.text} />;
  if (kind.kind === "markdown" && loaded.textReadable)
    return (
      <div className="file-markdown">
        <MarkdownView content={loaded.text} />
      </div>
    );
  if (
    ["code", "text", "json", "table"].includes(kind.kind) &&
    loaded.textReadable
  )
    return <CodeView code={loaded.text} language={kind.language} />;
  return (
    <div className="empty-state">
      <p>{tr("Preview is not available for this file.")}</p>
      <a href={href} download>
        {tr("Download")}
        {path}
      </a>
    </div>
  );
}
