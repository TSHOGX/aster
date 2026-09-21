"use client";
import Image from "next/image";
import { useEffect, useRef, useState } from "react";
import { classifyArtifact } from "@/lib/mime";
import { readPreview } from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { CodeView } from "@/components/workbench/viewers/code-view";

export function FilePreview({ path, href }: { path: string; href: string }) {
  const [loaded, setLoaded] = useState<{
    blob: Blob;
    url: string;
    text: string;
  } | null>(null);
  const [error, setError] = useState("");
  const kind = classifyArtifact(path);
  useEffect(() => {
    const abort = new AbortController();
    let objectURL = "";
    void (async () => {
      try {
        const blob = await readPreview(href, abort.signal);
        const type = classifyArtifact(path).kind;
        const text =
          ["code", "text", "markdown", "json", "table"].includes(type) &&
          blob.size <= 1 << 20
            ? await blob.text()
            : "";
        const mime =
          type === "pdf"
            ? "application/pdf"
            : type === "image"
              ? `image/${path.toLowerCase().endsWith(".svg") ? "svg+xml" : path.split(".").pop()}`
              : "application/octet-stream";
        if (abort.signal.aborted) return;
        objectURL = URL.createObjectURL(new Blob([blob], { type: mime }));
        if (!abort.signal.aborted) {
          setLoaded({ blob, url: objectURL, text });
          setError("");
        }
      } catch (e) {
        if (!abort.signal.aborted)
          setError(e instanceof Error ? e.message : "Preview failed.");
      }
    })();
    return () => {
      abort.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [href, path]);
  if (error) return <p className="notice error">{error}</p>;
  if (!loaded) return <p className="notice">Loading preview…</p>;
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
  if (kind.kind === "pdf")
    return (
      <iframe
        className="document-preview"
        title={path}
        src={loaded.url}
        sandbox=""
      />
    );
  if (["spreadsheet", "document", "presentation"].includes(kind.kind))
    return <OfficePreview blob={loaded.blob} kind={kind.kind} />;
  if (kind.kind === "markdown" && loaded.blob.size <= 1 << 20)
    return (
      <div className="file-markdown">
        <MarkdownView content={loaded.text} />
      </div>
    );
  if (
    ["code", "text", "json", "table"].includes(kind.kind) &&
    loaded.blob.size <= 1 << 20
  )
    return <CodeView code={loaded.text} language={kind.language} />;
  return (
    <div className="empty-state">
      <p>Preview is not available for this file.</p>
      <a href={href} download>
        Download {path}
      </a>
    </div>
  );
}
function OfficePreview({ blob, kind }: { blob: Blob; kind: string }) {
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const element = host.current;
    if (!element) return;
    let gone = false;
    let dispose = () => {};
    void (async () => {
      try {
        const buffer = await blob.arrayBuffer();
        if (gone) return;
        if (kind === "spreadsheet") {
          const { default: renderer } = await import(
            "@/components/workbench/viewers/office-excel-loader"
          );
          if (gone) return;
          const view = renderer.init(element, {
            minColLength: 10,
            minRowLength: 20,
          });
          dispose = () => view.destroy();
          await view.preview(buffer);
        } else if (kind === "document") {
          const renderer = await import("docx-preview");
          if (gone) return;
          await renderer.renderAsync(buffer, element, undefined, {
            useBase64URL: true,
          });
        } else {
          const { init } = await import("pptx-preview");
          if (gone) return;
          const view = init(element, {
            width: Math.max(320, element.clientWidth),
            height: 360,
            mode: "slide",
          });
          dispose = () => view.destroy();
          await view.preview(buffer);
        }
      } catch (e) {
        if (!gone)
          setError(e instanceof Error ? e.message : "Office preview failed.");
      }
    })();
    return () => {
      gone = true;
      dispose();
      element.replaceChildren();
    };
  }, [blob, kind]);
  return (
    <>
      {error && <p className="notice error">{error}</p>}
      <div className="office-preview" ref={host} />
    </>
  );
}
