/* eslint-disable react-hooks/immutability -- Workspace buffers are imperative controller objects; mutations publish through the Studio provider. */
"use client";
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
        <Link href="/" className="wordmark" aria-label="Volume Studio">
          ◈ <span>Studio</span>
        </Link>
        <span className="divider">/</span>
        <Link href="/">Libraries</Link>
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
          <button aria-label="Upload tasks" onClick={() => setTasks(true)}>
            <UploadCloud size={17} />
            <span className="desktop-label">Tasks</span>
            {count > 0 && <span className="count">{count}</span>}
          </button>
          <a href="/admin" className="icon-button" aria-label="Open Aster">
            <ArrowUpRight size={18} />
          </a>
          <Menu
            label="Account"
            items={[
              {
                label: "Sign out everywhere in this session",
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
          {error}
          <button onClick={() => setError("")}>Dismiss</button>
        </div>
      )}
      {studio.model.recoveryError && (
        <div className="notice error" role="alert">
          {studio.model.recoveryError}
        </div>
      )}
      <RecoveryBanner />
      {studio.model.notice && (
        <div className="toast" role="status">
          {studio.model.notice}
          <button
            aria-label="Dismiss notification"
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
        title="Switch library"
        open={chooser}
        onClose={() => setChooser(false)}
      >
        <label className="search">
          <Search size={16} />
          <input
            autoFocus
            placeholder="Search libraries…"
            aria-label="Search libraries to switch"
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
          {page?.items.length === 0 && <p>No matching libraries.</p>}
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
              Load more
            </button>
          )}
        </div>
      </Modal>
      <Modal
        title="Upload tasks"
        open={tasks}
        onClose={() => setTasks(false)}
        drawer
      >
        <p className="muted">Tasks from this session and visited libraries.</p>
        {studio.model.tasks.length === 0 &&
          Object.values(studio.model.uploads)
            .flat()
            .every((u) => u.state !== "open") && <p>No uploads in progress.</p>}
        {studio.model.tasks.map((t) => (
          <div className="task-row" key={t.id}>
            <Link href={`/libraries/${t.library}`}>
              {studio.model.libraries[t.library]?.name || t.library}
            </Link>
            <strong>{t.path}</strong>
            <span>
              {t.state} · {Math.round(t.progress * 100)}%
            </span>
            <progress max={1} value={t.progress} />
            {t.error && <p className="error">{t.error}</p>}
            {t.controller ? (
              <button onClick={() => t.controller?.abort()}>Pause</button>
            ) : (
              t.file && (
                <button onClick={() => void studio.retryUpload(t)}>
                  Retry / resume
                </button>
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
        Select the same file at the same target path to resume.
      </p>
      <button
        onClick={async () => {
          if (
            (await studio.ask(
              "Cancel upload",
              `Cancel the retained upload for ${upload.path}?`,
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
        Cancel upload
      </button>
    </div>
  );
}
function RecoveryBanner() {
  const studio = useStudio();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const items = studio.model.recoveryItems;
  return (
    <>
      {items.length > 0 && (
        <div className="notice">
          <span>
            {items.length} local recovery{" "}
            {items.length === 1 ? "copy" : "copies"} available.
          </span>
          <button onClick={() => setOpen(true)}>Review recovery</button>
        </div>
      )}
      <Modal
        title="Recover unsaved edits"
        open={open && items.length > 0}
        onClose={() => setOpen(false)}
      >
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        {items.map((r) => (
          <div className="recovery-row" key={r.id}>
            <strong>{r.path}</strong>
            <small>{new Date(r.updated).toLocaleString()}</small>
            <div className="actions">
              <button
                onClick={() =>
                  void studio
                    .discardRecovery(r)
                    .catch((e) => setError(String(e)))
                }
              >
                Discard
              </button>
              <button
                className="primary"
                onClick={() =>
                  void studio.restore(r).catch((e) => setError(String(e)))
                }
              >
                Restore
              </button>
              <Link
                className="button"
                href={`/libraries/${r.library}?path=${encodeURIComponent(r.path)}`}
              >
                Open library
              </Link>
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
  const studio = useStudio();
  const [name, setName] = useState(library?.name || ""),
    [description, setDescription] = useState(library?.description || ""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Modal
      title={library ? "Library details" : "New library"}
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
          Name
          <input
            autoFocus
            required
            maxLength={200}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label>
          Description
          <textarea
            maxLength={4000}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={busy || !name.trim()}>
            {library ? "Save details" : "Create draft"}
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
) =>
  `${base}/api/v1/libraries/${library}/${version ? `versions/${version}` : "draft"}/content?${query({ path, revision: version ? undefined : revision })}`;
