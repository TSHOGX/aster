/* eslint-disable react-hooks/immutability -- Workspace buffers are imperative controller objects; mutations publish through the Studio provider. */
"use client";
import { formatQuantity } from "../../../web/i18n/locale";
import { intlLocale } from "../../../web/i18n/locale";
import { message, useT } from "../lib/i18n";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  ChevronDown,
  UploadCloud,
  Search,
  LogOut,
  ArrowUpRight,
} from "lucide-react";
import { api, base, bytes, Library, requestID, Upload } from "../lib/api";
import { Page, query } from "../lib/workbench";
import { Modal, Menu } from "./ui";
import { useStudio } from "./studio-provider";
export function ShellHeader({ library }: { library?: Library | null }) {
  const tr = useT();
  const studio = useStudio();
  const [chooser, setChooser] = useState(false),
    [tasks, setTasks] = useState(false),
    [q, setQ] = useState(""),
    [page, setPage] = useState<Page<Library> | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    if (!chooser) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Page<Library>>(
        `/libraries?${query({ mode: "workbench", q, status: "active" })}`,
        { signal: abort.signal },
      )
        .then(setPage)
        .catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
  }, [q, chooser]);
  const count =
    studio.model.tasks.filter((t) => t.state !== "Complete").length +
    Object.values(studio.model.uploads)
      .flat()
      .filter((u) => u.state === "open").length;
  return (
    <>
      <header className="topbar">
        <Link href="/" className="wordmark" aria-label={tr("Volume Studio")}>
          ◈ <span>{tr("Studio")}</span>
        </Link>
        <span className="divider">/</span>
        <Link href="/">{tr("Libraries")}</Link>
        {library && (
          <>
            <span className="divider">/</span>
            <button className="library-switch" onClick={() => setChooser(true)}>
              {library.name}
              <ChevronDown size={14} />
            </button>
          </>
        )}
        <div className="topbar-right">
          <button aria-label={tr("Upload tasks")} onClick={() => setTasks(true)}>
            <UploadCloud size={17} />
            <span className="desktop-label">{tr("Tasks")}</span>
            {count > 0 && <span className="count">{formatQuantity(count)}</span>}
          </button>
          <a href="/admin" className="icon-button" aria-label={tr("Open Aster")}>
            <ArrowUpRight size={18} />
          </a>
          <Menu
            label={tr("Account")}
            items={[
              {
                label: message("Sign out everywhere in this session"),
                action: () =>
                  void studio.logout().catch((e) => {
                    setError(String(e));
                  }),
              },
            ]}
          >
            <LogOut size={16} />
          </Menu>
        </div>
      </header>
      {error && (
        <div className="notice error" role="alert">
          {tr(error)}
          <button onClick={() => setError("")}>{tr("Dismiss")}</button>
        </div>
      )}
      {studio.model.recoveryError && (
        <div className="notice error" role="alert">
          {tr(studio.model.recoveryError)}
        </div>
      )}
      <RecoveryBanner />
      {studio.model.notice && (
        <div className="toast" role="status">
          {tr(studio.model.notice)}
          <button
            aria-label={tr("Dismiss notification")}
            onClick={() => {
              studio.model.notice = "";
              studio.notify();
            }}
          >
            ×
          </button>
        </div>
      )}
      <Modal
        title={tr("Switch library")}
        open={chooser}
        onClose={() => setChooser(false)}
      >
        <label className="search">
          <Search size={16} />
          <input
            autoFocus
            placeholder={tr("Search libraries…")}
            aria-label={tr("Search libraries to switch")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </label>
        <div className="picker-results">
          {page?.items.map((l) => (
            <Link
              key={l.id}
              href={`/libraries/${l.id}`}
              onClick={() => {
                void studio.persist();
                setChooser(false);
              }}
            >
              {l.name}
              <small>{l.description}</small>
            </Link>
          ))}
          {page?.items.length === 0 && <p>{tr("No matching libraries.")}</p>}
          {page?.next && (
            <button
              onClick={() =>
                void api<Page<Library>>(
                  `/libraries?${query({ mode: "workbench", q, status: "active", cursor: page.next })}`,
                )
                  .then((p) =>
                    setPage({ ...p, items: [...page.items, ...p.items] }),
                  )
                  .catch((e) => setError(String(e)))
              }
            >
               {tr("Load more")} </button>
          )}
        </div>
      </Modal>
      <Modal
        title={tr("Upload tasks")}
        open={tasks}
        onClose={() => setTasks(false)}
        drawer
      >
        <p className="muted">{tr("Tasks from this session and visited libraries.")}</p>
        {studio.model.tasks.length === 0 &&
          Object.values(studio.model.uploads)
            .flat()
            .every((u) => u.state !== "open") && <p>{tr("No uploads in progress.")}</p>}
        {studio.model.tasks.map((t) => (
          <div className="task-row" key={t.id}>
            <Link href={`/libraries/${t.library}`}>
              {studio.model.libraries[t.library]?.name || t.library}
            </Link>
            <strong>{t.path}</strong>
            <span>
              {tr(t.state)} · {Math.round(t.progress * 100)}%
            </span>
            <progress max={1} value={t.progress} />
            {t.error && <p className="error">{tr(t.error)}</p>}
            {t.controller ? (
              <button onClick={() => t.controller?.abort()}>{tr("Pause")}</button>
            ) : (
              t.file && (
                <button onClick={() => void studio.retryUpload(t)}>
                   {tr("Retry / resume")} </button>
              )
            )}
          </div>
        ))}
        {Object.entries(studio.model.uploads).map(([id, uploads]) =>
          uploads
            .filter((u) => u.state === "open")
            .map((u) => <ServerUpload key={u.id} library={id} upload={u} />),
        )}
      </Modal>
    </>
  );
}
function ServerUpload({
  library,
  upload,
}: {
  library: string;
  upload: Upload;
}) {
  const tr = useT();
  const studio = useStudio();
  return (
    <div className="task-row">
      <Link href={`/libraries/${library}`}>
        {studio.model.libraries[library]?.name || library}
      </Link>
      <strong>{upload.path}</strong>
      <span>
        {bytes(upload.offset)} / {bytes(upload.size)}
      </span>
      <p className="muted">
         {tr("Select the same file at the same target path to resume.")} </p>
      <button
        onClick={async () => {
          if (
            (await studio.ask(
              "Cancel upload",
              message("Cancel the retained upload for {path}?", { path: upload.path }),
              ["Keep upload", "Cancel upload"],
            )) !== "Cancel upload"
          )
            return;
          try {
            await studio.write(
              `/libraries/${library}/uploads/${upload.id}`,
              { request_id: requestID() },
              "DELETE",
            );
            studio.model.uploads[library] = studio.model.uploads[
              library
            ].filter((x) => x.id !== upload.id);
            studio.changed();
          } catch (e) {
            studio.model.notice = String(e);
            studio.notify();
          }
        }}
      >
         {tr("Cancel upload")} </button>
    </div>
  );
}
function RecoveryBanner() {
  const tr = useT();
  const studio = useStudio();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const items = studio.model.recoveryItems;
  return (
    <>
      {items.length > 0 && (
        <div className="notice">
          <span>
            {formatQuantity(items.length)}  {tr("local recovery")}{" "}
            {items.length === 1 ? tr("copy") : tr("copies")}  {tr("available.")} </span>
          <button onClick={() => setOpen(true)}>{tr("Review recovery")}</button>
        </div>
      )}
      <Modal
        title={tr("Recover unsaved edits")}
        open={open && items.length > 0}
        onClose={() => setOpen(false)}
      >
        {error && (
          <p role="alert" className="error">
            {tr(error)}
          </p>
        )}
        {items.map((r) => (
          <div className="recovery-row" key={r.id}>
            <strong>{r.path}</strong>
            <small>{new Date(r.updated).toLocaleString(intlLocale())}</small>
            <div className="actions">
              <button
                onClick={() =>
                  void studio
                    .discardRecovery(r)
                    .catch((e) => setError(String(e)))
                }
              >
                 {tr("Discard")} </button>
              <button
                className="primary"
                onClick={() =>
                  void studio.restore(r).catch((e) => setError(String(e)))
                }
              >
                 {tr("Restore")} </button>
              <Link
                className="button"
                href={`/libraries/${r.library}?path=${encodeURIComponent(r.path)}`}
              >
                 {tr("Open library")} </Link>
            </div>
          </div>
        ))}
      </Modal>
    </>
  );
}
export function LibraryDetails({
  library,
  onClose,
  onSaved,
}: {
  library?: Library;
  onClose: () => void;
  onSaved: (l: Library) => void;
}) {
  const tr = useT();
  const studio = useStudio();
  const [name, setName] = useState(library?.name || ""),
    [description, setDescription] = useState(library?.description || ""),
    [profile, setProfile] = useState<Library["profile"]>(library?.profile || "authored-v1"),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Modal
      title={library ? tr("Library details") : tr("New library")}
      open
      onClose={onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            const l = await studio.write<Library>(
              library ? `/libraries/${library.id}` : "/libraries",
              {
                request_id: requestID(),
                name,
                description,
                ...(!library ? { profile } : {}),
                ...(library
                  ? {
                      expected_revision: library.metadata_revision,
                      archived: library.archived,
                    }
                  : {}),
              },
              library ? "PATCH" : "POST",
            );
            onSaved(l);
            onClose();
          } catch (e) {
            setError(String(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
           {tr("Name")} <input
            autoFocus
            required
            maxLength={200}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        {!library && <div>
          <fieldset className="content-profile"><legend>{tr("Content profile")}</legend>
            <label><input type="radio" name="content-profile" value="authored-v1" checked={profile==="authored-v1"} onChange={()=>setProfile("authored-v1")} />{tr("Ordinary data")}</label>
            <label><input type="radio" name="content-profile" value="shared-assets-v1" checked={profile==="shared-assets-v1"} onChange={()=>setProfile("shared-assets-v1")} />{tr("Shared assets")}</label>
          </fieldset>
          <p className="muted">{tr("Shared assets become read-only Agent system context. The profile is fixed after creation.")}</p>
        </div>}
        {library && <p>{tr("Content profile: {profile} · Fixed", { profile: tr(library.profile === "shared-assets-v1" ? "Shared assets" : "Ordinary data") })}</p>}

        <label>
           {tr("Description")} <textarea
            maxLength={4000}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        {error && (
          <p role="alert" className="error">
            {tr(error)}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>
             {tr("Cancel")} </button>
          <button className="primary" disabled={busy || !name.trim()}>
            {library ? tr("Save details") : tr("Create draft")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
export const contentURL = (
  library: string,
  version: string,
  path: string,
  revision?: number,
  branch?: string,
) => {
  if (!version && !branch) throw new Error("A branch is required for draft downloads.");
  return `${base}/api/v1/libraries/${library}/${version ? `versions/${version}` : `branches/${encodeURIComponent(branch!)}/draft`}/content?${query({ path, revision: version ? undefined : revision })}`;
};
