"use client";
import { useEffect, useRef, useState } from "react";
import { api, Library, requestID, Version } from "../lib/api";
import { Page, query, relativeTime } from "../lib/workbench";
import { Menu, Modal, Picker } from "./ui";
import { useStudio } from "./studio-provider";
export function VersionPicker({
  library,
  value,
  label,
  onChange,
}: {
  library: string;
  value: string;
  label: string;
  onChange: (id: string) => void;
}) {
  const [open, setOpen] = useState(false),
    [q, setQ] = useState(""),
    [page, setPage] = useState<Page<Version> | null>(null),
    [error, setError] = useState("");
  const params = query({ mode: "workbench", q, status: "readable" });
  useEffect(() => {
    if (!open) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Page<Version>>(`/libraries/${library}/versions?${params}`, {
        signal: abort.signal,
      })
        .then(setPage)
        .catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
  }, [library, params, open]);
  return (
    <>
      <button
        className="version-picker"
        onClick={() => setOpen(true)}
        aria-label="Select version"
      >
        <span>{label}</span>
        <span>{value ? "Read only" : "Editable"} ⌄</span>
      </button>
      <Modal title="Select version" open={open} onClose={() => setOpen(false)}>
        <label>
          Find version
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search version labels…"
          />
        </label>
        <div className="picker-results">
          <button
            onClick={() => {
              onChange("");
              setOpen(false);
            }}
          >
            Draft <small>Editable working copy</small>
          </button>
          {page?.items.map((v) => (
            <button
              key={v.id}
              onClick={() => {
                onChange(v.id);
                setOpen(false);
              }}
            >
              {v.label}
              <small>{v.state} · read only</small>
            </button>
          ))}
          {page?.next && (
            <button
              onClick={() =>
                void api<Page<Version>>(
                  `/libraries/${library}/versions?${params}&cursor=${encodeURIComponent(page.next)}`,
                )
                  .then((p) =>
                    setPage({ ...p, items: [...page.items, ...p.items] }),
                  )
                  .catch((e) => setError(String(e)))
              }
            >
              Load more versions
            </button>
          )}
        </div>
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
      </Modal>
    </>
  );
}
export function Versions({
  library,
  onInspect,
  onChanged,
}: {
  library: Library;
  onInspect: (id: string) => void;
  onChanged: () => void;
}) {
  const studio = useStudio();
  const [q, setQ] = useState(""),
    [status, setStatus] = useState("all"),
    [page, setPage] = useState<Page<Version> | null>(null),
    [selected, setSelected] = useState<Version | null>(null),
    [error, setError] = useState("");
  const loadedCount = useRef(50);
  useEffect(() => {
    loadedCount.current = Math.max(50, page?.items.length || 0);
  }, [page]);
  const params = query({ mode: "workbench", q, status });
  const root = `/libraries/${library.id}`;
  useEffect(() => {
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Page<Version>>(`${root}/versions?${params}`, {
        signal: abort.signal,
      })
        .then(setPage)
        .catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
  }, [root, params, studio.model.epoch, library.base_version]);
  useEffect(() => {
    const abort = new AbortController();
    let busy = false;
    const update = async () => {
      if (document.hidden || busy) return;
      busy = true;
      try {
        let p = await api<Page<Version>>(`${root}/versions?${params}`, {
          signal: abort.signal,
        });
        const items = [...p.items];
        while (p.next && items.length < loadedCount.current) {
          p = await api<Page<Version>>(
            `${root}/versions?${params}&cursor=${encodeURIComponent(p.next)}`,
            { signal: abort.signal },
          );
          items.push(...p.items);
        }
        if (!abort.signal.aborted) setPage({ ...p, items });
      } catch (e) {
        if (!abort.signal.aborted) setError(String(e));
      } finally {
        busy = false;
      }
    };
    const timer = setInterval(() => void update(), 2000);
    const visible = () => void update();
    document.addEventListener("visibilitychange", visible);
    return () => {
      clearInterval(timer);
      abort.abort();
      document.removeEventListener("visibilitychange", visible);
    };
  }, [root, params]);
  async function action(v: Version, action: string) {
    try {
      let message = `${action} version ${v.label}?`;
      if (action === "restore") {
        if (!(await studio.settle(library.id))) return;
        message +=
          " This replaces the saved draft. Existing imported copies are unaffected.";
      }
      if (action === "retire") {
        const retention = await api<Record<string, unknown>>(
          `${root}/versions/${v.id}/retention`,
        );
        message +=
          " Source files may be collected. Retention: " +
          JSON.stringify(retention);
      }
      if (
        (await studio.ask(
          `${action[0].toUpperCase() + action.slice(1)} version`,
          message,
          ["Cancel", "Continue"],
        )) !== "Continue"
      )
        return;
      if (action === "restore") {
        await studio.mutate(library.id, {
          action: "restore",
          version_id: v.id,
        });
        for (const b of [...studio.model.buffers])
          if (b.library === library.id && !b.version) await studio.close(b);
        onInspect("");
      } else if (action === "archive" || action === "retire")
        await studio.write(`${root}/versions/${v.id}/lifecycle`, {
          request_id: requestID(),
          action,
        });
      else
        await studio.write(`/operations/${v.id}/${action}`, {
          request_id: requestID(),
        });
      studio.changed();
      onChanged();
      setSelected(null);
    } catch (e) {
      setError(String(e));
    }
  }
  const actions = (v: Version) => [
    { label: "Version details", action: () => setSelected(v) },
    ...(["published", "archived"].includes(v.state)
      ? [{ label: "Restore to draft", action: () => void action(v, "restore") }]
      : []),
    ...(v.state === "published"
      ? [
          {
            label: "Archive source",
            action: () => void action(v, "archive"),
            danger: true,
          },
        ]
      : []),
    ...(["archived", "failed"].includes(v.state)
      ? [
          {
            label: "Retire source",
            action: () => void action(v, "retire"),
            danger: true,
          },
        ]
      : []),
    ...(v.state === "failed" || v.error === "principal_authority_required"
      ? [{ label: "Retry validation", action: () => void action(v, "retry") }]
      : []),
    ...(["pending", "validating"].includes(v.state)
      ? [
          {
            label: "Cancel publication",
            action: () => void action(v, "cancel"),
            danger: true,
          },
        ]
      : []),
  ];
  return (
    <section className="versions-view">
      <div className="view-heading">
        <h2>
          Versions <span className="count">{page?.total ?? "…"}</span>
        </h2>
        <div className="actions">
          <input
            aria-label="Search versions"
            placeholder="Search version labels…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <Picker
            label="Version status"
            value={status}
            onChange={setStatus}
            options={[
              "all",
              "published",
              "pending",
              "validating",
              "archived",
              "failed",
              "retired",
            ].map((value) => ({
              value,
              label: value === "all" ? "All states" : value,
            }))}
          />
        </div>
      </div>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Version</th>
              <th>Status</th>
              <th>Published</th>
              <th>Publisher</th>
              <th>Release note</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {page?.items.map((v) => (
              <tr key={v.id}>
                <td>
                  <button
                    className="text-action"
                    onClick={() => setSelected(v)}
                  >
                    {v.label}
                  </button>
                  <small>Created {relativeTime(v.created_at)}</small>
                </td>
                <td>
                  <span className={`badge ${v.state}`}>{v.state}</span>
                  {v.error && (
                    <small className="error">
                      {v.error.replaceAll("_", " ")}
                    </small>
                  )}
                </td>
                <td>{relativeTime(v.published_at)}</td>
                <td>{v.publisher.subject}</td>
                <td className="version-note">{v.note || "—"}</td>
                <td>
                  <div className="actions">
                    {["published", "archived"].includes(v.state) && (
                      <button onClick={() => onInspect(v.id)}>
                        Inspect files
                      </button>
                    )}
                    {v.state === "published" && (
                      <a
                        className="button"
                        href={`/admin/resources/volumes?${query({ volume_publisher: v.publisher_id, volume_library: library.id, volume_version: v.id })}`}
                      >
                        Use in Aster ↗
                      </a>
                    )}
                    <Menu
                      label={`Actions for version ${v.label}`}
                      items={actions(v)}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {page?.items.length === 0 && (
          <p className="empty-state">No versions found.</p>
        )}
        {page?.next && (
          <button
            className="load-more"
            onClick={() =>
              void api<Page<Version>>(
                `${root}/versions?${params}&cursor=${encodeURIComponent(page.next)}`,
              )
                .then((p) =>
                  setPage({ ...p, items: [...page.items, ...p.items] }),
                )
                .catch((e) => setError(String(e)))
            }
          >
            Load more versions
          </button>
        )}
      </div>
      <Modal
        title={selected?.label || "Version details"}
        open={!!selected}
        onClose={() => setSelected(null)}
        drawer
      >
        {selected && (
          <>
            <span className={`badge ${selected.state}`}>{selected.state}</span>
            <p className="preserve-lines">
              {selected.note || "No release note."}
            </p>
            <dl>
              <dt>Source revision</dt>
              <dd>{selected.source_revision}</dd>
              <dt>Publisher</dt>
              <dd>{selected.publisher.subject}</dd>
              <dt>Created</dt>
              <dd>
                {selected.created_at
                  ? new Date(selected.created_at * 1000).toLocaleString()
                  : "Unknown"}
              </dd>
              <dt>Published</dt>
              <dd>
                {selected.published_at
                  ? new Date(selected.published_at * 1000).toLocaleString()
                  : "Unknown / not published"}
              </dd>
              <dt>Descriptor</dt>
              <dd>
                <code>{selected.descriptor_digest}</code>
              </dd>
            </dl>
            {selected.error && <p className="error">{selected.error}</p>}
          </>
        )}
      </Modal>
    </section>
  );
}
