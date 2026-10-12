'use client';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useCallback, useEffect, useState } from 'react';
import { api, Library, requestID } from '../lib/api';
import { SessionGate, SignOut, useSession } from '../components/session';

export default function Home() {
  const router = useRouter();
  const { session, error: sessionError, loading } = useSession();
  const [libraries, setLibraries] = useState<Library[]>([]);
  const [next, setNext] = useState('');
  const [error, setError] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState(false);
  const load = useCallback(async (after = '') => {
    const page = await api<{ libraries: Library[]; next: string }>(`/libraries?after=${encodeURIComponent(after)}`);
    setLibraries(old => after ? [...old, ...page.libraries] : page.libraries); setNext(page.next);
  }, []);
  useEffect(() => {
    if (!session) return;
    const abort = new AbortController();
    api<{ libraries: Library[]; next: string }>('/libraries', { signal: abort.signal }).then(page => { setLibraries(page.libraries); setNext(page.next); }).catch(e => { if (!abort.signal.aborted) setError(String(e)); });
    return () => abort.abort();
  }, [session]);
  async function create(event: React.FormEvent) {
    event.preventDefault(); if (!session) return;
    setBusy(true); setError('');
    try {
      const library = await api<Library>('/libraries', { method: 'POST', body: JSON.stringify({ request_id: requestID(), name, description }) }, session.csrf);
      router.push(`/libraries/${library.id}`);
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  if (!session) return <SessionGate error={sessionError} loading={loading} />;
  return <main className="shell">
    <header className="topbar"><Link href="/" className="wordmark">◈ <span>Volume Studio</span></Link><nav><a href="/admin">Aster administration ↗</a><SignOut session={session} /></nav></header>
    <div className="hero"><div><p className="eyebrow">YOUR CONTENT LIBRARY</p><h1>Good work starts<br />with the right files.</h1><p className="lede">Build collections of knowledge, datasets and starter files. Publish an exact version whenever it is ready.</p></div><button className="button" onClick={() => setCreating(true)}>＋ New library</button></div>
    {error && <p className="notice error" role="alert">{error}</p>}
    {creating && <form className="panel create-form" onSubmit={create}><h2>Create a library</h2><label>Name<input required autoFocus maxLength={200} value={name} onChange={e => setName(e.target.value)} placeholder="Research handbook" /></label><label>Description<textarea maxLength={4000} value={description} onChange={e => setDescription(e.target.value)} placeholder="What should someone know about these files?" /></label><div className="actions"><button className="button" disabled={busy || !name.trim()}>Create draft</button><button type="button" className="quiet" onClick={() => setCreating(false)}>Cancel</button></div></form>}
    <section aria-labelledby="libraries"><div className="section-heading"><h2 id="libraries">Libraries <span className="count">{libraries.length}</span></h2><button className="quiet" onClick={() => void load().catch(e => setError(String(e)))}>Refresh</button></div>
      {libraries.length === 0 ? <div className="empty-state"><span aria-hidden="true">▧</span><h3>A place for reusable files</h3><p>Create a library to start a draft. Your files stay saved as you work.</p></div> : <div className="library-grid">{libraries.map(library => <Link className="library-card" key={library.id} href={`/libraries/${library.id}`}><div className="card-icon" aria-hidden="true">▤</div><h3>{library.name}</h3><p>{library.description || 'A reusable collection of files.'}</p><div><span className="badge">{library.archived ? 'Archived' : 'Draft'}</span><span className="muted">Revision {library.draft_revision}</span><span aria-hidden="true">→</span></div></Link>)}</div>}
      {next && <button className="quiet" onClick={() => void load(next).catch(e => setError(String(e)))}>Load more libraries</button>}
    </section>
    <footer>Published versions stay fixed. Existing projects keep the version they started with.</footer>
  </main>;
}
