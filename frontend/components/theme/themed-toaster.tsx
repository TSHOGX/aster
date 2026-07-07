"use client";

import { Toaster } from "sonner";

import { useTheme } from "@/components/theme/use-theme";

/**
 * Toaster bound to the in-app theme (not the OS), with sonner's CSS variables
 * mapped onto our palette tokens so toasts match the rest of the UI in both
 * light and dark. Falls back to "system" until the app theme is resolved.
 */
export function ThemedToaster() {
  const theme = useTheme();
  return (
    <Toaster
      position="top-right"
      theme={theme ?? "system"}
      style={
        {
          "--normal-bg": "var(--color-surface)",
          "--normal-text": "var(--color-foreground)",
          "--normal-border": "var(--color-border)",
          "--success-bg": "var(--color-surface)",
          "--success-text": "var(--color-success)",
          "--success-border": "var(--color-border)",
          "--error-bg": "var(--color-surface)",
          "--error-text": "var(--color-danger)",
          "--error-border": "var(--color-border)",
          "--info-bg": "var(--color-surface)",
          "--info-text": "var(--color-foreground)",
          "--info-border": "var(--color-border)"
        } as React.CSSProperties
      }
    />
  );
}
