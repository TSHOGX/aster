"use client";
import { useCapabilities } from "./capability-provider";
import dynamic from "next/dynamic";
import { useRef, useState } from "react";
import { Plus, TerminalSquare, X } from "lucide-react";
import {
  command,
  encode,
  newID,
  projectURL,
  type Project,
  type ProjectTerminal,
} from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import { IconButton, Menu, useDialog } from "./ui";
const TerminalPanel = dynamic(
  () =>
    import("@/components/workbench/terminal-panel").then(
      (m) => m.TerminalPanel,
    ),
  { ssr: false },
);
export function TerminalDock({
  project,
  terminals,
  open,
  active,
  height,
  onChange,
  onClose,
  onRefresh,
  onError,
}: {
  project: Project;
  terminals: ProjectTerminal[];
  open: string[];
  active: string;
  height: number;
  onChange: (ids: string[], active: string) => void;
  onClose: () => void;
  onRefresh: () => Promise<void>;
  onError: (e: string) => void;
}) {
  const can = useCapabilities();
  const canUse = can("terminals.use"), canManage = can("projects.manage");
  const tr = useT(),
    ask = useDialog(),
    [busy, setBusy] = useState(false),
    lock = useRef(false);
  const base = `${projectURL(project.id)}/terminals`;
  const live = terminals.filter((t) => t.deletion === 0),
    tabs = open
      .map((id) => live.find((t) => t.id === id))
      .filter((t): t is ProjectTerminal => !!t),
    selected = tabs.find((t) => t.id === active) || tabs[0];
  const act = async (fn: () => Promise<void>) => {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    try {
      await fn();
      await onRefresh();
    } catch (e) {
      onError(String(e));
    } finally {
      setBusy(false);
      lock.current = false;
    }
  };
  const start = async (t: ProjectTerminal) => {
    if (!canUse) return;
    onChange([...new Set([...open, t.id])], t.id);
    for (let attempt = 0; attempt < 100; attempt++) {
      const result = await command<{ state?: string }>(
        `${base}/${encode(t.id)}/start`,
        { rows: t.rows, cols: t.cols },
      );
      if (result.state === "running") return;
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(tr("Terminal start is still pending. Reopen it to retry."));
  };
  return (
    <section
      className="terminal-dock"
      aria-label={tr("Terminals")}
      style={{ height }}
    >
      <header className="terminal-tabs">
        <div role="tablist" aria-label={tr("Terminals")}>
          {tabs.map((t) => (
            <div
              className={`terminal-tab ${selected?.id === t.id ? "selected" : ""}`}
              key={t.id}
            >
              <button
                role="tab"
                aria-selected={selected?.id === t.id}
                onClick={() => onChange(open, t.id)}
              >
                <TerminalSquare size={15} />
                {t.title}
              </button>
              <IconButton
                label={`${tr("Close view")} ${t.title}`}
                onClick={() => {
                  const ids = open.filter((id) => id !== t.id);
                  onChange(ids, active === t.id ? ids[0] || "" : active);
                }}
              >
                <X size={14} />
              </IconButton>
            </div>
          ))}
        </div>
        <IconButton
          label={tr("New terminal")}
          disabled={busy || !canUse || ["deleting", "deleted"].includes(project.state)}
          onClick={() =>
            void act(async () => {
              const t = await command<ProjectTerminal>(base, {
                id: newID(),
                title: `Terminal ${terminals.length + 1}`,
                rows: 24,
                cols: 80,
              });
              await start(t);
            })
          }
        >
          <Plus size={18} />
        </IconButton>
        <div className="terminal-spacer" />
        <Menu
          label={tr("Terminal actions")}
          items={[
            ...live
              .filter((t) => !open.includes(t.id))
              .map((t) => ({
                label: `${tr("Open")} ${t.title} · ${tr(t.state)}`,
                action: () => onChange([...open, t.id], t.id),
              })),
            ...(selected
              ? [
                  {
                    label: tr("Rename"),
                    disabled: !canManage,
                    action: () =>
                      void (async () => {
                        const title = await ask({
                          title: tr("Terminal name"),
                          initial: selected.title,
                          input: true,
                          choices: [{ label: tr("Save"), value: "save" }],
                        });
                        if (title)
                          void act(async () => {
                            await command(
                              `${base}/${encode(selected.id)}`,
                              { title, revision: selected.revision },
                              "PATCH",
                            );
                          });
                      })(),
                  },
                  {
                    label: tr("Restart terminal"),
                    disabled:
                      busy || !canUse || ["running", "starting"].includes(selected.state),
                    action: () => void act(() => start(selected)),
                  },
                  {
                    label: tr("Terminate"),
                    disabled:
                      busy || !["running", "starting"].includes(selected.state),
                    action: () =>
                      void act(async () => {
                        await command(`${base}/${encode(selected.id)}/stop`);
                      }),
                  },
                  {
                    label: tr("Delete"),
                    danger: true,
                    action: () =>
                      void (async () => {
                        if (
                          await ask({
                            title: tr("Delete terminal"),
                            description: tr(
                              "This terminates the terminal process.",
                            ),
                            choices: [
                              {
                                label: tr("Delete"),
                                value: "delete",
                                danger: true,
                              },
                            ],
                          })
                        )
                          void act(async () => {
                            await command(
                              `${base}/${encode(selected.id)}?revision=${selected.revision}`,
                              undefined,
                              "DELETE",
                            );
                            onChange(
                              open.filter((id) => id !== selected.id),
                              "",
                            );
                          });
                      })(),
                  },
                ]
              : []),
          ]}
        />
        <IconButton label={tr("Hide terminal")} onClick={onClose}>
          <X size={18} />
        </IconButton>
      </header>
      {selected && !canUse ? <p className="notice" role="status">{tr("Terminal permission is required. You can still terminate or delete this terminal.")}</p> : selected ? (
        <TerminalPanel
          key={`${selected.id}:${selected.incarnation || "created"}`}
          projectId={project.id}
          terminalId={selected.id}
          title={selected.title}
          processState={selected.state}
          processExitCode={selected.exit_code}
        />
      ) : (
        <div className="terminal-empty">
          <TerminalSquare size={22} />
          <p>{tr("Open a terminal from the menu, or create one with +.")}</p>
        </div>
      )}
    </section>
  );
}
