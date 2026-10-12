'use client';
import Link from 'next/link';
import { useCallback, useEffect, useRef, useState } from 'react';
import { api, APIError, base, bytes, Entry, Library, requestID, Upload, Version } from '../lib/api';
import { hashChunk, hashFile } from '../lib/hash-file';
import { SessionGate, SignOut, useSession } from './session';

type Tree = { revision: number; entries: Entry[]; next: string };
type Change = { path: string; change: string };
type UploadProgress = { path: string; message: string; progress: number };
export function LibraryEditor({ id }: { id: string }) {
  const root = `/libraries/${encodeURIComponent(id)}`;
  const { session, error: sessionError, loading } = useSession();
  const [library, setLibrary] = useState<Library | null>(null);
  const [tree, setTree] = useState<Tree>({ revision: 0, entries: [], next: '' });
  const [parent, setParent] = useState('');
  const [versions, setVersions] = useState<Version[]>([]);
  const [versionNext, setVersionNext] = useState('');
  const [versionID, setVersionID] = useState('');
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [selected, setSelected] = useState<Entry | null>(null);
  const [text, setText] = useState('');
  const [saved, setSaved] = useState('');
  const [editRevision, setEditRevision] = useState(0);
  const [editable, setEditable] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<'files' | 'history' | 'changes'>('files');
  const [changes, setChanges] = useState<Change[]>([]);
  const [changeNext, setChangeNext] = useState('');
  const [publishing, setPublishing] = useState(false);
  const [label, setLabel] = useState('');
  const [note, setNote] = useState('');
  const [preview, setPreview] = useState<{ entry_count: number; total_bytes: string; revision: number } | null>(null);
  const [progress, setProgress] = useState<UploadProgress | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const controller = useRef<AbortController | null>(null);
  const selectionGeneration = useRef(0);
  const dirty = text !== saved;
  const readState = useCallback(async (signal?: AbortSignal) => {
    const [l, v, u] = await Promise.all([
      api<Library>(root, { signal }), api<{ versions: Version[]; next: string }>(`${root}/versions`, { signal }), api<{ uploads: Upload[] }>(`${root}/uploads`, { signal }),
    ]);
    const treePath = versionID ? `${root}/versions/${versionID}/tree` : `${root}/draft/tree?parent=${encodeURIComponent(parent)}`;
    const t = await api<Tree>(treePath, { signal });
    return { l, v, u, t: { ...t, revision: versionID ? 0 : t.revision } };
  }, [root, parent, versionID]);
  const applyState = useCallback((data: Awaited<ReturnType<typeof readState>>) => {
    setLibrary(data.l); setVersions(data.v.versions); setVersionNext(data.v.next); setUploads(data.u.uploads); setTree(data.t);
    return data.l;
  }, []);
  const load = useCallback(async () => applyState(await readState()), [applyState, readState]);
  useEffect(() => {
    if (!session) return;
    const abort = new AbortController();
    const refresh = () => readState(abort.signal).then(applyState).catch(e => { if (!abort.signal.aborted) setError(String(e)); });
    void refresh();
    const timer = setInterval(() => { void refresh(); }, 5000);
    return () => { abort.abort(); clearInterval(timer); };
  }, [session, readState, applyState]);
  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => { if (dirty || progress) event.preventDefault(); };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirty, progress]);
  useEffect(() => () => { controller.current?.abort(); }, []);
  const report = (e: unknown) => setError(e instanceof APIError && e.status === 409 ? 'The draft or file changed. Your text is still here. Reload and review the latest saved version before retrying.' : String(e));
  const confirmDiscard = () => !dirty || window.confirm('Discard your unsaved text and open another file?');
  async function choose(entry: Entry) {
    if (!confirmDiscard()) return;
    const generation = ++selectionGeneration.current;
    setError(''); setText(''); setSaved(''); setEditable(false); setSelected(null);
    if (entry.kind === 'directory' && !versionID) { setParent(entry.path); return; }
    setSelected(entry); setEditRevision(tree.revision);
    if (entry.kind === 'directory' || Number(entry.size) > 1 << 20) return;
    try {
      const response = await fetch(`${base}/api/v1${root}/${versionID ? `versions/${versionID}` : 'draft'}/content?path=${encodeURIComponent(entry.path)}`, { credentials: 'same-origin', cache: 'no-store' });
      if (!response.ok) throw new Error('Cannot read this file. Refresh to check access.');
      if (Number(response.headers.get('Content-Length')) > 1 << 20) { await response.body?.cancel(); return; }
      const data = await response.arrayBuffer();
      if (data.byteLength > 1 << 20) return;
      if (generation !== selectionGeneration.current) return;
      const content = new TextDecoder('utf-8', { fatal: true }).decode(data);
      if (content.includes('\0')) return;
      setText(content); setSaved(content); setEditable(true);
    } catch (e) { if (!(e instanceof TypeError)) report(e); }
  }
  async function mutate(body: object) {
    if (!session) return;
    return api<{ revision: number }>(`${root}/draft/mutations`, { method: 'POST', body: JSON.stringify({ request_id: requestID(), expected_revision: library?.draft_revision, ...body }) }, session.csrf);
  }
  async function save() {
    if (!selected || !session) return;
    setBusy(true); setError('');
    try {
      const result = await mutate({ action: 'text', path: selected.path, text, expected_revision: editRevision });
      setSaved(text); setEditRevision(result?.revision ?? editRevision); setNotice('Saved to draft.'); await load();
    } catch (e) { report(e); } finally { setBusy(false); }
  }
  async function create(kind: 'text' | 'mkdir') {
    const name = window.prompt(kind === 'mkdir' ? 'New directory name' : 'New text file name'); if (!name) return;
    setBusy(true); setError('');
    try { await mutate({ action: kind, path: parent ? `${parent}/${name}` : name }); await load(); setNotice('Created in the draft.'); }
    catch (e) { report(e); } finally { setBusy(false); }
  }
  async function removeOrMove(entry: Entry, action: 'remove' | 'move') {
    if (!confirmDiscard()) return;
    const to = action === 'move' ? window.prompt('New path, relative to the library root', entry.path) : null;
    if (action === 'move' && !to) return;
    if (action === 'remove') {
      let descendants = 0;
      if (entry.kind === 'directory') {
        try { descendants = (await api<{ descendant_count: number }>(`${root}/draft/tree?parent=${encodeURIComponent(entry.path)}&revision=${library?.draft_revision}`)).descendant_count; }
        catch (e) { report(e); return; }
      }
      if (!window.confirm(`Delete ${entry.path}${entry.kind === 'directory' ? ` and its ${descendants} contained entries` : ''} from the draft? Published versions are retained.`)) return;
    }
    setBusy(true); setError('');
    try { await mutate({ action, path: entry.path, ...(to ? { to } : {}), ...(action === 'remove' && entry.kind === 'directory' ? { recursive: true } : {}) }); setSelected(null); setText(''); setSaved(''); await load(); }
    catch (e) { report(e); } finally { setBusy(false); }
  }
  async function loadChanges(after = '') {
    if (!library) return;
    try { const p = await api<{ changes: Change[]; next: string }>(`${root}/draft/diff?revision=${library.draft_revision}&after=${encodeURIComponent(after)}`); setChanges(old => after ? [...old, ...p.changes] : p.changes); setChangeNext(p.next); setTab('changes'); }
    catch (e) { report(e); }
  }
  async function reviewPublish() {
    if (!session || !library) return;
    if (dirty) { setError('Save or discard the open text edit before reviewing publication.'); return; }
    setBusy(true); setError('');
    try { const p = await api<{ entry_count: number; total_bytes: string }>(`${root}/publish-preview`, { method: 'POST', body: JSON.stringify({ expected_revision: library.draft_revision }) }, session.csrf); setPreview({ ...p, revision: library.draft_revision }); setPublishing(true); }
    catch (e) { report(e); } finally { setBusy(false); }
  }
  async function publish(event: React.FormEvent) {
    event.preventDefault(); if (!session || !library) return;
    setBusy(true); setError('');
    try { await api(`${root}/versions`, { method: 'POST', body: JSON.stringify({ request_id: requestID(), expected_revision: preview?.revision, label, note }) }, session.csrf); setPublishing(false); setLabel(''); setNote(''); setTab('history'); setNotice('Publication admitted. Validation continues if you close this page.'); await load(); }
    catch (e) { report(e); } finally { setBusy(false); }
  }
  async function versionAction(version: Version, action: 'restore' | 'archive' | 'retire' | 'retry' | 'cancel') {
    if (!session || !library) return;
    if (action === 'retire') {
      try { const retention = await api<{ active_exports: number; active_readers: number; active_validations: number }>(`${root}/versions/${version.id}/retention`); if (retention.active_exports || retention.active_readers || retention.active_validations) { setError(`Source is retained by ${retention.active_exports} active exports, ${retention.active_readers} reader pins and ${retention.active_validations} validation jobs. Retry after these finish.`); return; } } catch (e) { report(e); return; }
    }
    if (!window.confirm(`${action[0].toUpperCase() + action.slice(1)} version ${version.label}?${action === 'restore' ? ' This replaces the current draft; existing projects are unaffected.' : action === 'retire' ? ' Source files may be collected. Existing imported copies and projects remain available.' : ''}`)) return;
    setBusy(true); setError('');
    try {
      if (action === 'restore') { await mutate({ action: 'restore', version_id: version.id, path: '' }); setVersionID(''); setSelected(null); setText(''); setSaved(''); }
      else if (action === 'archive' || action === 'retire') await api(`${root}/versions/${version.id}/lifecycle`, { method: 'POST', body: JSON.stringify({ request_id: requestID(), action }) }, session.csrf);
      else await api(`/operations/${version.id}/${action}`, { method: 'POST', body: JSON.stringify({ request_id: requestID() }) }, session.csrf);
      await load();
    } catch (e) { report(e); } finally { setBusy(false); }
  }
  async function uploadFiles(files: FileList | null) {
    if (!files || !session || !library || progress) return;
    const abort = new AbortController(); controller.current = abort; setError('');
    const failures: string[] = [];
    for (const file of Array.from(files)) {
      if (abort.signal.aborted) break;
      const name = file.webkitRelativePath || file.name;
      const target = parent ? `${parent}/${name}` : name;
      try {
        if (file.size > 256 << 20) throw new Error('exceeds 256 MiB per file');
        setProgress({ path: target, message: 'Checking file', progress: 0 });
        const digest = await hashFile(file, n => setProgress({ path: target, message: 'Checking file', progress: file.size ? n / file.size : 1 }), abort.signal);
        let l = await api<Library>(root);
        const parts = target.split('/');
        for (let i = 1; i < parts.length; i++) {
          const directory = parts.slice(0, i).join('/');
          try { const created = await api<{ revision: number }>(`${root}/draft/mutations`, { method: 'POST', body: JSON.stringify({ request_id: requestID(), expected_revision: l.draft_revision, action: 'mkdir', path: directory }) }, session.csrf); l = { ...l, draft_revision: created.revision }; }
          catch (e) { if (!(e instanceof APIError && e.status === 409)) throw e; l = await api<Library>(root); }
        }
        const pending = await api<{ uploads: Upload[] }>(`${root}/uploads`);
        let u = pending.uploads.find(x => x.state === 'open' && x.path === target && x.digest === digest && x.size === String(file.size) && x.owner.subject === session.subject && x.owner.issuer === session.issuer);
        if (!u) u = await api<Upload>(`${root}/uploads`, { method: 'POST', body: JSON.stringify({ request_id: requestID(), expected_revision: l.draft_revision, path: target, size: String(file.size), digest }) }, session.csrf);
        let offset = Number(u.offset);
        while (offset < file.size) {
          if (abort.signal.aborted) throw new DOMException('Upload paused', 'AbortError');
          const data = await file.slice(offset, offset + (8 << 20)).arrayBuffer();
          const chunkDigest = await hashChunk(data);
          u = await api<Upload>(`${root}/uploads/${u.id}/chunks/${offset}`, { method: 'PUT', body: data, headers: { 'Content-Type': 'application/octet-stream', 'X-Chunk-Digest': chunkDigest }, signal: abort.signal }, session.csrf);
          offset = Number(u.offset); setProgress({ path: target, message: 'Uploading', progress: file.size ? offset / file.size : 1 });
        }
        const current = await api<Upload>(`${root}/uploads/${u.id}`);
        let confirmTarget: string | undefined;
        if (current.current_target !== current.target_identity) {
          if (!window.confirm(`${target} changed while uploading. Replace the latest saved file with this upload?`)) throw new Error('target changed; uploaded bytes remain available for review');
          confirmTarget = current.current_target;
        }
        await api(`${root}/uploads/${u.id}/complete`, { method: 'POST', body: JSON.stringify({ request_id: requestID(), expected_revision: current.current_revision, confirm_target: confirmTarget }) }, session.csrf);
      } catch (e) { failures.push(`${target}: ${String(e)}`); }
    }
    setProgress(null); controller.current = null;
    if (failures.length) setError(failures.join('\n'));
    else setNotice('Files saved to the draft.');
    await load().catch(report);
    if (input.current) input.current.value = ''; if (folderInput.current) folderInput.current.value = '';
  }
  async function cancelUpload(u: Upload) {
    if (!session || !window.confirm(`Cancel the unfinished upload for ${u.path}?`)) return;
    try { await api(`${root}/uploads/${u.id}`, { method: 'DELETE', body: JSON.stringify({ request_id: requestID() }) }, session.csrf); await load(); } catch (e) { report(e); }
  }
  if (!session) return <SessionGate error={sessionError} loading={loading} />;
  const activeUploads = uploads.filter(u => u.state === 'open');
  const selectedVersion = versions.find(v => v.id === versionID);
  const contentURL = selected ? `${base}/api/v1${root}/${versionID ? `versions/${versionID}` : 'draft'}/content?path=${encodeURIComponent(selected.path)}` : '';
  return <main className="editor-shell">
    <header className="topbar"><Link href="/" className="wordmark">◈ <span>Volume Studio</span></Link><nav><a href="/admin">Aster administration ↗</a><SignOut session={session} /></nav></header>
    <div className="library-heading"><div><Link href="/" className="muted">Libraries /</Link><h1>{library?.name || 'Loading library…'}</h1><p>{library?.description}</p></div><div className="actions"><span className="badge">{versionID ? selectedVersion?.label || 'Published version' : `Draft · revision ${library?.draft_revision ?? '…'}`}</span><button className="button" disabled={busy || !!progress || !!versionID || activeUploads.length > 0 || !!library?.archived} onClick={() => void reviewPublish()}>Review & publish</button></div></div>
    {error && <div className="notice error" role="alert"><p>{error}</p><button className="quiet" onClick={() => { setError(''); void load().catch(report); }}>Refresh saved state</button></div>}
    {notice && <p className="notice" role="status">{notice}</p>}
    <div className="tabs" role="tablist" aria-label="Library views">{(['files', 'changes', 'history'] as const).map(t => <button key={t} role="tab" aria-selected={tab === t} onClick={() => t === 'changes' ? void loadChanges() : setTab(t)}>{t === 'files' ? 'Files' : t === 'changes' ? 'Draft changes' : `Version history (${versions.length})`}</button>)}<button className="quiet" onClick={() => void load().catch(report)}>↻ Refresh</button></div>
    {publishing && <form className="panel publish-form" onSubmit={publish}><h2>Publish this revision</h2><p>{preview?.entry_count} entries · {bytes(preview?.total_bytes ?? '0')} · draft revision {preview?.revision}</p><p className="muted">The published version is fixed. You can continue editing a new draft immediately.</p><label>Version label<input autoFocus required maxLength={128} value={label} onChange={e => setLabel(e.target.value)} placeholder="2026.10 or v1" /></label><label>Release note<textarea maxLength={4000} value={note} onChange={e => setNote(e.target.value)} /></label><div className="actions"><button className="button" disabled={busy}>Publish version</button><button className="quiet" type="button" onClick={() => setPublishing(false)}>Cancel</button></div></form>}
    {tab === 'files' && <>
      <div className="file-toolbar"><div className="actions"><button className="quiet" onClick={() => { if (confirmDiscard()) { setVersionID(''); setParent(''); setSelected(null); setText(''); setSaved(''); } }}>Draft root</button>{parent && !versionID && <button className="quiet" onClick={() => { if (confirmDiscard()) setParent(parent.split('/').slice(0, -1).join('/')); }}>↑ Parent directory</button>}<span className="muted">/{parent}</span></div><div className="actions"><button className="quiet" disabled={busy || !!versionID || !!progress} onClick={() => void create('text')}>＋ Text file</button><button className="quiet" disabled={busy || !!versionID || !!progress} onClick={() => void create('mkdir')}>＋ Directory</button><button className="button secondary" disabled={!!versionID || !!progress} onClick={() => input.current?.click()}>Upload files</button><button className="quiet" disabled={!!versionID || !!progress} onClick={() => folderInput.current?.click()}>Upload folder</button></div></div>
      <input className="visually-hidden" ref={input} type="file" multiple aria-label="Select files" onChange={e => void uploadFiles(e.target.files)} /><input className="visually-hidden" ref={folderInput} type="file" multiple {...{ webkitdirectory: '' }} aria-label="Select directory" onChange={e => void uploadFiles(e.target.files)} />
      <div className="file-workspace"><aside className="file-tree" aria-label="Files"><div className="tree-heading">Name <span>Size</span></div>{tree.entries.length === 0 && <p className="muted empty-tree">This directory is empty.</p>}{tree.entries.map(entry => <div className={`file-row ${selected?.path === entry.path ? 'selected' : ''}`} key={entry.path}><button className="file-name" aria-label={`Open ${entry.path}`} onClick={() => void choose(entry)} title={entry.path}><span aria-hidden="true">{entry.kind === 'directory' ? '▰' : '▤'}</span><span>{versionID ? entry.path : entry.path.split('/').at(-1)}</span><small>{entry.kind === 'directory' ? 'Directory' : bytes(entry.size)}</small></button>{!versionID && <details><summary aria-label={`Actions for ${entry.path}`}>⋯</summary><div className="row-menu"><button disabled={busy} onClick={() => void removeOrMove(entry, 'move')}>Rename / move</button><button disabled={busy} onClick={() => void removeOrMove(entry, 'remove')}>Delete</button></div></details>}</div>)}{tree.next && <button className="quiet" onClick={() => void api<Tree>(`${root}/${versionID ? `versions/${versionID}` : 'draft'}/tree?parent=${encodeURIComponent(parent)}&revision=${tree.revision}&after=${encodeURIComponent(tree.next)}`).then(t => setTree(old => ({ ...t, entries: [...old.entries, ...t.entries] }))).catch(report)}>Load more entries</button>}</aside>
        <section className="file-editor" aria-label="File editor">{selected ? <><div className="editor-toolbar"><strong>{selected.path}</strong><div className="actions">{dirty && <span className="unsaved">Unsaved text</span>}<a className="quiet" href={contentURL}>Download</a>{editable && !versionID && <button className="button" disabled={busy || !dirty || new TextEncoder().encode(text).length > 1 << 20} onClick={() => void save()}>Save file</button>}<button className="quiet" onClick={() => void choose(selected)}>Reload file</button></div></div>{editable ? <textarea className="code-editor" aria-label={`Contents of ${selected.path}`} spellCheck={false} readOnly={!!versionID} value={text} onChange={e => setText(e.target.value)} /> : <div className="empty-state"><span aria-hidden="true">▤</span><h3>{selected.kind === 'directory' ? 'Directory' : 'Download to inspect this file'}</h3><p>Text editing supports UTF-8 files up to 1 MiB. Binary files keep their exact uploaded bytes.</p></div>}</> : <div className="empty-state"><span aria-hidden="true">▧</span><h3>Select a file to begin</h3><p>Open text to edit it, or upload files and directories into this draft.</p></div>}</section>
      </div>
      {(progress || activeUploads.length > 0) && <section className="panel upload-panel" aria-label="Upload queue"><div className="section-heading"><h2>Uploads</h2><span className="muted">Unfinished uploads block publication</span></div>{progress && <div><strong>{progress.path}</strong><div className="progress-row"><progress max={1} value={progress.progress} /><span>{progress.message} · {Math.round(progress.progress * 100)}%</span><button className="quiet" onClick={() => controller.current?.abort()}>Pause</button></div></div>}{activeUploads.map(u => <div key={u.id} className="upload-row"><span>{u.path}</span><small>{bytes(u.offset)} / {bytes(u.size)}</small><span className="muted">Select the same file to resume</span><button className="quiet" disabled={!!progress} onClick={() => void cancelUpload(u)}>Cancel upload</button></div>)}</section>}
    </>}
    {tab === 'changes' && <section className="panel"><h2>Changes since the draft base</h2>{changes.length === 0 ? <p className="muted">No saved changes from the base version.</p> : <ul className="change-list">{changes.map(c => <li key={c.path}><span className={`badge ${c.change}`}>{c.change}</span><code>{c.path}</code></li>)}</ul>}{changeNext && <button className="quiet" onClick={() => void loadChanges(changeNext)}>Load more changes</button>}</section>}
    {tab === 'history' && <section className="history">{versions.length === 0 && <div className="empty-state"><h3>No published versions yet</h3><p>Review your saved draft when you are ready to publish.</p></div>}{versions.map(v => <article className="panel version-card" key={v.id}><div className="section-heading"><h2>{v.label}</h2><span className={`badge ${v.state}`}>{v.state}</span></div><p>{v.note || 'No release note.'}</p><p className="muted">Frozen from revision {v.source_revision} · Publisher {v.publisher.subject}</p><code className="digest">{v.descriptor_digest}</code>{v.error && <p role="alert">{v.error.replaceAll('_', ' ')}</p>}<div className="actions">{['published', 'archived'].includes(v.state) && <><button className="button secondary" onClick={() => { if (confirmDiscard()) { setVersionID(v.id); setSelected(null); setText(''); setSaved(''); setTab('files'); } }}>Inspect files</button><button className="quiet" disabled={busy} onClick={() => void versionAction(v, 'restore')}>Restore to draft</button></>}{v.state === 'published' && <><a className="button" href={`/admin?volume_publisher=${encodeURIComponent(v.publisher_id)}&volume_library=${id}&volume_version=${v.id}`}>Use in Aster ↗</a><button className="quiet" disabled={busy} onClick={() => void versionAction(v, 'archive')}>Archive source</button></>}{['archived', 'failed'].includes(v.state) && <button className="quiet" disabled={busy} onClick={() => void versionAction(v, 'retire')}>Retire source</button>}{v.state === 'failed' && <button className="quiet" disabled={busy} onClick={() => void versionAction(v, 'retry')}>Retry validation</button>}{['pending', 'validating'].includes(v.state) && <button className="quiet" disabled={busy} onClick={() => void versionAction(v, 'cancel')}>Cancel publication</button>}</div></article>)}{versionNext && <button className="quiet" onClick={() => void api<{ versions: Version[]; next: string }>(`${root}/versions?after=${versionNext}`).then(p => { setVersions(old => [...old, ...p.versions]); setVersionNext(p.next); }).catch(report)}>Load more versions</button>}</section>}
  </main>;
}
