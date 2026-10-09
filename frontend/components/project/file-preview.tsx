"use client";
import { cacheablePreview, rememberPreview } from "@/lib/preview-cache";
import { useT } from "@/lib/i18n";
import Image from "next/image";
import dynamic from "next/dynamic";
import { useEffect, useRef, useState } from "react";
import { decodeTextFile, isTextFile, TEXT_FILE_LIMIT, TEXT_EDIT_LIMIT, FORMATTED_TEXT_LIMIT } from "@/lib/file-view";
import { TextPreview } from "./text-preview";
import { classifyArtifact } from "@/lib/mime";
import {
  contentDigest,
  PreviewRepresentation,
} from "@/lib/preview-representation";
import { useResources } from "./resource-provider";
import { APIError, readPreview } from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";
const SourcePreview = dynamic(() => import("./source-preview").then(module => module.SourcePreview), { ssr: false });
import {
  acquirePreviewParse,
  PREVIEW_LIMITS,
  PreviewFailure,
  reservePreview,
} from "@/lib/preview-budget";
import { PreviewStatus, previewSize } from "./preview-status";
import { OfficePreview } from "./office-preview";
import { validatePreview } from "@/lib/preview-preflight-client";
const OfficeReadingPreview = dynamic(() => import("./office-reading-preview").then(module => module.OfficeReadingPreview), { ssr: false });
export function FilePreview({
  path, href, project, validation = 0, mode = "preview", onCapabilities,
}: {
  path: string; href: string; project: string; validation?: number; mode?: "preview" | "source";
  onCapabilities?: (path: string, readable: boolean, editable: boolean, rendered: boolean) => void;
}) {
  const kind = path.toLowerCase().split(".").pop();
  if (kind === "docx" || kind === "xlsx")
    return <OfficeReadingPreview key={`${project}:${path}`} project={project} path={path} href={href} kind={kind} validation={validation} />;
  return <LocalFilePreview path={path} href={href} project={project} validation={validation} mode={mode} onCapabilities={onCapabilities} />;
}
function LocalFilePreview({
  path,
  href,
  project,
  validation = 0,
  mode = "preview",
  onCapabilities,
}: {
  path: string;
  href: string;
  project: string;
  validation?: number;
  mode?: "preview" | "source";
  onCapabilities?: (path: string, readable: boolean, editable: boolean, rendered: boolean) => void;
}) {
  const tr = useT();
  const resources = useResources();
  const [loaded, setLoaded] = useState<PreviewRepresentation | null>(null);
  useEffect(() => {
    const readable = !!loaded?.textReadable;
    const kind = classifyArtifact(path).kind;
    const rendered = readable && (["html", "table"].includes(kind) || ["markdown", "json"].includes(kind) && !!loaded && loaded.blob.size <= FORMATTED_TEXT_LIMIT);
    onCapabilities?.(path, readable, readable && !!loaded && loaded.blob.size <= TEXT_EDIT_LIMIT, rendered);
  }, [path, loaded, onCapabilities]);
  const sourcePositionRef = useRef(0);
  useEffect(() => { sourcePositionRef.current = 0; }, [path]);
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
  const [errorTitle, setErrorTitle] = useState(tr("Preview unavailable"));
  const [progress, setProgress] = useState<{ received: number; total?: number }>({ received: 0 });
  const [phase, setPhase] = useState<"loading" | "preparing" | "canceled">("loading");
  const cancel = useRef(() => {});
  const userCanceled = useRef(false);
  const [retry, setRetry] = useState(0);
  const [retryable, setRetryable] = useState(false);
  const [validated, setValidated] = useState<number | null>(null);
  useEffect(() => {
    if (userCanceled.current) return;
    const abort = new AbortController();
    cancel.current = () => {
      userCanceled.current = true;
      abort.abort();
      setPhase("canceled");
    };
    const key = { project, kind: "file" as const, identity: href };
    const prepare = async (blob: Blob) => {
      if (blob.size > PREVIEW_LIMITS.blobBytes) throw new PreviewFailure("source_too_large", "download", { actual: blob.size, limit: PREVIEW_LIMITS.blobBytes, metric: "bytes" });
      setProgress({ received: blob.size, total: blob.size });
      setPhase("preparing");
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
        throw new Error(tr("This file is not readable as supported text. Download it to open in another app."));
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
        /\.(png|jpe?g|gif|pdf|pptx)$/i.test(path)
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
        const blob = await resources.read(key, signal => readPreview(href, signal, PREVIEW_LIMITS.blobBytes, (received, total) => {
          if (!abort.signal.aborted) setProgress({ received, total });
        }), abort.signal, 0);
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
          setRetryable(e instanceof PreviewFailure ? e.retryable : e instanceof TypeError || e instanceof APIError && [202, 429, 500, 502, 503, 504].includes(e.status));
          setErrorTitle(tr(e instanceof PreviewFailure && e.code === "source_too_large" ? "File too large to preview" : "Preview unavailable"));
          if (e instanceof PreviewFailure && e.code === "source_too_large") setProgress({ received: 0, total: e.actual });
          setError(
            e instanceof PreviewFailure
              ? previewFailureMessage(e, tr)
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
  const restart = () => {
    userCanceled.current = false;
    setError("");
    setProgress({ received: 0 });
    setPhase("loading");
    setRetry(n => n + 1);
  };
  if (error && !loaded)
    return <PreviewStatus path={path} href={href} title={errorTitle} description={error} size={progress.total} onRetry={retryable ? restart : undefined} />;
  if (!loaded)
    return <PreviewStatus path={path} href={href}
      title={tr(phase === "canceled" ? "Preview canceled." : phase === "preparing" ? "Preparing preview…" : "Loading preview…")}
      description={tr(phase === "canceled" ? "You can try again or download the original file." : "Larger files may take a moment. You can keep working while the preview loads.")}
      loading={phase !== "canceled"} size={progress.total} progress={phase === "loading" ? progress : undefined}
      onCancel={phase !== "canceled" ? () => cancel.current() : undefined} onRetry={phase === "canceled" ? restart : undefined} />;
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
      <PreviewContent loaded={loaded} path={path} href={href} mode={mode} sourcePositionRef={sourcePositionRef} />
    </>
  );
}
function previewFailureMessage(error: PreviewFailure, tr: ReturnType<typeof useT>): string {
  switch (error.code) {
    case "source_too_large":
      return tr("This file is larger than the {size} preview limit. Download it to view the complete file.", { size: previewSize(error.limit ?? PREVIEW_LIMITS.blobBytes) });
    case "container_invalid": return tr("This file is damaged or has an invalid structure. Download the original or try another copy.");
    case "unsupported_feature": return tr("This file uses a feature this preview cannot display. Download it to open in another app.");
    case "content_limit": return tr("This file has more {metric} than this preview can process ({actual} of {limit}). Download it to read the complete file.", {
      metric: tr(({ bytes: "Preview bytes", entries: "Preview entries", nodes: "Preview nodes", cells: "Preview cells", rows: "Preview rows", columns: "Preview columns", slides: "Preview slides", pixels: "Preview pixels", frames: "Preview frames", parses: "Preview parses" } as const)[error.metric ?? "nodes"]),
      actual: error.actual ?? "?", limit: error.limit ?? "?",
    });
    case "resource_busy": return tr("Preview resources are busy. Try again in a moment.");
    case "parse_timeout": return tr("Preparing this preview took too long. Retry or download the file.");
    case "renderer_failed": return tr("The preview renderer stopped. Retry or download the file.");
  }
}
function PreviewContent({
  loaded,
  path,
  href,
  mode,
  sourcePositionRef,
}: {
  loaded: PreviewRepresentation;
  path: string;
  href: string;
  mode: "preview" | "source";
  sourcePositionRef: { current: number };
}) {
  const tr = useT();
  const kind = classifyArtifact(path);
  if (kind.kind === "pdf")
    return <PDFPreview loaded={loaded} path={path} href={href} />;
  if (loaded.textReadable && mode === "source")
    return <SourcePreview key={loaded.digest} path={path} text={loaded.text} initialPosition={() => sourcePositionRef.current} onPosition={offset => { sourcePositionRef.current = offset; }} />;
  if (kind.kind === "image" && loaded.plan)
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
  if (kind.kind === "presentation")
    return (
      <OfficePreview
        representation={loaded}
        kind={kind.kind}
        path={path}
        href={href}
      />
    );
  if (loaded.textReadable && (kind.kind === "html" || kind.kind === "table" || kind.kind === "json" && loaded.blob.size <= FORMATTED_TEXT_LIMIT))
    return <TextPreview key={loaded.digest} path={path} text={loaded.text} href={href} />;
  if (kind.kind === "markdown" && loaded.textReadable && loaded.blob.size <= FORMATTED_TEXT_LIMIT)
    return (
      <div className="file-markdown">
        <MarkdownView content={loaded.text} />
      </div>
    );
  if (loaded.textReadable)
    return <SourcePreview key={loaded.digest} path={path} text={loaded.text} initialPosition={() => sourcePositionRef.current} onPosition={offset => { sourcePositionRef.current = offset; }} />;
  return <PreviewStatus path={path} href={href} title={tr("Preview unavailable")}
    description={tr("This format is not supported for preview. Download the file to open it in another app.")} size={loaded.blob.size} />;
}

function PDFPreview({ loaded, path, href }: { loaded: PreviewRepresentation; path: string; href: string }) {
  const tr = useT();
  if (typeof navigator !== "undefined" && navigator.pdfViewerEnabled === false)
    return <PreviewStatus path={path} href={href} title={tr("Preview unavailable")}
      description={tr("Your browser cannot display PDFs here. Download the file to read it.")} size={loaded.blob.size} />;
  return <div className="pdf-preview">
    <div className="preview-reading-bar">
      <span>PDF <span aria-hidden="true">·</span> {previewSize(loaded.blob.size)}</span>
      <a href={href} download>{tr("Download file")}</a>
    </div>
    <iframe className="document-frame" src={`${loaded.url}#view=FitH&navpanes=0`} title={`${tr("Preview")} ${path}`} referrerPolicy="no-referrer" />
  </div>;
}
