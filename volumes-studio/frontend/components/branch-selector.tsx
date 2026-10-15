"use client";
import { useEffect, useEffectEvent, useRef, useState } from "react";
import { branchAPI, type Branch } from "../lib/branches";
import { requestID } from "../lib/api";
import { storageRead, storageWrite } from "../lib/workbench";
import { useT } from "../lib/i18n";
import { useStudio } from "./studio-provider";

export function BranchSelector({ library, onSelect }: { library: string; onSelect: () => void }) {
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
  useEffect(() => {
    const abort = new AbortController();
    void (async () => {
      const page = await branchAPI.list(library, "", abort.signal);
      const selected = new URL(location.href).searchParams.get("branch");
      const branch = selected ? await branchAPI.read(library, selected, abort.signal) : null;
      if (abort.signal.aborted) return;
      setBranches(branch && !page.branches.some(item => item.id === branch.id) ? [...page.branches, branch] : page.branches);
      setNext(page.next);
      if (branch) restoreSelection(branch);
      const retained = storageRead("session", storageKey);
      if (retained) {
        const value: unknown = JSON.parse(retained);
        if (value && typeof value === "object" && "name" in value && "base" in value && "request" in value && typeof value.name === "string" && typeof value.base === "string" && typeof value.request === "string") {
          pending.current = { name: value.name, base: value.base, request: value.request };
          setName(value.name);
          setPendingCreate(true);
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
      const url = new URL(location.href); url.searchParams.set("branch", id); url.searchParams.delete("version"); history.replaceState(null, "", url);
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
      await select(branch.id);
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  };
  return <section aria-label={tr("Working branch")}>
    <label>{tr("Working branch")} <select disabled={busy} value={studio.model.branches[library]?.id ?? ""} onChange={event => { if (event.target.value) void select(event.target.value); }}>
      <option value="">{tr("Select a working branch")}</option>
      {branches.map(branch => <option key={branch.id} value={branch.id}>{branch.name}</option>)}
    </select></label>
    {next && <button disabled={busy} onClick={() => { setBusy(true); void branchAPI.list(library, next).then(page => { setBranches(items => [...items, ...page.branches]); setNext(page.next); }).catch(e => setError(String(e))).finally(() => setBusy(false)); }}>{tr("Load more")}</button>}
    <label>{tr("New branch name")} <input readOnly={pendingCreate} value={name} onChange={event => { if (!pending.current) setName(event.target.value); }} disabled={busy} /></label>
    <button disabled={busy || !name.trim()} onClick={() => void create()}>{tr("Create branch from main")}</button>
    {error && <p role="alert">{error}</p>}
  </section>;
}
