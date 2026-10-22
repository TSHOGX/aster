"use client";
import { useSearchParams } from "next/navigation";
import { useState } from "react";
import { base, type Session } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";
import type { ExamplePage, Threshold } from "../lib/examples";

const enc = encodeURIComponent;
type Artifact = { id: string; group_id: string; schema_digest: string; digest: string; size: number; media_type: string; grant_id: string; policy_revision: number };
type DatasetPage = { items: Artifact[]; next_after: string; has_more: boolean };
type Pin = { id: string; revision: number; digest: string };
const emptyPin = (): Pin => ({ id: "", revision: 1, digest: "" });
const digestPattern = /^sha256:[a-f0-9]{64}$/;
const validPin = (pin: Pin) => /^[A-Za-z0-9_-]{1,128}$/.test(pin.id) && Number.isSafeInteger(pin.revision) && pin.revision > 0 && digestPattern.test(pin.digest);
const mediaTypes: Record<string, string> = { json: "application/json", jsonl: "application/x-ndjson", ndjson: "application/x-ndjson", csv: "text/csv", txt: "text/plain" };

async function session(): Promise<Session> {
  const response = await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" });
  if (!response.ok) throw new Error("session");
  return response.json() as Promise<Session>;
}

// Step-by-step profile authoring. It publishes the same immutable profile
// document as the JSON editor; the server still validates and digests it.
export function ProfileWizard({ group, onPublished }: { group: string; onPublished: () => void }) {
  const tr = useT(); const search = useSearchParams(); const root = `/groups/${enc(group)}`;
  const examples = useRead<ExamplePage>(`${root}/examples`);
  const datasets = useRead<DatasetPage>(`${root}/datasets?limit=100`);
  const [template, setTemplate] = useState(search.get("preset") ?? "");
  const starter = examples.data?.items.find(item => item.template === template);
  const [id, setID] = useState(() => (search.get("preset") ?? "").replace(/^example-/, "") ? `${(search.get("preset") ?? "").replace(/^example-/, "")}-baseline` : "");
  const [revision, setRevision] = useState(1);
  const [dataset, setDataset] = useState("");
  const [thresholds, setThresholds] = useState<Threshold[] | null>(null);
  const [scorer, setScorer] = useState<Pin>(emptyPin); const [runtime, setRuntime] = useState<Pin>(emptyPin); const [model, setModel] = useState<Pin>(emptyPin);
  const [fixed, setFixed] = useState(true);
  const [upload, setUpload] = useState<File>(); const [uploadID, setUploadID] = useState("");
  const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const [status, setStatus] = useState("");
  const rows = thresholds ?? starter?.baseline.thresholds ?? [];
  const chosen = datasets.data?.items.find(item => item.id === dataset);
  function choosePreset(value: string) {
    setTemplate(value); setThresholds(null); setStatus("");
    if (value && !id) setID(`${value.replace(/^example-/, "")}-baseline`);
  }
  function editRow(index: number, change: Partial<Threshold>) { setThresholds(rows.map((row, i) => i === index ? { ...row, ...change } : row)); }
  async function uploadDataset() {
    if (!upload) return;
    const extension = upload.name.split(".").pop()?.toLowerCase() ?? "";
    const media = mediaTypes[extension];
    const name = uploadID || upload.name.replace(/\.[^.]+$/, "").toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64);
    if (!media) { setError("Use a .json, .jsonl, .csv or .txt file."); return; }
    if (upload.size > 4 << 20) { setError("Datasets uploaded here are limited to 4 MiB. Ask an operator to seal larger datasets."); return; }
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(name)) { setError("Choose a dataset ID with letters, digits, hyphens or underscores."); return; }
    setBusy(true); setError(""); setStatus("");
    try {
      const current = await session();
      const response = await fetch(`${base}/api/v1${root}/datasets?id=${enc(name)}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": media, "X-CSRF-Token": current.csrf }, body: upload });
      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as { error?: string };
        setError(body.error === "dataset_conflict" ? "A different dataset already uses this ID. Choose another ID." : body.error?.startsWith("invalid_json") || body.error === "invalid_jsonl_dataset" ? "The file is not valid JSON or JSON Lines." : response.status === 403 ? "Uploading datasets requires permission to manage configuration." : "Upload failed. Retry when the service is restored.");
        return;
      }
      const ref = await response.json() as Artifact;
      setDataset(ref.id); setUpload(undefined); setUploadID(""); datasets.refresh(); setStatus(tr("Dataset {id} sealed.", { id: ref.id }));
    } catch { setError("The upload result is unknown. Retry; the same file and ID replay safely."); } finally { setBusy(false); }
  }
  const ready = /^[A-Za-z0-9_-]{1,128}$/.test(id) && revision > 0 && !!chosen && rows.length > 0 && rows.every(row => /^[A-Za-z0-9_-]{1,128}$/.test(row.name) && row.unit && Number.isFinite(row.value) && row.minimum_samples >= 1 && row.minimum_coverage >= 0 && row.minimum_coverage <= 1) && validPin(scorer) && validPin(runtime) && validPin(model);
  async function publish() {
    if (!ready || !chosen) return;
    setBusy(true); setError(""); setStatus("");
    try {
      const current = await session();
      const body = { ref: { id, revision }, dataset: chosen, scorer, runtime, model, tools: [], thresholds: rows, fixed_model_required: fixed };
      const response = await fetch(`${base}/api/v1${root}/profiles`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": current.csrf }, body: JSON.stringify(body) });
      if (!response.ok) { setError(response.status === 409 ? "This ID and revision already exist with different content. Increase the revision." : response.status === 403 ? "Publishing profiles requires permission to manage configuration." : "The profile was rejected. Check every field and retry."); return; }
      const ref = await response.json() as Pin;
      setStatus(tr("Published evaluation profile {id} @{revision}. Select it in a binding's evaluation policy.", { id: ref.id, revision: String(ref.revision) })); setRevision(ref.revision + 1); onPublished();
    } catch { setError("The result is unknown. Publishing the same profile again recovers it."); } finally { setBusy(false); }
  }
  const pinFields = (label: string, help: string, value: Pin, set: (pin: Pin) => void) => <fieldset className="pin-fields"><legend>{tr(label)}</legend><p className="hint">{tr(help)}</p>
    <div className="pin-row"><label>{tr("ID")}<input aria-label={`${tr(label)}: ${tr("ID")}`} value={value.id} onChange={event => set({ ...value, id: event.target.value.trim() })} /></label>
      <label>{tr("Revision")}<input aria-label={`${tr(label)}: ${tr("Revision")}`} type="number" min={1} value={value.revision} onChange={event => set({ ...value, revision: Number(event.target.value) })} /></label>
      <label className="wide">{tr("Digest")}<input aria-label={`${tr(label)}: ${tr("Digest")}`} placeholder="sha256:…" value={value.digest} aria-invalid={!!value.digest && !digestPattern.test(value.digest)} onChange={event => set({ ...value, digest: event.target.value.trim() })} /></label></div></fieldset>;
  return <section className="card profile-wizard" aria-labelledby="wizard-heading">
    <h2 id="wizard-heading">{tr("Create an evaluation profile")}</h2>
    <p className="hint">{tr("A profile says how to measure a candidate: which held-out dataset, which scorer, and which metric thresholds count as a pass. A configuration binding then chooses Off, Informational or Required for merge and points at this profile.")}</p>
    <ol className="steps">
      <li><h3>{tr("Start from a baseline")}</h3>
        <label>{tr("Baseline preset")}<select aria-label={tr("Baseline preset")} value={template} onChange={event => choosePreset(event.target.value)}><option value="">{tr("Start from scratch")}</option>{examples.data?.items.map(item => <option key={item.template} value={item.template}>{item.template.replace(/^example-/, "")}</option>)}</select></label>
        {starter && <p className="hint">{tr(starter.baseline.description)} <code>{starter.baseline.thresholds.map(row => row.name).join(", ")}</code></p>}
        <div className="pin-row"><label>{tr("Profile ID")}<input aria-label={tr("Profile ID")} value={id} onChange={event => setID(event.target.value.trim())} /></label><label>{tr("Revision")}<input aria-label={tr("Profile revision")} type="number" min={1} value={revision} onChange={event => setRevision(Number(event.target.value))} /></label></div>
      </li>
      <li><h3>{tr("Choose the held-out dataset")}</h3>
        <p className="hint">{tr("The dataset is passed unchanged to the evaluator service, so its format is whatever that service's scorer reads. JSON Lines with one test case per line is the common choice. Keep these cases out of the sources the workflows learn from.")}</p>
        {starter && <><p className="hint">{tr("Example line for this baseline")}: {tr(starter.baseline.dataset)}</p><pre className="document-preview">{starter.baseline.dataset_example}</pre></>}
        {chosen && <p className="hint">{tr("Format")}: <code>{chosen.media_type}</code> · <code title={chosen.digest}>{chosen.digest.slice(0, 19)}…</code></p>}{datasets.status ? <p role="alert">{tr("Datasets are unavailable. Retry or check group access.")}</p> : <label>{tr("Dataset")}<select aria-label={tr("Dataset")} value={dataset} onChange={event => setDataset(event.target.value)}><option value="">{tr(datasets.data?.items.length ? "Choose a dataset" : "No datasets yet. Upload one below.")}</option>{datasets.data?.items.map(item => <option key={item.id} value={item.id}>{item.id} · {Math.max(1, Math.round(item.size / 1024))} KiB</option>)}</select></label>}
        <details className="advanced" open={!datasets.data?.items.length}><summary>{tr("Upload a dataset")}</summary>
          <div className="pin-row"><label className="wide">{tr("File (.json, .jsonl, .csv, .txt, up to 4 MiB)")}<input aria-label={tr("Dataset file")} type="file" accept=".json,.jsonl,.ndjson,.csv,.txt" onChange={event => setUpload(event.target.files?.[0])} /></label><label>{tr("Dataset ID")}<input aria-label={tr("Dataset ID")} placeholder={tr("From file name")} value={uploadID} onChange={event => setUploadID(event.target.value.trim())} /></label></div>
          <button disabled={busy || !upload} onClick={() => void uploadDataset()}>{tr("Upload and seal")}</button>
          <p className="hint">{tr("Sealing records the exact bytes and digest. An administrator still grants the evaluator service use of this dataset before an evaluation runs.")}</p>
        </details>
      </li>
      <li><h3>{tr("Pin the scorer")}</h3>
        <p className="hint">{tr("Your operator registers an evaluator service and gives you these exact revisions. The evaluator runs the candidate with this runtime and model, applies the scorer to every case, and reports the metrics below.")}</p>
        {pinFields("Scorer", "Code that grades each case and reports metrics.", scorer, setScorer)}
        {pinFields("Runtime", "Environment the evaluator runs candidates in.", runtime, setRuntime)}
        {pinFields("Model", "Model used during evaluation. Keep it fixed to compare runs fairly.", model, setModel)}
        <label className="check-label"><input type="checkbox" checked={fixed} onChange={event => setFixed(event.target.checked)} />{tr("Require the exact model; otherwise report the result as inconclusive")}</label>
      </li>
      <li><h3>{tr("Set pass thresholds")}</h3>
        <p className="hint">{tr("A result passes only if every metric is reported with enough samples and coverage and meets its threshold. Delta metrics compare the candidate with the reference content, so 0 means \"no worse than now\".")}</p>
        <div className="table-scroll"><table aria-label={tr("Thresholds")}><thead><tr><th scope="col">{tr("Metric")}</th><th scope="col">{tr("Unit")}</th><th scope="col">{tr("Rule")}</th><th scope="col">{tr("Value")}</th><th scope="col">{tr("Minimum samples")}</th><th scope="col">{tr("Minimum coverage")}</th><th /></tr></thead>
          <tbody>{rows.map((row, index) => <tr key={index}>
            <td><input aria-label={`${tr("Metric")} ${index + 1}`} value={row.name} onChange={event => editRow(index, { name: event.target.value.trim() })} /></td>
            <td><input aria-label={`${tr("Unit")} ${index + 1}`} value={row.unit} onChange={event => editRow(index, { unit: event.target.value })} /></td>
            <td><select aria-label={`${tr("Rule")} ${index + 1}`} value={row.operator} onChange={event => editRow(index, { operator: event.target.value as Threshold["operator"] })}><option value="gte">≥</option><option value="lte">≤</option></select></td>
            <td><input aria-label={`${tr("Value")} ${index + 1}`} type="number" step="any" value={row.value} onChange={event => editRow(index, { value: Number(event.target.value) })} /></td>
            <td><input aria-label={`${tr("Minimum samples")} ${index + 1}`} type="number" min={1} value={row.minimum_samples} onChange={event => editRow(index, { minimum_samples: Number(event.target.value) })} /></td>
            <td><input aria-label={`${tr("Minimum coverage")} ${index + 1}`} type="number" min={0} max={1} step="0.05" value={row.minimum_coverage} onChange={event => editRow(index, { minimum_coverage: Number(event.target.value) })} /></td>
            <td><button aria-label={`${tr("Remove metric")} ${index + 1}`} onClick={() => setThresholds(rows.filter((_, i) => i !== index))}>✕</button></td></tr>)}</tbody></table></div>
        <button onClick={() => setThresholds([...rows, { name: "", unit: "ratio", operator: "gte", value: 0.8, minimum_samples: 20, minimum_coverage: 0.9 }])}>{tr("Add metric")}</button>
      </li>
    </ol>
    {error && <p role="alert">{tr(error)}</p>}{status && <p role="status">{status}</p>}
    <div className="editor-actions"><button className="primary" disabled={busy || !ready} onClick={() => void publish()}>{tr("Publish evaluation profile")}</button>{!ready && <small className="hint">{tr("Fill in the profile ID, dataset, all three pins and at least one complete threshold.")}</small>}</div>
  </section>;
}
