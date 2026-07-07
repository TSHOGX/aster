"use client";

import { useEffect } from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";

import { MarkdownView } from "@/components/workbench/markdown-view";
import { Button } from "@/components/ui/button";
import type { ApiNotification } from "@/lib/types";

function formatTimestamp(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

export function NotificationDetailModal({
  notification,
  onClose,
}: {
  notification: ApiNotification;
  onClose: () => void;
}) {
  useEffect(() => {
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [onClose]);

  if (typeof document === "undefined") return null;

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={notification.title}
        className="flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-xl"
      >
        <div className="flex items-start justify-between gap-4 border-b border-[var(--color-border)] px-5 py-4">
          <div className="flex min-w-0 flex-col gap-1">
            <h2 className="text-sm font-semibold">{notification.title}</h2>
            <span className="text-[11px] text-[var(--color-foreground-muted)]">
              {formatTimestamp(notification.created_at)}
            </span>
          </div>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close" title="Close">
            <X className="h-4 w-4" />
          </Button>
        </div>
        <div className="overflow-y-auto px-5 py-4">
          <MarkdownView content={notification.body} />
        </div>
      </div>
    </div>,
    document.body,
  );
}
