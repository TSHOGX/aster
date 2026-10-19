"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/central";
import { useT } from "@/lib/i18n";
import { Select } from "@web/select";
import type { ConfigBinding } from "@/lib/evolution-config";
type Library = { id: string; name: string; profile: string; archived: boolean; owner_group: string; can_configure: boolean };
type Detail = { library: Library; main: { commit_id: string; tree_digest: string }; published?: { version_id: string }; reservations: { prefix: string; group_id: string; binding_id: string; generation: number; state: string }[] };
type Page = { items: Library[]; next: string; total: number };
export function EvolutionOutput({ group, binding, update }: { group: string; binding: ConfigBinding; update: (value: ConfigBinding) => void }) {
  const tr = useT(), base = `/api/admin/groups/${encodeURIComponent(group)}/optimization/outputs`;
  const [search, setSearch] = useState(""), [query, setQuery] = useState("");
  const [page, setPage] = useState<Page>({ items: [], next: "", total: 0 });
  const [detail, setDetail] = useState<Detail | null>(null);
  const [listError, setListError] = useState(""), [detailError, setDetailError] = useState("");
  const [loading, setLoading] = useState(false);
  const listRequest = useRef<AbortController | null>(null), detailRequest = useRef<AbortController | null>(null);
  const load = useCallback(async (cursor = "") => {
    listRequest.current?.abort();
    const request = new AbortController(); listRequest.current = request;
    setLoading(true); setListError("");
    if (!cursor) setPage({ items: [], next: "", total: 0 });
    try {
      const result = await api<Page>(`${base}?limit=25&search=${encodeURIComponent(query)}&cursor=${encodeURIComponent(cursor)}`, { signal: request.signal });
      if (!request.signal.aborted) setPage(old => ({ ...result, items: cursor ? [...old.items, ...result.items] : result.items }));
    } catch (error) {
      if (!request.signal.aborted) setListError(error instanceof Error ? error.message : String(error));
    } finally { if (!request.signal.aborted) setLoading(false); }
  }, [base, query]);
  const read = useCallback(async () => {
    detailRequest.current?.abort();
    const request = new AbortController(); detailRequest.current = request;
    setDetail(null); setDetailError("");
    if (!binding.output.library_id) return;
    try {
      const value = await api<Detail>(`${base}/${encodeURIComponent(binding.output.library_id)}`, { signal: request.signal });
      if (!request.signal.aborted) setDetail(value);
    } catch (error) {
      if (!request.signal.aborted) setDetailError(error instanceof Error ? error.message : String(error));
    }
  }, [base, binding.output.library_id]);
  useEffect(() => { const timer = setTimeout(() => void load(), 0); return () => { listRequest.current?.abort(); clearTimeout(timer); }; }, [load]);
  useEffect(() => { const timer = setTimeout(() => void read(), 0); return () => { detailRequest.current?.abort(); clearTimeout(timer); }; }, [read]);
  const selected = detail?.library.id === binding.output.library_id ? detail : null;
  const reason = (library: Library) => library.archived ? "Archived library" : library.profile !== binding.output.profile ? "Incompatible library profile" : library.owner_group && library.owner_group !== group ? "Owned by another group" : !library.can_configure ? "Output configuration permission required" : "";
  const overlap = (prefix: string) => prefix === binding.output.prefix || prefix.startsWith(binding.output.prefix + "/") || binding.output.prefix.startsWith(prefix + "/");
  const conflict = selected?.reservations.find(item => item.state !== "released" && overlap(item.prefix) && (item.group_id !== group || item.binding_id !== binding.id));
  const own = selected?.reservations.find(item => item.prefix === binding.output.prefix && item.group_id === group && item.binding_id === binding.id);
  return <fieldset><legend>{tr("Output ownership")}</legend><p>{tr("Select an existing Studio library. Its profile and ownership are checked again during activation.")}</p><p><a href="/volumes-studio/" target="_blank" rel="noopener noreferrer">{tr("Open Volume Studio ↗")}</a></p><label>{tr("Search output libraries")}<input value={search} onChange={event => setSearch(event.target.value)} /></label><div className="form-actions"><button type="button" onClick={() => { if (search === query) void load(); else setQuery(search); }}>{tr("Search")}</button><button type="button" disabled={loading} onClick={() => { void load(); void read(); }}>{tr("Refresh output metadata")}</button></div>
    <label>{tr("Output library")}<Select aria-label={`${binding.id}: ${tr("Output library")}`} value={binding.output.library_id} disabled={loading} onChange={event => { setDetail(null); update({ ...binding, output: { ...binding.output, library_id: event.target.value, reservation_generation: 1 } }); }}><option value="">{tr("Choose an output library")}</option>{binding.output.library_id && !page.items.some(item => item.id === binding.output.library_id) && <option value={binding.output.library_id}>{selected?.library.name || binding.output.library_id}</option>}{page.items.map(item => <option key={item.id} value={item.id} disabled={!!reason(item)}>{item.name} · {item.profile}{reason(item) ? ` · ${tr(reason(item))}` : ""}</option>)}</Select></label>
    {loading && <p role="status">{tr("Loading output libraries…")}</p>}{listError && <p role="alert">{tr(listError)}</p>}{detailError && <p role="alert">{tr(detailError)}</p>}{!loading && !listError && !page.items.length && <p>{tr("No readable libraries match this search.")}</p>}{page.next && <button type="button" disabled={loading} onClick={() => void load(page.next)}>{tr("More libraries")}</button>}
    {detail && detail.library.id === binding.output.library_id && <div className="volume-card"><h4>{detail.library.name}</h4><p>{tr("Profile")}: {detail.library.profile}</p><p>{tr("Owning group")}: {detail.library.owner_group || tr("Unassigned")}</p>{reason(detail.library) && <p role="alert">{tr(reason(detail.library))}</p>}<p>{tr("Accepted main")}: {detail.main.commit_id}</p><p>{tr("Published version")}: {detail.published?.version_id || tr("Not published")}</p><details><summary>{tr("Directory reservations")}</summary>{detail.reservations.length ? <ul>{detail.reservations.map(item => <li key={item.prefix}>{item.prefix} · {item.binding_id || tr("Another group’s binding")} · {tr(item.state)} · {item.generation}</li>)}</ul> : <p>{tr("No reserved directories.")}</p>}</details></div>}
    <div className="runtime-fields"><label>{tr("Owned directory")}<input value={binding.output.prefix} required onChange={event => update({ ...binding, output: { ...binding.output, prefix: event.target.value } })} /></label><label>{tr("Reservation generation")}<input type="number" min={1} max={Number.MAX_SAFE_INTEGER} value={binding.output.reservation_generation} required onChange={event => { const n = Number(event.target.value); if (Number.isSafeInteger(n)) update({ ...binding, output: { ...binding.output, reservation_generation: n } }); }} /></label></div>
    {conflict && <p role="alert">{tr("This directory overlaps an existing reservation.")} {conflict.binding_id || tr("Another group’s binding")}: {conflict.prefix}</p>}{own && <p>{tr("Existing reservation generation")}: {own.generation} · {tr(own.state)} <button type="button" disabled={(own.state !== "active" && own.state !== "released") || !Number.isSafeInteger(own.generation + 1)} onClick={() => update({ ...binding, output: { ...binding.output, reservation_generation: own.generation + 1 } })}>{tr("Use next reservation generation")}</button></p>}
  </fieldset>;
}
