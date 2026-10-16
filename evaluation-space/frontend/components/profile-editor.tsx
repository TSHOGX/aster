/* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps -- group changes reset remote immutable definitions. */
"use client";

import { useEffect, useRef, useState } from "react";
import { api, APIError, base, type Session } from "../lib/api";
import { useT } from "../lib/i18n";

type Ref = { id: string; revision: number; digest: string };
type ProfilePage = { revisions: Ref[]; next_id: string; next_revision: number; has_more: boolean };
const listError = "Evaluator profiles are unavailable. Retry when the service is restored.";
const detailError = "Profile detail is unavailable. Retry or check group access.";
const publishError = "Profile validation or publication failed. Check dataset, scorer, runtime, model, tools and metric thresholds.";
const key = (ref: Ref) => `${ref.id}:${ref.revision}`;

export function ProfileEditor({ group }: { group: string }) {
  const tr = useT();
  const [refs, setRefs] = useState<Ref[]>([]);
  const [cursor, setCursor] = useState<ProfilePage>();
  const [selected, setSelected] = useState("");
  const [text, setText] = useState("");
  const [saved, setSaved] = useState("");
  const [published, setPublished] = useState<Ref>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef<AbortController | null>(null);
  const root = `/groups/${encodeURIComponent(group)}/profiles`;
  function begin() {
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    setBusy(true); setError(""); setPublished(undefined);
    return controller;
  }
  function clear() { setRefs([]); setCursor(undefined); setSelected(""); setText(""); setSaved(""); }
  function finish(controller: AbortController) { if (!controller.signal.aborted) setBusy(false); }
  function discard() { return text === saved || window.confirm(tr("Discard unsaved profile changes?")); }
  async function detail(ref: Ref, signal: AbortSignal) {
    const value = await api<unknown>(`${root}/${encodeURIComponent(ref.id)}/${ref.revision}?digest=${encodeURIComponent(ref.digest)}`, signal);
    if (signal.aborted) return;
    const content = JSON.stringify(value, null, 2);
    setSelected(key(ref)); setText(content); setSaved(content);
  }
  async function load(more = false) {
    const controller = begin();
    if (!more) clear();
    try {
      const query = more && cursor ? `?${new URLSearchParams({ after_id: cursor.next_id, after_revision: String(cursor.next_revision) })}` : "";
      const page = await api<ProfilePage>(root + query, controller.signal);
      if (controller.signal.aborted) return;
      setRefs(prior => more ? [...prior, ...page.revisions.filter(ref => !prior.some(existing => key(existing) === key(ref)))] : page.revisions);
      setCursor(page);
      if (!more && page.revisions[0]) {
        try { await detail(page.revisions[0], controller.signal); }
        catch { if (!controller.signal.aborted) { clear(); setError(detailError); } }
      }
    } catch { if (!controller.signal.aborted) { clear(); setError(listError); } }
    finally { finish(controller); }
  }
  async function read(ref?: Ref) {
    const controller = begin(); setSelected(""); setText(""); setSaved("");
    try { if (ref) await detail(ref, controller.signal); }
    catch { if (!controller.signal.aborted) { clear(); setError(detailError); } }
    finally { finish(controller); }
  }
  useEffect(() => { void load(); return () => request.current?.abort(); }, [group]);
  async function publish() {
    const controller = begin();
    try {
      const profile = JSON.parse(text);
      const session = await api<Session>("/session", controller.signal);
      const response = await fetch(`${base}/api/v1${root}`, { method: "POST", credentials: "same-origin", signal: controller.signal, headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(profile) });
      if (!response.ok) {
        if (response.status === 401 || response.status === 403) clear();
        throw new Error();
      }
      const ref = await response.json() as Ref;
      if (controller.signal.aborted) return;
      setPublished(ref);
      // Retain the exact submitted fields for idempotent recovery after response loss.
      const canonical = JSON.stringify({ ...profile, ref }, null, 2);
      setText(canonical); setSaved(canonical); setSelected(key(ref));
      setRefs(prior => prior.some(existing => key(existing) === key(ref)) ? prior : [...prior, ref]);
    } catch (failure) {
      if (!controller.signal.aborted) {
        if (failure instanceof APIError && (failure.status === 401 || failure.status === 403)) clear();
        setError(publishError);
      }
    }
    finally { finish(controller); }
  }
  return <section className="card">
    <div className="section-heading"><div><h2>{tr("Evaluator profile editor")}</h2><p>{tr("Profiles pin dataset, scorer, runtime, model, tools, thresholds and coverage. Every save creates an immutable revision.")}</p><p>{tr("Choose a new ID with revision 1, or increment the revision to publish edits. The server computes the profile digest; referenced digests remain required.")}</p></div><button onClick={() => { if (discard()) void load(); }}>{tr("Refresh")}</button></div>
    {error && <p role="alert">{tr(error)}</p>}
    {busy && <p role="status">{tr("Loading profiles…")}</p>}
    {published && <p role="status">{tr("Published immutable evaluator profile")}: {published.id} @{published.revision}</p>}
    <label>{tr("Existing profile")}<select aria-label={tr("Existing profile")} disabled={busy} value={selected} onChange={event => { if (discard()) void read(refs.find(ref => key(ref) === event.target.value)); }}><option value="">{tr("Create or choose a profile")}</option>{refs.map(ref => <option key={key(ref)} value={key(ref)}>{ref.id} @{ref.revision}</option>)}</select></label>
    {cursor?.has_more && <button disabled={busy} onClick={() => void load(true)}>{tr("Load more profiles")}</button>}
    <label>{tr("Profile JSON")}<textarea aria-label={tr("Profile JSON")} disabled={busy} rows={14} value={text} onChange={event => { setText(event.target.value); setPublished(undefined); }} placeholder={JSON.stringify({ ref: { id: "profile", revision: 1 }, dataset: { group_id: group }, scorer: {}, runtime: {}, model: {}, tools: [], thresholds: [{ name: "accuracy", unit: "ratio", operator: "gte", value: 0.9, minimum_samples: 10, minimum_coverage: 0.8 }], fixed_model_required: true }, null, 2)} /></label>
    <button className="primary" disabled={busy || !text.trim()} onClick={() => void publish()}>{tr("Publish profile revision")}</button>
  </section>;
}
