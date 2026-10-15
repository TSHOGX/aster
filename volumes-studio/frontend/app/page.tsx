/* eslint-disable react-hooks/set-state-in-effect -- Hydrate browser navigation and synchronize external workspace snapshots. */
"use client";
import { intlLocale } from "../../../web/i18n/locale";
import { message, useT } from "../lib/i18n";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import {
  Search,
  Plus,
  RefreshCw,
  ArrowDown,
  ArrowUp,
  ChevronsUpDown,
} from "lucide-react";
import { api, Library, requestID } from "../lib/api";
import { storageRead, storageWrite } from "../lib/workbench";
import { Page, query, relativeTime } from "../lib/workbench";
import { LibraryDetails, ShellHeader } from "../components/shell";
import { Menu, Picker } from "../components/ui";
import { useStudio } from "../components/studio-provider";
export default function Home() {
  const tr = useT();
  const studio = useStudio();
  const router = useRouter();
  const [q, setQ] = useState(""),
    [status, setStatus] = useState("active"),
 [profile,setProfile] = useState(""),
    [changes, setChanges] = useState(""),
    [sort, setSort] = useState("updated"),
    [direction, setDirection] = useState("desc"),
    [ready, setReady] = useState(false),
    [page, setPage] = useState<Page<Library> | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [editing, setEditing] = useState<Library | null | undefined>(),
    [refresh, setRefresh] = useState(0);
  const scroll = useRef<HTMLDivElement>(null);
  const navigating = useRef(false);
  useEffect(() => {
    const p = new URLSearchParams(location.search);
    setQ(p.get("q") || "");
    setStatus(p.get("status") || "active");
 setProfile(p.get("profile") || "");
    setChanges(p.get("changes") || "");
    setSort(p.get("sort") || "updated");
    setDirection(p.get("direction") || "desc");
    setReady(true);
  }, []);
  const params = query({
    mode: "workbench",
    q,
    status,
    profile,
    changes,
    sort,
    direction,
    limit: 50,
  });
  useEffect(() => {
    if (!ready) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      setLoading(true);
      if (!navigating.current)
        history.replaceState(
          null,
          "",
          `?${query({ q, profile, status, changes, sort, direction })}`,
        );
      void api<Page<Library>>(`/libraries?${params}`, { signal: abort.signal })
        .then((p) => {
          setPage(p);
          setError("");
          requestAnimationFrame(() => {
            if (scroll.current)
              scroll.current.scrollTop = Number(
                storageRead("session", `studio-list-scroll:${params}`) || 0,
              );
          });
        })
        .catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        })
        .finally(() => {
          if (!abort.signal.aborted) setLoading(false);
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
  }, [ready, params, refresh, q, profile, status, changes, sort, direction]);
  const setOrder = (value: string) => {
    if (sort === value) setDirection(direction === "asc" ? "desc" : "asc");
    else {
      setSort(value);
      setDirection(value === "updated" ? "desc" : "asc");
    }
  };
  const archive = async (l: Library) => {
    if (
      (await studio.ask(
        l.archived ? "Restore library" : "Archive library",
        l.archived
          ? "Make this library active again?"
          : "Archive this library? Existing imported copies are unaffected.",
        ["Cancel", l.archived ? "Restore" : "Archive"],
      )) === "Cancel"
    )
      return;
    try {
      await studio.write(
        `/libraries/${l.id}`,
        {
          request_id: requestID(),
          expected_revision: l.metadata_revision,
          name: l.name,
          description: l.description,
          archived: !l.archived,
        },
        "PATCH",
      );
      setRefresh((n) => n + 1);
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <main className="studio-shell">
      <ShellHeader />
      <div className="catalog-heading">
        <h1>
           {tr("Libraries")} <span className="count">{page?.total ?? "…"}</span>
        </h1>
        <button className="primary" disabled={!studio.session?.can_create_library} onClick={() => setEditing(null)}>
          <Plus size={16} />
           {tr("New library")} </button>
      </div>
      <div className="catalog-toolbar">
        <label className="search">
          <Search size={16} />
          <input
            placeholder={tr("Search libraries…")}
            aria-label={tr("Search libraries")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </label>
        <Picker label="Content profile" value={profile} onChange={setProfile} options={[{value:"",label:"All profiles"},{value:"authored-v1",label:"Ordinary data"},{value:"shared-assets-v1",label:"Shared assets"}]} />
 <Picker
          label={tr("Library status")}
          value={status}
          onChange={setStatus}
          options={[
            { value: "active", label: "Active" },
            { value: "archived", label: "Archived" },
            { value: "all", label: "All libraries" },
          ]}
        />
        <Picker
          label={tr("Draft changes filter")}
          value={changes}
          onChange={setChanges}
          options={[
            { value: "", label: "All drafts" },
            { value: "yes", label: "With changes" },
            { value: "no", label: "Without changes" },
          ]}
        />
        <button
          aria-label={tr("Refresh libraries")}
          onClick={() => setRefresh((n) => n + 1)}
        >
          <RefreshCw size={16} />
        </button>
      </div>
      {error && (
        <p className="notice error" role="alert">
          {tr(error)}
        </p>
      )}
      <div
        ref={scroll}
        className="catalog-scroll"
        onScroll={(e) =>
          storageWrite(
            "session",
            `studio-list-scroll:${params}`,
            String(e.currentTarget.scrollTop),
          )
        }
        aria-busy={loading}
      >
        <table>
          <thead>
            <tr>
              {[
                ["name", "Name"],
                ["status", "Status"],
              ].map(([key, label]) => (
                <th
                  key={key}
                  aria-sort={
                    sort === key ? direction === "asc" ? "ascending" : "descending" : "none"
                  }
                >
                  <button onClick={() => setOrder(key)}>
                    {tr(label)}
                    {sort === key ? (
                      direction === "asc" ? (
                        <ArrowUp size={13} />
                      ) : (
                        <ArrowDown size={13} />
                      )
                    ) : (
                      <ChevronsUpDown size={13} />
                    )}
                  </button>
                </th>
              ))}
              <th>{tr("Latest published version")}</th>
              <th>{tr("Draft changes")}</th>
              <th
                aria-sort={
                  sort === "updated" ? direction === "asc" ? "ascending" : "descending" : "none"
                }
              >
                <button onClick={() => setOrder("updated")}>
                   {tr("Updated")} {sort === "updated" ? (
                    direction === "asc" ? (
                      <ArrowUp size={13} />
                    ) : (
                      <ArrowDown size={13} />
                    )
                  ) : (
                    <ChevronsUpDown size={13} />
                  )}
                </button>
              </th>
              <th>
                <span className="visually-hidden">{tr("Actions")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {page?.items.map((l) => (
              <tr key={l.id}>
                <td>
                  <Link className="library-name" href={`/libraries/${l.id}`}>
                    {l.name}
                  </Link>
                  <p className="description">{l.description}</p>
                  {l.builtin && <small>{tr("Built-in origin · Publish a new version to extend")}</small>}
                  <small className="catalog-status">{l.profile === "shared-assets-v1" ? tr("Shared assets") : tr("Ordinary data")}</small>
                </td>
                <td>
                  <span className="badge">
                    {l.archived ? tr("Archived") : tr("Active")}
                  </span>
                </td>
                <td>
                  {l.latest_published_version ? (
                    <Link
                      href={`/libraries/${l.id}?version=${l.latest_published_version.id}`}
                    >
                      {l.latest_published_version.label}
                    </Link>
                  ) : (
                    <span className="muted">{tr("No published version")}</span>
                  )}
                </td>
                <td>
                  {l.draft_change_count ? tr("{value0} changed", { value0: l.draft_change_count }) : tr("No changes")}
                </td>
                <td>
                  <time
                    title={
                      l.updated_at ? new Date(l.updated_at * 1000).toLocaleString(intlLocale()) : undefined
                    }
                  >
                    {relativeTime(l.updated_at, intlLocale())}
                  </time>
                </td>
                <td>
                  <Menu
                    label={tr("Actions for {value0}", { value0: l.name })}
                    items={[
                      {
                        label: message("Edit library details"),
                        action: () => setEditing(l),
                      },
                      {
                        label: l.archived ? message("Restore library") : message("Archive library"),
                        action: () => void archive(l),
                        danger: !l.archived,
                      },
                    ]}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!page && loading && <p className="empty-state">{tr("Loading libraries…")}</p>}
        {page?.items.length === 0 && (
          <div className="empty-state">
            <h2>
              {q || changes || status !== "active" ? tr("No matching libraries") : tr("No libraries yet")}
            </h2>
            <p>
              {q || changes ? tr("Adjust your search or filters.") : tr("Create a library to begin.")}
            </p>
          </div>
        )}
        {page?.next && (
          <button
            className="load-more"
            disabled={loading}
            onClick={async () => {
              setLoading(true);
              try {
                const p = await api<Page<Library>>(
                  `/libraries?${params}&cursor=${encodeURIComponent(page.next)}`,
                );
                setPage({ ...p, items: [...page.items, ...p.items] });
              } catch (e) {
                setError(String(e));
              } finally {
                setLoading(false);
              }
            }}
          >
             {tr("Load more libraries")} </button>
        )}
      </div>
      {editing !== undefined && (
        <LibraryDetails
          library={editing || undefined}
          onClose={() => setEditing(undefined)}
          onSaved={(l) => {
            if (!editing) {
              navigating.current = true;
              router.push(`/libraries/${l.id}`);
            } else setRefresh((n) => n + 1);
          }}
        />
      )}
    </main>
  );
}
