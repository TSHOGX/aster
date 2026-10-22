"use client";
import { useEffect, useRef, useState } from "react";
import { api, APIError, requestID, type Version } from "../lib/api";
import { useT } from "../lib/i18n";
import { useStudio } from "./studio-provider";

type Retention = { active_exports: number; active_readers: number; active_validations: number; draft_blob_references: number };
type Command = { version: string; action: "archive" | "retire"; request_id: string };
type Evidence = { version: Version; retention: Retention };

export function VersionRetention({ library, id, onChanged }: { library: string; id: string; onChanged: () => void }) {
  const tr = useT(), studio = useStudio();
  const [evidence, setEvidence] = useState<Evidence>();
  const [pending, setPending] = useState<Command>();
  const retained = useRef<Command>(undefined), hydrated = useRef(false);
  const [invalid, setInvalid] = useState(false), [busy, setBusy] = useState(false);
  const [error, setError] = useState(""), [storageError, setStorageError] = useState("");
  const active = useRef<AbortController>(undefined), inFlight = useRef(false);
  const root = `/libraries/${encodeURIComponent(library)}/versions/${encodeURIComponent(id)}`;
  const key = `studio-version-lifecycle:${JSON.stringify([studio.session?.issuer, studio.session?.subject, library, id])}`;
  const storageMessage = "Local operation recovery is unavailable. Keep this tab open until the operation is confirmed.";
  function remember(command?: Command) {
    retained.current = command; setPending(command);
    try {
      if (command) sessionStorage.setItem(key, JSON.stringify(command)); else sessionStorage.removeItem(key);
      setStorageError("");
    } catch { setStorageError(storageMessage); }
  }
  async function read(abort: AbortController) {
    setEvidence(undefined);
    const [version, retention] = await Promise.all([
      api<Version>(root, { signal: abort.signal }),
      api<Retention>(`${root}/retention`, { signal: abort.signal }),
    ]);
    if (abort.signal.aborted) return;
    if (!hydrated.current) {
      try {
        const raw = sessionStorage.getItem(key);
        if (raw) {
          try {
            if (raw.length > 1024) throw new Error("oversized recovery");
            const command = JSON.parse(raw) as Command;
            if (command?.version !== id || !["archive", "retire"].includes(command.action) || typeof command.request_id !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(command.request_id)) throw new Error("invalid recovery");
            retained.current = { version: id, action: command.action, request_id: command.request_id };
          } catch { setInvalid(true); }
        }
      } catch { setStorageError(storageMessage); }
      hydrated.current = true;
    }
    setPending(retained.current); setEvidence({ version, retention });
  }
  async function reload() {
    if (inFlight.current) return;
    active.current?.abort(); const abort = new AbortController(); active.current = abort;
    setBusy(true); setError("");
    try { await read(abort); }
    catch { if (!abort.signal.aborted) setError("Version and retention details are unavailable. Reload or check Volume access."); }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  useEffect(() => {
    hydrated.current = false; retained.current = undefined;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Recover commands only after current server authorization.
    void reload();
    return () => active.current?.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  async function submit(command: Command) {
    if (!studio.session || !evidence || inFlight.current) return;
    active.current?.abort(); const abort = new AbortController(); active.current = abort;
    inFlight.current = true; setBusy(true); setError(""); remember(command);
    let confirmed = false;
    try {
      await api<Version>(`${root}/lifecycle`, { method: "POST", body: JSON.stringify({ request_id: command.request_id, action: command.action }), signal: abort.signal }, studio.session.csrf);
      if (abort.signal.aborted) return;
      confirmed = true; remember();
      studio.changed(); onChanged();
      await read(abort);
    } catch (cause) {
      if (abort.signal.aborted) return;
      if (confirmed) setError("Version and retention details are unavailable. Reload or check Volume access.");
      else if (cause instanceof APIError && cause.status < 500) {
        setEvidence(undefined);
        if (![401, 403].includes(cause.status)) remember();
        setError("Version operation was rejected. Reload current state and retention before deciding again.");
      } else setError("Version operation is unconfirmed. Recover the original request before starting another action.");
    } finally { inFlight.current = false; if (!abort.signal.aborted) setBusy(false); }
  }
  async function decide(action: Command["action"]) {
    if (!evidence || busy || pending || invalid) return;
    const prompt = action === "archive"
      ? "Archive version {label}? New imports stop; admitted readers and existing imported copies remain."
      : "Retire version {label}? Its distribution files become eligible for collection when unreferenced. Accepted history, branch drafts and existing imported copies remain.";
    if (await studio.ask(action === "archive" ? "Archive version" : "Retire version", { key: prompt, params: { label: evidence.version.label } }, ["Cancel", "Continue"]) !== "Continue") return;
    await submit({ version: id, action, request_id: requestID() });
  }
  async function discard() {
    if (await studio.ask("Discard version recovery", "The operation may already have succeeded. Discard its local request identity?", ["Keep recovery", "Discard"]) !== "Discard") return;
    try { sessionStorage.removeItem(key); }
    catch { setStorageError(storageMessage); return; }
    retained.current = undefined; setPending(undefined); setInvalid(false); setStorageError("");
    await reload();
  }
  const blockers = evidence && (evidence.retention.active_exports + evidence.retention.active_readers + evidence.retention.active_validations > 0);
  return <section aria-label={tr("Version retention")}>
    <button disabled={busy} onClick={() => void reload()}>{tr("Reload version details")}</button>
    {error && <p role="alert">{tr(error)}</p>}{storageError && <p role="alert">{tr(storageError)}</p>}
    {busy && <p role="status">{tr("Loading…")}</p>}
    {evidence && <>
      <h3>{evidence.version.label}</h3><p>{tr(evidence.version.state)}</p>
      <p className="preserve-lines">{evidence.version.note || tr("No release note.")}</p>
      <dl>
        <dt>{tr("Accepted commit")}</dt><dd><code>{evidence.version.accepted_commit}</code></dd>
        <dt>{tr("Publisher")}</dt><dd>{evidence.version.publisher.subject}</dd>
        <dt>{tr("Descriptor")}</dt><dd><code>{evidence.version.descriptor_digest}</code></dd>
      </dl>
      {evidence.version.error && <p role="alert">{tr(evidence.version.error)}</p>}
      <h3>{tr("Source retention")}</h3>
      <dl>
        <dt>{tr("Active exports")}</dt><dd>{evidence.retention.active_exports}</dd>
        <dt>{tr("Reader pins")}</dt><dd>{evidence.retention.active_readers}</dd>
        <dt>{tr("Publication validations")}</dt><dd>{evidence.retention.active_validations}</dd>
        <dt>{tr("Branch draft references")}</dt><dd>{evidence.retention.draft_blob_references}</dd>
      </dl>
      <p>{tr("Exports, reader pins and validations block retirement. Reader pins may count one reader more than once.")}</p>
      <p>{tr("Branch draft references retain shared bytes independently and do not block retirement. Accepted history, backups and imported Project copies are not erased.")}</p>
      {blockers && <p role="status">{tr("Retirement is blocked. Wait for active work to settle, then reload these details.")}</p>}
      {evidence.version.state === "retired" && <p>{tr("Source retired. Distribution downloads are unavailable; retained accepted history remains readable with current access.")}</p>}
      {invalid && <p role="alert">{tr("Local version recovery is invalid. Discard it before starting another operation.")}</p>}
      {pending && <button disabled={busy} onClick={() => void submit(pending)}>{tr("Recover version operation")}</button>}
      {(pending || invalid) && <button disabled={busy} onClick={() => void discard()}>{tr("Discard version recovery")}</button>}
      {evidence.version.state === "published" && <button disabled={busy || !!pending || invalid} onClick={() => void decide("archive")}>{tr("Archive source")}</button>}
      {["archived", "failed"].includes(evidence.version.state) && <button disabled={busy || !!pending || invalid || !!blockers} onClick={() => void decide("retire")}>{tr("Retire source")}</button>}
    </>}
  </section>;
}
