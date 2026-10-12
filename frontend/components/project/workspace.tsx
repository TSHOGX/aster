"use client";
import {
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type RefObject,
} from "react";
import { PanelBottom, PanelRight } from "lucide-react";
import {
  remember,
  type AgentSession,
  type Project,
  type ProjectTerminal,
} from "@/lib/project-api";
import {
  readWorkspace,
  workspaceKey,
  type Navigation,
} from "@/lib/workspace-state";
import { useT } from "@/lib/i18n";
import { IconButton, Splitter } from "./ui";
import { SessionPanel } from "./session-panel";
import { FilesPanel } from "./files-panel";
import { TerminalDock } from "./terminal-dock";
import { NewChat } from "./new-chat";
import { SharePanel } from "./adjacent-panels";
export function ProjectWorkspace({
  actor,
  project,
  projects,
  session,
  sessionId,
  sessions,
  terminals,
  nav,
  onNavigate,
  onRefresh,
  onError,
  guard,
  fullscreen,
  onFullscreen,
}: {
  actor: string;
  project: Project;
  projects: Project[];
  session?: AgentSession;
  sessionId: string;
  sessions: AgentSession[];
  terminals: ProjectTerminal[];
  nav: Navigation;
  onNavigate: (n: Navigation) => void;
  onRefresh: () => Promise<void>;
  onError: (s: string) => void;
  guard: RefObject<() => Promise<boolean>>;
  fullscreen: boolean;
  onFullscreen: (b: boolean) => void;
}) {
  const tr = useT();
  const [pref, setPref] = useState(() => readWorkspace(actor, project.id));
  const host = useRef<HTMLDivElement>(null),
    fullButton = useRef<HTMLButtonElement | null>(null);
  const [size, setSize] = useState({ width: 1200, height: 900 });
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const observer = new ResizeObserver(() => {
      if (el.clientWidth > 0)
        setSize({ width: el.clientWidth, height: el.clientHeight });
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    remember(workspaceKey(actor, project.id), {
      ...pref,
      activeSessionId:
        sessionId || readWorkspace(actor, project.id).activeSessionId,
    });
  }, [actor, project.id, pref, sessionId]);
  useEffect(() => {
    if (!fullscreen) return;
    const exit = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !document.querySelector("dialog[open]")) {
        onFullscreen(false);
        fullButton.current?.focus();
      }
    };
    window.addEventListener("keydown", exit);
    return () => window.removeEventListener("keydown", exit);
  }, [fullscreen, onFullscreen]);
  const filesOverlay = size.width < 960;
  const width = Math.max(
    560,
    Math.min(size.width - 400, (size.width * pref.fileWidth) / 100),
  );
  const selectFile = (file: string) => {
    void (async () => {
      if (file !== pref.file && !(await guard.current())) return;
      setPref((p) => ({ ...p, file, filesOpen: true }));
    })();
  };
  return (
    <div className="project-workspace" ref={host}>
      <header className="workspace-heading" inert={fullscreen}>
        <div className="workspace-title">
          <span title={session?.title || project.title}>
            {session?.title || tr("New Chat")}
          </span>
          <small>
            {project.execution_suspended ? tr("Execution suspended") : project.state !== "active" ? (
              project.state
            ) : session?.state === "running" ? (
              <span
                className="state-dot running"
                aria-label={tr("Running")}
                title={tr("Running")}
              />
            ) : (
              ""
            )}
          </small>
        </div>
        <div className="workspace-switches">
          <IconButton
            label={tr("Toggle Terminal")}
            aria-pressed={pref.terminalOpen}
            onClick={() =>
              setPref((p) => ({ ...p, terminalOpen: !p.terminalOpen }))
            }
          >
            <PanelBottom size={19} />
          </IconButton>
          <IconButton
            label={tr("File preview")}
            aria-pressed={pref.filesOpen}
            onClick={() => setPref((p) => ({ ...p, filesOpen: !p.filesOpen }))}
          >
            <PanelRight size={19} />
          </IconButton>
        </div>
      </header>
      {project.failure && (
        <p className="notice error" role="alert">
          {project.failure}
        </p>
      )}
      <div
        className={`workspace-body ${pref.filesOpen ? "files-visible" : ""} ${filesOverlay ? "files-overlay" : ""}`}
        style={{ "--file-width": `${width}px` } as CSSProperties}
      >
        <div className="conversation-area" hidden={fullscreen}>
          {nav.view === "share" ? (
            <SharePanel
              key={sessionId}
              project={project.id}
              sessions={sessions}
              initialSession={sessionId}
            />
          ) : session && session.deletion === 0 ? (
            <SessionPanel
              key={session.id}
              actor={actor}
              session={session}
              onFile={selectFile}
              targetMessage={nav.message}
              showHistory={nav.history}
            />
          ) : sessionId && !nav.draft ? (
            <div className="empty-state">
              <p>{tr("This conversation is unavailable.")}</p>
              <button
                onClick={() => onNavigate({ ...nav, session: "", draft: true })}
              >
                {tr("New Chat")}
              </button>
            </div>
          ) : (
            <NewChat
              actor={actor}
              project={project}
              projects={projects}
              onProject={(id) =>
                onNavigate({ ...nav, project: id, session: "", draft: true })
              }
              onCreated={(s) => {
                void onRefresh();
                onNavigate({ ...nav, session: s.id, draft: false });
              }}
            />
          )}
        </div>
        {pref.filesOpen && !filesOverlay && !fullscreen && (
          <Splitter
            label={tr("Resize file preview")}
            value={width}
            min={560}
            max={Math.max(560, size.width - 400)}
            reverse
            onChange={(n) =>
              setPref((p) => ({ ...p, fileWidth: (n / size.width) * 100 }))
            }
          />
        )}
        <div
          className="file-workspace-host"
          hidden={!pref.filesOpen && !fullscreen}
        >
          <FilesPanel
            project={project}
            selected={pref.file}
            onSelect={(file) => setPref((p) => ({ ...p, file }))}
            visible={pref.filesOpen || fullscreen}
            fullscreen={fullscreen}
            onFullscreen={(value) => onFullscreen(value)}
            onClose={() => {
              setPref((p) => ({ ...p, filesOpen: false }));
              onFullscreen(false);
            }}
            treeWidth={pref.treeWidth}
            onTreeWidth={(treeWidth) => setPref((p) => ({ ...p, treeWidth }))}
            directories={pref.directories}
            onDirectories={(directories) =>
              setPref((p) => ({ ...p, directories }))
            }
            guard={guard}
            fullButton={fullButton}
          />
        </div>
      </div>
      {pref.terminalOpen && !fullscreen && (
        <>
          <Splitter
            label={tr("Resize terminal")}
            horizontal
            reverse
            value={Math.min(pref.terminalHeight, size.height / 2)}
            min={Math.min(160, size.height / 2)}
            max={Math.max(160, size.height / 2)}
            onChange={(terminalHeight) =>
              setPref((p) => ({ ...p, terminalHeight }))
            }
          />
          <TerminalDock
            project={project}
            terminals={terminals}
            open={pref.terminals}
            active={pref.activeTerminal}
            height={Math.min(pref.terminalHeight, size.height / 2)}
            onChange={(terminals, activeTerminal) =>
              setPref((p) => ({ ...p, terminals, activeTerminal }))
            }
            onClose={() => setPref((p) => ({ ...p, terminalOpen: false }))}
            onRefresh={onRefresh}
            onError={onError}
          />
        </>
      )}
    </div>
  );
}
