"use client";

import { HarnessProvider, useHarness } from "@/components/harness-provider";
import { Sidebar } from "@/components/workbench/sidebar";
import { Conversation } from "@/components/workbench/conversation";
import { ArtifactPane } from "@/components/workbench/artifact-pane";
import { LoginScreen } from "@/components/workbench/login-screen";

function Shell() {
  const { state, onLogin } = useHarness();
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
  return (
    <div className="grid h-dvh min-h-0 overflow-hidden grid-cols-[280px_minmax(0,1fr)_400px] bg-[var(--color-background)]">
      <Sidebar />
      <Conversation />
      <ArtifactPane />
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
