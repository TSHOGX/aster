"use client";
import { useState } from "react";
import { Guide, StateBadge, type GuideStep } from "../../../web/guide";
import type { Library } from "../lib/api";
import type { Branch } from "../lib/branches";
import { useT } from "../lib/i18n";

// Lifecycle position of the current library view. Branch work moves from
// editing to review; main and published versions are where reviewed work lands.
export type Stage = "branch" | "edit" | "review" | "publish";

const storageKey = "volume-studio:guide";

// LibraryHeader names the library, shows which branch is being edited and
// explains, in order, how work moves from a branch to a published version.
export function LibraryHeader({ library, branch, stage, version }: { library?: Library | null; branch?: Branch; stage: Stage; version?: string }) {
  const tr = useT();
  // The full guide opens by default only before a branch is chosen; once work
  // starts it stays out of the editor's way unless the user opens it.
  const [choice, setChoice] = useState<"open" | "closed" | "">(() => {
    try { const value = typeof window !== "undefined" ? localStorage.getItem(storageKey) : ""; return value === "open" || value === "closed" ? value : ""; } catch { return ""; }
  });
  const collapsed = choice ? choice === "closed" : stage !== "branch";
  const toggle = () => {
    const next = collapsed ? "open" : "closed";
    try { localStorage.setItem(storageKey, next); } catch { /* storage disabled */ }
    setChoice(next);
  };
  const order: Stage[] = ["branch", "edit", "review", "publish"];
  const position = order.indexOf(stage);
  const state = (step: Stage) => order.indexOf(step) < position ? "done" : step === stage ? "current" : "todo";
  const steps: GuideStep[] = [
    { id: "branch", state: state("branch"), title: tr("Start a branch"), description: tr("Main holds accepted content. Everyone edits on a branch, so main changes only through review.") },
    { id: "edit", state: state("edit"), title: tr("Edit and commit"), description: tr("Save edits as a draft on the branch, then commit the saved draft as a checkpoint.") },
    { id: "review", state: state("review"), title: tr("Review and merge"), description: tr("Prepare an integration against main. Required checks and an approval allow merging into main.") },
    { id: "publish", state: state("publish"), title: tr("Publish a version"), description: tr("Publish an accepted commit as an immutable version, then import it in Administration for Projects.") },
  ];
  return <header className="library-header">
    <div className="library-title">
      <div>
        <h1>{library?.name ?? tr("Library")}</h1>
        {library?.description && <p>{library.description}</p>}
      </div>
      <div className="library-meta">
        <StateBadge tone={library?.profile === "shared-assets-v1" ? "info" : "neutral"}>{tr(library?.profile === "shared-assets-v1" ? "Shared assets" : "Ordinary data")}</StateBadge>
        {version ? <StateBadge tone="info">{tr("Viewing a published version")}</StateBadge>
          : branch ? <StateBadge tone="success">{tr("Branch")}: {branch.name}</StateBadge>
          : <StateBadge tone="warning">{tr("No branch selected")}</StateBadge>}
        {library?.archived && <StateBadge tone="danger">{tr("Archived")}</StateBadge>}
        <button type="button" className="ghost" aria-expanded={!collapsed} onClick={toggle}>{tr(collapsed ? "Show how it works" : "Hide guide")}</button>
      </div>
    </div>
    {!collapsed && <Guide label={tr("How changes reach a published version")} steps={steps} current={stage}
      note={tr("Evolution Space writes generated content to its own branches. Review them here like any other branch; nothing reaches main without approval.")} />}
  </header>;
}
