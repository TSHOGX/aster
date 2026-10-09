"use client";

import { useEffect, useState } from "react";
import { useT } from "@/lib/i18n";
import { reservePreview } from "@/lib/preview-budget";
import type { HtmlPreviewModel } from "@/lib/html-preview-model";

const HTML_POLICY = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";

export function HTMLPreview({ text, path, href }: { text: string; path: string; href: string }) {
  const tr = useT();
  const [model, setModel] = useState<HtmlPreviewModel | null>(null);
  const [failed, setFailed] = useState(false);
  const [admitted, setAdmitted] = useState(false);
  useEffect(() => {
    let disposed = false;
    let release = () => {};
    let worker: Worker | undefined;
    const timer = setTimeout(() => {
      if (disposed) return;
      setFailed(true); worker?.terminate(); release(); release = () => {};
    }, 10_000);
    try {
      release = reservePreview(0, "workers");
      worker = new Worker(new URL("../../lib/html-preview.worker.ts", import.meta.url), { type: "module" });
      worker.onmessage = (event: MessageEvent<HtmlPreviewModel>) => {
        clearTimeout(timer);
        if (!disposed) setModel(event.data);
        worker?.terminate(); release(); release = () => {};
      };
      worker.onerror = () => { clearTimeout(timer); if (!disposed) setFailed(true); worker?.terminate(); release(); release = () => {}; };
      worker.postMessage({ source: text });
    } catch {
      clearTimeout(timer);
      worker?.terminate(); release();
      queueMicrotask(() => { if (!disposed) setFailed(true); });
    }
    return () => { disposed = true; clearTimeout(timer); worker?.terminate(); release(); };
  }, [text]);
  useEffect(() => {
    if (!model || model.simplified) return;
    let release = () => {};
    try { release = reservePreview(0, "frames"); queueMicrotask(() => setAdmitted(true)); }
    catch { queueMicrotask(() => setFailed(true)); }
    return release;
  }, [model]);
  if (failed) return <p className="notice">{tr("Preview is busy. Choose Source to read this file.")}</p>;
  if (!model) return <p className="notice">{tr("Preparing preview…")}</p>;
  const scriptNotice = model.scripts > 0 && <p className="preview-reading-hint" role="status">
    {tr("Scripts are disabled in this preview. Download the original HTML to interact with generated charts.")} {" "}
    <a href={href} download>{tr("Download file")}</a>
  </p>;
  if (model.simplified) return <div className="html-simplified" role="region" aria-label={`${tr("Simplified HTML preview")} ${path}`}>
    <div className="preview-reading-bar"><strong>{tr("Simplified HTML preview")}</strong><span>{tr("{count} elements inspected", { count: model.nodes })}</span></div>
    <p className="preview-reading-hint" role="status">{tr("Showing {shown} of {total} text blocks. Choose Source for the complete document.", { shown: model.blocks.length, total: model.blocks.length + model.omittedBlocks })}</p>
    {scriptNotice}
    <article>{model.blocks.map((block, index) => block.kind === "heading"
      ? <h3 key={index} role="heading" aria-level={block.level ?? 3}>{block.text}</h3>
      : <p key={index} className={block.kind === "row" ? "html-simplified-row" : undefined}>{block.text}</p>)}</article>
  </div>;
  return <div className="html-reader">
    <div className="preview-reading-bar"><strong>{tr("Static HTML preview")}</strong><span>{tr("{count} elements inspected", { count: model.nodes })}</span></div>
    {scriptNotice}
    {admitted ? <iframe className="document-frame html-preview" title={`${tr("Preview")} ${path}`} sandbox="" referrerPolicy="no-referrer"
      srcDoc={`<!doctype html><meta http-equiv="Content-Security-Policy" content="${HTML_POLICY}"><meta name="referrer" content="no-referrer">${text}`} />
      : <p className="notice">{tr("Loading preview…")}</p>}
  </div>;
}
