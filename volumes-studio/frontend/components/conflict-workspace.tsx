"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError, base, bytes, requestID, type Entry } from "../lib/api";
import { branchAPI, conflictAPI, type ConflictWorkspace, type PreparedIntegration } from "../lib/branches";
import { useT } from "../lib/i18n";
import { useStudio } from "./studio-provider";
type Preview = { text?: string; preview: string; commit_id: string; entry: Entry };
type Choice = "main" | "branch" | "delete" | "text";
type Draft = { path: string; choice: Choice; text: string; expected_revision: number };
type Command = { action: "save"; request_id: string; draft: Draft } | { action: "complete"; request_id: string; expected_revision: number };
type Recovery = { workspace: string; selected: string; draft?: Draft; pending?: Command };
const empty = (workspace: string): Recovery => ({ workspace, selected: "" });
function readRecovery(raw: string, id: string): Recovery {
  if (raw.length > 3 << 20) throw new Error("recovery too large");
  const value = JSON.parse(raw) as Recovery;
  const revision = (n: number) => Number.isSafeInteger(n) && n > 0;
  const draft = (d: Draft) => d && typeof d.path === "string" && d.path.length > 0 && typeof d.text === "string" && d.text.length <= 1 << 20 && ["main", "branch", "delete", "text"].includes(d.choice) && revision(d.expected_revision);
  if (value?.workspace !== id || typeof value.selected !== "string" || value.draft && (!draft(value.draft) || value.draft.path !== value.selected)) throw new Error("invalid recovery");
  if (value.pending) {
    const command = value.pending;
    if (typeof command.request_id !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(command.request_id) || (command.action !== "save" && command.action !== "complete")) throw new Error("invalid command");
    if (command.action === "save" ? !draft(command.draft) || JSON.stringify(command.draft) !== JSON.stringify(value.draft) : !revision(command.expected_revision) || !!value.draft) throw new Error("invalid command identity");
  }
  return value;
}
export function ConflictEditor({ library, id, onComplete, onReplacement, onBlockedChange }: { library: string; id: string; onComplete: (digest: string) => void; onReplacement: () => void; onBlockedChange: (blocked: boolean) => void }) {
  const tr = useT(), studio = useStudio();
  const [workspace, setWorkspace] = useState<ConflictWorkspace>();
  const [local, setLocal] = useState<Recovery>(empty(id));
  const retained = useRef<Recovery>(empty(id));
  const storageDirty = useRef(false);
  const [previews, setPreviews] = useState<Preview[]>([]);
  const [choice, setChoice] = useState<Choice>("main"), [text, setText] = useState("");
  const [error, setError] = useState(""), [storageError, setStorageError] = useState(""), [malformed, setMalformed] = useState(false), [busy, setBusy] = useState(false);
  const active = useRef<AbortController | null>(null), inFlight = useRef(false);
  const root = `/libraries/${encodeURIComponent(library)}/conflicts/${encodeURIComponent(id)}`;
  const key = `studio-conflict:${JSON.stringify([studio.session?.issuer, studio.session?.subject, library, id])}`;
  const conflict = !!workspace && !!local.draft && local.draft.expected_revision !== workspace.revision;
  function persist(value: Recovery) {
    retained.current = value; setLocal(value);
    try { sessionStorage.setItem(key, JSON.stringify(value)); storageDirty.current = false; setStorageError(""); }
    catch { storageDirty.current = true; setStorageError("Local conflict recovery is unavailable. Keep this tab open until the operation is confirmed."); }
  }
  function clearVisible() { setWorkspace(undefined); setPreviews([]); setLocal(empty(id)); setText(""); setChoice("main"); }
  async function show(current: ConflictWorkspace, value: Recovery, abort: AbortController) {
    if (!current.conflicts.some(item => item.path === value.selected)) {
      if (value.draft || value.pending?.action === "save") throw new Error("unknown conflict path");
      value = { ...value, selected: "" };
    }
    const files = value.selected ? await Promise.all(["base", "main", "branch"].map(side => api<Preview>(`${root}/file?${new URLSearchParams({ side, path: value.selected })}`, { signal: abort.signal }))) : [];
    if (abort.signal.aborted) return;
    const saved = current.resolutions.find(item => item.path === value.selected);
    setWorkspace(current); setLocal(value); setPreviews(files);
    setChoice(value.draft?.choice ?? saved?.choice ?? "main"); setText(value.draft?.text ?? saved?.text ?? "");
  }
  async function reload() {
    if (inFlight.current) return;
    active.current?.abort(); const abort = new AbortController(); active.current = abort;
    setBusy(true); setError(""); clearVisible();
    try {
      const current = await conflictAPI.read(library, id, abort.signal);
      if (abort.signal.aborted) return;
      let value = retained.current;
      let raw: string | null = null;
      try { if (!storageDirty.current) raw = sessionStorage.getItem(key); }
      catch { setStorageError("Local conflict recovery is unavailable. Keep this tab open until the operation is confirmed."); }
      try {
        if (raw) value = readRecovery(raw, id);
        if ((value.draft || value.pending?.action === "save") && !current.conflicts.some(item => item.path === value.selected)) throw new Error("unknown conflict path");
        setMalformed(false);
      } catch { setMalformed(true); value = empty(id); }
      retained.current = value;
      await show(current, value, abort);
    } catch { if (!abort.signal.aborted) { clearVisible(); setError("Conflict evidence is unavailable. Reload or check Volume access."); } }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  useEffect(() => {
    retained.current = empty(id); storageDirty.current = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Restore local content only after current scoped server authorization.
    void reload();
    return () => active.current?.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  useEffect(() => {
    if (!local.draft && !local.pending) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", warn); return () => window.removeEventListener("beforeunload", warn);
  }, [local.draft, local.pending]);
  useEffect(() => {
    onBlockedChange(busy || !!retained.current.draft || !!retained.current.pending || malformed);
    return () => onBlockedChange(false);
  }, [busy, local, malformed, onBlockedChange]);
  async function inspect(path: string) {
    if (!workspace || busy || inFlight.current || local.pending || malformed) return;
    if (local.draft && path !== local.selected && await studio.ask("Choose conflict", "Discard the unsaved resolution before choosing another path?", ["Keep editing", "Discard and choose"]) !== "Discard and choose") return;
    const value = path === local.selected ? local : { workspace: id, selected: path };
    active.current?.abort(); const abort = new AbortController(); active.current = abort; setBusy(true); setError(""); setPreviews([]);
    try { await show(workspace, value, abort); if (!abort.signal.aborted) persist(value); }
    catch { if (!abort.signal.aborted) { clearVisible(); setError("Conflict evidence is unavailable. Reload or check Volume access."); } }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  function edit(nextChoice: Choice, nextText: string) {
    if (!workspace) return;
    setChoice(nextChoice); setText(nextText);
    persist({ ...local, draft: { path: local.selected, choice: nextChoice, text: nextChoice === "text" ? nextText : "", expected_revision: local.draft?.expected_revision ?? workspace.revision } });
  }
  async function submit(command: Command) {
    if (!studio.session || !workspace || inFlight.current) return;
    inFlight.current = true; active.current?.abort(); const abort = new AbortController(); active.current = abort;
    let confirmed = false;
    setBusy(true); setError(""); persist({ ...retained.current, ...(command.action === "save" ? { draft: command.draft } : {}), pending: command });
    try {
      const body = command.action === "save" ? { request_id: command.request_id, ...command.draft } : { request_id: command.request_id, expected_revision: command.expected_revision };
      const result = await api<PreparedIntegration | { revision: number }>(`${root}/${command.action === "save" ? "resolutions" : "complete"}`, { method: "POST", body: JSON.stringify(body), signal: abort.signal }, studio.session.csrf);
      if (abort.signal.aborted) return;
      if (command.action === "complete") {
        if (!("identity" in result) || !result.identity) throw new Error("missing prepared identity");
        persist(empty(id)); onComplete(result.digest);
      } else {
        const next = { workspace: id, selected: local.selected };
        confirmed = true; persist(next);
        const current = await conflictAPI.read(library, id, abort.signal);
        await show(current, next, abort);
      }
    } catch (cause) {
      if (abort.signal.aborted) return;
      if (confirmed || cause instanceof APIError && [401, 403, 404].includes(cause.status)) { clearVisible(); setError("Conflict evidence is unavailable. Reload or check Volume access."); }
      else if (cause instanceof APIError && cause.status < 500) {
        persist({ ...retained.current, pending: undefined });
        setError(cause.status === 409 ? "Conflict workspace changed. Reload, review the current resolution, and explicitly use its revision before saving." : "Conflict operation was rejected. Check the resolution and reload.");
      } else setError("Conflict operation is unconfirmed. Recover the original request before making another change.");
    } finally { inFlight.current = false; if (!abort.signal.aborted) setBusy(false); }
  }
  async function uploadReplacement(file: File) {
    if (!workspace || busy || inFlight.current || local.draft || local.pending || workspace.stale || malformed || workspace.state !== "open") return;
    inFlight.current = true; setBusy(true); setError("");
    active.current?.abort(); const abort = new AbortController(); active.current = abort;
    try {
      const branch = await branchAPI.read(library, workspace.branch_id, abort.signal);
      if (abort.signal.aborted) return;
      if (branch.head !== workspace.source_commit || studio.model.branches[library]?.id !== branch.id) {
        setError("Inputs changed. Prepare a new integration before resolving."); return;
      }
      const slash = local.selected.lastIndexOf("/");
      const name = local.selected.slice(slash + 1), parent = slash < 0 ? "" : local.selected.slice(0, slash);
      const tasks = await studio.upload(library, parent, [new File([file], name, { type: file.type, lastModified: file.lastModified })]);
      if (abort.signal.aborted) return;
      if (tasks.length !== 1 || tasks[0].state !== "Complete") {
        setError("Replacement upload is not confirmed. Inspect Upload tasks and recover it before committing."); return;
      }
      onReplacement();
    } catch { if (!abort.signal.aborted) setError("Replacement upload is not confirmed. Inspect Upload tasks and recover it before committing."); }
    finally { inFlight.current = false; if (!abort.signal.aborted) setBusy(false); }
  }
  async function discard() {
    if (await studio.ask("Discard conflict recovery", "An uncertain operation may already have succeeded. Discard the local request and resolution?", ["Keep editing", "Discard"]) !== "Discard") return;
    try { sessionStorage.removeItem(key); setStorageError(""); } catch { setStorageError("Local conflict recovery is unavailable. Keep this tab open until the operation is confirmed."); return; }
    retained.current = empty(id); storageDirty.current = false; setMalformed(false); await reload();
  }
  function renderPreview(preview: Preview) {
    return <>
      <p><code>{preview.commit_id}</code></p>
      {preview.entry?.kind && <p>{tr(preview.entry.kind === "file" ? "File" : "Directory")} · {bytes(preview.entry.size)}{preview.entry.digest && <> · <code>{preview.entry.digest}</code></>}</p>}
      <pre>{preview.text ?? tr(preview.preview === "absent" ? "File absent" : preview.preview === "binary" ? "Binary file: text preview unavailable." : preview.preview === "too_large" ? "File exceeds the 1 MiB text preview limit." : "Directory")}</pre>
      {preview.entry?.kind === "file" && <a href={`${base}/api/v1/libraries/${encodeURIComponent(library)}/commits/${encodeURIComponent(preview.commit_id)}/content?${new URLSearchParams({ path: local.selected })}`}>{tr("Download exact file")}</a>}
      {preview.entry?.kind === "directory" && <p>{tr("Choosing this side includes the complete directory subtree. Nested conflict choices must agree.")}</p>}
    </>;
  }
  const result = choice === "main" ? previews[1] : choice === "branch" ? previews[2] : undefined;
  const blocked = busy || !!local.pending || malformed || workspace?.stale || workspace?.state !== "open";
  return <section aria-label={tr("Resolve merge conflicts")}>
    <h3>{tr("Resolve merge conflicts")}</h3><button disabled={busy} onClick={() => void reload()}>{tr("Reload conflict workspace")}</button>
    {error && <p role="alert">{tr(error)}</p>}{storageError && <p role="alert">{tr(storageError)}</p>}
    {workspace && <>
      {malformed && <p role="alert">{tr("Local conflict recovery is invalid. Discard it to continue.")}</p>}
      {workspace.stale && <p role="alert">{tr("Inputs changed. Prepare a new integration before resolving.")}</p>}
      {local.draft && <p role="status">{tr("Unsaved resolution is retained in this tab.")}</p>}
      {local.pending && <button disabled={busy} onClick={() => void submit(local.pending!)}>{tr("Recover conflict operation")}</button>}
      {(local.draft || local.pending || malformed) && <button disabled={busy} onClick={() => void discard()}>{tr("Discard conflict recovery")}</button>}
      <ul>{workspace.conflicts.map(item => <li key={item.path}><button disabled={busy || !!local.pending || malformed} onClick={() => void inspect(item.path)}>{item.path}</button> — {workspace.resolutions.some(saved => saved.path === item.path) ? tr("Resolution saved") : tr("Unresolved")}</li>)}</ul>
      {local.selected && <><h4>{local.selected}</h4><div className="conflict-sides">{previews.map((preview, i) => <section key={i}><h4>{tr(["Common ancestor", "Main before merge", "Branch candidate"][i])}</h4>{renderPreview(preview)}</section>)}</div>
        {conflict && <><p role="alert">{tr("Conflict workspace changed. Reload, review the current resolution, and explicitly use its revision before saving.")}</p><details><summary>{tr("Current saved resolution")}</summary><pre>{JSON.stringify(workspace.resolutions.find(saved => saved.path === local.selected) ?? null, null, 2)}</pre></details><button disabled={blocked} onClick={() => persist({ ...local, draft: { ...local.draft!, expected_revision: workspace.revision } })}>{tr("Use current conflict revision")}</button></>}
        <label>{tr("Resolution choice")} <select aria-label={tr("Resolution choice")} value={choice} disabled={blocked} onChange={event => edit(event.target.value as Choice, text)}><option value="main">{tr("Use main")}</option><option value="branch">{tr("Use branch")}</option><option value="delete">{tr("Delete path")}</option><option value="text">{tr("Write resolved text")}</option></select></label>
        {choice === "text" && <label>{tr("Resolved text")} <textarea aria-label={tr("Resolved text")} value={text} maxLength={1 << 20} onChange={event => edit(choice, event.target.value)} disabled={blocked} /></label>}
        <section aria-label={tr("Resolution result")}><h4>{tr("Resolution result")}</h4>
          {choice === "delete" ? <p>{tr("This path and its descendants will be removed.")}</p> : choice === "text" ? <pre>{text}</pre> : result && renderPreview(result)}
          <p>{tr(local.draft ? "This choice is unsaved. Save it before preparing the resolved integration." : "The complete resolved tree still requires preparation and fresh review.")}</p>
        </section>
        <section aria-label={tr("Upload replacement")}>
          <p>{tr("Upload a replacement to this path in the working branch draft. Then commit the draft and prepare a new integration; existing conflict choices remain on this workspace.")}</p>
          <p><code>{local.selected}</code></p>
          {previews[2]?.entry?.kind === "directory" && <p>{tr("Replace this directory explicitly in branch authoring before uploading a file.")}</p>}
          <label>{tr("Replacement file")} <input aria-label={tr("Replacement file")} type="file" disabled={blocked || !!local.draft || previews[2]?.entry?.kind === "directory"} onChange={event => { const file = event.target.files?.[0]; event.target.value = ""; if (file) void uploadReplacement(file); }} /></label>
        </section>
        <button disabled={blocked || conflict} onClick={() => { const draft = local.draft ?? { path: local.selected, choice, text: choice === "text" ? text : "", expected_revision: workspace.revision }; const next = { ...local, draft }; retained.current = next; setLocal(next); void submit({ action: "save", request_id: requestID(), draft }); }}>{tr("Save resolution")}</button>
      </>}
      <button disabled={blocked || !!local.draft || workspace.resolutions.length !== workspace.conflicts.length} onClick={() => void submit({ action: "complete", request_id: requestID(), expected_revision: workspace.revision })}>{tr("Prepare resolved integration")}</button>
    </>}
  </section>;
}
