"use client";

import { forwardRef, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Check, Copy, MoreHorizontal, ThumbsDown, ThumbsUp } from "lucide-react";
import { toast } from "sonner";

import { useHarness } from "@/components/harness-provider";
import type { ApiMessage } from "@/lib/types";
import { copyTextToClipboard } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { stripMarkdown } from "@/lib/format";
import { useAnchoredPosition, useDismissOnOutside } from "@/lib/use-popover";

const MENU_WIDTH = 200;
const VIEWPORT_MARGIN = 8;

const IconButton = forwardRef<
  HTMLButtonElement,
  React.ButtonHTMLAttributes<HTMLButtonElement> & { active?: boolean }
>(({ active, className, ...rest }, ref) => (
  <button
    ref={ref}
    type="button"
    className={cn(
      "inline-flex h-7 w-7 items-center justify-center rounded-[var(--radius-sm)] transition-colors",
      active
        ? "text-[var(--color-accent)] bg-[var(--color-accent-soft)]"
        : "text-[var(--color-foreground-muted)] hover:text-[var(--color-foreground)] hover:bg-[var(--color-surface-muted)]",
      className
    )}
    {...rest}
  />
));
IconButton.displayName = "IconButton";

export function MessageActions({ message, sessionId }: { message: ApiMessage; sessionId: string }) {
  const { submitFeedback } = useHarness();
  const [copied, setCopied] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const rating = message.feedback ?? "";

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1200);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const menuPos = useAnchoredPosition(menuOpen, menuButtonRef, {
    width: MENU_WIDTH,
    gap: 6,
    margin: VIEWPORT_MARGIN,
    align: "right"
  });
  useDismissOnOutside(menuOpen, [menuButtonRef, menuRef], () => setMenuOpen(false));

  const rate = (next: "up" | "down") => {
    void submitFeedback(sessionId, message.id, rating === next ? null : next);
  };

  const copyText = async (text: string, label: string) => {
    if (await copyTextToClipboard(text)) return true;
    toast.error(`Failed to copy ${label}`);
    return false;
  };

  const handleCopy = async () => {
    if (await copyText(stripMarkdown(message.content), "message")) {
      setCopied(true);
      toast.success("Copied");
    }
  };

  const handleMenuAction = async (action: "sessionId" | "markdown") => {
    setMenuOpen(false);
    if (action === "sessionId") {
      if (await copyText(sessionId, "session id")) toast.success("已复制 session id");
    } else {
      if (await copyText(message.content, "markdown")) toast.success("已复制为 Markdown");
    }
  };

  return (
    <div className="mt-1.5 flex items-center gap-0.5">
      <IconButton
        active={rating === "up"}
        aria-label="Good response"
        aria-pressed={rating === "up"}
        title="Good response"
        onClick={() => rate("up")}
      >
        <ThumbsUp className="h-3.5 w-3.5" fill={rating === "up" ? "currentColor" : "none"} />
      </IconButton>
      <IconButton
        active={rating === "down"}
        aria-label="Bad response"
        aria-pressed={rating === "down"}
        title="Bad response"
        onClick={() => rate("down")}
      >
        <ThumbsDown className="h-3.5 w-3.5" fill={rating === "down" ? "currentColor" : "none"} />
      </IconButton>
      <IconButton aria-label="Copy message" title="Copy message" onClick={handleCopy}>
        {copied ? <Check className="h-3.5 w-3.5 text-[var(--color-success)]" /> : <Copy className="h-3.5 w-3.5" />}
      </IconButton>
      <IconButton
        ref={menuButtonRef}
        aria-label="More actions"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        title="More"
        onClick={() => setMenuOpen((o) => !o)}
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </IconButton>
      {menuOpen && menuPos && typeof document !== "undefined"
        ? createPortal(
            <div
              ref={menuRef}
              role="menu"
              style={{ top: menuPos.top, left: menuPos.left, width: MENU_WIDTH }}
              className="fixed z-20 overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] py-1 shadow-lg"
            >
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1.5 text-left text-xs text-[var(--color-foreground)] hover:bg-[var(--color-surface-muted)]"
                onClick={() => void handleMenuAction("sessionId")}
              >
                复制 session id
              </button>
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1.5 text-left text-xs text-[var(--color-foreground)] hover:bg-[var(--color-surface-muted)]"
                onClick={() => void handleMenuAction("markdown")}
              >
                复制为 Markdown
              </button>
            </div>,
            document.body
          )
        : null}
    </div>
  );
}
