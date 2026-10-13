"use client";
import { useT } from "@/lib/i18n";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { APIError, mutate } from "@/lib/api";
import type { ProjectDetail } from "@/lib/project-models";
import type { Operation } from "@/lib/models";

type Request = { action: string; body: Record<string, unknown> };
const labels: Record<string, string> = { pause: "Pause Project", stop: "Stop Project work", restart: "Restart saved environment", "apply-network": "Apply desired network now" };
export function ProjectControls({ project, onDirty, onAccepted }: { project: ProjectDetail; onDirty: (value: boolean) => void; onAccepted: () => void }) {
  const tr = useT();
  const [acknowledged, setAcknowledged] = useState(false), [busy, setBusy] = useState(false), [pending, setPending] = useState<Request | null>(null), [operation, setOperation] = useState<Operation | null>(null), [error, setError] = useState("");
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => { onDirty(!!pending); return () => onDirty(false); }, [pending, onDirty]);
  const submit = async (request: Request) => {
    setBusy(true); setPending(request); setError("");
    try {
      const result = await mutate<{ operation: Operation; replayed: boolean }>(`/api/admin/projects/${encodeURIComponent(project.id)}/${request.action}`, request.body);
      if (!alive.current) return;
      setPending(null); setOperation(result.operation); setAcknowledged(false); onAccepted();
    } catch (error) {
      if (!alive.current) return;
      if (error instanceof APIError && error.code !== "outcome_unknown" && error.status >= 400 && error.status < 500) setPending(null);
      if (error instanceof APIError && error.code === "artifact_unavailable") setPending(null);
      setError(error instanceof APIError && error.status === 409 ? "The Project, reviewed network, or control state changed. Refresh observations and review the current state before trying again." : String(error));
    } finally { if (alive.current) setBusy(false); }
  };
  const closed = busy || !!pending || project.control_pending || ["deleting", "deleted"].includes(project.state);
  return <section className="panel" aria-labelledby="runtime-controls-title"><h2 id="runtime-controls-title">{tr("Administrative runtime controls")}</h2>
    <p>{tr("These actions interrupt this owner’s active Agents and Terminals and hold unstarted prompts. Files, conversation history and the complete saved mount plan are preserved.")}</p>
    <p>{tr("Pause and stop leave execution held until explicit resume. Restart and apply-network-now pause, recheck the owner’s current allowance, then resume the saved environment. They cannot select the newest default or resize storage.")}</p>
    {project.control_pending && <p role="status">{tr("A runtime control is already pending. Follow its operation below; refresh observations for updated progress.")}</p>}
    <label className="check-label"><input type="checkbox" checked={acknowledged} disabled={closed} onChange={event => setAcknowledged(event.target.checked)} />{tr("I reviewed the interruption and the current Project state.")}</label>
    <div className="form-actions">{Object.entries(labels).map(([action, label]) => <button key={action} disabled={closed || !acknowledged || (["restart", "apply-network"].includes(action) && (!project.bound || project.owner_status !== "active"))} onClick={() => {
      const body: Record<string, unknown> = { request_id: crypto.randomUUID(), expected_revision: project.revision, interruption_acknowledged: true };
      if (action === "apply-network") body.expected_network_revision = project.desired_network.revision;
      void submit({ action, body });
    }}>{tr(label)}</button>)}</div>
    {error && <p role="alert" className="error">{tr(error)}</p>}
    {pending && !busy && <div className="notice"><p>{tr("The outcome is unconfirmed. Check the original request before starting another operation.")}</p><button onClick={() => void submit(pending)}>{tr("Check original control request")}</button></div>}
    {operation && <div className="notice"><p role="status">{tr("Operation accepted. Runtime completion is confirmed separately by observation.")}</p><Link href={`/operations/${encodeURIComponent(operation.id)}`}>{tr("View runtime control operation")}</Link></div>}
  </section>;
}
