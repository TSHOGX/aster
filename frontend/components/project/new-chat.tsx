"use client";
import { useCapabilities } from "./capability-provider";
import { ProjectPicker } from "./project-picker";
import { ComposerInput } from "./composer-input";
import { useEffect, useRef, useState } from "react";
import { ArrowUp } from "lucide-react";
import {
  api,
  command,
  newID,
  projectURL,
  recalled,
  remember,
  type AgentSession,
  type Project,
} from "@/lib/project-api";
import { useT } from "@/lib/i18n";
type Intent = {
  sessionId: string;
  requestId: string;
  content: string;
  startedAt?: string;
};
export function NewChat({
  actor,
  project,
  projects,
  onProject,
  onCreated,
}: {
  actor: string;
  project: Project;
  projects: Project[];
  onProject: (id: string) => void;
  onCreated: (session: AgentSession) => void;
}) {
  const canRun = useCapabilities()("agents.run");
  const tr = useT(),
    key = `project-view:${actor}:${project.id}:draft:new`;
  const [draft, setDraft] = useState(() => recalled<string>(key, ""));
  const [pending, setPending] = useState<Intent | null>(() =>
    recalled<Intent | null>(key + ":intent", null),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const lock = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    remember(key, draft);
  }, [key, draft]);
  const send = async () => {
    if (!canRun || lock.current || (!draft.trim() && !pending)) return;
    lock.current = true;
    setBusy(true);
    setError("");
    const intent = pending || {
      sessionId: newID(),
      requestId: newID(),
      content: draft,
      startedAt: new Date().toISOString(),
    };
    setPending(intent);
    remember(key + ":intent", intent);
    try {
      // Same creation and turn identities survive failed replies and explicit retries.
      let session: AgentSession;
      try {
        session = await api<AgentSession>(
          `${projectURL(project.id)}/sessions/${intent.sessionId}`,
        );
      } catch (e) {
        if (!(e instanceof Error && "status" in e && e.status === 404)) throw e;
        session = await command<AgentSession>(
          `${projectURL(project.id)}/sessions`,
          { id: intent.sessionId, title: "" },
        );
      }
      await command(
        `${projectURL(project.id)}/sessions/${intent.sessionId}/turns`,
        { request_id: intent.requestId, content: intent.content, held: false },
      );
      remember(key, "");
      remember(key + ":intent", null);
      setDraft("");
      setPending(null);
      if (mounted.current) onCreated(session);
    } catch (e) {
      setError(String(e));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  };
  return (
    <section className={`new-chat-view ${pending ? "submitting-chat" : ""}`}>
      {pending && (
        <div className="pending-first-turn" role="status">
          <strong>{tr("Sending…")}</strong>
          <p>{pending.content}</p>
        </div>
      )}
      <div className="new-chat-intro" hidden={!!pending}>
        <h1>{tr("What would you like to work on?")}</h1>
        <div className="project-picker">
          <span>{tr("Project")}</span>
          <ProjectPicker
            projects={projects}
            value={project.id}
            disabled={!!pending}
            onChange={onProject}
          />
        </div>
      </div>
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      {pending && error && (
        <p className="notice">
          {tr(
            "Retry keeps the same conversation and message. Your draft is retained.",
          )}
        </p>
      )}
      {!canRun && <p role="status" className="notice">{tr("Permission to run agents is required. Your draft is retained.")}</p>}
      <form
        className="agent-composer"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
        <ComposerInput
          autoFocus
          aria-label={tr("New message")}
          placeholder={tr("Ask anything, or describe a task…")}
          value={pending?.content || draft}
          disabled={!!pending}
          maxLength={64000}
          onChange={(e) => setDraft(e.target.value)}
          onSend={() => {
            void send();
          }}
        />
        <div className="composer-actions">
          <small>{project.title}</small>
          <button
            className="send-button"
            aria-label={pending ? tr("Retry") : tr("Send")}
            title={pending ? tr("Retry") : tr("Send")}
            disabled={
              busy || !canRun ||
              (!draft.trim() && !pending) ||
              ["deleting", "deleted"].includes(project.state)
            }
          >
            <ArrowUp size={19} />
          </button>
        </div>
      </form>
    </section>
  );
}
