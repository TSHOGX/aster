/* eslint-disable react-hooks/immutability -- Imperative editor controller publishes every mutation through notify; React state only owns its stable lifetime. */
"use client";
import { message, useT } from "../lib/i18n";
import { branchAPI, branchRoot, type Branch } from "../lib/branches";

import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  ReactNode,
} from "react";
import type { EditorState } from "@codemirror/state";
import {
  api,
  APIError,
  base,
  Entry,
  Library,
  requestID,
  Session,
  Upload,
} from "../lib/api";
import {
  decodeText,
  encodeText,
  entryIdentity,
  parentPath,
  query,
  TextFormat,
  WriteQueue,
} from "../lib/workbench";
import {
  clearRecoveries,
  deleteRecovery,
  putRecovery,
  recoveries,
  Recovery,
} from "../lib/recovery";
import { hashChunk, hashFile } from "../lib/hash-file";
import { clearAppearanceBinding } from "../../../web/appearance";
import { useAccountPreferences } from "../../../web/account-preferences";
import type { Message } from "../../../web/i18n/locale";
import { Modal } from "./ui";
import { useSession, SessionGate } from "./session";
export const workspaceKey = (library: string, branch: string, version = "") => JSON.stringify([library, branch, version]);
export type BufferFile = {
  branch: string;
  key: string;
  library: string;
  version: string;
  entry: Entry;
  revision: number;
  text: string;
  saved: string;
  format: TextFormat;
  editable: boolean;
  status: string;
  error: string;
  image?: string;
  editorState?: EditorState;
  scroll?: number;
  reset?: number;
  isolateHistory?: () => void;
  recoveryID: string;
  pending?: { body: string; text: string };
  conflict?: {
    entry: Entry | null;
    revision: number;
    text: string;
    format?: TextFormat;
    readable: boolean;
  };
};
export type Task = {
  branch: string;
  id: string;
  library: string;
  path: string;
  state: string;
  progress: number;
  file?: File;
  error?: string;
  controller?: AbortController;
};
type Question = {
  title: Message;
  message: Message;
  choices: { value: string; label: string }[];
  resolve: (value: string) => void;
};
type Model = {
  buffers: BufferFile[];
  active: Record<string, string>;
  tasks: Task[];
  uploads: Record<string, Upload[]>;
  libraries: Record<string, Library>;
  branches: Record<string, Branch>;
  questions: Question[];
  notice: string;
  recoveryError: string;
  recoveryItems: Recovery[];
  expired: boolean;
  epoch: number;
  selectionGeneration: Record<string, number>;
};
function makeModel(): Model {
  return {
    buffers: [],
    active: {},
    tasks: [],
    uploads: {},
    libraries: {},
    branches: {},
    questions: [],
    notice: "",
    recoveryError: "",
    recoveryItems: [],
    expired: false,
    epoch: 0,
    selectionGeneration: {},
  };
}
function useStudioController(session: Session | null) {
  const [model] = useState(() => ({ current: makeModel() }));
  const [, redraw] = useState(0);
  const [queue] = useState(() => ({ current: new WriteQueue() }));
  const sessionID = useRef("");
  const account = session ? `${session.issuer}\n${session.subject}` : "";
  const [auth] = useState(() => ({ current: session }));
  auth.current = session;
  const notify = () => redraw((n) => n + 1);
  const changed = () => {
    model.current.epoch++;
    notify();
  };
  const ask = (
    title: Message,
    message: Message,
    choices = ["Cancel", "Continue"],
  ) =>
    new Promise<string>((resolve) => {
      model.current.questions.push({ title, message, choices: choices.map(value => ({ value, label: value })), resolve });
      notify();
    });
  const write = async <T,>(
    path: string,
    body: Record<string, unknown>,
    method = "POST",
  ): Promise<T> => {
    if (!auth.current || model.current.expired)
      throw new Error("Sign in again to continue.");
    const exact = JSON.stringify(body);
    try {
      return await api<T>(path, { method, body: exact }, auth.current.csrf);
    } catch (e) {
      if (e instanceof APIError && e.status < 500) throw e;
      return api<T>(path, { method, body: exact }, auth.current.csrf);
    }
  };
  const selectedBranch = (library: string) => {
    const branch = model.current.branches[library];
    if (!branch) throw new Error("Select a working branch before editing.");
    return branch;
  };
  const snapshot = (library: string, path: string, branch = selectedBranch(library).id) =>
    branchAPI.entry(library, branch, path);
  const read = async (
    library: string,
    version: string,
    entry: Entry,
    revision: number,
    branch = version ? "" : selectedBranch(library).id,
  ) => {
    const response = await fetch(
      `${base}/api/v1/libraries/${library}/${version ? `versions/${version}` : `branches/${encodeURIComponent(branch)}/draft`}/content?${query({ path: entry.path, revision: version ? undefined : revision })}`,
      { credentials: "same-origin", cache: "no-store" },
    );
    if (!response.ok) throw new APIError(response.status, "file_read_failed");
    return response.arrayBuffer();
  };
  const persist = async () =>
    queue.current.run("local-recovery", async () => {
      if (!account) return;
      try {
        for (const b of model.current.buffers) {
          if (b.version) continue;
          if (!b.version && b.editable && (b.text !== b.saved || !!b.pending))
            await putRecovery({
              id: b.recoveryID,
              account,
              library: b.library,
              branch: b.branch,
              path: b.entry.path,
              session: sessionID.current,
              text: b.text,
              saved: b.saved,
              entry: b.entry,
              revision: b.revision,
              format: b.format,
              updated: Date.now(),
              pending: b.pending,
            });
          else await deleteRecovery(b.recoveryID);
        }
        if (model.current.recoveryError) {
          model.current.recoveryError = "";
          notify();
        }
      } catch (e) {
        const message = `Local recovery unavailable. Your edits remain in this tab. ${String(e)}`;
        if (model.current.recoveryError !== message) {
          model.current.recoveryError = message;
          notify();
        }
      }
    });
  const removeLocalCopy = async (id: string) => {
    try {
      await queue.current.run("local-recovery", () => deleteRecovery(id));
    } catch (e) {
      model.current.recoveryError = `Local recovery could not be cleared: ${String(e)}`;
      notify();
    }
  };
  const open = async (
    library: string,
    version: string,
    entry: Entry,
    revision = 0,
    branch = version ? "" : selectedBranch(library).id,
  ) => {
    const scope = workspaceKey(library, branch, version);
    const generation = (model.current.selectionGeneration[scope] || 0) + 1;
    model.current.selectionGeneration[scope] = generation;
    const key = JSON.stringify([library, branch, version, entry.path]);
    let b = model.current.buffers.find((x) => x.key === key);
    if (b) {
      model.current.active[scope] = key;
      notify();
      return b;
    }
    if (entry.kind !== "file") return;
    if (!version) {
      const current = await snapshot(library, entry.path, branch);
      if (!current.entry) throw new Error("File no longer exists.");
      entry = current.entry;
      revision = current.revision;
    }
    b = {
      key,
      branch,
      library,
      version,
      entry,
      revision,
      text: "",
      saved: "",
      format: { bom: false, separator: "\n", mixed: false },
      editable: false,
      status: "Saved",
      error: "",
      recoveryID: JSON.stringify([
        account,
        sessionID.current,
        library,
        branch,
        entry.path,
      ]),
    };
    const image = /\.(png|jpe?g|gif|webp)$/i.test(entry.path);
    if (Number(entry.size) <= (image ? 16 << 20 : 1 << 20)) {
      const data = await read(library, version, entry, revision, branch);
      if (image) {
        const ext = entry.path.split(".").at(-1)!.toLowerCase();
        b.image = URL.createObjectURL(
          new Blob([data], {
            type:
              ext === "jpg" || ext === "jpeg" ? "image/jpeg" : `image/${ext}`,
          }),
        );
      } else {
        try {
          const decoded = decodeText(data);
          b.text = b.saved = decoded.text;
          b.format = decoded.format;
          b.editable = true;
        } catch {
          b.editable = false;
        }
      }
    }
    const concurrent = model.current.buffers.find((x) => x.key === key);
    if (concurrent) {
      if (b.image) URL.revokeObjectURL(b.image);
      b = concurrent;
    } else model.current.buffers.push(b);
    if (
      model.current.selectionGeneration[scope] === generation &&
      !model.current.expired
    )
      model.current.active[scope] = key;
    notify();
    return b;
  };
  const conflict = async (
    b: BufferFile,
    current: { entry: Entry | null; revision: number },
  ) => {
    let text = "";
    let format: TextFormat | undefined;
    let readable = false;
    if (current.entry && Number(current.entry.size) <= 1 << 20) {
      try {
        const decoded = decodeText(
          await read(b.library, "", current.entry, current.revision, b.branch),
        );
        text = decoded.text;
        format = decoded.format;
        readable = true;
      } catch {
        text = "The saved file cannot be displayed as text.";
      }
    }
    b.conflict = { ...current, text, format, readable };
    b.status = "Conflict";
    b.error = "The saved file changed. Review it before saving.";
    notify();
  };
  const save = (b: BufferFile) =>
    queue.current.run(`${b.library}:${b.branch}`, async () => {
      if (b.version || !b.editable || (b.text === b.saved && !b.pending))
        return;
      if (model.current.expired) throw new Error("Sign in again to save.");
      b.isolateHistory?.();
      b.status = "Saving";
      b.error = "";
      notify();
      try {
        for (let attempt = 0; attempt < 2; attempt++) {
          if (!b.pending) {
            const current = await snapshot(b.library, b.entry.path, b.branch);
            if (entryIdentity(current.entry) !== entryIdentity(b.entry)) {
              await conflict(b, current);
              throw new Error(b.error);
            }
            const raw = encodeText(b.text, b.format);
            if (new TextEncoder().encode(raw).length > 1 << 20)
              throw new Error("Text exceeds the 1 MiB UTF-8 limit.");
            b.pending = {
              text: b.text,
              body: JSON.stringify({
                request_id: requestID(),
                expected_revision: current.revision,
                expected_head: current.head,
                path: b.entry.path,
                text: raw,
              }),
            };
          }
          const pending = b.pending;
          try {
            const result = await api<{ revision: number }>(
              `${branchRoot(b.library, b.branch)}/draft/text`,
              { method: "POST", body: pending.body },
              auth.current!.csrf,
            );
            const raw = encodeText(pending.text, b.format);
            const digest = await hashChunk(
              new TextEncoder().encode(raw).buffer,
            );
            b.saved = pending.text;
            b.entry = {
              ...b.entry,
              size: String(new TextEncoder().encode(raw).length),
              digest,
            };
            b.revision = result.revision;
            b.pending = undefined;
            b.conflict = undefined;
            b.status = b.text === b.saved ? "Saved" : "Unsaved";
            model.current.notice = "Saved to draft";
            await persist();
            changed();
            return;
          } catch (e) {
            if (e instanceof APIError && e.status === 409) {
              b.pending = undefined;
              if (attempt === 0) continue;
            }
            throw e;
          }
        }
      } catch (e) {
        if (b.status !== "Conflict") b.status = "Save failed";
        b.error = String(e);
        notify();
        throw e;
      }
    });
  const saveAll = async (library?: string) => {
    for (const b of [...model.current.buffers])
      if (
        (!library || b.library === library) &&
        !b.version &&
        (b.text !== b.saved || !!b.pending)
      )
        await save(b);
  };
  const discard = (b: BufferFile) =>
    queue.current.run(`${b.library}:${b.branch}`, async () => {
      // Wait for an in-flight save before changing its buffer. An uncertain
      // write must retain its receipt until the user can resolve its outcome.
      if (b.pending)
        throw new Error(
          "The last save is still unconfirmed. Retry saving before discarding these edits.",
        );
      b.text = b.saved;
      b.conflict = undefined;
      b.error = "";
      b.status = "Saved";
      b.editorState = undefined;
      b.reset = (b.reset || 0) + 1;
      await removeLocalCopy(b.recoveryID);
      notify();
    });
  const close = async (b: BufferFile) => {
    // A disabled Save button also means an in-flight save. Settle it before
    // deciding whether closing needs an unsaved-content confirmation.
    await queue.current.run(`${b.library}:${b.branch}`, async () => {});
    if (b.text !== b.saved || !!b.pending) {
      const answer = await ask(
        "Unsaved changes",
        message("Save changes to {path}?", { path: b.entry.path }),
        ["Cancel", "Discard", "Save"],
      );
      if (answer === "Cancel") return;
      if (answer === "Discard") await discard(b);
      if (answer === "Save") {
        await save(b);
        if (b.text !== b.saved || b.pending)
          throw new Error(
            "The file changed while saving. Its unsaved edits remain open.",
          );
      }
    }
    if (b.image) URL.revokeObjectURL(b.image);
    if (!b.version) await removeLocalCopy(b.recoveryID);
    model.current.buffers = model.current.buffers.filter((x) => x !== b);
    if (model.current.active[workspaceKey(b.library,b.branch,b.version)] === b.key)
      model.current.active[workspaceKey(b.library,b.branch,b.version)] =
        model.current.buffers.filter((x) => x.library === b.library && x.branch === b.branch && x.version === b.version).at(-1)
          ?.key || "";
    notify();
  };
  const settle = async (library: string, path = "", branch = selectedBranch(library).id) => {
    await queue.current.run(`${library}:${branch}`, async () => {});
    const dirty = model.current.buffers.filter(
      (b) =>
        b.library === library &&
        b.branch === branch &&
        !b.version &&
        (b.text !== b.saved || !!b.pending) &&
        (!path || b.entry.path === path || b.entry.path.startsWith(path + "/")),
    );
    if (!dirty.length) return true;
    const answer = await ask(
      "Unsaved files",
      message("{count} file(s) have unsaved changes.", { count: dirty.length }),
      ["Cancel", "Discard changes", "Save all"],
    );
    if (answer === "Cancel") return false;
    if (answer === "Save all") {
      for (const b of dirty) {
        await save(b);
        if (b.text !== b.saved || b.pending)
          throw new Error(
            "A file changed while saving. Review its unsaved edits before continuing.",
          );
      }
    } else {
      for (const b of dirty) await discard(b);
    }
    return true;
  };
  const mutate = async (
    library: string,
    input: Record<string, unknown>,
    expectedRevision?: number,
  ) => {
    const branchID = selectedBranch(library).id;
    return queue.current.run(`${library}:${branchID}`, async () => {
      const branch = await branchAPI.read(library, branchID);
      const text = input.action === "text";
      const payload = { ...input };
      if (text) delete payload.action;
      const result = await write<{ revision: number }>(
        `${branchRoot(library, branchID)}/draft/${text ? "text" : "structure"}`,
        { request_id: requestID(), expected_revision: expectedRevision ?? branch.draft_revision, expected_head: branch.head, ...payload },
      );
      changed();
      return result;
    });
  };
  const uploadOne = async (task: Task) => {
    if (!task.file) return;
    const file = task.file;
    const abort = new AbortController();
    task.controller = abort;
    task.state = "Checking";
    task.error = "";
    notify();
    try {
      const digest = await hashFile(
        file,
        (p) => {
          task.progress = file.size ? p / file.size : 1;
          notify();
        },
        abort.signal,
      );
      for (const directory of ancestors(task.path)) {
        await queue.current.run(`${task.library}:${task.branch}`, async () => {
          const current = await snapshot(task.library, directory, task.branch);
          if (!current.entry)
            await write(`${branchRoot(task.library, task.branch)}/draft/structure`, {
              request_id: requestID(),
              expected_revision: current.revision,
              expected_head: current.head,
              action: "mkdir",
              path: directory,
            });
          else if (current.entry.kind !== "directory")
            throw new Error(`${directory} is not a directory.`);
        });
      }
      const root = `/libraries/${task.library}`;
      const all = await api<{ uploads: Upload[] }>(`${root}/uploads?branch_id=${encodeURIComponent(task.branch)}`);
      let u = all.uploads.find(
        (x) =>
          x.branch_id === task.branch &&
          x.state === "open" &&
          x.path === task.path &&
          x.digest === digest &&
          x.size === String(file.size) &&
          x.owner.issuer === auth.current?.issuer &&
          x.owner.subject === auth.current?.subject,
      );
      if (!u) {
        u = await queue.current.run(`${task.library}:${task.branch}`, async () => {
          const target = await snapshot(task.library, task.path, task.branch);
          if (
            target.entry &&
            (await ask(
              "Replace file?",
              message("{path} already exists. Replace it with this upload?", { path: task.path }),
              ["Cancel", "Replace"],
            )) !== "Replace"
          )
            throw new Error("Replacement canceled.");
          return write<Upload>(`${root}/uploads`, {
            request_id: requestID(),
            branch_id: task.branch,
            expected_revision: target.revision,
            path: task.path,
            size: String(file.size),
            digest,
          });
        });
      }
      task.state = "Uploading";
      let offset = Number(u.offset);
      while (offset < file.size) {
        if (abort.signal.aborted)
          throw new DOMException("Paused", "AbortError");
        const data = await file.slice(offset, offset + (8 << 20)).arrayBuffer();
        const hash = await hashChunk(data);
        u = await api<Upload>(
          `${root}/uploads/${u.id}/chunks/${offset}`,
          {
            method: "PUT",
            body: data,
            headers: {
              "Content-Type": "application/octet-stream",
              "X-Chunk-Digest": hash,
            },
            signal: abort.signal,
          },
          auth.current!.csrf,
        );
        offset = Number(u.offset);
        task.progress = file.size ? offset / file.size : 1;
        notify();
      }
      await queue.current.run(`${task.library}:${task.branch}`, async () => {
        const current = await api<Upload>(`${root}/uploads/${u!.id}`);
        let confirm_target: string | undefined;
        if (current.current_target !== current.target_identity) {
          task.state = "Awaiting confirmation";
          notify();
          if (
            (await ask(
              "Upload target changed",
              message("{path} changed during upload. Replace the latest saved file?", { path: task.path }),
              ["Cancel", "Replace"],
            )) !== "Replace"
          )
            throw new Error("Uploaded bytes retained; replacement canceled.");
          confirm_target = current.current_target;
        }
        await write(`${root}/uploads/${u!.id}/complete`, {
          request_id: requestID(),
          expected_revision: current.current_revision,
          confirm_target,
        });
      });
      task.state = "Complete";
      task.progress = 1;
      task.file = undefined;
      model.current.notice = "Files saved to the draft";
      changed();
    } catch (e) {
      task.state = abort.signal.aborted ? "Paused" : "Failed";
      task.error = String(e);
    } finally {
      task.controller = undefined;
      notify();
    }
  };
  const upload = async (library: string, parent: string, files: File[]) => {
    const branch = selectedBranch(library).id;
    for (const file of files) {
      const path = [parent, file.webkitRelativePath || file.name]
        .filter(Boolean)
        .join("/");
      const task: Task = {
        id: requestID(),
        branch,
        library,
        path,
        state: "Queued",
        progress: 0,
        file,
      };
      model.current.tasks.push(task);
      notify();
      await uploadOne(task);
    }
  };
  const logout = async () => {
    for (const b of model.current.buffers.filter(b => !b.version))
      if (!(await settle(b.library,"",b.branch))) return;
    const response = await fetch(`${base}/auth/logout`, {
      method: "POST",
      headers: { "X-CSRF-Token": auth.current!.csrf },
    });
    if (!response.ok) throw new Error("Sign out failed. Please retry.");
    for (const t of model.current.tasks) t.controller?.abort();
    try {
      await queue.current.run("local-recovery", () => clearRecoveries(account));
    } finally {
      for (const b of model.current.buffers)
        if (b.image) URL.revokeObjectURL(b.image);
      model.current.buffers = [];
      model.current.recoveryItems = [];
      clearAppearanceBinding();
      window.location.assign(base + "/");
    }
  };
  useEffect(() => {
    sessionID.current = requestID();
  }, []);
  useEffect(() => {
    if (!account) return;
    let canceled = false;
    void recoveries(account)
      .then((items) => {
        if (!canceled) {
          model.current.recoveryItems = items;
          notify();
        }
      })
      .catch((e) => {
        model.current.recoveryError = String(e);
        notify();
      });
    return () => {
      canceled = true;
    };
  }, [account, model]);
  useEffect(() => {
    const expired = () => {
      model.current.expired = true;
      for (const t of model.current.tasks) t.controller?.abort();
      notify();
    };
    window.addEventListener("studio-auth-expired", expired);
    return () => window.removeEventListener("studio-auth-expired", expired);
  }, [model]);
  useEffect(() => {
    const unload = (e: BeforeUnloadEvent) => {
      if (
        model.current.buffers.some((b) => b.text !== b.saved || !!b.pending) ||
        model.current.tasks.some((t) => t.controller)
      )
        e.preventDefault();
    };
    window.addEventListener("beforeunload", unload);
    return () => window.removeEventListener("beforeunload", unload);
  }, [model]);
  useEffect(() => {
    const timer = setTimeout(() => {
      void persist();
    }, 500);
    return () => clearTimeout(timer);
  });
  useEffect(() => {
    const flush = () => {
      void persist();
    };
    window.addEventListener("pagehide", flush);
    return () => window.removeEventListener("pagehide", flush);
  });
  const restore = async (r: Recovery) => {
    if (!r.branch) throw new Error("Recovery has no branch identity and cannot be restored into a working branch.");
    model.current.branches[r.library] = await branchAPI.read(r.library, r.branch);
    const existing = model.current.buffers.find(
      (b) => b.library === r.library && b.branch === r.branch && !b.version && b.entry.path === r.path,
    );
    if (
      existing &&
      (existing.pending ||
        (existing.text !== existing.saved && existing.text !== r.text))
    ) {
      const choice = await ask(
        "Replace unsaved buffer?",
        "This file already has unsaved edits in this tab.",
        [
          "Keep current edits",
          "Save current and restore",
          "Replace with recovery",
        ],
      );
      if (choice === "Keep current edits") return;
      if (choice === "Save current and restore") {
        await save(existing);
        if (existing.text !== existing.saved || existing.pending)
          throw new Error(
            "The file changed while saving. Review its unsaved edits before restoring another copy.",
          );
      } else {
        if (existing.pending)
          throw new Error(
            "The last save is still unconfirmed. Retry saving before replacing these edits.",
          );
        await persist();
      }
    }
    let b = await open(r.library, "", r.entry, 0, r.branch).catch(() => undefined);
    if (!b) {
      b = {
        key: JSON.stringify([r.library, r.branch, "", r.path]),
        library: r.library,
        branch: r.branch,
        version: "",
        entry: r.entry,
        revision: r.revision,
        text: r.text,
        saved: r.saved,
        format: r.format,
        editable: true,
        status: "Unsaved",
        error: "",
        recoveryID: r.id,
      };
      model.current.buffers.push(b);
    }
    b.text = r.text;
    b.saved = r.saved;
    b.entry = r.entry;
    b.format = r.format;
    b.revision = r.revision;
    b.recoveryID = r.id;
    b.pending = r.pending;
    b.editorState = undefined;
    b.reset = (b.reset || 0) + 1;
    model.current.active[workspaceKey(r.library,r.branch)] = b.key;
    const current = await snapshot(r.library, r.path, r.branch);
    if (entryIdentity(current.entry) !== entryIdentity(r.entry))
      await conflict(b, current);
    else b.status = "Unsaved";
    model.current.recoveryItems = model.current.recoveryItems.filter(
      (x) => x.id !== r.id,
    );
    notify();
  };
  return {
    model: model.current,
    selectBranch: (branch: Branch) => { model.current.branches[branch.library_id] = branch; notify(); },
    session,
    notify,
    changed,
    ask,
    write,
    open,
    save,
    saveAll,
    close,
    settle,
    mutate,
    upload,
    retryUpload: uploadOne,
    logout,
    persist,
    restore,
    snapshot,
    read,
    queue: queue.current,
    discardRecovery: async (r: Recovery) => {
      await deleteRecovery(r.id);
      model.current.recoveryItems = model.current.recoveryItems.filter(
        (x) => x.id !== r.id,
      );
      notify();
    },
  };
}
function ancestors(path: string) {
  const parts = parentPath(path).split("/").filter(Boolean);
  return parts.map((_, i) => parts.slice(0, i + 1).join("/"));
}
type Studio = ReturnType<typeof useStudioController>;
const StudioContext = createContext<Studio | null>(null);
export function useStudio() {
  const value = useContext(StudioContext);
  if (!value) throw new Error("Studio provider missing");
  return value;
}
export function StudioProvider({ children }: { children: ReactNode }) {
  const tr = useT();
  const { session, error, loading } = useSession();
  const studio = useStudioController(session);
  useAccountPreferences(session?.subject, true);
  useEffect(() => { document.title = "Volume Studio"; }, []);
  const question = studio.model.questions[0];
  return (
    <StudioContext.Provider value={studio}>
      {!session ? (
        <SessionGate loading={loading} error={tr(error)} />
      ) : studio.model.expired ? (
        <main className="session-gate">
          <h1>{tr("Sign in again")}</h1>
          <p>{tr("Your unsaved edits are retained for this account.")}</p>
          <a
            className="button primary"
            href={`${base}/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`}
          >
             {tr("Sign in")} </a>
        </main>
      ) : (
        children
      )}
      {question && (
        <Modal
          title={tr(question.title)}
          open
          onClose={() => {
            studio.model.questions.shift();
            question.resolve(question.choices[0].value);
            studio.notify();
          }}
        >
          <p>{tr(question.message)}</p>
          <div className="dialog-actions">
            {question.choices.map((c, i) => (
              <button
                key={c.value}
                className={i === question.choices.length - 1 ? "primary" : ""}
                onClick={() => {
                  studio.model.questions.shift();
                  question.resolve(c.value);
                  studio.notify();
                }}
              >
                {tr(c.label)}
              </button>
            ))}
          </div>
        </Modal>
      )}
    </StudioContext.Provider>
  );
}
