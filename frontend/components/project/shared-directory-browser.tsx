"use client";
import { Select } from "@web/select";

import { useEffect, useRef, useState } from "react";
import {
  api,
  command,
  readPreview,
  writeFileBytes,
  type FilePage,
} from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import { useDialog } from "./ui";

type Mount = { key: string; name: string; access: "read_only" | "read_write" };
type Location = { mount: string; path: string; cursors: string[] };

// Keep a listing tied to its location: an old response must never become an
// actionable row under a newly selected directory or pagination cursor.
export function SharedDirectoryBrowser({
  project,
  onDirty,
}: {
  project: string;
  onDirty: (value: boolean) => void;
}) {
  const tr = useT(),
    ask = useDialog();
  const [mounts, setMounts] = useState<Mount[] | null>(null);
  const [location, setLocation] = useState<Location>({
    mount: "",
    path: "",
    cursors: [""],
  });
  const [listing, setListing] = useState<{
    key: string;
    page: FilePage;
  } | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [file, setFile] = useState<{
    path: string;
    original: string;
    text: string;
  } | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const inFlight = useRef(false);
  const { mount, path, cursors } = location;
  const cursor = cursors.at(-1) || "";
  const base = `/api/projects/${encodeURIComponent(project)}/files`;
  const key = JSON.stringify([project, mount, path, cursor, attempt]);
  const page = listing?.key === key ? listing.page : null;
  const writable =
    mounts?.find((item) => item.key === mount)?.access === "read_write";
  const dirty = !!file && file.text !== file.original;
  const locked = busy || !!file;
  const query = (name: string) =>
    new URLSearchParams({ mount, path: name }).toString();
  useEffect(() => {
    onDirty(dirty || busy || uncertain);
    return () => onDirty(false);
  }, [dirty, busy, uncertain, onDirty]);
  useEffect(() => {
    const controller = new AbortController();
    void api<{ mounts: Mount[] }>(
      `/api/projects/${encodeURIComponent(project)}/mounts`,
      { signal: controller.signal },
    )
      .then((result) => {
        if (!controller.signal.aborted) setMounts(result.mounts);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(String(error));
      });
    return () => controller.abort();
  }, [project, attempt]);
  useEffect(() => {
    if (!mount) return;
    const controller = new AbortController();
    const params = new URLSearchParams({
      mount,
      path,
      after: cursor,
      limit: "100",
    });
    void api<FilePage>(`${base}?${params}`, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          setListing({ key, page: result });
          setError("");
          setUncertain(false);
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(String(error));
      });
    return () => controller.abort();
  }, [base, mount, path, cursor, key]);

  const refresh = () => {
    setError("");
    setAttempt((value) => value + 1);
  };
  const navigate = (next: Location) => {
    setError("");
    setLocation(next);
  };
  const run = async (action: () => Promise<void>, mutation = false) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (error) {
      setError(String(error));
      if (mutation) setUncertain(true);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };
  const confirmed = (result: { state: string }) => {
    if (result.state !== "succeeded")
      throw new Error(
        tr("The operation is not confirmed. Refresh before trying again."),
      );
    setLocation((current) => ({ ...current, cursors: [""] }));
    refresh();
  };
  const openFile = (name: string) =>
    run(async () => {
      const blob = await readPreview(
        `${base}/content?${query(name)}`,
        new AbortController().signal,
        1 << 20,
      );
      let text: string;
      try {
        text = new TextDecoder("utf-8", {
          fatal: true,
          ignoreBOM: true,
        }).decode(await blob.arrayBuffer());
      } catch {
        throw new Error(tr("Download this file to view it."));
      }
      if (text.includes("\0"))
        throw new Error(tr("Download this file to view it."));
      setFile({ path: name, text, original: text });
    });
  const closeFile = async () => {
    if (
      dirty &&
      (await ask({
        title: tr("Discard unsaved file changes?"),
        description: file?.path,
        choices: [
          { label: tr("Discard changes"), value: "discard", danger: true },
        ],
      })) !== "discard"
    )
      return;
    setFile(null);
  };
  const mutate = (operation: "mkdir" | "move" | "remove", name: string) =>
    run(async () => {
      let destination = "";
      if (operation === "remove") {
        if (
          (await ask({
            title: tr("Delete this shared file?"),
            description: name,
            choices: [{ label: tr("Delete"), value: "delete", danger: true }],
          })) !== "delete"
        )
          return;
      } else {
        const value = await ask({
          title: operation === "mkdir" ? tr("New directory") : tr("Rename"),
          input: true,
          initial: operation === "move" ? name.split("/").at(-1) : "",
          choices: [{ label: tr("Save"), value: "save" }],
        });
        if (value === null) return;
        if (
          !value.trim() ||
          /[\/\\\x00-\x1f]/.test(value) ||
          value === "." ||
          value === ".."
        )
          throw new Error(
            tr("Enter a name without slashes or control characters."),
          );
        const target = path ? `${path}/${value}` : value;
        if (operation === "mkdir") name = target;
        else destination = target;
      }
      confirmed(
        await command<{ state: string }>(
          `${base}?${new URLSearchParams({ mount })}`,
          { id: crypto.randomUUID(), operation, path: name, destination },
        ),
      );
    }, true);
  const upload = (value: File) =>
    run(async () => {
      // A listing may be paginated, so always explain replacement before upload.
      if (
        (await ask({
          title: tr("Upload to shared directory?"),
          description: tr("A file with the same name will be replaced."),
          choices: [{ label: tr("Upload"), value: "upload" }],
        })) !== "upload"
      )
        return;
      const name = path ? `${path}/${value.name}` : value.name;
      confirmed(await writeFileBytes(`${base}/content?${query(name)}`, value));
    }, true);
  return (
    <section className="advanced-files" aria-label={tr("Shared directories")}>
      <h4>{tr("Shared directories")}</h4>
      {!mounts && !error && (
        <p role="status">{tr("Loading shared directories…")}</p>
      )}
      {mounts?.length === 0 && (
        <p>{tr("No shared directories are mounted in this project.")}</p>
      )}
      <label>
        {tr("Directory")}
        <Select aria-label={tr("Directory")}
          value={mount}
          disabled={locked || !mounts?.length}
          onChange={(event) =>
            navigate({ mount: event.target.value, path: "", cursors: [""] })
          }
        >
          <option value="">{tr("Choose a directory")}</option>
          {mounts?.map((item) => (
            <option key={item.key} value={item.key}>
              {item.name} ·{" "}
              {item.access === "read_write"
                ? tr("Read and write")
                : tr("Read only")}
            </option>
          ))}
        </Select>
      </label>
      {error && (
        <div className="settings-error" role="alert">
          <p>{error}</p>
          <button disabled={busy || dirty} onClick={refresh}>
            {tr("Refresh")}
          </button>
        </div>
      )}
      {uncertain && (
        <p role="status">
          {tr("The operation is not confirmed. Refresh before trying again.")}
        </p>
      )}
      {mount && (
        <>
          <div className="settings-actions">
            <button
              disabled={locked || !path}
              onClick={() =>
                navigate({
                  mount,
                  path: path.split("/").slice(0, -1).join("/"),
                  cursors: [""],
                })
              }
            >
              {tr("Parent directory")}
            </button>
            <button disabled={locked} onClick={refresh}>
              {tr("Refresh")}
            </button>
          </div>
          <p className="advanced-path">{path || "/"}</p>
          {writable && !file && (
            <div className="settings-actions">
              <button
                disabled={busy || !page || uncertain}
                onClick={() => void mutate("mkdir", "")}
              >
                {tr("New directory")}
              </button>
              <label className="advanced-upload">
                {tr("Upload")}
                <input
                  type="file"
                  disabled={busy || !page || uncertain}
                  onChange={(event) => {
                    const value = event.target.files?.[0];
                    event.target.value = "";
                    if (value) void upload(value);
                  }}
                />
              </label>
            </div>
          )}
          {file ? (
            <>
              <label>
                {file.path}
                <textarea
                  rows={12}
                  readOnly={!writable || busy || uncertain}
                  value={file.text}
                  onChange={(event) =>
                    setFile({ ...file, text: event.target.value })
                  }
                />
              </label>
              <div className="settings-actions">
                {writable && (
                  <button
                    className="primary"
                    disabled={busy || !dirty || uncertain}
                    onClick={() =>
                      void run(async () => {
                        confirmed(
                          await writeFileBytes(
                            `${base}/editor-content?${query(file.path)}`,
                            new Blob([file.text], { type: "text/plain" }),
                          ),
                        );
                        setFile(null);
                      }, true)
                    }
                  >
                    {busy ? tr("Saving…") : tr("Save")}
                  </button>
                )}
                <button disabled={busy} onClick={() => void closeFile()}>
                  {tr("Close")}
                </button>
              </div>
            </>
          ) : !page ? (
            !error && <p role="status">{tr("Loading files…")}</p>
          ) : (
            <>
              {page.entries.length === 0 && (
                <p>{tr("This directory is empty.")}</p>
              )}
              <ul>
                {page.entries.map((entry) => {
                  const name = path ? `${path}/${entry.name}` : entry.name;
                  return (
                    <li key={entry.name}>
                      <button
                        className="advanced-file-name"
                        disabled={busy}
                        onClick={() =>
                          entry.kind === "directory"
                            ? navigate({ mount, path: name, cursors: [""] })
                            : void openFile(name)
                        }
                      >
                        {entry.name}
                        {entry.kind === "directory" ? "/" : ""}
                      </button>
                      <div className="settings-actions">
                        {entry.kind === "file" && (
                          <a href={`${base}/download?${query(name)}`} download>
                            {tr("Download")}
                          </a>
                        )}
                        {writable && (
                          <>
                            <button
                              disabled={busy || uncertain}
                              onClick={() => void mutate("move", name)}
                            >
                              {tr("Rename")}
                            </button>
                            <button
                              className="danger"
                              disabled={busy || uncertain}
                              onClick={() => void mutate("remove", name)}
                            >
                              {tr("Delete")}
                            </button>
                          </>
                        )}
                      </div>
                    </li>
                  );
                })}
              </ul>
              {(cursors.length > 1 || page.next) && (
                <nav
                  className="settings-actions"
                  aria-label={tr("Shared directories")}
                >
                  <button
                    disabled={busy || cursors.length <= 1}
                    onClick={() =>
                      navigate({ ...location, cursors: cursors.slice(0, -1) })
                    }
                  >
                    {tr("Previous")}
                  </button>
                  <button
                    disabled={busy || !page.next}
                    onClick={() =>
                      navigate({
                        ...location,
                        cursors: [...cursors, page.next!],
                      })
                    }
                  >
                    {tr("Next")}
                  </button>
                </nav>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}
