"use client";

import { useCallback, useEffect, useState } from "react";

import { HarnessProvider, useHarness } from "@/components/harness-provider";
import { Sidebar } from "@/components/workbench/sidebar";
import { Conversation } from "@/components/workbench/conversation";
import { ArtifactPane } from "@/components/workbench/artifact-pane";
import { LoginScreen } from "@/components/workbench/login-screen";
import { SearchPalette } from "@/components/search/search-palette";
import { requestMessageJump } from "@/lib/message-jump";
import type { SearchHit } from "@/lib/types";

function Shell() {
  const { state, onLogin, selectSession } = useHarness();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  // The right panel widens into an Artifact Preview when a file is open.
  const [artifactExpanded, setArtifactExpanded] = useState(false);

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
  if (!state.auth.checked) {
    return <div className="h-dvh bg-[var(--color-background)]" />;
  }
  if (state.auth.required && !state.auth.userId) {
    return <LoginScreen onSuccess={onLogin} />;
  }
  if (state.bootError) {
    return (
      <div className="flex h-dvh overflow-hidden items-center justify-center bg-[var(--color-background)] p-8 text-center">
        <div className="max-w-sm rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
          <h1 className="text-base font-semibold">Backend unreachable</h1>
          <p className="mt-2 text-sm text-[var(--color-foreground-muted)]">{state.bootError}</p>
          <p className="mt-3 text-xs text-[var(--color-foreground-muted)]">
            Start the orchestrator (./orchestrator) and the page will recover automatically.
          </p>
        </div>
      </div>
    );
  }
  // Column widths animate: the left rail collapses to an icon strip, and the
  // right panel widens when an artifact preview is open.
  const gridCols = `${sidebarCollapsed ? "64px" : "280px"} minmax(0,1fr) ${
    artifactExpanded ? "min(46vw,640px)" : "360px"
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
      {paletteOpen ? (
        <SearchPalette onClose={() => setPaletteOpen(false)} onOpenResult={handleOpenResult} />
      ) : null}
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
