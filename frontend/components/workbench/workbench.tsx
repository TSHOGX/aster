"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { FolderOpen, PanelLeftOpen, Search } from "lucide-react";

import { HarnessProvider, useAuth, useBootError, useHarness } from "@/components/harness-provider";
import { Sidebar } from "@/components/workbench/sidebar";
import { Conversation } from "@/components/workbench/conversation";
import { ArtifactPane } from "@/components/workbench/artifact-pane";
import { PaneResizer } from "@/components/workbench/pane-resizer";
import { LoginScreen } from "@/components/workbench/login-screen";
import { SearchPalette } from "@/components/search/search-palette";
import { SchedulesWorkbench } from "@/components/workbench/schedules-workbench";
import { Button } from "@/components/ui/button";
import { Wordmark } from "@/components/ui/wordmark";
import { requestMessageJump } from "@/lib/message-jump";
import { NARROW_QUERY, useMediaQuery } from "@/lib/use-media-query";
import { usePaneWidth } from "@/lib/use-pane-width";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n";
import type { SearchHit } from "@/lib/types";

// Bounds for the draggable workspace column. The floor keeps the file tree's
// name + size columns legible; the ceiling is a share of the viewport so the
// conversation cannot be squeezed out of existence on a narrow display.
const ARTIFACT_MIN = 260;
const ARTIFACT_MAX_FRACTION = 0.62;
const ARTIFACT_WIDTH_KEY = "workbench:artifact-width";

