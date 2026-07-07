"use client";

import { useEffect, useRef, useState } from "react";
import { Bell } from "lucide-react";

import { useNotifications } from "@/components/harness-provider";
import { Button } from "@/components/ui/button";

function formatTimestamp(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

export function NotificationBell() {
  const { notifications, markNotificationRead } = useNotifications();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const unreadCount = notifications.filter((n) => !n.read).length;

  useEffect(() => {
    if (!open) return;
    const handleClick = (event: MouseEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [open]);

  return (
    <div ref={containerRef} className="relative">
      <Button
        variant="ghost"
        size="icon"
        onClick={() => setOpen((o) => !o)}
        aria-label="Notifications"
        title="Notifications"
      >
        <Bell className="h-4 w-4" />
        {unreadCount > 0 ? (
          <span className="absolute right-0.5 top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full bg-[var(--color-danger)] px-1 text-[10px] font-medium leading-none text-white">
            {unreadCount > 9 ? "9+" : unreadCount}
          </span>
        ) : null}
      </Button>
      {open ? (
        <div className="absolute right-0 top-full z-20 mt-2 w-80 max-h-96 overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-lg">
          {notifications.length === 0 ? (
            <p className="px-4 py-6 text-center text-xs text-[var(--color-foreground-muted)]">No notifications yet.</p>
          ) : (
            <ul className="divide-y divide-[var(--color-border)]">
              {notifications.map((n) => (
                <li key={n.id}>
                  <button
                    className="flex w-full flex-col gap-1 px-4 py-3 text-left hover:bg-[var(--color-surface-muted)]"
                    onClick={() => {
                      if (!n.read) void markNotificationRead(n.id);
                    }}
                  >
                    <span className="flex items-center gap-2">
                      {!n.read ? <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--color-accent)]" /> : null}
                      <span className="truncate text-sm font-medium">{n.title}</span>
                    </span>
                    <span className="text-xs text-[var(--color-foreground-muted)]">{n.body}</span>
                    <span className="text-[11px] text-[var(--color-foreground-muted)]">
                      {formatTimestamp(n.created_at)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </div>
  );
}
