'use client';
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";
import { useConfirm } from "@web/confirm";

/* eslint-disable @next/next/no-html-link-for-pages -- Studio is an independently deployed application outside the admin Next router. */
import Link from 'next/link';
import { useEffect, useRef, useState } from 'react';
import { api, APIError } from '@/lib/api';
import { importEndpoint, readImportCommand, type ImportAction, type ImportCommand } from '@/lib/import-recovery';
import { Select } from '@web/select';

type Source = { id: string; publisher_id: string };
type Library = { id: string; name: string; description: string };
type Release = { id: string; library_id: string; label: string; note: string; descriptor_digest: string; profile: 'authored-v1' | 'shared-assets-v1'; publisher: { issuer: string; subject: string } };
type Imported = { id: string; source_id: string; publisher_id: string; library_id: string; version_id: string; manifest_digest: string; state: string; error?: string; capacity_bytes: string; transfer_reserved_bytes: string; actor_id: string; publisher_actor: string; artifact: { id: string; version: string; digest: string } };
// Memory takes precedence after failed persistence; the parent guards leaving
// an unpersisted operation, while this map also survives local view remounts.
const memory = new Map<string, ImportCommand | null>();
const unpersisted = new Set<string>();
export function VolumeImports({ accountID, onDirty }: { accountID: string; onDirty: (value: boolean) => void }) {
  const tr = useT(); const confirm = useConfirm();
  const [sources, setSources] = useState<Source[]>([]), [source, setSource] = useState('');
  const [libraries, setLibraries] = useState<Library[]>([]), [library, setLibrary] = useState('');
  const [releases, setReleases] = useState<Release[]>([]), [releaseNext, setReleaseNext] = useState(''), [libraryNext, setLibraryNext] = useState('');
  const [imports, setImports] = useState<Imported[]>([]), [cursor, setCursor] = useState('');
  const [libraryError, setLibraryError] = useState('');
  const [catalogError, setCatalogError] = useState(''), [error, setError] = useState(''), [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0), [localRevision,setLocalRevision] = useState(0), [pending, setPending] = useState<ImportCommand | null>(null);
  const [ready,setReady] = useState(false), [invalid,setInvalid] = useState(false), [storageError,setStorageError] = useState(false);
  const [catalogBusy,setCatalogBusy] = useState(false), [loadingImports,setLoadingImports] = useState(true);
  const inFlight = useRef(false), retained = useRef<ImportCommand | null>(null), hydrated = useRef(false), pages = useRef(1);
  const mounted=useRef(true), localRead=useRef<AbortController>(undefined);
  useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;};},[]);
  const sourceRead = useRef<AbortController>(undefined), releaseRead = useRef<AbortController>(undefined);
  const storageKey = `admin-volume-import:${JSON.stringify(accountID)}`;
  const blocked = busy || !ready || !!pending || invalid;
  const remember = (command: ImportCommand | null) => {
    retained.current=command;memory.set(storageKey,command);setPending(command);
    try {if(command)sessionStorage.setItem(storageKey,JSON.stringify(command));else sessionStorage.removeItem(storageKey);setStorageError(false);unpersisted.delete(storageKey);}catch{setStorageError(true);if(command)unpersisted.add(storageKey);else unpersisted.delete(storageKey);}
  };
  useEffect(() => { onDirty(!!pending && storageError); return () => onDirty(false); }, [pending, storageError, onDirty]);
  useEffect(() => {
    const abort = new AbortController();
    api<{ sources: Source[] }>('/api/admin/volume-sources', { signal: abort.signal }).then(result => {
      if(abort.signal.aborted)return;
      setSources(result.sources);setCatalogError('');
      const wanted = new URLSearchParams(window.location.search).get('volume_publisher');
      setSource(old => result.sources.some(item=>item.id===old)?old:result.sources.find(s => s.publisher_id === wanted)?.id || result.sources[0]?.id || '');
    }).catch(e => { if (!abort.signal.aborted) {setSources([]);setSource('');setLibraries([]);setLibrary('');setLibraryNext('');setReleases([]);setReleaseNext('');setCatalogError(String(e));} });
    return () => abort.abort();
  }, [refresh]);
  useEffect(() => {
    const abort = new AbortController();localRead.current=abort;let timer:ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const loaded:Imported[]=[];let after='';
        for(let index=0;index<pages.current;index++){
          const result=await api<{imports:Imported[]}>(`/api/admin/volume-imports${after?`?after=${encodeURIComponent(after)}`:''}`,{signal:abort.signal});
          if(abort.signal.aborted)return;
          loaded.push(...result.imports);after=result.imports.length===200?result.imports[199].id:'';
          if(!after)break;
        }
        const pinned=new URLSearchParams(location.search).get('volume_import');
        if(pinned&&!loaded.some(item=>item.id===pinned)){
          const selected=await api<Imported>(`/api/admin/volume-imports/${encodeURIComponent(pinned)}`,{signal:abort.signal});
          loaded.unshift(selected);
        }
        if(abort.signal.aborted)return;
        setImports([...new Map(loaded.map(item=>[item.id,item])).values()]);setCursor(after);setError('');setReady(true);
        if(!hydrated.current){
          if(memory.has(storageKey)){retained.current=memory.get(storageKey)??null;setStorageError(unpersisted.has(storageKey));}
          else try{const raw=sessionStorage.getItem(storageKey);if(raw){try{retained.current=readImportCommand(raw,accountID);}catch{setInvalid(true);}}}catch{setStorageError(true);}
          setPending(retained.current);hydrated.current=true;
        }
      }catch(e){if(!abort.signal.aborted){setImports([]);setCursor('');setReady(false);setError(String(e));}}
      finally{if(!abort.signal.aborted){setLoadingImports(false);timer=setTimeout(()=>void load(),3000);}}
    };
    void load();return()=>{abort.abort();clearTimeout(timer);};
  },[refresh,localRevision,storageKey,accountID]);
  useEffect(() => {
    const abort = new AbortController();sourceRead.current?.abort();sourceRead.current=abort;
    setLibraries([]);setLibraryNext('');setLibrary('');setReleases([]);setReleaseNext('');
    if (!source) return ()=>abort.abort();
    api<{ libraries: Library[]; next: string }>(`/api/admin/volume-sources/${encodeURIComponent(source)}/libraries`, { signal: abort.signal }).then(result => {
      if(abort.signal.aborted)return;
      setLibraries(result.libraries); setLibraryNext(result.next); setLibraryError('');
      const wanted = new URLSearchParams(window.location.search).get('volume_library');
      setLibrary(result.libraries.find(l => l.id === wanted)?.id || result.libraries[0]?.id || '');
    }).catch(e => { if (!abort.signal.aborted) { setLibraries([]); setLibrary('');setLibraryNext(''); setReleases([]);setReleaseNext(''); setLibraryError(`Source unavailable. Locally ready volumes remain usable. ${String(e)}`); } });
    return () => {abort.abort();sourceRead.current?.abort();};
  }, [source, refresh]);
  useEffect(() => {
    const abort = new AbortController();releaseRead.current?.abort();releaseRead.current=abort;
    setReleases([]);setReleaseNext('');
    if (!source || !library) return ()=>abort.abort();
    api<{ releases: Release[]; next: string }>(`/api/admin/volume-sources/${encodeURIComponent(source)}/releases?library_id=${encodeURIComponent(library)}`, { signal: abort.signal }).then(result => { if(!abort.signal.aborted){setReleases(result.releases); setReleaseNext(result.next); setCatalogError('');} }).catch(e => { if (!abort.signal.aborted) { setReleases([]);setReleaseNext(''); setCatalogError(`Release catalog unavailable. ${String(e)}`); } });
    return () => {abort.abort();releaseRead.current?.abort();};
  }, [source, library, refresh]);
  async function moreCatalog(kind:'libraries'|'releases'){
    if(catalogBusy)return;setCatalogBusy(true);
    const ref=kind==='libraries'?sourceRead:releaseRead;ref.current?.abort();const abort=new AbortController();ref.current=abort;
    try{
      if(kind==='libraries'){
        const page=await api<{libraries:Library[];next:string}>(`/api/admin/volume-sources/${encodeURIComponent(source)}/libraries?after=${encodeURIComponent(libraryNext)}`,{signal:abort.signal});
        if(!abort.signal.aborted){setLibraries(old=>[...new Map([...old,...page.libraries].map(item=>[item.id,item])).values()]);setLibraryNext(page.next);}
      }else{
        const page=await api<{releases:Release[];next:string}>(`/api/admin/volume-sources/${encodeURIComponent(source)}/releases?${new URLSearchParams({library_id:library,after:releaseNext})}`,{signal:abort.signal});
        if(!abort.signal.aborted){setReleases(old=>[...new Map([...old,...page.releases].map(item=>[item.id,item])).values()]);setReleaseNext(page.next);}
      }
    }catch(e){if(!abort.signal.aborted){if(kind==='libraries'){setLibraries([]);setLibrary('');setLibraryNext('');}setReleases([]);setReleaseNext('');setCatalogError(String(e));}}
    finally{setCatalogBusy(false);}
  }
  async function submit(request: ImportCommand) {
    if (inFlight.current || !ready) return;
    inFlight.current = true; setBusy(true); setError(''); remember(request);
    try {
      const confirmed=await api<Imported>(importEndpoint(request),{method:'POST',body:request.body});
      if(!confirmed || typeof confirmed.id!=='string' || !/^[A-Za-z0-9_-]{1,128}$/.test(confirmed.id))throw new APIError(502,'outcome_unknown','The server returned an incomplete response.');
      remember(null);if(!mounted.current)return;
      const url=new URL(location.href);url.searchParams.set('volume_import',confirmed.id);history.replaceState(null,'',url);
      setRefresh(x => x + 1);
    }catch (e) {
      setError(String(e));
      if(e instanceof APIError&&[401,403].includes(e.status)){localRead.current?.abort();setReady(false);setImports([]);setCursor('');}
      if (e instanceof APIError && e.code !== 'outcome_unknown' && e.status >= 400 && e.status < 500 && ![401,403].includes(e.status)) remember(null);
    }finally { inFlight.current = false; setBusy(false); }
  }
  function selectRelease(release: Release) {
    void submit({account:accountID,action:'admit',imported:'',body:JSON.stringify({request_id:crypto.randomUUID(),source_id:source,version_id:release.id,expected_manifest_digest:release.descriptor_digest,consumer_profile:release.profile})});
  }
  async function action(imported: Imported, action: Exclude<ImportAction,'admit'>) {
    if ((action === 'cancel' || action === 'retire') && !(await confirm({ title: tr(action === 'retire' ? 'Retire this local import?' : 'Cancel this local import?'), body: tr('Retirement checks saved Environment and Project references before cleaning up engine volumes.'), confirm: tr(action === 'retire' ? 'Retire locally' : 'Cancel import'), cancel: tr('Keep import'), danger: true })).confirmed) return;
    void submit({ account:accountID, action, imported:imported.id, body:JSON.stringify({request_id:crypto.randomUUID()}) });
  }
  const matching = (release: Release) => imports.find(i => i.source_id === source && i.version_id === release.id && i.manifest_digest === release.descriptor_digest);
  return <section className="panel" aria-label={tr("Published library volumes")}>
    <header className="page-heading"><div><h2>{tr("Published library volumes")}</h2><p>{tr("Import a fixed Studio release into this deployment, then select it in an Environment template.")}</p></div><a className="primary inline-link" href="/volumes-studio/">{tr("Open Volume Studio ↗")}</a></header>
    {error && <p className="error" role="alert">{tr(error)}</p>}
    {ready && pending && <div className="panel" role="status"><p>{tr("The request is retained until its result is known.")}</p><button disabled={busy} onClick={() => void submit(pending)}>{tr("Check original request")}</button></div>}
    {storageError&&<p role="alert" className="error">{tr("Local import recovery is unavailable. Keep this tab open until the request is confirmed.")}</p>}
    {ready&&invalid&&<p role="alert" className="error">{tr("Local import recovery is invalid. Discard it before starting another operation.")}</p>}
    {ready&&(pending||invalid)&&<button className="danger" disabled={busy} onClick={async ()=>{if((await confirm({title:tr("Discard import recovery?"),body:tr("The original import request may already have completed. Check local imports before discarding recovery."),confirm:tr("Discard import recovery"),cancel:tr("Cancel"),danger:true})).confirmed){remember(null);setInvalid(false);}}}>{tr("Discard import recovery")}</button>}
    <div className="form-actions"><label>{tr("Source")}<Select aria-label={tr("Volume source")} value={source} disabled={blocked || catalogBusy} onChange={e => { setSource(e.target.value); setLibraries([]); setLibrary(''); setReleases([]); }}>{sources.map(s => <option key={s.id} value={s.id}>{s.id} · {s.publisher_id}</option>)}</Select></label><label>{tr("Library")}<Select aria-label={tr("Volume library")} value={library} disabled={!libraries.length || blocked || catalogBusy} onChange={e => { setLibrary(e.target.value); setReleases([]); }}>{libraries.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}</Select></label><button disabled={busy || catalogBusy} onClick={() => {pages.current=1;setLoadingImports(true);setRefresh(x => x + 1);}}>{tr("Refresh sources and imports")}</button></div>
    {sources.length === 0 && <p>{tr("No trusted Studio sources are configured. An operator must register the publisher identity and service credential.")}</p>}
    {libraryError && <p role="status" className="error">{tr(libraryError)}</p>}
    {catalogError && <p role="status" className="error">{tr(catalogError)}</p>}
    {libraryNext && <button disabled={catalogBusy||busy} onClick={()=>void moreCatalog('libraries')}>{tr("More libraries")}</button>}
    {releases.length > 0 && <div className="table-scroll"><table><thead><tr><th>{tr("Release")}</th><th>{tr("Publisher")}</th><th>{tr("Availability")}</th><th>{tr("Action")}</th></tr></thead><tbody>{releases.map(release => {
      const imported = matching(release);
      return <tr key={release.id}><td>{release.label}<small>{release.note}</small><small>{release.profile === 'shared-assets-v1' ? tr("Shared assets") : tr("Ordinary data")}</small><details><summary>{tr("Exact descriptor")}</summary><code>{release.descriptor_digest}</code></details></td><td>{release.publisher.subject}</td><td>{imported?.state === 'ready' ? tr("Ready in this deployment") : tr(imported?.state) || tr("Available to import")}</td><td>{imported?.state === 'ready' ? <Link className="button" href="/resources/environment">{tr("Select in Environment")}</Link> : <button className="primary" disabled={blocked || !!imported} onClick={() => selectRelease(release)}>{tr("Import release")}</button>}</td></tr>;
    })}</tbody></table></div>}
    {releaseNext && <button disabled={catalogBusy||busy} onClick={()=>void moreCatalog('releases')}>{tr("More releases")}</button>}
    <h3>{tr("Local imports")}</h3><p className="hint">{tr("Ready content is retained locally. Changes or outages in Studio do not update existing projects.")}</p>
    <div className="table-scroll"><table><thead><tr><th>{tr("Source / version")}</th><th>{tr("State")}</th><th>{tr("Capacity and provenance")}</th><th>{tr("Actions")}</th></tr></thead><tbody>{imports.map(i => <tr key={i.id} id={`import-${i.id}`}><td>{i.source_id}<small>{i.version_id}</small></td><td>{tr(i.state)}{i.error && <small role="status">{tr(i.error.replaceAll('_', ' '))}</small>}</td><td>{formatQuantity(i.capacity_bytes)}  {tr("bytes per engine object")}<small>{tr("Transfer reservation:")} {formatQuantity(i.transfer_reserved_bytes)}  {tr("bytes")}</small><details><summary>{tr("Provenance")}</summary><p>{tr("Publisher:")} {i.publisher_actor || i.publisher_id}</p><p>{tr("Imported by:")} {i.actor_id}</p><p>{tr("Local artifact:")} {i.artifact.id} / {i.artifact.version}</p><code>{i.manifest_digest}</code></details></td><td><div className="form-actions">{i.state === 'ready' && <><Link className="button" href="/resources/environment">{tr("Use in Environment")}</Link><button className="danger" disabled={blocked || catalogBusy} onClick={() => void action(i, 'retire')}>{tr("Retire locally")}</button></>}{['paused', 'failed'].includes(i.state) && <button disabled={blocked || catalogBusy} onClick={() => action(i, 'retry')}>{tr("Retry as current admin")}</button>}{['acquiring', 'source_acquired', 'materializing', 'paused', 'failed'].includes(i.state) && <button disabled={blocked || catalogBusy} onClick={() => action(i, 'cancel')}>{tr("Cancel")}</button>}{['canceled', 'failed'].includes(i.state) && <button disabled={blocked || catalogBusy} onClick={() => action(i, 'retire')}>{tr("Clean up local artifacts")}</button>}</div></td></tr>)}</tbody></table>{imports.length === 0 && <p>{tr("No local imports yet.")}</p>}</div>
    {cursor && <button disabled={loadingImports||busy} onClick={()=>{pages.current++;setLoadingImports(true);setLocalRevision(value=>value+1);}}>{tr("More imports")}</button>}

  </section>;
}
