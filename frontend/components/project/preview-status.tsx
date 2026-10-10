"use client";

import { Download, FileText, LoaderCircle, RotateCcw, X } from "lucide-react";
import { PREVIEW_LIMITS } from "@/lib/preview-budget";
import { useT } from "@/lib/i18n";

export function previewSize(bytes: number) {
  return bytes >= 1024 * 1024
    ? `${Number((bytes / (1024 * 1024)).toFixed(1))} MB`
    : `${Math.max(1, Math.ceil(bytes / 1024))} KB`;
}

export function PreviewStatus({ path, href, title, description, loading = false, size, progress, onCancel, onRetry, maxBytes=PREVIEW_LIMITS.blobBytes }: {
  path: string;
  href?: string;
  title: string;
  description: string;
  loading?: boolean;
  size?: number;
  progress?: { received: number; total?: number };
  onCancel?: () => void;
  onRetry?: () => void;
  maxBytes?: number;
}) {
  const tr = useT();
  const percent = progress?.total ? Math.min(100, Math.floor(progress.received / progress.total * 100)) : undefined;
  return <div className="preview-status-shell">
    <div className="preview-status-card">
      <div className="preview-status-icon" aria-hidden="true">
        {loading ? <LoaderCircle className="preview-spinner" size={26} /> : <FileText size={26} />}
      </div>
      <div className="preview-file-meta">
        <span title={path}>{path.split("/").pop()}</span>
        {size !== undefined && <small>{previewSize(size)}</small>}
      </div>
      <div role="status" aria-live="polite" aria-atomic="true">
        <h3>{title}</h3>
        <p className="preview-status-description">{description}</p>
      </div>
      {loading && <div className="preview-transfer">
        <progress aria-label={tr("Loading preview…")} max={100} value={percent} />
        <span>{progress && progress.received > 0 ? `${previewSize(progress.received)}${progress.total ? ` / ${previewSize(progress.total)}` : ""}` : tr("Preparing preview…")}</span>
      </div>}
      <div className="preview-status-actions">
        {href && <a className={loading ? "" : "preview-action-primary"} href={href} download aria-label={`${tr("Download")} ${path}`}><Download size={16} aria-hidden="true" />{tr("Download file")}</a>}
        {onCancel && <button onClick={onCancel}><X size={16} aria-hidden="true" />{tr("Cancel preview")}</button>}
        {onRetry && <button onClick={onRetry}><RotateCcw size={16} aria-hidden="true" />{tr("Retry")}</button>}
      </div>
      <small className="preview-status-footnote">{tr("Preview files up to {size}", { size: previewSize(maxBytes) })}</small>
    </div>
  </div>;
}
