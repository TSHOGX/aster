"use client";
import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { api, APIError, mutate } from "@/lib/api";

type Rule = { cidr: string; port: number; protocol: string };
type Policy = { revision: string; direct: Rule[]; resolvers: string[] };
type Settings = Partial<Policy> & { configuration_revision: number; mode?: string; desired?: Policy; default?: Policy; default_configuration_revision?: number; replayed?: boolean };
type Draft = { mode: string; direct: Rule[]; resolvers: string[] };
const draftOf = (value: Settings): Draft => { const policy = value.desired || value as Policy; return { mode: value.mode || "override", direct: policy.direct, resolvers: policy.resolvers }; };
const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
export function NetworkEditor({ id, onDirty }: { id?: string; onDirty: (dirty: boolean) => void }) {
  const path = id ? `/api/admin/users/${encodeURIComponent(id)}/network` : "/api/admin/network/default";
  const [settings, setSettings] = useState<Settings | null>(null), [draft, setDraft] = useState<Draft>({ mode: "inherit", direct: [], resolvers: [] });
  const [error, setError] = useState(""), [notice, setNotice] = useState(""), [busy, setBusy] = useState(false), [pending, setPending] = useState<Record<string, unknown> | null>(null), [attempt, setAttempt] = useState(0);
  const alive = useRef(true);
  const edited = !!settings && (draft.mode !== (settings.mode || "override") || (draft.mode === "override" && !equal(draft, draftOf(settings))));
  useEffect(() => { onDirty(edited || !!pending); return () => onDirty(false); }, [edited, pending, onDirty]);
  const accept = (value: Settings) => { setSettings(value); setDraft(draftOf(value)); };
  useEffect(() => {
    alive.current = true;
    const abort = new AbortController();
    void api<Settings>(path, { signal: abort.signal }).then(value => { if (!abort.signal.aborted) accept(value); }).catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    return () => { alive.current = false; abort.abort(); };
  }, [path, attempt]);
  const reload = async () => {
    setBusy(true);
    try { const latest = await api<Settings>(path); if (!alive.current) return; setSettings(latest); if (!edited) setDraft(draftOf(latest)); setError(""); setNotice("Current settings loaded. Review your retained edits before saving."); }
    catch (error) { if (alive.current) setError(String(error)); }
    finally { if (alive.current) setBusy(false); }
  };
  const submit = async (body: Record<string, unknown>) => {
    setBusy(true); setPending(body); setError(""); setNotice("");
    try {
      const result = await mutate<Settings>(path, body, "PATCH");
      if (!alive.current) return;
      // Receipt replay returns the historical result. Read current settings
      // before enabling editing so intervening changes are not displayed as current.
      const current = result.replayed ? await api<Settings>(path) : result;
      if (!alive.current) return;
      accept(current); setPending(null); setNotice(result.replayed ? "Original change confirmed. Current settings loaded." : "Saved. Applies on next creation or resume; running placements are unchanged.");
    } catch (error) {
      if (!alive.current) return;
      if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.code !== "outcome_unknown") setPending(null);
      setError(error instanceof APIError && error.status === 409 ? "Settings changed. Reload current settings, review your edits and save again." : String(error));
    } finally { if (alive.current) setBusy(false); }
  };
  const locked = busy || !!pending;
  const rulesEditable = !id || draft.mode === "override";
  const shown = rulesEditable ? draft : settings?.default || draft;
  return <section>
    <header className="page-heading"><div><h1>{id ? "User network settings" : "Default network settings"}</h1><p>Direct connections and DNS selected for the next Project creation or resume.</p></div>{id && <Link href={`/users/${encodeURIComponent(id)}`}>Back to account</Link>}</header>
    <p>Running placements retain their observed policy until resumed. Saving does not interrupt work.</p>
    {error && <p role="alert" className="error">{error}</p>}{notice && <p role="status">{notice}</p>}
    {!settings && (!error ? <p role="status">Loading network settings…</p> : <button onClick={() => { setError(""); setAttempt(value => value + 1); }}>Retry loading</button>)}
    {pending && !busy && <div className="notice"><p>The outcome is unconfirmed. Check the original request before making another change.</p><button onClick={() => void submit(pending)}>Check original request</button></div>}
    {settings && <>
      <form className="account-form network-form" onSubmit={event => {
        event.preventDefault();
        const body: Record<string, unknown> = { request_id: crypto.randomUUID(), expected_revision: settings.configuration_revision };
        if (id) { body.mode = draft.mode; body.expected_default_revision = settings.default_configuration_revision; }
        if (rulesEditable) { body.direct = draft.direct; body.resolvers = draft.resolvers; }
        void submit(body);
      }}>
        <fieldset disabled={locked}>
          {id && <label>Network policy<select value={draft.mode} onChange={event => setDraft(value => ({ ...value, mode: event.target.value }))}><option value="inherit">Inherit platform default</option><option value="override">Complete user override</option></select></label>}
          {id && <p className="hint">Overrides replace all default rules and resolvers. Empty lists allow no direct connections or DNS.</p>}
          <fieldset disabled={!rulesEditable}><legend>Direct connections ({shown.direct.length}/256)</legend>
            {shown.direct.map((rule, index) => <div className="network-rule" key={index}>
              <label>IPv4 CIDR {index + 1}<input required aria-label={`IPv4 CIDR ${index + 1}`} maxLength={18} placeholder="192.0.2.0/24" value={rule.cidr} onChange={event => setDraft(value => ({ ...value, direct: value.direct.map((item, i) => i === index ? { ...item, cidr: event.target.value } : item) }))} /></label>
              <label>Port {index + 1}<input required type="number" min={1} max={65535} step={1} value={rule.port || ""} onChange={event => setDraft(value => ({ ...value, direct: value.direct.map((item, i) => i === index ? { ...item, port: Number(event.target.value) } : item) }))} /></label>
              <label>Protocol {index + 1}<select value={rule.protocol} onChange={event => setDraft(value => ({ ...value, direct: value.direct.map((item, i) => i === index ? { ...item, protocol: event.target.value } : item) }))}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
              <button type="button" aria-label={`Remove direct rule ${index + 1}`} onClick={() => setDraft(value => ({ ...value, direct: value.direct.filter((_, i) => i !== index) }))}>Remove</button>
            </div>)}
            {!shown.direct.length && <p>No direct rules.</p>}
            <button type="button" disabled={shown.direct.length >= 256} onClick={() => setDraft(value => ({ ...value, direct: [...value.direct, { cidr: "", port: 443, protocol: "tcp" }] }))}>Add direct rule</button>
          </fieldset>
          <fieldset disabled={!rulesEditable}><legend>IPv4 resolvers ({shown.resolvers.length}/16)</legend>
            {shown.resolvers.map((resolver, index) => <div className="network-resolver" key={index}><label>Resolver {index + 1}<input required maxLength={15} placeholder="192.0.2.53" value={resolver} onChange={event => setDraft(value => ({ ...value, resolvers: value.resolvers.map((item, i) => i === index ? event.target.value : item) }))} /></label><button type="button" aria-label={`Remove resolver ${index + 1}`} onClick={() => setDraft(value => ({ ...value, resolvers: value.resolvers.filter((_, i) => i !== index) }))}>Remove</button></div>)}
            {!shown.resolvers.length && <p>No DNS resolvers.</p>}
            <button type="button" disabled={shown.resolvers.length >= 16} onClick={() => setDraft(value => ({ ...value, resolvers: [...value.resolvers, ""] }))}>Add resolver</button>
          </fieldset>
          <p className="hint">Use canonical IPv4 CIDRs, ports 1–65535 and TCP or UDP. Use 0.0.0.0/0 for public IPv4 access on the selected port; private, loopback, link-local and shared address ranges require separate rules where supported. For web access, add TCP rules for ports 80 and 443 and configure DNS resolvers. Resolver order is preserved. Resolvers allow UDP/53; TCP DNS requires a direct rule.</p>
          <button className="primary" disabled={!edited}>Save network settings</button>
        </fieldset>
      </form>
      <section className="panel"><p>Configuration revision: {settings.configuration_revision}{id && ` · Default revision: ${settings.default_configuration_revision}`}</p><p className="identifier">Saved desired policy: {settings.desired?.revision || settings.revision}</p><div className="form-actions"><button disabled={locked} onClick={() => void reload()}>Reload current settings</button><button disabled={locked || !edited} onClick={() => { accept(settings); setError(""); }}>Discard edits</button></div></section>
    </>}
  </section>;
}
