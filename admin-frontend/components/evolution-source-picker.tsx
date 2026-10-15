"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { Select } from "@web/select";
import type { Source } from "@/lib/evolution";
import type { Group } from "@/lib/models";
type Resource = { id: string; name: string; project_id?: string; can_configure?: boolean };
type Page = { items: Resource[]; next_after?: string; has_more?: boolean; next?: string };
export function SourceResourcePicker({ group, kind, value, update }: { group: string; kind: Source["kind"]; value: string; update: (id: string) => void }) {
 const tr = useT(), [search, setSearch] = useState(""), [query, setQuery] = useState("");
 const [items, setItems] = useState<Resource[]>([]), [next, setNext] = useState(""), [busy, setBusy] = useState(false), [error, setError] = useState("");
 const request = useRef<AbortController | null>(null);
 const load = useCallback(async (cursor = "") => {
  request.current?.abort(); const abort = new AbortController(); request.current = abort;
  setBusy(true); setError(""); if (!cursor) { setItems([]); setNext(""); }
  const base = `/api/admin/groups/${encodeURIComponent(group)}/optimization`;
  const path = kind === "volume" ? `${base}/outputs?limit=25&search=${encodeURIComponent(query)}&cursor=${encodeURIComponent(cursor)}` : `${base}/source-resources?kind=${kind}&limit=25&search=${encodeURIComponent(query)}&after=${encodeURIComponent(cursor)}`;
  try {
   const result = await api<Page>(path, { signal: abort.signal });
   if (!abort.signal.aborted) { setItems(old => cursor ? [...old, ...result.items] : result.items); setNext(kind === "volume" ? result.next || "" : result.has_more ? result.next_after || "" : ""); }
  } catch (error) { if (!abort.signal.aborted) setError(error instanceof Error ? error.message : String(error)); }
  finally { if (!abort.signal.aborted) setBusy(false); }
 }, [group, kind, query]);
 useEffect(() => { const timer = setTimeout(() => void load(), 0); return () => { clearTimeout(timer); request.current?.abort(); }; }, [load]);
 return <fieldset><legend>{tr("Source resource")}</legend><p>{tr("Projects and sessions list only your own resources. Studio libraries require read and configuration permission.")}</p><label>{tr("Search source resources")}<input value={search} maxLength={256} onChange={event => setSearch(event.target.value)} /></label><button type="button" disabled={busy} onClick={() => { if (search === query) void load(); else setQuery(search); }}>{tr("Search resources")}</button><label>{tr("Resource")}<Select aria-label={tr("Source resource")} required value={value} disabled={busy} onChange={event => update(event.target.value)}><option value="">{tr("Choose a source resource")}</option>{value && !items.some(item => item.id === value) && <option value={value}>{value} · {tr("Current selection")}</option>}{items.map(item => <option key={item.id} value={item.id} disabled={kind === "volume" && !item.can_configure}>{item.name || item.id} · {item.id}{kind === "volume" && !item.can_configure ? ` · ${tr("Output configuration permission required")}` : ""}</option>)}</Select></label>{busy && <p role="status">{tr("Loading source resources…")}</p>}{error && <p role="alert">{tr(error)}</p>}{!busy && !error && !items.length && <p>{tr("No eligible source resources match. Ownership is required even for administrators.")}</p>}{next && <button type="button" disabled={busy} onClick={() => void load(next)}>{tr("More source resources")}</button>}<small>{tr("Saving rechecks current source authority. Selection does not grant evidence access.")}</small></fieldset>;
}
export function SourceAudiencePicker({ value, update }: { value: string[]; update: (groups: string[]) => void }) {
 const tr = useT(), [groups, setGroups] = useState<Group[]>([]), [next, setNext] = useState(""), [error, setError] = useState(""), [busy, setBusy] = useState(false);
 const request = useRef<AbortController | null>(null);
 const load = useCallback(async (cursor = "") => {
  request.current?.abort(); const abort = new AbortController(); request.current = abort; setBusy(true);
  try { const result = await api<{ groups: Group[]; next_cursor?: string }>(`/api/admin/groups?limit=50&cursor=${encodeURIComponent(cursor)}`, { signal: abort.signal }); if (!abort.signal.aborted) { setGroups(old => cursor ? [...old, ...result.groups] : result.groups); setNext(result.next_cursor || ""); setError(""); } }
  catch (error) { if (!abort.signal.aborted) setError(error instanceof Error ? error.message : String(error)); }
  finally { if (!abort.signal.aborted) setBusy(false); }
 }, []);
 useEffect(() => { const timer = setTimeout(() => void load(), 0); return () => { clearTimeout(timer); request.current?.abort(); }; }, [load]);
 const choices = [...new Map([...value.map(id => ({ id, name: id })), ...groups].map(group => [group.id, group])).values()];
 return <fieldset><legend>{tr("Source audience groups")}</legend><p>{tr("Select each group allowed to use this source. Membership never selects an audience automatically.")}</p>{choices.map(group => <label className="check-label" key={group.id}><input type="checkbox" checked={value.includes(group.id)} onChange={event => update(event.target.checked ? [...value, group.id] : value.filter(id => id !== group.id))} />{group.name} · {group.id}</label>)}{error && <p role="alert">{tr(error)} <button type="button" disabled={busy} onClick={() => void load()}>{tr("Retry loading")}</button></p>}{next && <button type="button" disabled={busy} onClick={() => void load(next)}>{tr("More groups")}</button>}</fieldset>;
}
