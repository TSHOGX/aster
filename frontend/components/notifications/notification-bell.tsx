"use client";

import { useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Bell } from "lucide-react";

import { useNotifications } from "@/components/harness-provider";
import { Button } from "@/components/ui/button";
import { NotificationDetailModal } from "@/components/notifications/notification-detail-modal";
import { useAnchoredPosition, useDismissOnOutside } from "@/lib/use-popover";
import type { ApiNotification } from "@/lib/types";
import { useT } from "@/lib/i18n";

const PANEL_WIDTH = 320;
const VIEWPORT_MARGIN = 8;

function formatRelativeTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const diffMs = Date.now() - date.getTime();
  const diffSec = Math.round(diffMs / 1000);
  if (diffSec < 60) return "just now";
  const diffMin = Math.round(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHour = Math.round(diffMin / 60);
  if (diffHour < 24) return `${diffHour}h ago`;
  const diffDay = Math.round(diffHour / 24);
  if (diffDay < 7) return `${diffDay}d ago`;
  return date.toLocaleDateString();
}

export function NotificationBell() {
  const t = useT();
  const { notifications, markNotificationRead } = useNotifications();
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<ApiNotification | null>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const unreadCount = notifications.filter((n) => !n.read).length;

  const panelPos = useAnchoredPosition(open, buttonRef, {
    width: PANEL_WIDTH,
    gap: 8,
    margin: VIEWPORT_MARGIN,
    align: "left"
  });
  useDismissOnOutside(open, [buttonRef, panelRef], () => setOpen(false));

  const handleSelect = (n: ApiNotification) => {
    setSelected(n);
    setOpen(false);
    if (!n.read) void markNotificationRead(n.id);
  };

  return (
    <div className="relative">
      <Button
        ref={buttonRef}
        variant="ghost"
        size="icon"
        onClick={() => setOpen((o) => !o)}
        aria-label={t("notifications.title")}
        title={t("notifications.title")}
      >
        <Bell className="h-4 w-4" />
        {unreadCount > 0 ? (
          <span className="absolute right-0.5 top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full bg-[var(--color-danger)] px-1 text-2xs font-medium leading-none text-white">
            {unreadCount > 9 ? "9+" : unreadCount}
          </span>
        ) : null}
      </Button>
      {open && panelPos && typeof document !== "undefined"
        ? createPortal(
            <div
              ref={panelRef}
              style={{ top: panelPos.top, left: panelPos.left, width: PANEL_WIDTH }}
              className="fixed z-20 max-h-96 overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-lg)]"
            >
              {notifications.length === 0 ? (
                <p className="px-4 py-6 text-center text-xs text-[var(--color-foreground-muted)]">No notifications yet.</p>
              ) : (
                <ul className="divide-y divide-[var(--color-border)]">
                  {notifications.map((n) => (
                    <li key={n.id}>
                      <button
                        className="flex w-full flex-col gap-1 px-4 py-3 text-left hover:bg-[var(--color-surface-muted)]"
                        onClick={() => handleSelect(n)}
                      >
                        <span className="flex items-center gap-2">
                          {!n.read ? <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--color-accent)]" /> : null}
                          <span className="min-w-0 flex-1 truncate text-sm font-medium">{n.title}</span>
                          <span className="shrink-0 text-2xs text-[var(--color-foreground-muted)]">
                            {formatRelativeTime(n.created_at)}
                          </span>
                        </span>
                        <span className="truncate text-xs text-[var(--color-foreground-muted)]">{n.body}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>,
            document.body
          )
        : null}
      {selected ? (
        <NotificationDetailModal notification={selected} onClose={() => setSelected(null)} />
      ) : null}
    </div>
  );
}
