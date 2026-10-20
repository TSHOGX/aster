"use client";
import { useState } from "react";
import { base, type Session } from "../lib/api";
import { useT } from "../lib/i18n";
import { useRead } from "../lib/use-read";
import { PageHeading } from "./group-layout";
import { useSession } from "./group-context";

type Settings = { base_url: string; model: string; max_output_tokens: number; temperature?: number; json_mode: boolean };
type Policy = { call_tokens: number; input_micros_per_million: number; output_micros_per_million: number; timeout_seconds: number; concurrency: number };
type Connection = { id: string; name: string; enabled: boolean; settings: Settings; policy: Policy; current: { id: string; revision: number; digest: string }; key_set: boolean; key_hint?: string; updated_at: string };
type Page = { items: Connection[]; key_encryption: boolean };
type TestResult = { ok: boolean; status: number; latency_ms: number; response_model?: string; reply?: string; tokens: number; error?: string; models?: string[] };
type Draft = { id: string; name: string; enabled: boolean; base_url: string; model: string; api_key: string; max_output_tokens: string; temperature: string; json_mode: boolean; call_tokens: string; input_price: string; output_price: string; timeout_seconds: string; concurrency: string };

export const modelsEntitlement = "evolution_space.models";
const empty: Draft = { id: "", name: "", enabled: true, base_url: "https://", model: "", api_key: "", max_output_tokens: "16000", temperature: "", json_mode: true, call_tokens: "100000", input_price: "0", output_price: "0", timeout_seconds: "600", concurrency: "4" };
const fromConnection = (c: Connection): Draft => ({ id: c.id, name: c.name, enabled: c.enabled, base_url: c.settings.base_url, model: c.settings.model, api_key: "", max_output_tokens: String(c.settings.max_output_tokens), temperature: c.settings.temperature === undefined ? "" : String(c.settings.temperature), json_mode: c.settings.json_mode, call_tokens: String(c.policy.call_tokens), input_price: String(c.policy.input_micros_per_million), output_price: String(c.policy.output_micros_per_million), timeout_seconds: String(c.policy.timeout_seconds), concurrency: String(c.policy.concurrency) });

async function send<T>(method: string, path: string, body: unknown): Promise<{ status: number; value?: T; error?: string; message?: string }> {
  const session = await (await fetch(`${base}/api/v1/session`, { credentials: "same-origin", cache: "no-store" })).json() as Session;
  const response = await fetch(`${base}/api/v1/${path}`, { method, credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(body) });
  const value = await response.json().catch(() => undefined);
  return response.ok ? { status: response.status, value: value as T } : { status: response.status, error: value?.error, message: value?.message };
}

// Installation-wide model connections. Only platform administrators can edit
// them; the API key is write-only and never returned to the browser.
export function ModelConnectionsPage() {
  const tr = useT(); const session = useSession();
  const admin = session.entitlements.includes(modelsEntitlement);
  return <><PageHeading title={tr("Model connections")} description="Connect Evolution Space to an OpenAI-compatible chat service. Agent nodes then choose a connection as their model. Connections are shared by every group." />
    {admin ? <Editor /> : <p role="status">{tr("Only platform administrators can manage model connections. Ask an administrator to add or change one.")}</p>}</>;
}

