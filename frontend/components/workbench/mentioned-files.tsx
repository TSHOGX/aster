"use client";

import { useMemo } from "react";
import { ChevronRight } from "lucide-react";

import { ArtifactIcon } from "./artifact-icon";
import { buildPathIndex, extractMentionedPaths } from "@/lib/mentioned-files";
import type { MessageKey } from "@/lib/i18n";
import { usePersistedFlag } from "@/lib/use-persisted-flag";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n";

// Global, not per-session: collapsing this once means "I don't want this
// section", and re-opening it on the next reply would be nagging.
const COLLAPSED_KEY = "workbench:mentioned-collapsed";
// About five rows before the list scrolls internally. A section at the top of the
// panel must not push the file tree off screen, however chatty a reply is.
const LIST_MAX_HEIGHT = 148;

/**
 * Which files the reply mentions, for callers that need to know *whether* there
 * are any before laying out around the section.
 *
 * The pane labels its tree "All files" only when this section is above it — with
 * nothing above, a lone header on the panel's only list is noise. Recomputing the
 * extraction in the parent to find that out would be wasteful and could disagree
 * with the child, so the answer comes from one place.
 */
export function useMentionedPaths(
  content: string,
  files: readonly { path: string }[]
): string[] {
  return useMemo(() => extractMentionedPaths(content, buildPathIndex(files)), [content, files]);
}

/**
 * The workspace files the agent's latest reply refers to, one per row.
 *
 * Renders nothing at all when the reply mentions none — the value here is being
 * an occasional pointer, and a permanently empty strip above the tree would cost
 * more than it gives.
 *
 * Data comes in as props rather than being read from the store, so this serves
 * both the workbench pane and the anonymous public-link page — which mounts no
 * provider at all and passes its own file DTO.
 *
 * On a public link the file list is the link's **whitelist**, not the workspace,
 * so a path the reply mentions but the owner did not publish resolves to nothing
 * and is silently dropped. That is the correct behaviour rather than a gap: the
 * row would 404, and naming an unpublished file would leak what exists.
 */
export function MentionedFiles({
  content,
  files,
  activePath,
  onOpen
}: {
  /** The reply's raw markdown. Empty string when there is no reply yet. */
  content: string;
  /** The files reachable in this context. Anything outside it cannot match. */
  files: readonly { path: string; size?: number }[];
  activePath: string | null;
  onOpen: (path: string) => void;
}) {
  const t = useT();
  const [collapsed, setCollapsed] = usePersistedFlag(COLLAPSED_KEY, false);

  const sizes = useMemo(() => {
    const map = new Map<string, number>();
    for (const file of files) {
      if (file.size !== undefined) map.set(file.path, file.size);
    }
    return map;
  }, [files]);

  const paths = useMentionedPaths(content, files);

  if (paths.length === 0) return null;

  return (
    <section className="shrink-0 border-b border-[var(--color-border)]">
      <SectionHeader
        labelKey="mentioned.title"
        count={paths.length}
        collapsed={collapsed}
        onToggle={() => setCollapsed(!collapsed)}
        title={collapsed ? t("mentioned.expand") : t("mentioned.collapse")}
      />
      {collapsed ? null : (
        <ul className="overflow-y-auto pb-1" style={{ maxHeight: LIST_MAX_HEIGHT }}>
          {paths.map((path) => {
            const size = sizes.get(path);
            const active = activePath === path;
            return (
              <li key={path}>
                <button
                  type="button"
                  onClick={() => onOpen(path)}
                  title={path}
                  aria-current={active ? "true" : undefined}
                  className={cn(
                    // 13px mono to match a tree row exactly: this is the same
                    // index of the same files, so it should read as a section of
                    // the tree and not as a separate widget bolted on top.
                    "flex h-7 w-full items-center gap-1.5 pl-6 pr-3 text-left text-[13px] hover:bg-[var(--color-surface-muted)]",
                    active && "bg-[var(--color-surface-muted)]"
                  )}
                >
                  <ArtifactIcon path={path} />
                  <span className="min-w-0 flex-1 truncate font-mono">
                    {path.split("/").pop() ?? path}
                  </span>
                  {size === undefined ? null : (
                    <span className="shrink-0 text-[11px] text-[var(--color-foreground-muted)]">
                      {formatBytes(size)}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/**
 * A group label for one list inside the workspace panel.
 *
 * Shared by this section and the tree's "All files" header so the two read as two
 * groups of one list — the same job the sidebar's SessionSection does for "Shared
 * with me" / "History". Before this existed the panel had two stacked lists
 * separated by nothing but a hairline, which does not say what changed across it.
 *
 * `onToggle` is optional: the tree's header is a fixed label with no disclosure,
 * so it renders as static text rather than a button that looks interactive and is
 * not.
 */
export function SectionHeader({
  labelKey,
  count,
  collapsed,
  onToggle,
  title
}: {
  labelKey: MessageKey;
  count: number;
  collapsed?: boolean;
  onToggle?: () => void;
  title?: string;
}) {
  const t = useT();
  const label = (
    <>
      <span className="font-medium uppercase tracking-wider">{t(labelKey)}</span>
      {/* The count stays visible while collapsed: a header that loses it reads as
          the section having broken rather than having been put away. */}
      <span className="tabular-nums">· {count}</span>
    </>
  );
  const shared = "flex w-full items-center gap-1.5 px-3 py-2 text-left text-2xs text-[var(--color-foreground-muted)]";

  if (!onToggle) {
    return (
      // Indented to the chevron's width so both headers' text starts on the same
      // vertical line despite only one having a disclosure control.
      <h2 className={cn(shared, "pl-[1.625rem]")}>{label}</h2>
    );
  }
  return (
    <h2>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={!collapsed}
        title={title}
        className={cn(shared, "hover:bg-[var(--color-surface-muted)]")}
      >
        <ChevronRight
          className={cn("h-3 w-3 shrink-0 transition-transform", !collapsed && "rotate-90")}
        />
        {label}
      </button>
    </h2>
  );
}
