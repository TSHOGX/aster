"use client";
import { useEffect, useState } from "react";
import { api, command, encode } from "@/lib/project-api";
import { useT } from "@/lib/i18n";

type Task = {
  title: string;
  id: string;
  project_id: string;
  session_id: string;
  state: string;
  error: string;
};
export function CopyTasks({
  onOpen,
}: {
  onOpen: (project: string, session: string) => void;
}) {
  const tr = useT();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      try {
        const data = await api<{ copies: Task[] }>("/api/conversation-copies", {
          signal: abort.signal,
        });
        if (abort.signal.aborted) return;
        setTasks(data.copies);
        setError("");
        clearTimeout(timer);
        if (
          data.copies.some((t) => ["preparing", "committed"].includes(t.state))
        )
          timer = setTimeout(() => void load(), 2000);
      } catch (e) {
        if (!abort.signal.aborted) setError(String(e));
      }
    };
    void load();
    const changed = () => void load();
    window.addEventListener("conversation-copy-changed", changed);
    return () => {
      abort.abort();
      clearTimeout(timer);
      window.removeEventListener("conversation-copy-changed", changed);
    };
  }, [attempt]);
  if (!tasks.length && !error) return null;
  return (
    <details className="copy-tasks">
      <summary>{tr("Copy tasks")}</summary>
      {error && (
        <p role="alert">
          {error}
          <button onClick={() => setAttempt((n) => n + 1)}>
            {tr("Retry")}
          </button>
        </p>
      )}
      {tasks.map((task) => (
        <div className="copy-task" key={task.id}>
          <small>
            {tr(
              task.state === "complete"
                ? "Copy complete"
                : task.state === "failed"
                  ? "Copy failed"
                  : "Copying…",
            )}{" "}
            · {task.title || task.id.slice(0, 8)}
          </small>
          {task.state === "complete" ? (
            <button onClick={() => onOpen(task.project_id, task.session_id)}>
              {tr("Open conversation")}
            </button>
          ) : task.state === "failed" ? (
            <button
              onClick={() =>
                void command(
                  `/api/conversation-copies/${encode(task.id)}/retry`,
                )
                  .then(() => setAttempt((n) => n + 1))
                  .catch((e) => setError(String(e)))
              }
              title={task.error}
            >
              {tr("Retry")}
            </button>
          ) : null}
        </div>
      ))}
    </details>
  );
}
