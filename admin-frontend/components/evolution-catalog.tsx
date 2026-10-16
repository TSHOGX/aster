"use client";
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { Select } from "@web/select";
import type { Revision } from "@/lib/evolution";
import { refKey, type CatalogEntry, type CatalogPage, type ServiceDefinition } from "@/lib/evolution-config";
const enc = encodeURIComponent;
type ProfileDefinition = { dataset?: { id?: string; digest?: string; group_id?: string }; scorer?: Revision; runtime?: Revision; model?: Revision; fixed_model_required?: boolean; thresholds?: { name: string; unit: string; operator: string; value: number; minimum_samples: number; minimum_coverage: number }[] };
export function DefinitionPicker({ group, kind, label, value, onChange, disabled, serviceKind, allowed }: { group: string; kind: string; label: string; value?: Revision; onChange: (entry: CatalogEntry) => void; disabled?: boolean; serviceKind?: string; allowed?: Revision[] }) {
  const tr = useT(), [refs, setRefs] = useState<Revision[]>([]), [next, setNext] = useState<{id: string; revision: number} | null>(null);
  const [listing, setListing] = useState(false), [selecting, setSelecting] = useState(false);
  const [listError, setListError] = useState(""), [detailError, setDetailError] = useState(""), [selectionError, setSelectionError] = useState("");
  const [retained, setDetail] = useState<{ base: string; entry: CatalogEntry } | null>(null);
  const listRequest = useRef<AbortController | null>(null), selectionRequest = useRef<AbortController | null>(null);
  const [refresh, setRefresh] = useState(0);
  const base = `/api/admin/groups/${enc(group)}/optimization/catalog/${kind}`;
  const key = value?.id ? refKey(value) : "";
  const detail = !listError && !detailError && !selectionError && retained?.base === base && refKey(retained.entry.ref) === key ? retained.entry : null;
  const loading = listing || selecting;
  const load = async (cursor?: {id: string; revision: number}) => {
    listRequest.current?.abort(); selectionRequest.current?.abort(); setSelecting(false);
    const request = new AbortController(); listRequest.current = request;
    setListing(true); setListError("");
    if (!cursor) { setRefs([]); setNext(null); }
    try {
      const page = await api<CatalogPage>(`${base}?limit=25${cursor ? `&after_id=${enc(cursor.id)}&after_revision=${cursor.revision}` : ""}`, { signal: request.signal });
      if (!request.signal.aborted) {
        setRefs(old => cursor ? [...old, ...page.revisions.filter(ref => !old.some(prior => refKey(prior) === refKey(ref)))] : page.revisions);
        setNext(page.has_more ? { id: page.next_id, revision: page.next_revision } : null);
      }
    } catch (error) {
      if (!request.signal.aborted) { setRefs([]); setNext(null); setListError(error instanceof Error ? error.message : String(error)); }
    } finally { if (!request.signal.aborted) setListing(false); }
  };
  useEffect(() => {
    const timer = setTimeout(() => void load(), 0);
    return () => { listRequest.current?.abort(); selectionRequest.current?.abort(); clearTimeout(timer); };
    // load depends only on the catalog path.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base]);
  useEffect(() => {
    const abort = new AbortController();
    if (value?.id) void api<CatalogEntry>(`${base}/${enc(value.id)}/${value.revision}?digest=${enc(value.digest)}`, { signal: abort.signal }).then(entry => {
      if (!abort.signal.aborted) { setDetail({ base, entry }); setDetailError(""); }
    }).catch(error => { if (!abort.signal.aborted) { setDetail(null); setDetailError(String(error)); } });
    return () => abort.abort();
  }, [base, key, value?.id, value?.revision, value?.digest, refresh]);
  const options = listError ? [] : allowed || refs;
  const select = async (key: string) => {
    const selected = options.find(ref => refKey(ref) === key); if (!selected) return;
    selectionRequest.current?.abort();
    const request = new AbortController(); selectionRequest.current = request;
    setSelecting(true); setSelectionError("");
    try {
      const entry = await api<CatalogEntry>(`${base}/${enc(selected.id)}/${selected.revision}?digest=${enc(selected.digest)}`, { signal: request.signal });
      if (request.signal.aborted) return;
      if (kind === "service") { const service = entry.definition as ServiceDefinition; if (!entry.status?.enabled || !service.enabled || serviceKind && service.kind !== serviceKind) throw new Error("This service is disabled or has the wrong service kind."); }
      setDetail({ base, entry }); setDetailError(""); onChange(entry);
    } catch (error) { if (!request.signal.aborted) { setDetail(null); setSelectionError(error instanceof Error ? error.message : String(error)); } }
    finally { if (!request.signal.aborted) setSelecting(false); }
  };
  const error = [...new Set([listError, detailError, selectionError].filter(Boolean))].join(" ");
  const unavailable = kind === "service" && key && detail && (!(detail.definition as ServiceDefinition).enabled || !detail.status?.enabled || serviceKind && (detail.definition as ServiceDefinition).kind !== serviceKind);
  const profile = kind === "profile" && detail ? detail.definition as ProfileDefinition : undefined;
  return <div className="definition-picker"><label>{tr(label)}<Select aria-label={tr(label)} value={key} disabled={disabled || loading} onChange={event => void select(event.target.value)}><option value="">{tr("Choose a published revision")}</option>{key && !options.some(ref => refKey(ref) === key) && <option value={key}>{value!.id} · {value!.revision}</option>}{options.map(ref => <option key={refKey(ref)} value={refKey(ref)}>{ref.id} · {ref.revision}</option>)}</Select></label>{loading && <small role="status">{tr("Loading catalog…")}</small>}{error && <p role="alert">{tr(error)}</p>}{unavailable && <p role="alert">{tr("This service is disabled or has the wrong service kind.")}</p>}{profile && <div className="catalog-detail"><small>{tr("Dataset")}: {profile.dataset?.id || "—"} · {profile.dataset?.digest || tr("Redacted or unavailable")}</small><small>{tr("Scorer")}: {profile.scorer?.id || "—"} @{profile.scorer?.revision || "—"} · {tr("Runtime")}: {profile.runtime?.id || "—"} @{profile.runtime?.revision || "—"}</small><small>{tr("Model")}: {profile.model?.id || "—"} @{profile.model?.revision || "—"}{profile.fixed_model_required ? ` · ${tr("Fixed model")}` : ""}</small>{profile.thresholds?.length ? <ul>{profile.thresholds.map(threshold => <li key={`${threshold.name}:${threshold.unit}`}>{threshold.name} {threshold.operator} {threshold.value} {threshold.unit} · {threshold.minimum_samples} {tr("samples minimum")} · {Math.round(threshold.minimum_coverage * 100)}% {tr("coverage minimum")}</li>)}</ul> : <small>{tr("No metric thresholds")}</small>}</div>}{!loading && !refs.length && !allowed && !error && <small>{tr("No published entries in this catalog.")}</small>}<div className="form-actions"><button type="button" disabled={disabled || loading} onClick={() => { setSelectionError(""); setRefresh(value => value + 1); void load(); }}>{tr("Refresh catalog")}</button>{next && !allowed && <button type="button" disabled={disabled || loading} onClick={() => void load(next)}>{tr("More revisions")}</button>}</div></div>;
}
