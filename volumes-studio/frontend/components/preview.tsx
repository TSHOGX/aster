/* eslint-disable @next/next/no-img-element -- Authenticated, bounded blob previews must not use the public image optimizer. */
"use client";
import { useT } from "../lib/i18n";

import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import { BufferFile } from "./studio-provider";
import { contentURL } from "./shell";
import { parentPath } from "../lib/workbench";
function resolvePath(path: string, href: string) {
  if (
    /^[a-z][a-z\d+.-]*:/i.test(href) ||
    href.startsWith("//") ||
    href.startsWith("/")
  )
    return null;
  const out = parentPath(path).split("/").filter(Boolean);
  for (const part of decodeURIComponent(href.split("#")[0]).split("/")) {
    if (part === "..") {
      if (!out.length) return null;
      out.pop();
    } else if (part !== "." && part) out.push(part);
  }
  return out.join("/");
}
function MarkdownImage({
  buffer,
  src,
  alt,
}: {
  buffer: BufferFile;
  src: string;
  alt?: string;
}) {
  const tr = useT();
  const source = JSON.stringify([
    buffer.library,
    buffer.version,
    buffer.entry.path,
    buffer.revision,
    src,
  ]);
  const [image, setImage] = useState({ source: "", url: "" });
  useEffect(() => {
    let path: string | null = null;
    try {
      path = resolvePath(buffer.entry.path, src);
    } catch {}
    if (!path || !/\.(png|jpe?g|gif|webp)$/i.test(path)) return;
    const abort = new AbortController();
    let objectURL = "";
    void fetch(
      contentURL(buffer.library, buffer.version, path, buffer.revision,buffer.branch),
      { signal: abort.signal },
    )
      .then(async (r) => {
        if (!r.ok || Number(r.headers.get("content-length")) > 16 << 20) return;
        const b = await r.blob();
        if (b.size > 16 << 20) return;
        if (abort.signal.aborted) return;
        objectURL = URL.createObjectURL(b);
        setImage({ source, url: objectURL });
      })
      .catch(() => {});
    return () => {
      abort.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [
    buffer.branch,
    buffer.library,
    buffer.version,
    buffer.entry.path,
    buffer.revision,
    src,
    source,
  ]);
  return image.source === source && image.url ? (
    <img src={image.url} alt={alt || ""} />
  ) : (
    <span className="muted">{alt || tr("Image")}  {tr("(image not loaded)")}</span>
  );
}
export function MarkdownPreview({
  buffer,
  onOpen,
}: {
  buffer: BufferFile;
  onOpen: (path: string) => void;
}) {
  const tr = useT();
  return (
    <article className="markdown-preview">
      <ReactMarkdown
        components={{
          img: ({ src, alt }) => (
            <MarkdownImage
              buffer={buffer}
              src={typeof src === "string" ? src : ""}
              alt={alt}
            />
          ),
          a: ({ href, children }) => {
            let path: string | null = null;
            try {
              path = resolvePath(buffer.entry.path, href || "");
            } catch {}
            return path ? (
              <a
                href={`?path=${encodeURIComponent(path)}`}
                onClick={(e) => {
                  e.preventDefault();
                  onOpen(path!);
                }}
              >
                {children}
              </a>
            ) : (
              <a href={href} target="_blank" rel="noopener noreferrer">
                {children}
              </a>
            );
          },
        }}
      >
        {buffer.text}
      </ReactMarkdown>
    </article>
  );
}
export function ImagePreview({ buffer }: { buffer: BufferFile }) {
  const tr = useT();
  const [zoom, setZoom] = useState(0);
  const [size, setSize] = useState({ source: "", width: 0 });
  const nativeWidth = size.source === buffer.image ? size.width : 0;
  return (
    <div className="image-panel">
      <div className="image-controls">
        <button aria-pressed={!zoom} onClick={() => setZoom(0)}>
           {tr("Fit")} </button>
        <button aria-pressed={zoom === 100} onClick={() => setZoom(100)}>
          100%
        </button>
        <button
          aria-label={tr("Zoom out")}
          disabled={zoom === 25}
          onClick={() => setZoom((n) => Math.max(25, (n || 100) - 25))}
        >
          −
        </button>
        <span>{zoom ? `${zoom}%` : tr("Fit to window")}</span>
        <button
          aria-label={tr("Zoom in")}
          disabled={zoom === 400}
          onClick={() => setZoom((n) => Math.min(400, (n || 100) + 25))}
        >
          +
        </button>
      </div>
      <div className={`image-scroll ${zoom ? "" : "fit"}`}>
        <img
          src={buffer.image}
          alt={buffer.entry.path}
          onLoad={(e) =>
            setSize({
              source: buffer.image || "",
              width: e.currentTarget.naturalWidth,
            })
          }
          style={
            zoom ? {
                  maxWidth: "none",
                  width: nativeWidth ? (nativeWidth * zoom) / 100 : undefined,
                } : undefined
          }
        />
      </div>
    </div>
  );
}
