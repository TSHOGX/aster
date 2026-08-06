"use client";

import { useState } from "react";
import { Download, FileText } from "lucide-react";

import type { SharedLinkFile } from "@/lib/types";
import { formatBytes } from "@/lib/format";

/**
 * The files a public link exposes.
 *
 * These are frozen snapshots served from `/shared/{token}/files/...`, not the
 * session's live workspace: the list is the link's whitelist, so anything the
 * owner did not check is not reachable at all.
 *
 * Previews are framed same-origin. That works because the orchestrator sends
 * `X-Frame-Options: SAMEORIGIN` for HTML/PDF snapshots and `next.config.mjs`
 * excepts `/shared/` from the app-wide `DENY` — if both headers were sent the
 * browser would block the frame and the panel would just be blank.
 */
export function SharedFilePanel({ token, files }: { token: string; files: SharedLinkFile[] }) {
  const [openPath, setOpenPath] = useState<string | null>(null);
  if (files.length === 0) return null;

  const fileUrl = (path: string) =>
    `/shared/${encodeURIComponent(token)}/files/${path
      .split("/")
      .map((segment) => encodeURIComponent(segment))
      .join("/")}`;

  const open = files.find((file) => file.path === openPath) ?? null;

  return (
    <aside className="flex w-full shrink-0 flex-col border-t border-[var(--color-border)] md:w-80 md:border-l md:border-t-0">
      <header className="border-b border-[var(--color-border)] px-4 py-2.5">
        <h2 className="text-2xs font-medium uppercase tracking-wider text-[var(--color-foreground-muted)]">
          Files
        </h2>
      </header>
      <ul className="max-h-56 overflow-y-auto md:max-h-none md:flex-1">
        {files.map((file) => {
          const selected = file.path === openPath;
          return (
            <li key={file.path}>
              <div
                className={
                  selected
                    ? "flex items-center gap-2 bg-[var(--color-surface-muted)] px-4 py-2"
                    : "flex items-center gap-2 px-4 py-2 hover:bg-[var(--color-surface-muted)]"
                }
              >
                <button
                  type="button"
                  onClick={() => setOpenPath(selected ? null : file.path)}
                  className="flex min-w-0 flex-1 items-center gap-2 text-left"
                  aria-expanded={selected}
                >
                  <FileText className="h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)]" />
                  <span className="min-w-0 flex-1 truncate font-mono text-2xs">{file.path}</span>
                  <span className="shrink-0 text-2xs text-[var(--color-foreground-muted)]">
                    {formatBytes(file.size)}
                  </span>
                </button>
                <a
                  href={fileUrl(file.path)}
                  download
                  className="shrink-0 rounded p-1 text-[var(--color-foreground-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-foreground)]"
                  aria-label={`Download ${file.path}`}
                  title="Download"
                >
                  <Download className="h-3.5 w-3.5" />
                </a>
              </div>
            </li>
          );
        })}
      </ul>
      {open ? <SharedFilePreview url={fileUrl(open.path)} path={open.path} /> : null}
    </aside>
  );
}

function SharedFilePreview({ url, path }: { url: string; path: string }) {
  const extension = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
  const framable = extension === "html" || extension === "htm" || extension === "pdf";
  const image = ["png", "jpg", "jpeg", "gif", "webp", "svg"].includes(extension);

  if (framable) {
    return (
      <div className="border-t border-[var(--color-border)]">
        <iframe
          src={url}
          title={path}
          // The snapshot's own CSP already forbids outbound connections and
          // forms; sandbox here is belt-and-braces for a public page.
          sandbox="allow-same-origin allow-scripts allow-popups"
          className="h-72 w-full bg-white md:h-96"
        />
      </div>
    );
  }
  if (image) {
    return (
      <div className="border-t border-[var(--color-border)] p-3">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={url} alt={path} className="max-h-72 w-full object-contain md:max-h-96" />
      </div>
    );
  }
  return (
    <div className="border-t border-[var(--color-border)] p-3">
      <p className="text-2xs text-[var(--color-foreground-muted)]">
        No inline preview for this file type.{" "}
        <a href={url} download className="underline">
          Download it
        </a>{" "}
        to open it locally.
      </p>
    </div>
  );
}
