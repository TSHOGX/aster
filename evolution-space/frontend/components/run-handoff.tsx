"use client";
import { studioBranchPath } from "../../../web/application-paths";
import { useT } from "../lib/i18n";

export function RunHandoff({ run }: { run: {
  state: string; handoff: string; result_branch?: string;
  draft_test?: unknown; pins: { output?: { library_id: string; prefix: string } };
} }) {
  const tr = useT();
  if (run.draft_test || run.state !== "completed") return null;
  const library = run.pins.output?.library_id;
  return <section className="card" aria-label={tr("Review output")}>
    <h2>{tr("Review output")}</h2>
    {run.handoff === "submitted" && library && run.result_branch ? <>
      <p>{tr("Generation is complete. Review and merge the output branch in Volume Studio, then publish a version.")}</p>
      <a className="button" href={studioBranchPath(library, run.result_branch)}>{tr("Open branch in Volume Studio")}</a>
    </> : run.handoff === "submitted" ? <p role="status">{tr("The output reference is unavailable. Refresh to recover the review link.")}</p> : run.handoff === "no_change" ? <p>{tr("This run produced no changes. No output branch needs review.")}</p>
      : <p role="status">{tr("Output transfer is pending. It retries automatically; refresh to check progress without rerunning generation.")}</p>}
  </section>;
}
