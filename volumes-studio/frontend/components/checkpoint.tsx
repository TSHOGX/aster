"use client";
import { useRef, useState } from "react";
import { branchAPI, type Branch, type Checkpoint } from "../lib/branches";
import { APIError, requestID } from "../lib/api";
import { useT } from "../lib/i18n";
import { useStudio } from "./studio-provider";

export function BranchCheckpoint({ branch, onCommitted }: { branch: Branch; onCommitted: () => void }) {
  const studio = useStudio();
  const tr = useT();
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Checkpoint>();
  const pending = useRef<{ branch: Branch; message: string; request: string } | null>(null);
  const unsaved = studio.model.buffers.some(buffer => buffer.library === branch.library_id && buffer.branch === branch.id && !buffer.version && (buffer.text !== buffer.saved || !!buffer.pending));
  const commit = async () => {
    if (!studio.session) return;
    setBusy(true); setError("");
    try {
      if (!pending.current) {
        // Save acknowledgements can arrive before the periodic branch refresh.
        // Settle this branch's writes, then pin its current saved revision.
        await studio.queue.run(`${branch.library_id}:${branch.id}`, async () => {});
        const current = await branchAPI.read(branch.library_id, branch.id);
        pending.current = { branch: current, message, request: requestID() };
      }
      const operation = pending.current;
      const checkpoint = await branchAPI.checkpoint(operation.branch, operation.message, operation.request, studio.session.csrf);
      pending.current = null; setUncertain(false); setResult(checkpoint);
      studio.selectBranch({ ...operation.branch, head: checkpoint.commit_id });
      onCommitted();
    } catch (e) {
      setError(String(e));
      if (e instanceof APIError && e.status < 500) { pending.current = null; setUncertain(false); }
      else setUncertain(true);
    } finally { setBusy(false); }
  };
  return <section aria-label={tr("Commit saved draft")} className="notice branch-checkpoint">
    <p>{tr("Saved draft revision {revision}", { revision: branch.draft_revision })} · <code>{branch.head}</code></p>
    {unsaved && <p>{tr("Unsaved editor changes are excluded from this commit.")}</p>}
    <label>{tr("Commit message")} <input value={message} disabled={busy || uncertain} onChange={event => setMessage(event.target.value)} /></label>
    <button disabled={busy || !message.trim()} onClick={() => void commit()}>{tr(uncertain ? "Retry commit" : "Commit saved draft")}</button>
    {error && <p role="alert">{error}</p>}
    {result && <p role="status">{tr("Committed saved revision {revision}", { revision: result.draft_revision })} · <code>{result.commit_id}</code></p>}
  </section>;
}
