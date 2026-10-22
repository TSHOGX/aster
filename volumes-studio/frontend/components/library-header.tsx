"use client";
import { useState } from "react";
import { CircleHelp, GitCommitHorizontal } from "lucide-react";
import { Guide, StateBadge, type GuideStep } from "../../../web/guide";
import type { Library } from "../lib/api";
import type { Branch } from "../lib/branches";
import { useT } from "../lib/i18n";
import { BranchSelector } from "./branch-selector";
import { BranchCheckpoint } from "./checkpoint";

// Lifecycle position of the current library view.
export type Stage = "branch" | "edit" | "review" | "publish";

export function useLifecycleSteps(stage: Stage): GuideStep[] {
  const tr = useT();
  const order: Stage[] = ["branch", "edit", "review", "publish"];
  const position = order.indexOf(stage);
  const state = (step: Stage) => order.indexOf(step) < position ? "done" : step === stage ? "current" : "todo";
  return [
    { id: "branch", state: state("branch"), title: tr("Start a branch"), description: tr("Main holds accepted content. Everyone edits on a branch, so main changes only through review.") },
    { id: "edit", state: state("edit"), title: tr("Edit and commit"), description: tr("Save edits as a draft on the branch, then commit the saved draft as a checkpoint.") },
    { id: "review", state: state("review"), title: tr("Review and merge"), description: tr("Prepare an integration against main in the Review tab. Required checks and an approval allow merging into main.") },
    { id: "publish", state: state("publish"), title: tr("Publish a version"), description: tr("Publish an accepted commit as an immutable version, then import it in Administration for Projects.") },
  ];
}

// LibraryBar is the single toolbar above the workbench: branch switcher,
// branch state, commit and help. Branch creation, commit and the guide open
// as one-line panels under it, so the file area keeps its height.
export function LibraryBar({ id, library, branch, stage, version, creating, setCreating, onBranch, onCommitted }: {
  id: string; library?: Library | null; branch?: Branch; stage: Stage; version?: string;
  creating: boolean; setCreating: (open: boolean) => void; onBranch: () => void; onCommitted: () => void;
}) {
  const tr = useT();
  const [committing, setCommitting] = useState(false);
  const [help, setHelp] = useState(false);
  const steps = useLifecycleSteps(stage);
  const status = version ? tr("Viewing a published version")
    : branch ? tr("Draft revision {revision} · head {head}", { revision: branch.draft_revision, head: branch.head.slice(0, 7) })
    : tr("No branch selected · files are read-only");
  return <header className="library-bar">
    <h1 className="visually-hidden">{library?.name ?? tr("Library")}</h1>
    <BranchSelector library={id} creating={creating} setCreating={open => { setCreating(open); if (open) setCommitting(false); }} onSelect={onBranch} />
    <span className={`bar-status${branch || version ? "" : " warning"}`}>{status}</span>
    <span className="bar-spacer" />
    {library?.profile === "shared-assets-v1" && <StateBadge tone="info">{tr("Shared assets")}</StateBadge>}
    {library?.archived && <StateBadge tone="danger">{tr("Archived")}</StateBadge>}
    {!version && branch && <button type="button" className={committing ? "secondary" : "primary"} aria-expanded={committing} aria-controls="studio-commit" onClick={() => { setCommitting(!committing); setCreating(false); }}><GitCommitHorizontal size={15} aria-hidden="true" />{tr("Commit changes")}</button>}
    <button type="button" className="icon-button" aria-expanded={help} aria-controls="studio-guide" aria-label={tr("How changes reach a published version")} title={tr("How changes reach a published version")} onClick={() => setHelp(!help)}><CircleHelp size={17} /></button>
    {!version && branch && <div id="studio-commit" className="bar-panel" hidden={!committing}>
      <BranchCheckpoint key={`${branch.library_id}:${branch.id}`} branch={branch} onCommitted={onCommitted} />
    </div>}
    <div id="studio-guide" className="bar-panel bar-guide" hidden={!help}>
      <Guide compact label={tr("Change workflow steps")} steps={steps} current={stage} />
      <span className="hint">{tr("Evolution Space writes generated content to its own branches. Review them here like any other branch; nothing reaches main without approval.")}</span>
    </div>
  </header>;
}
