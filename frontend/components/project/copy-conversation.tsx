"use client";
import type { EffectiveAccess } from "@web/capabilities";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  command,
  encode,
  newID,
  recalled,
  remember,
  type Project,
} from "@/lib/project-api";
import type { ConversationSource } from "@/lib/conversation-source";
import { useT } from "@/lib/i18n";
import { ProjectPicker } from "./project-picker";
import { trapDialogFocus } from "./ui";

type CopyTask = {
  id: string;
  project_id: string;
  session_id: string;
  state: string;
  committed: boolean;
  error: string;
};
export function CopyConversation({
  source,
  title,
  paths,
  onCopied,
}: {
  source: ConversationSource;
  title: string;
  paths: string[];
  onCopied?: (project: string, session: string) => void;
}) {
  const router = useRouter();
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [actor, setActor] = useState("");
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState("");
  const [create, setCreate] = useState(false);
  const [canCreate, setCanCreate] = useState(false);
  const [canImport, setCanImport] = useState(false);
  const [projectTitle, setProjectTitle] = useState("");
  const [name, setName] = useState(`${title} · ${tr("Copy")}`);
  const [selected, setSelected] = useState(
    source.capabilities.download ? paths : [],
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [task, setTask] = useState<CopyTask>();
  const dialog = useRef<HTMLDialogElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const request = useRef<Record<string, unknown> | null>(null);
  const key = `conversation-copy:${source.kind}:${source.session}`;
  const begin = async () => {
    setError("");
    try {
      const identity = await fetch("/api/auth/session", { cache: "no-store" });
      if (identity.status === 401) {
        router.push(
          `/?return_to=${encode(`${window.location.pathname}?copy=1`)}`,
        );
        return;
      }
      if (!identity.ok) throw new Error(tr("Copy failed"));
      const user = (await identity.json()) as { id: string };
      setActor(user.id);
      const access = await api<EffectiveAccess>("/api/me/capabilities");
      if (
        access.account_id !== user.id ||
        !access.actions["agents.run"]?.allowed
      )
        throw new Error(
          tr("You do not have permission to create conversations."),
        );
      setCanCreate(!!access.actions["projects.create"]?.allowed);
      const importAllowed = [
        "files.upload",
        "files.manage",
        "files.list",
      ].every(
        (action) =>
          access.actions[action as keyof typeof access.actions]?.allowed,
      );
      setCanImport(importAllowed);
      if (!importAllowed) setSelected([]);
      const all: Project[] = [];
      let after = "";
      do {
        const page = await api<{ projects: Project[] }>(
          `/api/projects?after=${encode(after)}&limit=100`,
        );
        all.push(...page.projects);
        if (page.projects.length < 100) break;
        after = page.projects.at(-1)!.id;
      } while (true);
      const available = all.filter(
        (p) =>
          !["deleted", "deleting"].includes(p.state) && !p.execution_suspended,
      );
      setProjects(available);
      const previous = recalled<string>(`last-copy-project:${user.id}`, "");
      setProject(
        available.find((p) => p.id === previous)?.id || available[0]?.id || "",
      );
      setCreate(!available.length);
      const saved = recalled<CopyTask | null>(`${key}:${user.id}`, null);
      const pending = recalled<Record<string, unknown> | null>(
        `${key}:${user.id}:pending`,
        null,
      );
      if (saved || pending) {
        try {
          setTask(
            await api<CopyTask>(
              `/api/conversation-copies/${encode(String(saved?.id || pending!.id))}`,
            ),
          );
        } catch (e) {
          if (!(e instanceof APIError) || e.status !== 404) throw e;
          if (pending) {
            request.current = {
              ...pending,
              ...(source.kind === "publicSnapshot"
                ? { token: source.base.split("/").at(-1) }
                : {}),
            };
            setName(String(pending.title));
            setCreate(!!pending.project_title);
            setProjectTitle(String(pending.project_title || ""));
            setProject(String(pending.project_id));
            setSelected(pending.paths as string[]);
          } else remember(`${key}:${user.id}`, null);
        }
      }
      setOpen(true);
    } catch (e) {
      setError(String(e));
    }
  };
  useEffect(() => {
    if (
      source.kind === "publicSnapshot" &&
      new URLSearchParams(location.search).get("copy") === "1"
    ) {
      history.replaceState(null, "", location.pathname);
      void Promise.resolve().then(begin);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (open) dialog.current?.showModal();
    else dialog.current?.close();
  }, [open]);
  useEffect(() => {
    if (!task || ["complete", "failed", "canceled"].includes(task.state))
      return;
    const abort = new AbortController();
    const timer = setInterval(() => {
      void api<CopyTask>(`/api/conversation-copies/${encode(task.id)}`, {
        signal: abort.signal,
      })
        .then((value) => {
          if (abort.signal.aborted) return;
          setTask(value);
          remember(`${key}:${actor}`, value);
          if (value.state === "complete") {
            remember(`last-copy-project:${actor}`, value.project_id);
            if (onCopied) onCopied(value.project_id, value.session_id);
            else
              router.push(
                `/?project=${encode(value.project_id)}&session=${encode(value.session_id)}`,
              );
          }
        })
        .catch((e) => {
          if (!abort.signal.aborted) setError(String(e));
        });
    }, 1000);
    return () => {
      abort.abort();
      clearInterval(timer);
    };
  }, [task, actor, key, router, onCopied]);
  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      if (!request.current)
        request.current = {
          id: newID(),
          kind: source.kind,
          ...(source.kind === "received"
            ? { session: source.session }
            : { token: source.base.split("/").at(-1) }),
          project_id: create ? newID() : project,
          project_title: create ? projectTitle.trim() : "",
          title: name.trim(),
          paths: selected,
        };
      const { token: _token, ...savedRequest } = request.current;
      remember(`${key}:${actor}:pending`, savedRequest);
      const result = await command<CopyTask>(
        "/api/conversation-copies",
        request.current,
      );
      setTask(result);
      window.dispatchEvent(new Event("conversation-copy-changed"));
      remember(`${key}:${actor}`, result);
      remember(`${key}:${actor}:pending`, null);
    } catch (e) {
      setError(String(e));
      if (e instanceof APIError && e.status >= 400 && e.status < 500) {
        request.current = null;
        remember(`${key}:${actor}:pending`, null);
      }
    } finally {
      setBusy(false);
    }
  };
  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };
  return (
    <>
      <button
        ref={trigger}
        disabled={!source.capabilities.copy}
        title={
          !source.capabilities.copy
            ? tr("No completed turns to copy yet.")
            : undefined
        }
        onClick={() => void begin()}
      >
        {tr("Copy to my project")}
      </button>
      {!open && error && <span role="alert">{error}</span>}
      <dialog
        ref={dialog}
        className="copy-dialog"
        onCancel={close}
        onKeyDown={trapDialogFocus}
        aria-labelledby="copy-title"
      >
        <h2 id="copy-title">{tr("Copy conversation")}</h2>
        {error && (
          <p role="alert" className="notice error">
            {error}
          </p>
        )}
        {task ? (
          <>
            <p role="status">
              {tr(
                task.state === "complete"
                  ? "Copy complete"
                  : task.state === "failed"
                    ? "Copy failed"
                    : "Copying…",
              )}
            </p>
            {task.error && <p>{task.error}</p>}
            {task.state === "failed" && (
              <button
                onClick={() => {
                  void command(
                    `/api/conversation-copies/${encode(task.id)}/retry`,
                  )
                    .then(() =>
                      api<CopyTask>(
                        `/api/conversation-copies/${encode(task.id)}`,
                      ),
                    )
                    .then(setTask)
                    .catch((e) => setError(String(e)));
                }}
              >
                {tr("Retry")}
              </button>
            )}
            {task.state === "failed" && !task.committed && (
              <button
                onClick={() => {
                  void command(
                    `/api/conversation-copies/${encode(task.id)}`,
                    undefined,
                    "DELETE",
                  )
                    .then(() => {
                      setTask(undefined);
                      request.current = null;
                      setSelected([]);
                      remember(`${key}:${actor}`, null);
                      remember(`${key}:${actor}:pending`, null);
                    })
                    .catch((e) => setError(String(e)));
                }}
              >
                {tr("Copy without files")}
              </button>
            )}
            {task.state === "complete" && (
              <button
                onClick={() => {
                  setTask(undefined);
                  request.current = null;
                  remember(`${key}:${actor}`, null);
                }}
              >
                {tr("Make another copy")}
              </button>
            )}
            {task.state === "complete" && (
              <a
                href={`/?project=${encode(task.project_id)}&session=${encode(task.session_id)}`}
              >
                {tr("Open conversation")}
              </a>
            )}
          </>
        ) : (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void submit();
            }}
          >
            <fieldset disabled={busy}>
              <legend>{tr("Project")}</legend>
              <label>
                <input
                  type="radio"
                  name="copy-target"
                  checked={!create}
                  disabled={!projects.length}
                  onChange={() => {
                    request.current = null;
                    setCreate(false);
                  }}
                />
                {tr("Existing project")}
              </label>
              <label>
                <input
                  type="radio"
                  name="copy-target"
                  checked={create}
                  disabled={!canCreate}
                  onChange={() => {
                    request.current = null;
                    setCreate(true);
                  }}
                />
                {tr("New project")}
              </label>
              {create ? (
                <label>
                  {tr("Project name")}
                  <input
                    required
                    maxLength={256}
                    value={projectTitle}
                    onChange={(e) => {
                      request.current = null;
                      setProjectTitle(e.target.value);
                    }}
                  />
                </label>
              ) : (
                <ProjectPicker
                  projects={projects}
                  value={project}
                  onChange={(id) => {
                    request.current = null;
                    setProject(id);
                  }}
                />
              )}
              <label>
                {tr("Conversation name")}
                <input
                  required
                  maxLength={256}
                  value={name}
                  onChange={(e) => {
                    request.current = null;
                    setName(e.target.value);
                  }}
                />
              </label>
              <p>
                {tr(
                  "Copies include completed turns only and remain independent of the source.",
                )}
              </p>
              {paths.map((path) => (
                <label key={path}>
                  <input
                    type="checkbox"
                    disabled={!source.capabilities.download || !canImport}
                    checked={selected.includes(path)}
                    onChange={(e) => {
                      request.current = null;
                      setSelected((old) =>
                        e.target.checked
                          ? [...old, path]
                          : old.filter((p) => p !== path),
                      );
                    }}
                  />
                  {path}
                  {!source.capabilities.download && (
                    <small> · {tr("Not copied")}</small>
                  )}
                </label>
              ))}
              <button
                className="primary"
                disabled={
                  !name.trim() ||
                  (create ? !canCreate || !projectTitle.trim() : !project)
                }
              >
                {tr("Copy conversation")}
              </button>
            </fieldset>
          </form>
        )}
        <button onClick={close}>{tr("Close")}</button>
      </dialog>
    </>
  );
}
