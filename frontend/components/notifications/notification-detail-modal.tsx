"use client";

import { useEffect } from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";

import { MarkdownView } from "@/components/workbench/markdown-view";
import { Button } from "@/components/ui/button";
import type { ApiNotification } from "@/lib/types";
import { useT } from "@/lib/i18n";

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
  const t = useT();
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
      className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--color-overlay)] p-4"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={notification.title}
        className="flex max-h-[85vh] w-full max-w-3xl flex-col overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-xl)]"
      >
        <div className="flex items-start justify-between gap-4 border-b border-[var(--color-border)] px-6 py-4">
          <div className="flex min-w-0 flex-col gap-1">
            <h2 className="text-base font-semibold leading-snug">{notification.title}</h2>
            <span className="text-2xs text-[var(--color-foreground-muted)]">
              {formatTimestamp(notification.created_at)}
            </span>
          </div>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label={t("common.close")} title={t("common.close")}>
            <X className="h-4 w-4" />
          </Button>
        </div>
        <div className="overflow-y-auto px-6 py-5">
          <MarkdownView content={notification.body} />
        </div>
      </div>
    </div>,
    document.body,
  );
}
