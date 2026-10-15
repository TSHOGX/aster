/* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps -- profile catalog synchronizes remote immutable definitions. */
"use client";

import { useEffect, useState } from "react";
import { base } from "../lib/api";
import { useT } from "../lib/i18n";

type Ref = { id: string; revision: number; digest: string };
type Profile = { ref: Ref; dataset: { id: string; group_id: string; digest: string; schema_digest: string; grant_id: string; policy_revision: number; size: number; media_type: string }; scorer: Ref; runtime: Ref; model: Ref; tools: Ref[]; thresholds: { name: string; unit: string; operator: "gte" | "lte"; value: number; minimum_samples: number; minimum_coverage: number }[]; fixed_model_required: boolean };

export function ProfileEditor({ group }: { group: string }) {
  const tr = useT(); const [refs, setRefs] = useState<Ref[]>([]); const [selected, setSelected] = useState(""); const [text, setText] = useState(""); const [status, setStatus] = useState(""); const [error, setError] = useState("");
  async function load() {
    setError("");
    try { const response = await fetch(`${base}/api/v1/groups/${encodeURIComponent(group)}/profiles`, { credentials: "same-origin", cache: "no-store" }); if (!response.ok) throw new Error(); const value = await response.json() as { revisions: Ref[] }; setRefs(value.revisions); if (!selected && value.revisions[0]) await read(value.revisions[0]); }
    catch { setError(tr("Evaluator profiles are unavailable. Retry when the service is restored.")); }
  }
  async function read(ref: Ref) { setSelected(`${ref.id}:${ref.revision}`); setError(""); try { const response = await fetch(`${base}/api/v1/groups/${encodeURIComponent(group)}/profiles/${encodeURIComponent(ref.id)}/${ref.revision}?digest=${encodeURIComponent(ref.digest)}`, { credentials: "same-origin", cache: "no-store" }); if (!response.ok) throw new Error(); setText(JSON.stringify(await response.json(), null, 2)); } catch { setError(tr("Profile detail is unavailable. Retry or check group access.")); } }
  useEffect(() => { void load(); }, [group]);
  async function publish() { setStatus(""); setError(""); try { const profile = JSON.parse(text) as Profile; const response = await fetch(`${base}/api/v1/groups/${encodeURIComponent(group)}/profiles`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(profile) }); if (!response.ok) throw new Error(); const ref = await response.json() as Ref; setStatus(`${tr("Published immutable evaluator profile")}: ${ref.id} @${ref.revision}`); await load(); } catch { setError(tr("Profile validation or publication failed. Check dataset, scorer, runtime, model, tools and metric thresholds.")); } }
  return <section className="card"><div className="section-heading"><div><h2>{tr("Evaluator profile editor")}</h2><p>{tr("Profiles pin dataset, scorer, runtime, model, tools, thresholds and coverage. Every save creates an immutable revision.")}</p></div><button onClick={() => void load()}>{tr("Refresh")}</button></div>{error && <p role="alert">{error}</p>}{status && <p role="status">{status}</p>}<label>{tr("Existing profile") }<select value={selected} onChange={event => { const ref = refs.find(value => `${value.id}:${value.revision}` === event.target.value); if (ref) void read(ref); }}><option value="">{tr("Create or choose a profile")}</option>{refs.map(ref => <option key={`${ref.id}:${ref.revision}`} value={`${ref.id}:${ref.revision}`}>{ref.id} @{ref.revision}</option>)}</select></label><label>{tr("Profile JSON") }<textarea aria-label={tr("Profile JSON")} rows={14} value={text} onChange={event => setText(event.target.value)} placeholder={JSON.stringify({ ref: { id: "profile", revision: 1, digest: "sha256:…" }, dataset: {}, scorer: {}, runtime: {}, model: {}, tools: [], thresholds: [{ name: "accuracy", unit: "ratio", operator: "gte", value: 0.9, minimum_samples: 10, minimum_coverage: 0.8 }], fixed_model_required: true }, null, 2)} /></label><button className="primary" disabled={!text.trim()} onClick={() => void publish()}>{tr("Publish profile revision")}</button></section>;
}