function Shell({ initialView = "conversation" }: { initialView?: "conversation" | "schedules" }) {
  const t = useT();
  const { onLogin, selectSession, discardEmptySession } = useHarness();
  const auth = useAuth();
  const bootError = useBootError();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [workspaceView, setWorkspaceView] = useState<"conversation" | "schedules">(initialView);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  // The right panel widens into an Artifact Preview when a file is open.
  const [artifactExpanded, setArtifactExpanded] = useState(false);
  // User-chosen workspace width, persisted and clamped to the viewport.
  const {
    width: artifactWidth,
    max: artifactMax,
    setWidth: setArtifactWidth,
    reset: resetArtifactWidth
  } = usePaneWidth({
    key: ARTIFACT_WIDTH_KEY,
    min: ARTIFACT_MIN,
    maxFraction: ARTIFACT_MAX_FRACTION
  });
  const [resizing, setResizing] = useState(false);
  const artifactColRef = useRef<HTMLDivElement | null>(null);
  // The column's true rendered width, so the resizer can report an honest
  // aria-valuenow while the width is still the responsive CSS default.
  const [measuredArtifact, setMeasuredArtifact] = useState<number | null>(null);
  // Under ~1024px three columns cannot all be usable at once, so the side panels
  // become overlay drawers over a full-width conversation.
  const narrow = useMediaQuery(NARROW_QUERY);
  const [requestedDrawer, setRequestedDrawer] = useState<"sessions" | "files" | null>(null);
  // Derived, not synced: widening the window has to close the overlay, and
  // gating on `narrow` here does that without an effect that writes state (which
  // would render the drawer for one frame at desktop width before clearing it).
  const drawer = narrow ? requestedDrawer : null;

  // Subscribing to the element's own size is the only way to know the width the
  // CSS clamp() actually resolved to. Re-run when `narrow` flips, since the
  // column is unmounted in the drawer layout.
  useEffect(() => {
    const el = artifactColRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) setMeasuredArtifact(w);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [narrow]);

  // The handle sits on the column's left edge, so the width is the distance from
  // the pointer to the column's right edge (which is the viewport's right edge).
  const resizeFromPointer = useCallback(
    (clientX: number) => {
      const right = artifactColRef.current?.getBoundingClientRect().right ?? window.innerWidth;
      setArtifactWidth(right - clientX);
    },
    [setArtifactWidth]
  );

  // Keyboard steps measure from what is on screen, so the first arrow press works
  // from the CSS default rather than snapping to some remembered number.
  const nudgeArtifact = useCallback(
    (delta: number) => {
      const current =
        artifactColRef.current?.getBoundingClientRect().width ?? measuredArtifact ?? ARTIFACT_MIN;
      setArtifactWidth(current + delta);
    },
    [setArtifactWidth, measuredArtifact]
  );

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
  const handleOpenSession = useCallback((id: string) => {
    selectSession(id);
    setWorkspaceView("conversation");
  }, [selectSession]);
  const handleOpenSchedules = useCallback(() => {
    // Schedules is another top-level view. Treat an untouched new conversation
    // as abandoned when leaving it, just like selecting another session does.
    void discardEmptySession();
    setWorkspaceView("schedules");
    setRequestedDrawer(null);
  }, [discardEmptySession]);
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
          <h1 className="text-base font-semibold">{t("boot.title")}</h1>
          <p className="mt-2 text-sm text-[var(--color-foreground-muted)]">{bootError}</p>
          <p className="mt-3 text-xs text-[var(--color-foreground-muted)]">
            {t("boot.hint")}
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
          {workspaceView === "schedules" ? <SchedulesWorkbench onOpenSession={handleOpenSession} /> : <Conversation />}
        </div>
        {drawer !== null ? (
          <div className="fixed inset-0 z-40 flex" role="dialog" aria-modal="true">
            <button
              className="absolute inset-0 bg-[var(--color-overlay)]"
              aria-label={t("common.closePanel")}
              onClick={() => setRequestedDrawer(null)}
            />
            <div
              className={cn(
                "relative z-10 flex h-full w-[min(88vw,320px)] flex-col bg-[var(--color-surface)] shadow-[var(--shadow-xl)]",
                drawer === "sessions" ? "mr-auto" : "ml-auto"
              )}
            >
              {drawer === "sessions" ? (
                <Sidebar onOpenSearch={() => setPaletteOpen(true)} onOpenSchedules={handleOpenSchedules} onOpenConversation={() => setWorkspaceView("conversation")} schedulesActive={workspaceView === "schedules"} onNavigate={() => setRequestedDrawer(null)} />
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
  // right panel widens when an artifact preview is open. Once the user has
  // dragged the workspace edge their width wins over both defaults — an explicit
  // choice should not be overridden by opening a file.
  const artifactCol =
    artifactWidth !== null
      ? `${artifactWidth}px`
      : artifactExpanded
      ? "min(46vw,640px)"
      : "clamp(300px,26vw,380px)";
  // Floor stays at 240px: below that the header's wordmark starts truncating
  // against the four icon buttons sharing its row. The ceiling and vw share come
  // down instead, which is what narrows the rail at ordinary window sizes.
  const gridCols = `${sidebarCollapsed ? "64px" : "clamp(240px,17vw,264px)"} minmax(0,1fr) ${artifactCol}`;
  return (
    <div
      className={cn(
        "grid h-dvh min-h-0 overflow-hidden bg-[var(--color-background)]",
        // No width transition mid-drag: animating toward each pointer sample puts
        // the edge permanently behind the cursor and feels like lag.
        resizing ? "" : "transition-[grid-template-columns] duration-300 ease-out"
      )}
      style={{ gridTemplateColumns: gridCols }}
    >
      <Sidebar
        collapsed={sidebarCollapsed}
        onToggleCollapse={() => setSidebarCollapsed((v) => !v)}
        onOpenSearch={() => setPaletteOpen(true)}
        onOpenSchedules={handleOpenSchedules}
        onOpenConversation={() => setWorkspaceView("conversation")}
        schedulesActive={workspaceView === "schedules"}
      />
      {workspaceView === "schedules" ? <div className="col-span-2 min-h-0 min-w-0"><SchedulesWorkbench onOpenSession={handleOpenSession} /></div> : <>
        <Conversation />
        {/* `relative` so the resizer can pin itself to this column's left edge. */}
        <div ref={artifactColRef} className="relative h-full min-h-0 min-w-0">
          <PaneResizer
            ariaLabel={t("workspace.resize")}
            hint={t("workspace.resizeHint")}
            width={artifactWidth ?? measuredArtifact ?? ARTIFACT_MIN}
            min={ARTIFACT_MIN}
            max={artifactMax}
            onResize={resizeFromPointer}
            onNudge={nudgeArtifact}
            onReset={resetArtifactWidth}
            onDragStateChange={setResizing}
          />
          <ArtifactPane onExpandedChange={setArtifactExpanded} />
        </div>
      </>}
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
  const t = useT();
  return (
    <div className="flex shrink-0 items-center gap-1 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-2">
      <Button variant="ghost" size="icon" onClick={onOpenSessions} aria-label={t("nav.sessions")} title={t("nav.sessions")}>
        <PanelLeftOpen className="h-[18px] w-[18px]" />
      </Button>
      <Wordmark className="mx-1 min-w-0 flex-1" />
      <Button variant="ghost" size="icon" onClick={onOpenSearch} aria-label={t("sidebar.search")} title={t("sidebar.search")}>
        <Search className="h-[18px] w-[18px]" />
      </Button>
      <Button variant="ghost" size="icon" onClick={onOpenFiles} aria-label={t("nav.files")} title={t("nav.files")}>
        <FolderOpen className="h-[18px] w-[18px]" />
      </Button>
    </div>
  );
}

export function Workbench({ initialView = "conversation" }: { initialView?: "conversation" | "schedules" }) {
  return (
    <HarnessProvider>
      <Shell initialView={initialView} />
    </HarnessProvider>
  );
}
