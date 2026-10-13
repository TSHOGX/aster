'use client';
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

/* eslint-disable @next/next/no-html-link-for-pages -- Studio is an independently deployed application outside the admin Next router. */
import Link from 'next/link';
import { useEffect, useRef, useState } from 'react';
import { api, APIError, mutate } from '@/lib/api';
import { Select } from '@web/select';

type Source = { id: string; publisher_id: string };
type Library = { id: string; name: string; description: string };
type Release = { id: string; library_id: string; label: string; note: string; descriptor_digest: string; publisher: { issuer: string; subject: string } };
type Imported = { id: string; source_id: string; publisher_id: string; library_id: string; version_id: string; manifest_digest: string; state: string; error?: string; capacity_bytes: string; transfer_reserved_bytes: string; actor_id: string; publisher_actor: string; artifact: { id: string; version: string; digest: string } };
type Pending = { path: string; body: Record<string, string> };
export function VolumeImports({ onDirty }: { onDirty: (value: boolean) => void }) {
  const tr = useT();
  const [sources, setSources] = useState<Source[]>([]), [source, setSource] = useState('');
  const [libraries, setLibraries] = useState<Library[]>([]), [library, setLibrary] = useState('');
  const [releases, setReleases] = useState<Release[]>([]), [releaseNext, setReleaseNext] = useState(''), [libraryNext, setLibraryNext] = useState('');
  const [imports, setImports] = useState<Imported[]>([]), [cursor, setCursor] = useState('');
  const [libraryError, setLibraryError] = useState('');
  const [catalogError, setCatalogError] = useState(''), [error, setError] = useState(''), [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0), [pending, setPending] = useState<Pending | null>(null);
  const inFlight = useRef(false);
  useEffect(() => { onDirty(!!pending); return () => onDirty(false); }, [pending, onDirty]);
  useEffect(() => {
    const abort = new AbortController();
    api<{ sources: Source[] }>('/api/admin/volume-sources', { signal: abort.signal }).then(result => {
      setSources(result.sources);
      const wanted = new URLSearchParams(window.location.search).get('volume_publisher');
      setSource(old => old || result.sources.find(s => s.publisher_id === wanted)?.id || result.sources[0]?.id || '');
    }).catch(e => { if (!abort.signal.aborted) setCatalogError(String(e)); });
    const load = () => api<{ imports: Imported[] }>('/api/admin/volume-imports', { signal: abort.signal }).then(result => {
      setImports(result.imports); setCursor(result.imports.length === 200 ? result.imports[199].id : '');
    }).catch(e => { if (!abort.signal.aborted) setError(String(e)); });
    void load(); const timer = setInterval(() => void load(), 3000);
    return () => { abort.abort(); clearInterval(timer); };
  }, [refresh]);
  useEffect(() => {
    if (!source) return;
    const abort = new AbortController();
    api<{ libraries: Library[]; next: string }>(`/api/admin/volume-sources/${encodeURIComponent(source)}/libraries`, { signal: abort.signal }).then(result => {
      setLibraries(result.libraries); setLibraryNext(result.next); setLibraryError('');
      const wanted = new URLSearchParams(window.location.search).get('volume_library');
      setLibrary(result.libraries.find(l => l.id === wanted)?.id || result.libraries[0]?.id || '');
    }).catch(e => { if (!abort.signal.aborted) { setLibraries([]); setLibrary(''); setReleases([]); setLibraryError(`Source unavailable. Locally ready volumes remain usable. ${String(e)}`); } });
    return () => abort.abort();
  }, [source, refresh]);
  useEffect(() => {
    if (!source || !library) return;
    const abort = new AbortController();
    api<{ releases: Release[]; next: string }>(`/api/admin/volume-sources/${encodeURIComponent(source)}/releases?library_id=${encodeURIComponent(library)}`, { signal: abort.signal }).then(result => { setReleases(result.releases); setReleaseNext(result.next); setCatalogError(''); }).catch(e => { if (!abort.signal.aborted) { setReleases([]); setCatalogError(`Release catalog unavailable. ${String(e)}`); } });
    return () => abort.abort();
  }, [source, library, refresh]);
  async function submit(request: Pending) {
    if (inFlight.current) return;
    inFlight.current = true; setBusy(true); setError(''); setPending(request);
    try { await mutate<Imported>(request.path, request.body); setPending(null); setRefresh(x => x + 1); }
    catch (e) { setError(String(e)); if (e instanceof APIError && e.code !== 'outcome_unknown' && e.status >= 400 && e.status < 500) setPending(null); }
    finally { inFlight.current = false; setBusy(false); }
  }
  function selectRelease(release: Release) {
    const request = { path: '/api/admin/volume-imports', body: { request_id: crypto.randomUUID(), source_id: source, version_id: release.id, expected_manifest_digest: release.descriptor_digest, consumer_profile: 'authored-v1' } };
    void submit(request);
  }
  function action(imported: Imported, action: string) {
    if ((action === 'cancel' || action === 'retire') && !window.confirm(tr(action === 'retire' ? 'Retire this local import? Retirement checks saved Environment and Project references before cleaning up engine volumes.' : 'Cancel this local import? Retirement checks saved Environment and Project references before cleaning up engine volumes.'))) return;
    void submit({ path: `/api/admin/volume-imports/${imported.id}/${action}`, body: { request_id: crypto.randomUUID() } });
  }
  const matching = (release: Release) => imports.find(i => i.source_id === source && i.version_id === release.id && i.manifest_digest === release.descriptor_digest);
  return <section className="panel" aria-label={tr("Published library volumes")}>
    <header className="page-heading"><div><h2>{tr("Published library volumes")}</h2><p>{tr("Import a fixed Studio release into this deployment, then select it in an Environment template.")}</p></div><a className="primary inline-link" href="/volumes-studio/">{tr("Open Volume Studio ↗")}</a></header>
    {error && <p className="error" role="alert">{tr(error)}</p>}
    {pending && <div className="panel" role="status"><p>{tr("The request is retained until its result is known.")}</p><button disabled={busy} onClick={() => void submit(pending)}>{tr("Check original request")}</button></div>}
    <div className="form-actions"><label>{tr("Source")}<Select aria-label={tr("Volume source")} value={source} disabled={busy || !!pending} onChange={e => { setSource(e.target.value); setLibraries([]); setLibrary(''); setReleases([]); }}>{sources.map(s => <option key={s.id} value={s.id}>{s.id} · {s.publisher_id}</option>)}</Select></label><label>{tr("Library")}<Select aria-label={tr("Volume library")} value={library} disabled={!libraries.length || busy || !!pending} onChange={e => { setLibrary(e.target.value); setReleases([]); }}>{libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}</Select></label><button disabled={busy || !!pending} onClick={() => setRefresh(x => x + 1)}>{tr("Refresh sources and imports")}</button></div>
    {sources.length === 0 && <p>{tr("No trusted Studio sources are configured. An operator must register the publisher identity and service credential.")}</p>}
    {libraryError && <p role="status" className="error">{tr(libraryError)}</p>}
    {catalogError && <p role="status" className="error">{tr(catalogError)}</p>}
    {libraryNext && <button onClick={() => void api<{ libraries: Library[]; next: string }>(`/api/admin/volume-sources/${source}/libraries?after=${libraryNext}`).then(p => { setLibraries(old => [...old, ...p.libraries]); setLibraryNext(p.next); }).catch(e => setCatalogError(String(e)))}>{tr("More libraries")}</button>}
    {releases.length > 0 && <div className="table-scroll"><table><thead><tr><th>{tr("Release")}</th><th>{tr("Publisher")}</th><th>{tr("Availability")}</th><th>{tr("Action")}</th></tr></thead><tbody>{releases.map(release => {
      const imported = matching(release);
      return <tr key={release.id}><td>{release.label}<small>{release.note}</small><details><summary>{tr("Exact descriptor")}</summary><code>{release.descriptor_digest}</code></details></td><td>{release.publisher.subject}</td><td>{imported?.state === 'ready' ? tr("Ready in this deployment") : tr(imported?.state) || tr("Available to import")}</td><td>{imported?.state === 'ready' ? <Link className="button" href="/resources/environment">{tr("Select in Environment")}</Link> : <button className="primary" disabled={busy || !!pending || !!imported} onClick={() => selectRelease(release)}>{tr("Import release")}</button>}</td></tr>;
    })}</tbody></table></div>}
    {releaseNext && <button onClick={() => void api<{ releases: Release[]; next: string }>(`/api/admin/volume-sources/${source}/releases?library_id=${library}&after=${releaseNext}`).then(p => { setReleases(old => [...old, ...p.releases]); setReleaseNext(p.next); }).catch(e => setCatalogError(String(e)))}>{tr("More releases")}</button>}
    <h3>{tr("Local imports")}</h3><p className="hint">{tr("Ready content is retained locally. Changes or outages in Studio do not update existing projects.")}</p>
    <div className="table-scroll"><table><thead><tr><th>{tr("Source / version")}</th><th>{tr("State")}</th><th>{tr("Capacity and provenance")}</th><th>{tr("Actions")}</th></tr></thead><tbody>{imports.map(i => <tr key={i.id}><td>{i.source_id}<small>{i.version_id}</small></td><td>{tr(i.state)}{i.error && <small role="status">{tr(i.error.replaceAll('_', ' '))}</small>}</td><td>{formatQuantity(i.capacity_bytes)}  {tr("bytes per engine object")}<small>{tr("Transfer reservation:")} {formatQuantity(i.transfer_reserved_bytes)}  {tr("bytes")}</small><details><summary>{tr("Provenance")}</summary><p>{tr("Publisher:")} {i.publisher_actor || i.publisher_id}</p><p>{tr("Imported by:")} {i.actor_id}</p><p>{tr("Local artifact:")} {i.artifact.id} / {i.artifact.version}</p><code>{i.manifest_digest}</code></details></td><td><div className="form-actions">{i.state === 'ready' && <><Link className="button" href="/resources/environment">{tr("Use in Environment")}</Link><button disabled={busy || !!pending} onClick={() => action(i, 'retire')}>{tr("Retire locally")}</button></>}{['paused', 'failed'].includes(i.state) && <button disabled={busy || !!pending} onClick={() => action(i, 'retry')}>{tr("Retry as current admin")}</button>}{['acquiring', 'source_acquired', 'materializing', 'paused', 'failed'].includes(i.state) && <button disabled={busy || !!pending} onClick={() => action(i, 'cancel')}>{tr("Cancel")}</button>}{['canceled', 'failed'].includes(i.state) && <button disabled={busy || !!pending} onClick={() => action(i, 'retire')}>{tr("Clean up local artifacts")}</button>}</div></td></tr>)}</tbody></table>{imports.length === 0 && <p>{tr("No local imports yet.")}</p>}</div>
    {cursor && <button onClick={() => void api<{ imports: Imported[] }>(`/api/admin/volume-imports?after=${cursor}`).then(p => { setImports(old => [...old, ...p.imports]); setCursor(p.imports.length === 200 ? p.imports[199].id : ''); }).catch(e => setError(String(e)))}>{tr("More imports")}</button>}
  </section>;
}