function Editor() {
  const tr = useT();
  const page = useRead<Page>("/model-connections");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [expected, setExpected] = useState(0);
  const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const [status, setStatus] = useState("");
  const [test, setTest] = useState<TestResult>();
  if (page.loading) return <p role="status">{tr("Loading model connections…")}</p>;
  if (page.status === 409) return <p role="alert">{tr("Model connections are not enabled on this installation. An operator must set model_connections in the native registry and restart Evolution Space.")}</p>;
  if (page.status || !page.data) return <div role="alert"><p>{tr("Model connections are unavailable. Retry when the service is restored.")}</p><button onClick={page.refresh}>{tr("Retry")}</button></div>;
  const set = (change: Partial<Draft>) => { setDraft(value => value && { ...value, ...change }); setTest(undefined); };
  const number = (text: string) => Number.isSafeInteger(Number(text)) ? Number(text) : NaN;
  function body(d: Draft) {
    const temperature = d.temperature.trim() === "" ? undefined : Number(d.temperature);
    return { settings: { base_url: d.base_url.trim(), model: d.model.trim(), max_output_tokens: number(d.max_output_tokens), ...(temperature === undefined ? {} : { temperature }), json_mode: d.json_mode },
      policy: { call_tokens: number(d.call_tokens), input_micros_per_million: number(d.input_price), output_micros_per_million: number(d.output_price), timeout_seconds: number(d.timeout_seconds), concurrency: number(d.concurrency) } };
  }
  async function runTest() {
    if (!draft) return;
    setBusy(true); setError(""); setTest(undefined);
    const result = await send<TestResult>("POST", "model-connections/test", { ...(expected ? { id: draft.id } : {}), settings: body(draft).settings, ...(draft.api_key ? { api_key: draft.api_key } : {}) }).catch(() => ({ status: 0 } as { status: number; value?: TestResult }));
    setBusy(false);
    if (result.value) setTest(result.value); else setError("The connection test failed to run. Retry.");
  }
  async function save() {
    if (!draft) return;
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(draft.id)) { setError("Use letters, digits, hyphens or underscores for the connection ID."); return; }
    setBusy(true); setError(""); setStatus("");
    const result = await send<Connection>("PUT", `model-connections/${encodeURIComponent(draft.id)}`, { expected_revision: expected, name: draft.name.trim(), enabled: draft.enabled, ...body(draft), ...(draft.api_key ? { api_key: draft.api_key } : {}) }).catch(() => ({ status: 0 } as { status: number; value?: Connection; error?: string; message?: string }));
    setBusy(false);
    if (result.value) {
      setStatus(tr("Saved {id}. Agent nodes pin revision {revision}.", { id: result.value.id, revision: String(result.value.current.revision) }));
      setDraft(null); page.refresh(); return;
    }
    setError(result.error === "model_connection_changed" ? "This connection changed elsewhere, or the ID is already taken. Reload and edit again." : result.error === "model_key_encryption_not_configured" ? "Key encryption is not configured on this host. An operator must set EVOLUTION_SPACE_MODEL_KEY_SECRET." : result.message ? result.message : result.status === 0 ? "The result is unknown. Reload to check whether it was saved." : "Saving failed. Check the fields and retry.");
  }
  return <>
    {!page.data.key_encryption && <p role="alert">{tr("Key encryption is not configured on this host, so API keys cannot be stored. An operator must set EVOLUTION_SPACE_MODEL_KEY_SECRET.")}</p>}
    {status && <p role="status">{status}</p>}
    <section className="card" aria-labelledby="connections-heading">
      <div className="section-heading"><h2 id="connections-heading">{tr("Connections")}</h2>{!draft && <button className="primary" onClick={() => { setDraft({ ...empty }); setExpected(0); setTest(undefined); setStatus(""); }}>{tr("Add connection")}</button>}</div>
      {page.data.items.length === 0 ? <p>{tr("No model connections yet. Add one to let agent nodes call a model.")}</p> :
        <table aria-label={tr("Model connections")}><thead><tr><th scope="col">{tr("Name")}</th><th scope="col">{tr("Model")}</th><th scope="col">{tr("Revision")}</th><th scope="col">{tr("API key")}</th><th scope="col">{tr("Status")}</th><th /></tr></thead>
          <tbody>{page.data.items.map(c => <tr key={c.id}><th scope="row">{c.name}<br /><small><code>{c.id}</code></small></th><td><code>{c.settings.model}</code><br /><small>{c.settings.base_url}</small></td><td>{c.current.revision}</td><td>{c.key_set ? <>{tr("Set")} <code>{c.key_hint}</code></> : tr("Not set")}</td><td>{tr(c.enabled ? "Enabled" : "Disabled")}</td>
            <td><button onClick={() => { setDraft(fromConnection(c)); setExpected(c.current.revision); setTest(undefined); setStatus(""); }}>{tr("Edit")}</button></td></tr>)}</tbody></table>}
    </section>
    {draft && <section className="card profile-wizard" aria-labelledby="connection-form">
      <h2 id="connection-form">{tr(expected ? "Edit connection" : "Add connection")}</h2>
      <p className="hint">{tr("Changing the base URL, model, output limit, temperature or JSON mode publishes a new revision; workflows keep the revision they pinned. Name, key, limits and prices apply to every revision.")}</p>
      <div className="pin-row"><label>{tr("Connection ID")}<input aria-label={tr("Connection ID")} value={draft.id} disabled={!!expected} onChange={event => set({ id: event.target.value.trim() })} /></label><label className="wide">{tr("Display name")}<input aria-label={tr("Display name")} value={draft.name} onChange={event => set({ name: event.target.value })} /></label></div>
      <label>{tr("Base URL")}<input aria-label={tr("Base URL")} placeholder="https://api.example.com/v1" value={draft.base_url} onChange={event => set({ base_url: event.target.value })} /></label>
      <p className="hint">{tr("The OpenAI-compatible root; Evolution Space calls {base}/chat/completions. HTTPS is required.")}</p>
      <label>{tr("API key")}<input aria-label={tr("API key")} type="password" autoComplete="off" placeholder={expected ? tr("Leave empty to keep the stored key") : ""} value={draft.api_key} onChange={event => set({ api_key: event.target.value })} /></label>
      <label>{tr("Model")}{test?.models?.length ? <select aria-label={tr("Model")} value={draft.model} onChange={event => set({ model: event.target.value })}><option value="">{tr("Choose a model")}</option>{test.models.map(m => <option key={m} value={m}>{m}</option>)}</select> : <input aria-label={tr("Model")} placeholder="glm-5.2" value={draft.model} onChange={event => set({ model: event.target.value })} />}</label>
      <p className="hint">{tr("Test the connection to load the service's model list.")}</p>
      <label className="check-label"><input type="checkbox" checked={draft.json_mode} onChange={event => set({ json_mode: event.target.checked })} />{tr("Request JSON output (response_format json_object)")}</label>
      <label className="check-label"><input type="checkbox" checked={draft.enabled} onChange={event => set({ enabled: event.target.checked })} />{tr("Enabled: offer to agent nodes and admit new calls")}</label>
      <details className="advanced"><summary>{tr("Limits and prices")}</summary>
        <div className="pin-row"><label>{tr("Maximum output tokens")}<input aria-label={tr("Maximum output tokens")} inputMode="numeric" value={draft.max_output_tokens} onChange={event => set({ max_output_tokens: event.target.value })} /></label><label>{tr("Temperature")}<input aria-label={tr("Temperature")} placeholder={tr("Service default")} value={draft.temperature} onChange={event => set({ temperature: event.target.value })} /></label><label>{tr("Tokens reserved per call")}<input aria-label={tr("Tokens reserved per call")} inputMode="numeric" value={draft.call_tokens} onChange={event => set({ call_tokens: event.target.value })} /></label></div>
        <p className="hint">{tr("Reasoning models count reasoning in output tokens. Each call reserves its token ceiling from the run budget, so a binding's maximum tokens must be at least this value.")}</p>
        <div className="pin-row"><label>{tr("Input price (micros per million tokens)")}<input aria-label={tr("Input price")} inputMode="numeric" value={draft.input_price} onChange={event => set({ input_price: event.target.value })} /></label><label>{tr("Output price (micros per million tokens)")}<input aria-label={tr("Output price")} inputMode="numeric" value={draft.output_price} onChange={event => set({ output_price: event.target.value })} /></label></div>
        <div className="pin-row"><label>{tr("Call timeout (seconds)")}<input aria-label={tr("Call timeout")} inputMode="numeric" value={draft.timeout_seconds} onChange={event => set({ timeout_seconds: event.target.value })} /></label><label>{tr("Concurrent calls")}<input aria-label={tr("Concurrent calls")} inputMode="numeric" value={draft.concurrency} onChange={event => set({ concurrency: event.target.value })} /></label></div>
      </details>
      {test && <div className={test.ok ? "test-result ok" : "test-result"} role="status">{test.ok ? tr("Connection works: {model} replied in {ms} ms.", { model: test.response_model ?? draft.model, ms: String(test.latency_ms) }) : <>{tr("Connection test failed")}: {test.error}</>}{test.models && <small> · {tr("{count} models available", { count: String(test.models.length) })}</small>}</div>}
      {error && <p role="alert">{tr(error)}</p>}
      <div className="editor-actions"><button disabled={busy} onClick={() => void runTest()}>{tr("Test connection")}</button><button className="primary" disabled={busy || !draft.name.trim() || !draft.model.trim()} onClick={() => void save()}>{tr("Save connection")}</button><button disabled={busy} onClick={() => setDraft(null)}>{tr("Cancel")}</button></div>
    </section>}
  </>;
}
