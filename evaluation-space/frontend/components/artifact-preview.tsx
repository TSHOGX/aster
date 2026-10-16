"use client";

import { useEffect, useRef, useState } from "react";
import { useT } from "../lib/i18n";

type Preview = { state: "complete" | "truncated" | "binary"; content: string; bytes: number; truncated: boolean; total?: number };
const limit = 1 << 20;

// Inline content stays inert and bounded. Every open/retry resolves the current
// source grant through the same endpoint as an artifact download.
export function ArtifactPreview({ root, id, label }: { root: string; id: string; label?: string }) {
  const tr = useT(); const active = useRef<AbortController | null>(null);
  const [open, setOpen] = useState(false); const [busy, setBusy] = useState(false);
  const [value, setValue] = useState<Preview>(); const [error, setError] = useState("");
  const href = `${root}/artifacts/${encodeURIComponent(id)}?max_bytes=${limit}`;
  useEffect(() => () => active.current?.abort(), [href]);
  async function read() {
    active.current?.abort(); const controller = new AbortController(); active.current = controller;
    setOpen(true); setBusy(true); setValue(undefined); setError("");
    try {
      const response = await fetch(href, { credentials: "same-origin", cache: "no-store", signal: controller.signal });
      if (!response.ok) {
        const message = response.status === 401 || response.status === 403 ? "Artifact access was denied." : response.status === 404 ? "Artifact content is missing." : response.status === 410 ? "Artifact content has expired." : "Artifact content is unavailable. Retry when access or service is restored.";
        throw new Error(message);
      }
      const reader = response.body?.getReader(); if (!reader) throw new Error();
      const chunks: Uint8Array[] = []; let bytes = 0; let truncated = response.headers.get("X-Artifact-Truncated") === "true";
      try {
        for (;;) {
          const result = await reader.read(); if (result.done) break;
          const available = limit - bytes; chunks.push(result.value.slice(0, available)); bytes += Math.min(available, result.value.byteLength);
          if (result.value.byteLength > available) { truncated = true; await reader.cancel(); break; }
        }
      } finally { reader.releaseLock(); }
      if (controller.signal.aborted) return;
      const body = new Uint8Array(bytes); let offset = 0;
      for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.byteLength; }
      const media = response.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase() ?? "";
      const textual = media.startsWith("text/") || media === "application/json" || media.endsWith("+json");
      const rawSize = response.headers.get("X-Artifact-Size"); const total = rawSize === null ? undefined : Number(rawSize);
      setValue({ state: textual ? truncated ? "truncated" : "complete" : "binary", content: textual ? new TextDecoder().decode(body) : "", bytes, truncated, total: Number.isSafeInteger(total) && total! >= 0 ? total : undefined });
    } catch (failure) {
      if (!controller.signal.aborted) setError(failure instanceof Error && failure.message.startsWith("Artifact ") ? failure.message : "Artifact content is unavailable. Retry when access or service is restored.");
    } finally { if (active.current === controller) { active.current = null; setBusy(false); } }
  }
  function close() { active.current?.abort(); active.current = null; setOpen(false); setValue(undefined); setError(""); setBusy(false); }
  return <span className="artifact-preview"><a href={href} onClick={event => { event.preventDefault(); void read(); }}>{label ?? tr("Open bounded content")}</a>{open && <span className="artifact-preview-panel" role="region" aria-label={`${tr("Artifact preview")}: ${id}`}>
    <strong>{id}</strong>
    {busy && <span role="status">{tr("Loading artifact content…")}</span>}
    {error && <span role="alert">{tr(error)}</span>}
    {value && <><span role="status">{tr(value.state === "complete" ? "Complete artifact content." : value.state === "truncated" ? "Truncated preview. Only the first bytes are shown." : "Binary content cannot be previewed as text.")} {value.bytes} {tr("bytes loaded")}{value.total !== undefined ? ` / ${value.total}` : ""}</span>{value.state === "binary" && value.truncated && <span role="status">{tr("Truncated preview. Only the first bytes are shown.")}</span>}{value.state !== "binary" && <code className="artifact-preview-body">{value.content || tr("Empty artifact content.")}</code>}{value.state === "binary" && <a href={href}>{tr("Download bounded bytes")}</a>}</>}
    <button disabled={busy} onClick={() => void read()}>{tr("Retry")}</button><button onClick={close}>{tr("Close")}</button>
  </span>}</span>;
}
