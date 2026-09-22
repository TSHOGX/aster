"use client";
import { useT } from "@/lib/i18n";
import Image from "next/image";
import { useEffect, useRef, useState } from "react";
import { classifyArtifact } from "@/lib/mime";
import { contentDigest, PreviewRepresentation } from "@/lib/preview-representation";
import { useResources } from "./resource-provider";
import { APIError, readPreview } from "@/lib/project-api";
import { MarkdownView } from "@/components/workbench/markdown-view";
import { CodeView } from "@/components/workbench/viewers/code-view";
export function FilePreview({ path, href, project, validation = 0 }: { path: string; href: string; project: string; validation?: number }) {
  const tr = useT();
  const resources = useResources();
  const [loaded, setLoaded] = useState<PreviewRepresentation | null>(null);
  const current = useRef<PreviewRepresentation | null>(null);
  const owned = useRef(new Set<PreviewRepresentation>());
  useEffect(() => {
    for (const item of owned.current) if (item !== loaded) {item.release();owned.current.delete(item);}
  }, [loaded]);
  useEffect(() => {
    const retained = owned.current;
    return () => {for(const item of retained) item.release();retained.clear();current.current=null;};
  }, []);
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    void (async () => {
      try {
        const blob = await resources.read(
          {project,kind:"file",identity:href}, signal=>readPreview(href,signal), abort.signal, 0,
        );
        const digest = await contentDigest(blob);
        if (abort.signal.aborted) return;
        if(current.current?.path===path && current.current.digest===digest) {setError("");return;}
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
        const representation = new PreviewRepresentation(blob,digest,text,path,mime);
        owned.current.add(representation);
        current.current=representation;
        setLoaded(representation);
        setError("");
      } catch (e) {
        if (!abort.signal.aborted) {
          if(e instanceof APIError && [401,403,404].includes(e.status)) {
            resources.clear(project);current.current=null;setLoaded(null);
          }
          setError(e instanceof Error ? e.message : "Preview failed.");
        }
      }
    })();
    return () => {
      abort.abort();
    };
  }, [href, path, validation, resources, project]);
  if (error && !loaded) return <p className="notice error" role="alert">{error}</p>;
  if (!loaded) return <p className="notice">{tr("Loading preview\u2026")}</p>;
  return <>{error && <p className="notice error" role="alert">{error}</p>}<PreviewContent loaded={loaded} path={path} href={href} /></>;
}
function PreviewContent({loaded,path,href}:{loaded:PreviewRepresentation;path:string;href:string}) {
  const tr=useT();
  const kind=classifyArtifact(path);
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
        representation={loaded}
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
  representation,
  kind,
  path,
}: {
  representation: PreviewRepresentation;
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
    const release = representation.retain();
    const {blob,url} = representation;
    // Each parse owns a detached stage. Superseded callbacks cannot touch a new one.
    const stage = document.createElement("div");
    stage.style.height = "100%";
    element.appendChild(stage);
    let gone = false;
    let dispose: () => void | Promise<void> = () => {};
    let resize = () => {};
    let frame = 0;
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!gone && element.clientWidth > 0) resize();
      });
    });
    observer.observe(element);
    const parsing = (async () => {
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
          const view = renderer.init(stage, options);
          resize = () => window.dispatchEvent(new Event("resize"));
          let rendering: Promise<unknown> = Promise.resolve();
          dispose = async () => {
            await rendering.catch(() => {});
            await new Promise<void>(resolve => setTimeout(() => {view.destroy();resolve();},0));
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
          await renderer.renderAsync(buffer, stage, undefined, {
            useBase64URL: true,
          });
        } else {
          const { init } = await import("pptx-preview");
          if (gone) return;
          const view = init(stage, {
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
      stage.remove();
      void parsing.finally(async () => {try {await dispose();} finally {release();}});
    };
  }, [representation, kind, path, tr]);
  return (
    <>
      {notice && <p className="notice">{notice}</p>}
      {error && <p className="notice error">{error}</p>}
      <div className="office-preview" ref={host} />
    </>
  );
}
