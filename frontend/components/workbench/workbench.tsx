"use client";

import { useCallback, useEffect, useState } from "react";

import { FolderOpen, PanelLeftOpen, Search } from "lucide-react";

import { HarnessProvider, useAuth, useBootError, useHarness } from "@/components/harness-provider";
import { Sidebar } from "@/components/workbench/sidebar";
import { Conversation } from "@/components/workbench/conversation";
import { ArtifactPane } from "@/components/workbench/artifact-pane";
import { LoginScreen } from "@/components/workbench/login-screen";
import { SearchPalette } from "@/components/search/search-palette";
import { Button } from "@/components/ui/button";
import { Wordmark } from "@/components/ui/wordmark";
import { requestMessageJump } from "@/lib/message-jump";
import { NARROW_QUERY, useMediaQuery } from "@/lib/use-media-query";
import { cn } from "@/lib/cn";
import type { SearchHit } from "@/lib/types";

function Shell() {
  const { onLogin, selectSession } = useHarness();
  const auth = useAuth();
  const bootError = useBootError();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  // The right panel widens into an Artifact Preview when a file is open.
  const [artifactExpanded, setArtifactExpanded] = useState(false);
  // Under ~1024px three columns cannot all be usable at once, so the side panels
  // become overlay drawers over a full-width conversation.
  const narrow = useMediaQuery(NARROW_QUERY);
  const [requestedDrawer, setRequestedDrawer] = useState<"sessions" | "files" | null>(null);
  // Derived, not synced: widening the window has to close the overlay, and
  // gating on `narrow` here does that without an effect that writes state (which
  // would render the drawer for one frame at desktop width before clearing it).
  const drawer = narrow ? requestedDrawer : null;

  // Escape closes an open drawer before anything else handles the key.
  useEffect(() => {
    if (drawer === null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setRequestedDrawer(null);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [drawer]);

  // Global Ctrl/⌘-P opens the palette (overriding the browser print dialog).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "p") {
        e.preventDefault();
        setPaletteOpen(true);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const handleOpenResult = useCallback(
    (hit: SearchHit) => {
      selectSession(hit.session_id);
      // For message hits, also scroll to + flash the specific message once the
      // target session's conversation has loaded (see lib/message-jump.ts).
      if (hit.kind === "message" && typeof hit.message_id === "number") {
        requestMessageJump(hit.session_id, hit.message_id);
      }
    },
    [selectSession]
  );
  // Hold the screen until /api/me resolves; otherwise the main workbench
  // renders for one frame before we know auth is required (login flash).
  if (!auth.checked) {
    return <div className="h-dvh bg-[var(--color-background)]" />;
  }
  if (auth.required && !auth.userId) {
    return <LoginScreen onSuccess={onLogin} />;
  }
  if (bootError) {
    return (
      <div className="flex h-dvh overflow-hidden items-center justify-center bg-[var(--color-background)] p-8 text-center">
        <div className="max-w-sm rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
          <h1 className="text-base font-semibold">Backend unreachable</h1>
          <p className="mt-2 text-sm text-[var(--color-foreground-muted)]">{bootError}</p>
          <p className="mt-3 text-xs text-[var(--color-foreground-muted)]">
            Start the orchestrator (./orchestrator) and the page will recover automatically.
          </p>
        </div>
      </div>
    );
  }
  const palette = paletteOpen ? (
    <SearchPalette onClose={() => setPaletteOpen(false)} onOpenResult={handleOpenResult} />
  ) : null;

  if (narrow) {
    return (
      <div className="relative flex h-dvh min-h-0 flex-col overflow-hidden bg-[var(--color-background)]">
        <MobileBar
          onOpenSessions={() => setRequestedDrawer("sessions")}
          onOpenFiles={() => setRequestedDrawer("files")}
          onOpenSearch={() => setPaletteOpen(true)}
        />
        <div className="min-h-0 flex-1">
          <Conversation />
        </div>
        {drawer !== null ? (
          <div className="fixed inset-0 z-40 flex" role="dialog" aria-modal="true">
            <button
              className="absolute inset-0 bg-[var(--color-overlay)]"
              aria-label="Close panel"
              onClick={() => setRequestedDrawer(null)}
            />
            <div
              className={cn(
                "relative z-10 flex h-full w-[min(88vw,320px)] flex-col bg-[var(--color-surface)] shadow-[var(--shadow-xl)]",
                drawer === "sessions" ? "mr-auto" : "ml-auto"
              )}
            >
              {drawer === "sessions" ? (
                <Sidebar onOpenSearch={() => setPaletteOpen(true)} onNavigate={() => setRequestedDrawer(null)} />
              ) : (
                <ArtifactPane />
              )}
            </div>
          </div>
        ) : null}
        {palette}
      </div>
    );
  }

  // Column widths animate: the left rail collapses to an icon strip, and the
  // right panel widens when an artifact preview is open.
  const gridCols = `${sidebarCollapsed ? "64px" : "clamp(240px,20vw,300px)"} minmax(0,1fr) ${
    artifactExpanded ? "min(46vw,640px)" : "clamp(300px,26vw,380px)"
  }`;
  return (
    <div
      className="grid h-dvh min-h-0 overflow-hidden bg-[var(--color-background)] transition-[grid-template-columns] duration-300 ease-out"
      style={{ gridTemplateColumns: gridCols }}
    >
      <Sidebar
        collapsed={sidebarCollapsed}
        onToggleCollapse={() => setSidebarCollapsed((v) => !v)}
        onOpenSearch={() => setPaletteOpen(true)}
      />
      <Conversation />
      <ArtifactPane onExpandedChange={setArtifactExpanded} />
      {palette}
    </div>
  );
}

/**
 * Narrow-width top bar. Both side panels are drawers here, so their entry points
 * have to live somewhere persistent — the panels' own toggles are off-screen.
 */
function MobileBar({
  onOpenSessions,
  onOpenFiles,
  onOpenSearch
}: {
  onOpenSessions: () => void;
  onOpenFiles: () => void;
  onOpenSearch: () => void;
}) {
  return (
    <div className="flex shrink-0 items-center gap-1 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-2">
      <Button variant="ghost" size="icon" onClick={onOpenSessions} aria-label="Sessions" title="Sessions">
        <PanelLeftOpen className="h-[18px] w-[18px]" />
      </Button>
      <Wordmark className="mx-1 min-w-0 flex-1" />
      <Button variant="ghost" size="icon" onClick={onOpenSearch} aria-label="Search" title="Search">
        <Search className="h-[18px] w-[18px]" />
      </Button>
      <Button variant="ghost" size="icon" onClick={onOpenFiles} aria-label="Files" title="Files">
        <FolderOpen className="h-[18px] w-[18px]" />
      </Button>
    </div>
  );
}

export function Workbench() {
  return (
    <HarnessProvider>
      <Shell />
    </HarnessProvider>
  );
}
