"use client";
import { useEffect, useEffectEvent, useRef, useState } from "react";
import { branchAPI, type Branch } from "../lib/branches";
import { APIError, requestID } from "../lib/api";
import { storageRead, storageWrite } from "../lib/workbench";
import { useT } from "../lib/i18n";
import { GitBranch, Plus } from "lucide-react";
import { useStudio } from "./studio-provider";

// BranchSelector is the toolbar branch switcher. Creating a branch opens a
// one-line panel under the toolbar; the parent owns its open state so empty
// states can open it too.
export function BranchSelector({ library, onSelect, creating, setCreating }: { library: string; onSelect: () => void; creating: boolean; setCreating: (open: boolean) => void }) {
  const tr = useT();
  const studio = useStudio();
  const [branches, setBranches] = useState<Branch[]>([]);
  const [next, setNext] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [pendingCreate, setPendingCreate] = useState(false);
  const pending = useRef<{ name: string; base: string; request: string } | null>(null);
  const account = studio.session ? `${studio.session.issuer}:${studio.session.subject}` : "";
  const storageKey = `studio-branch-create:${account}:${library}`;
  const restoreSelection = useEffectEvent((branch: Branch) => { studio.selectBranch(branch); });
  const reopenCreate = useEffectEvent(() => setCreating(true));
  useEffect(() => {
    const abort = new AbortController();
    void (async () => {
      const page = await branchAPI.list(library, "", abort.signal);
      if (abort.signal.aborted) return;
      setBranches(page.branches); setNext(page.next);
      const selected = new URL(location.href).searchParams.get("branch");
      if (selected) {
        try {
          const branch = await branchAPI.read(library, selected, abort.signal);
          if (abort.signal.aborted) return;
          setBranches(items => items.some(item => item.id === branch.id) ? items : [...items, branch]);
          restoreSelection(branch);
        } catch (e) {
          if (abort.signal.aborted) return;
          // A stale link must not keep failing on every reload.
          const url = new URL(location.href); url.searchParams.delete("branch"); history.replaceState(null, "", url);
          setError(String(e));
        }
      }
      const retained = storageRead("session", storageKey);
      if (retained) {
        const value: unknown = JSON.parse(retained);
        if (value && typeof value === "object" && "name" in value && "base" in value && "request" in value && typeof value.name === "string" && typeof value.base === "string" && typeof value.request === "string") {
          pending.current = { name: value.name, base: value.base, request: value.request };
          setName(value.name);
          setPendingCreate(true);
          reopenCreate();
        }
      }
    })().catch(e => { if (!abort.signal.aborted) setError(String(e)); });
    return () => abort.abort();
  }, [library, storageKey]);
  const select = async (id: string) => {
    setBusy(true); setError("");
    try {
      const branch = await branchAPI.read(library, id);
      studio.selectBranch(branch);
      const url = new URL(location.href); url.searchParams.set("branch", id);
      if (id !== studio.model.branches[library]?.id) for (const key of ["integration", "conflict", "publication", "history_head"]) url.searchParams.delete(key);
      url.searchParams.delete("version"); history.replaceState(null, "", url);
      onSelect();
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  };
  const create = async () => {
    if (!studio.session || !name.trim()) return;
    setBusy(true); setError("");
    try {
      if (!pending.current) {
        const main = await branchAPI.main(library);
        pending.current = { name: name.trim(), base: main.commit_id, request: requestID() };
        storageWrite("session", storageKey, JSON.stringify(pending.current));
        setPendingCreate(true);
      }
      const request = pending.current;
      const branch = await branchAPI.create(library, request.name, request.base, request.request, studio.session.csrf);
      pending.current = null;
      setPendingCreate(false);
      storageWrite("session", storageKey, "");
      setBranches(items => items.some(item => item.id === branch.id) ? items : [...items, branch]); setName("");
      setCreating(false);
      await select(branch.id);
    } catch (e) {
      if (e instanceof APIError && e.status < 500 && e.status !== 401) {
        pending.current = null; setPendingCreate(false); storageWrite("session", storageKey, "");
      }
      setError(String(e));
    } finally { setBusy(false); }
  };
  return <>
    <section aria-label={tr("Working branch")} className="bar-branch">
      <label><GitBranch size={15} aria-hidden="true" /><span className="visually-hidden">{tr("Working branch")}</span><select disabled={busy} value={studio.model.branches[library]?.id ?? ""} onChange={event => { if (event.target.value) void select(event.target.value); }}>
        <option value="">{tr("Select a working branch")}</option>
        {branches.map(branch => <option key={branch.id} value={branch.id}>{branch.name}</option>)}
      </select></label>
      {next && <button className="ghost" disabled={busy} onClick={() => { setBusy(true); void branchAPI.list(library, next).then(page => { setBranches(items => [...items, ...page.branches.filter(branch => !items.some(item => item.id === branch.id))]); setNext(page.next); }).catch(e => setError(String(e))).finally(() => setBusy(false)); }}>{tr("Load more")}</button>}
      <button type="button" className="ghost" aria-expanded={creating} aria-controls="studio-new-branch" onClick={() => setCreating(!creating)}><Plus size={14} aria-hidden="true" />{tr("New branch")}</button>
    </section>
    <div id="studio-new-branch" className="bar-panel" hidden={!creating}>
      <label>{tr("New branch name")} <input readOnly={pendingCreate} value={name} onChange={event => { if (!pending.current) setName(event.target.value); }} onKeyDown={event => { if (event.key === "Enter" && name.trim()) void create(); if (event.key === "Escape" && !pendingCreate) setCreating(false); }} disabled={busy} /></label>
      <button className="primary" disabled={busy || !name.trim()} onClick={() => void create()}>{tr("Create branch from main")}</button>
      {!pendingCreate && <button type="button" className="ghost" onClick={() => setCreating(false)}>{tr("Cancel")}</button>}
      <span className="hint">{tr("A new branch starts from the current main. Edits on it stay out of main until reviewed.")}</span>
    </div>
    {error && <p role="alert" className="bar-error">{error}</p>}
  </>;
}
