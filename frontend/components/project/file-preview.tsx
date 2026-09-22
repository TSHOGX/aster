"use client";
import { useT } from "@/lib/i18n";
import Image from "next/image";
import { useEffect, useRef, useState } from "react";
import { classifyArtifact } from "@/lib/mime";
import { readPreview } from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { CodeView } from "@/components/workbench/viewers/code-view";
export function FilePreview({ path, href }: { path: string; href: string }) {
  const tr = useT();
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
  if (!loaded) return <p className="notice">{tr("Loading preview\u2026")}</p>;
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
    return (
      <OfficePreview
        blob={loaded.blob}
        url={loaded.url}
        kind={kind.kind}
        path={path}
      />
    );
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
      <p>{tr("Preview is not available for this file.")}</p>
      <a href={href} download>
        {tr("Download")}
        {path}
      </a>
    </div>
  );
}
function OfficePreview({
  blob,
  url,
  kind,
  path,
}: {
  blob: Blob;
  url: string;
  kind: string;
  path: string;
}) {
  const tr = useT();
  const [notice, setNotice] = useState("");
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const element = host.current;
    if (!element) return;
    let gone = false;
    let dispose = () => {};
    let resize = () => {};
    let frame = 0;
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!gone && element.clientWidth > 0) resize();
      });
    });
    observer.observe(element);
    void (async () => {
      try {
        const buffer = await blob.arrayBuffer();
        if (gone) return;
        if (kind === "spreadsheet") {
          const { default: renderer } =
            await import("@/components/workbench/viewers/office-excel-loader");
          if (gone) return;
          const options = {
            minColLength: 10,
            minRowLength: 20,
            xls: path.toLowerCase().endsWith(".xls"),
          };
          const stage = document.createElement("div");
          stage.style.height = "100%";
          element.appendChild(stage);
          const view = renderer.init(stage, options);
          resize = () => window.dispatchEvent(new Event("resize"));
          let rendering: Promise<unknown> = Promise.resolve();
          dispose = () => {
            // The parser queues callbacks against its workbook during load.
            // Keep its detached stage valid until those callbacks finish.
            const finish = () => {
              setTimeout(() => view.destroy(), 0);
            };
            void rendering.then(finish, finish);
          };
          try {
            // Reuse the owned object URL: passing an ArrayBuffer makes this
            // renderer create another blob URL that it never revokes.
            rendering = view.preview(url);
            await rendering;
          } catch (error) {
            if (
              gone ||
              !(error instanceof Error) ||
              !error.message.includes("anchors")
            )
              throw error;
            // Its embedded ExcelJS parser cannot read some chart-only drawing
            // relationships. Spreadsheet conversion preserves cells so the
            // original workbook remains useful without altering its download.
            const converted = { ...options, xls: true };
            view.setOptions(converted);
            rendering = view.preview(url);
            await rendering;
            if (!gone)
              setNotice(
                tr(
                  "Some drawings are unavailable in this preview. Download the workbook to view them.",
                ),
              );
          }
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
            width: 720,
            height: 405,
            mode: "slide",
          });
          dispose = () => view.destroy();
          await view.preview(buffer);
          resize = () => {
            view.wrapper.style.zoom = String(
              Math.max(1, element.clientWidth) / 720,
            );
          };
          if (!gone) resize();
        }
      } catch (e) {
        if (!gone)
          setError(e instanceof Error ? e.message : "Office preview failed.");
      }
    })();
    return () => {
      gone = true;
      observer.disconnect();
      cancelAnimationFrame(frame);
      dispose();
      element.replaceChildren();
    };
  }, [blob, url, kind, path, tr]);
  return (
    <>
      {notice && <p className="notice">{notice}</p>}
      {error && <p className="notice error">{error}</p>}
      <div className="office-preview" ref={host} />
    </>
  );
}
