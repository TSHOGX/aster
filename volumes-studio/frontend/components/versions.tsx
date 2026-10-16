"use client";
import { intlLocale } from "../../../web/i18n/locale";
import { message, useT } from "../lib/i18n";

import { useEffect, useRef, useState } from "react";
import type { Branch } from "../lib/branches";
import { api, Library, requestID, Version } from "../lib/api";
import { Page, query, relativeTime } from "../lib/workbench";
import { Menu, Modal, Picker } from "./ui";
import { useStudio } from "./studio-provider";
import { VersionRetention } from "./version-retention";
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
  const tr = useT();
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
        aria-label={tr("Select version")}
      >
        <span>{tr(label)}</span>
        <span>{value ? tr("Read only") : tr("Editable")} ⌄</span>
      </button>
      <Modal title={tr("Select version")} open={open} onClose={() => setOpen(false)}>
        <label>
           {tr("Find version")} <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={tr("Search version labels…")}
          />
        </label>
        <div className="picker-results">
          <button
            onClick={() => {
              onChange("");
              setOpen(false);
            }}
          >
             {tr("Draft")} <small>{tr("Editable working copy")}</small>
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
              <small>{tr(v.state)}  {tr("· read only")}</small>
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
               {tr("Load more versions")} </button>
          )}
        </div>
        {error && (
          <p role="alert" className="error">
            {tr(error)}
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
  const tr = useT();
  const studio = useStudio();
  const [q, setQ] = useState(""),
    [status, setStatus] = useState("all"),
    [page, setPage] = useState<Page<Version> | null>(null),
    [selected, setSelected] = useState<string | null>(null),
    [error, setError] = useState("");
  const loadedCount = useRef(50);
  useEffect(() => {
    loadedCount.current = Math.max(50, page?.items.length || 0);
  }, [page]);
  const params = query({ mode: "workbench", q, status });
  const root = `/libraries/${library.id}`;
  function selectDetails(id: string | null) {
    setSelected(id);
    const url = new URL(location.href);
    if (id) url.searchParams.set("version_details", id); else url.searchParams.delete("version_details");
    history.replaceState(null, "", url);
  }
  useEffect(() => {
    const restore = () => setSelected(new URL(location.href).searchParams.get("version_details"));
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, [root]);
  useEffect(() => {
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void api<Page<Version>>(`${root}/versions?${params}`, {
        signal: abort.signal,
      })
        .then(value => { if (!abort.signal.aborted) { setPage(value); setError(""); } })
        .catch((e) => {
          if (!abort.signal.aborted) { setPage(null); setSelected(null); setError(String(e)); }
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
  }, [root, params, studio.model.epoch]);
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
        if (!abort.signal.aborted) { setPage({ ...p, items }); setError(""); }
      } catch (e) {
        if (!abort.signal.aborted) { setPage(null); setSelected(null); setError(String(e)); }
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
      const titles: Record<string, string> = { branch: "Create branch from version", retry: "Retry version", cancel: "Cancel version" };
      const prompts: Record<string, string> = { retry: "Retry version {label}?", cancel: "Cancel version {label}?" };
      let prompt = message(prompts[action] || titles[action], { label: v.label });
      if (action === "branch") {
        prompt = message("Create a working branch from version {label}? Current drafts remain unchanged.", { label: v.label });
      }
      if (
        (await studio.ask(
          titles[action],
          prompt,
          ["Cancel", "Continue"],
        )) !== "Continue"
      )
        return;
      if (action === "branch") {
        if (!v.accepted_commit) throw new Error("This version has no accepted commit available for branching.");
        const request = requestID();
        const branch = await studio.write<Branch>(`${root}/branches`, {
          request_id: request,
          name: `${v.label.slice(0, 80)}-${request.slice(0, 8)}`,
          base_commit: v.accepted_commit,
        });
        studio.selectBranch(branch);
        const url = new URL(location.href); url.searchParams.set("branch", branch.id); url.searchParams.delete("version"); history.replaceState(null, "", url);
        onInspect("");
      } else
        await studio.write(`/operations/${v.id}/${action}`, {
          request_id: requestID(),
        });
      studio.changed();
      onChanged();
      selectDetails(null);
    } catch (e) {
      setError(String(e));
    }
  }
  const actions = (v: Version) => [
    { label: message("Version details"), action: () => selectDetails(v.id) },
    ...(["published", "archived"].includes(v.state)
      ? [{ label: message("Create branch from version"), action: () => void action(v, "branch") }]
      : []),
    ...(v.state === "published"
      ? [
          {
            label: message("Archive source"),
            action: () => selectDetails(v.id),
            danger: true,
          },
        ]
      : []),
    ...(["archived", "failed"].includes(v.state)
      ? [
          {
            label: message("Retire source"),
            action: () => selectDetails(v.id),
            danger: true,
          },
        ]
      : []),
    ...(v.state === "failed" || v.error === "principal_authority_required"
      ? [{ label: message("Retry validation"), action: () => void action(v, "retry") }]
      : []),
    ...(["pending", "validating"].includes(v.state)
      ? [
          {
            label: message("Cancel publication"),
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
           {tr("Versions")} <span className="count">{page?.total ?? "…"}</span>
        </h2>
        <div className="actions">
          <input
            aria-label={tr("Search versions")}
            placeholder={tr("Search version labels…")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <Picker
            label={tr("Version status")}
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
          {tr(error)}
        </p>
      )}
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{tr("Version")}</th>
              <th>{tr("Status")}</th>
              <th>{tr("Published")}</th>
              <th>{tr("Publisher")}</th>
              <th>{tr("Release note")}</th>
              <th>{tr("Actions")}</th>
            </tr>
          </thead>
          <tbody>
            {page?.items.map((v) => (
              <tr key={v.id}>
                <td>
                  <button
                    className="text-action"
                    onClick={() => selectDetails(v.id)}
                  >
                    {v.label}
                  </button>
                  <small>{tr("Created")} {relativeTime(v.created_at, intlLocale())}</small>
                </td>
                <td>
                  <span className={`badge ${v.state}`}>{tr(v.state)}</span>
                  {v.error && (
                    <small className="error">
                      {tr(v.error.replaceAll("_", " "))}
                    </small>
                  )}
                </td>
                <td>{relativeTime(v.published_at, intlLocale())}</td>
                <td>{v.publisher.subject}</td>
                <td className="version-note">{v.note || "—"}</td>
                <td>
                  <div className="actions">
                    {["published", "archived"].includes(v.state) && (
                      <button onClick={() => onInspect(v.id)}>
                         {tr("Inspect files")} </button>
                    )}
                    {v.state === "published" && (
                      <a
                        className="button"
                        href={`/admin/resources/volumes?${query({ volume_publisher: v.publisher_id, volume_library: library.id, volume_version: v.id })}`}
                      >
                         {tr("Use in Aster ↗")} </a>
                    )}
                    <Menu
                      label={tr("Actions for version {value0}", { value0: v.label })}
                      items={actions(v)}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {page?.items.length === 0 && (
          <p className="empty-state">{tr("No versions found.")}</p>
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
             {tr("Load more versions")} </button>
        )}
      </div>
      <Modal title={tr("Version details")} open={!!selected} onClose={() => selectDetails(null)} drawer>
        {selected && <VersionRetention key={selected} library={library.id} id={selected} onChanged={onChanged} />}
      </Modal>
    </section>
  );
}
