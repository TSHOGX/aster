"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy, Link2, Loader2, RotateCcw, Trash2, X } from "lucide-react";
import { toast } from "sonner";

import {
  createSessionShare,
  createShareLink,
  fetchArtifacts,
  fetchSessionShares,
  fetchShareLinks,
  fetchUsers,
  revokeSessionShare,
  revokeShareLink,
  rotateShareLink
} from "@/lib/api";
import type { ApiArtifact, ApiSessionShare, ApiShareLink, ShareVisibility } from "@/lib/types";
import { formatBytes } from "@/lib/format";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatFullTimestamp } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { useDismissOnOutside } from "@/lib/use-popover";

/**
 * Owner-only sharing controls for one session: read grants to named users, and
 * public links.
 *
 * Both halves are owner-gated on the server; this dialog is only ever opened
 * from a row the caller owns. A reader never sees the entry point, and would get
 * a 404 from every call here if they reached it anyway.
 */
export function ShareDialog({
  sessionId,
  sessionTitle,
  isShell,
  onClose
}: {
  sessionId: string;
  sessionTitle: string;
  isShell: boolean;
  onClose: () => void;
}) {
  const [shares, setShares] = useState<ApiSessionShare[]>([]);
  const [links, setLinks] = useState<ApiShareLink[]>([]);
  const [users, setUsers] = useState<string[]>([]);
  const [artifacts, setArtifacts] = useState<ApiArtifact[]>([]);
  // Files to freeze into the next link. Empty means a text-only link: a public
  // link exposes only what is checked here, never the whole workspace.
  const [selectedFiles, setSelectedFiles] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [grantee, setGrantee] = useState("");
  // Identity shares default to full: inside the org, seeing the work is the point.
  const [shareVisibility, setShareVisibility] = useState<ShareVisibility>("full");
  // A public link defaults to summary. Shell sessions cannot be summarized at
  // all (the command output IS the content), so the server forces full there and
  // rejects an explicit summary; mirror that here instead of offering a choice
  // that would 400.
  const [linkVisibility, setLinkVisibility] = useState<ShareVisibility>(isShell ? "full" : "summary");
  const [busy, setBusy] = useState(false);
  // The raw token, held only in memory and only until the dialog closes. The
  // server keeps a hash, so this is the one chance to copy it.
  const [freshToken, setFreshToken] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const dialogRef = useRef<HTMLDivElement | null>(null);
  useDismissOnOutside(true, [dialogRef], onClose, { escape: true });

  const load = useCallback(async () => {
    const [sharesRes, linksRes, usersRes, artifactsRes] = await Promise.all([
      fetchSessionShares(sessionId),
      fetchShareLinks(sessionId),
      fetchUsers(),
      fetchArtifacts(sessionId)
    ]);
    return {
      shares: sharesRes.ok ? sharesRes.data.shares ?? [] : [],
      links: linksRes.ok ? linksRes.data.links ?? [] : [],
      users: usersRes.ok ? (usersRes.data.users ?? []).map((user) => user.user_id) : [],
      artifacts: artifactsRes.ok ? artifactsRes.data.artifacts ?? [] : []
    };
  }, [sessionId]);

  useEffect(() => {
    let cancelled = false;
    void load().then((next) => {
      if (cancelled) return;
      setShares(next.shares);
      setLinks(next.links);
      setUsers(next.users);
      setArtifacts(next.artifacts);
      setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [load]);

  const refresh = async () => {
    const next = await load();
    setShares(next.shares);
    setLinks(next.links);
    setUsers(next.users);
    setArtifacts(next.artifacts);
  };

  const toggleFile = (path: string) => {
    setSelectedFiles((current) =>
      current.includes(path) ? current.filter((entry) => entry !== path) : [...current, path]
    );
  };

  const addShare = async () => {
    const target = grantee.trim();
    if (!target || busy) return;
    setBusy(true);
    const res = await createSessionShare(sessionId, target, shareVisibility);
    setBusy(false);
    if (!res.ok) {
      toast.error(res.error);
      return;
    }
    setGrantee("");
    await refresh();
  };

  const removeShare = async (shareId: string) => {
    setBusy(true);
    const res = await revokeSessionShare(sessionId, shareId);
    setBusy(false);
    if (!res.ok) {
      toast.error(res.error);
      return;
    }
    await refresh();
  };

  const mintLink = async () => {
    setBusy(true);
    const res = await createShareLink(sessionId, linkVisibility, undefined, selectedFiles);
    setBusy(false);
    if (!res.ok) {
      toast.error(res.error);
      return;
    }
    setFreshToken(res.data.path);
    setCopied(false);
    setSelectedFiles([]);
    await refresh();
  };

  const rotate = async (linkId: string) => {
    setBusy(true);
    const res = await rotateShareLink(sessionId, linkId);
    setBusy(false);
    if (!res.ok) {
      toast.error(res.error);
      return;
    }
    setFreshToken(res.data.path);
    setCopied(false);
    toast.success("Old link invalidated");
    await refresh();
  };

  const revoke = async (linkId: string) => {
    setBusy(true);
    const res = await revokeShareLink(sessionId, linkId);
    setBusy(false);
    if (!res.ok) {
      toast.error(res.error);
      return;
    }
    setFreshToken(null);
    await refresh();
  };

  const copyLink = async () => {
    if (!freshToken) return;
    const url = `${window.location.origin}${freshToken}`;
    if (await copyTextToClipboard(url)) {
      setCopied(true);
      return;
    }
    toast.error("Could not copy the link");
  };

  const shareable = users.filter((user) => !shares.some((share) => share.grantee === user));

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 px-4 py-16">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={`Share ${sessionTitle}`}
        className="w-full max-w-md overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)]"
      >
        <header className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
          <div className="min-w-0">
            <h2 className="truncate text-sm font-medium">Share</h2>
            <p className="truncate text-2xs text-[var(--color-foreground-muted)]">{sessionTitle}</p>
          </div>
          <Button variant="ghost" size="icon" className="h-7 w-7" onClick={onClose} aria-label="Close">
            <X className="h-4 w-4" />
          </Button>
        </header>

        {loading ? (
          <p className="px-4 py-6 text-xs text-[var(--color-foreground-muted)]">Loading…</p>
        ) : (
          <div className="max-h-[60vh] overflow-y-auto px-4 py-3">
            <section>
              <h3 className="text-2xs font-medium uppercase tracking-wider text-[var(--color-foreground-muted)]">
                People
              </h3>
              <div className="mt-2 flex gap-2">
                <input
                  list="share-users"
                  value={grantee}
                  onChange={(event) => setGrantee(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      void addShare();
                    }
                  }}
                  placeholder={shareable.length > 0 ? "Username" : "No other users configured"}
                  className="min-w-0 flex-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-2 py-1.5 text-sm outline-none transition-colors focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent-soft)]"
                />
                <datalist id="share-users">
                  {shareable.map((user) => (
                    <option key={user} value={user} />
                  ))}
                </datalist>
                <select
                  value={shareVisibility}
                  onChange={(event) => setShareVisibility(event.target.value as ShareVisibility)}
                  aria-label="Visibility"
                  className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-2 py-1.5 text-xs"
                >
                  <option value="full">Full</option>
                  <option value="summary">Summary</option>
                </select>
                <Button size="sm" onClick={() => void addShare()} disabled={busy || !grantee.trim()}>
                  Add
                </Button>
              </div>
              <ul className="mt-2 space-y-1">
                {shares.length === 0 ? (
                  <li className="py-1 text-xs text-[var(--color-foreground-muted)]">Not shared with anyone.</li>
                ) : (
                  shares.map((share) => (
                    <li
                      key={share.id}
                      className="flex items-center justify-between gap-2 rounded-[var(--radius)] px-2 py-1.5 hover:bg-[var(--color-surface-muted)]"
                    >
                      <span className="min-w-0 truncate text-sm">{share.grantee}</span>
                      <span className="shrink-0 text-2xs text-[var(--color-foreground-muted)]">
                        {share.visibility === "full" ? "full" : "summary"} · read-only
                      </span>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-6 w-6 shrink-0"
                        onClick={() => void removeShare(share.id)}
                        disabled={busy}
                        aria-label={`Remove ${share.grantee}`}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </li>
                  ))
                )}
              </ul>
            </section>

            <section className="mt-5 border-t border-[var(--color-border)] pt-4">
              <h3 className="text-2xs font-medium uppercase tracking-wider text-[var(--color-foreground-muted)]">
                Public link
              </h3>
              <p className="mt-1 text-2xs text-[var(--color-foreground-muted)]">
                Anyone with the link can read this conversation without logging in. The link expires, and
                shows the conversation as it stands now — later messages are not included.
              </p>
              <div className="mt-2 flex items-center gap-2">
                <select
                  value={linkVisibility}
                  onChange={(event) => setLinkVisibility(event.target.value as ShareVisibility)}
                  disabled={isShell}
                  aria-label="Link visibility"
                  className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-background)] px-2 py-1.5 text-xs disabled:opacity-60"
                >
                  <option value="summary">Summary</option>
                  <option value="full">Full, including execution trace</option>
                </select>
                <Button size="sm" onClick={() => void mintLink()} disabled={busy}>
                  {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Link2 className="h-3.5 w-3.5" />}
                  Create link
                </Button>
              </div>
              {isShell ? (
                <p className="mt-1.5 text-2xs text-[var(--color-foreground-muted)]">
                  Shell sessions are always shared in full: the command output is the content.
                </p>
              ) : linkVisibility === "full" ? (
                <p className="mt-1.5 text-2xs text-[var(--color-warning,var(--color-foreground-muted))]">
                  Full includes shell commands, file contents and queries from the execution trace.
                </p>
              ) : null}

              {artifacts.length > 0 ? (
                <div className="mt-3">
                  <p className="text-2xs font-medium">Files to include</p>
                  <p className="mt-0.5 text-2xs text-[var(--color-foreground-muted)]">
                    Only what you check is reachable through the link, and a copy is frozen now — later
                    changes to these files will not appear. Nothing checked means the conversation only.
                  </p>
                  <ul className="mt-1.5 max-h-40 space-y-0.5 overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border)] p-1">
                    {artifacts.map((artifact) => (
                      <li key={artifact.path}>
                        <label className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 hover:bg-[var(--color-surface-muted)]">
                          <input
                            type="checkbox"
                            checked={selectedFiles.includes(artifact.path)}
                            onChange={() => toggleFile(artifact.path)}
                            className="h-3.5 w-3.5"
                          />
                          <span className="min-w-0 flex-1 truncate font-mono text-2xs">{artifact.path}</span>
                          <span className="shrink-0 text-2xs text-[var(--color-foreground-muted)]">
                            {formatBytes(artifact.size)}
                          </span>
                        </label>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {freshToken ? (
                <div className="mt-3 rounded-[var(--radius)] border border-[var(--color-accent)] bg-[var(--color-surface-muted)] p-2.5">
                  <p className="text-2xs font-medium">Copy this link now — it is not shown again.</p>
                  <div className="mt-1.5 flex items-center gap-2">
                    <code className="min-w-0 flex-1 truncate rounded bg-[var(--color-background)] px-2 py-1 font-mono text-2xs">
                      {freshToken}
                    </code>
                    <Button size="sm" variant="ghost" onClick={() => void copyLink()}>
                      {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                      {copied ? "Copied" : "Copy"}
                    </Button>
                  </div>
                </div>
              ) : null}

              <ul className="mt-2 space-y-1">
                {links.length === 0 ? (
                  <li className="py-1 text-xs text-[var(--color-foreground-muted)]">No public links.</li>
                ) : (
                  links.map((link) => (
                    <li
                      key={link.id}
                      className="flex items-center justify-between gap-2 rounded-[var(--radius)] px-2 py-1.5 hover:bg-[var(--color-surface-muted)]"
                    >
                      <span className="min-w-0 flex-1 truncate text-2xs">
                        {link.visibility} ·{" "}
                        {link.expired ? (
                          <span className="text-[var(--color-foreground-muted)]">expired</span>
                        ) : (
                          <>
                            expires{" "}
                            <time dateTime={link.expires_at} title={formatFullTimestamp(link.expires_at)}>
                              {new Date(link.expires_at).toLocaleDateString()}
                            </time>
                          </>
                        )}{" "}
                        · {link.access_count} view{link.access_count === 1 ? "" : "s"}
                      </span>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-6 w-6 shrink-0"
                        onClick={() => void rotate(link.id)}
                        disabled={busy}
                        aria-label="Replace link"
                        title="Issue a new link and invalidate the old one"
                      >
                        <RotateCcw className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-6 w-6 shrink-0"
                        onClick={() => void revoke(link.id)}
                        disabled={busy}
                        aria-label="Revoke link"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </li>
                  ))
                )}
              </ul>
            </section>
          </div>
        )}
      </div>
    </div>
  );
}
