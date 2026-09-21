"use client";
import { useT } from "@/lib/i18n";
import { useCallback, useEffect, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { attachTerminal, type TerminalSocket } from "@/lib/terminal-socket";
type Attachment = "connecting" | "live" | "readonly" | "ended" | "detached";
/**
 * One terminal panel: one emulator bound to one PTY through the application's
 * fan-out socket.
 *
 * Closing this panel closes a view, not the terminal. The PTY keeps running and
 * the Project stays awake, so reopening rejoins the same live process. Ending the
 * process is a separate, explicit action.
 */
export function TerminalPanel({
  projectId,
  terminalId,
  title,
  processState,
  processExitCode,
  wantInput = true,
  onClose,
}: {
  projectId: string;
  terminalId: string;
  title: string;
  processState: string;
  processExitCode?: number;
  wantInput?: boolean;
  onClose?: () => void;
}) {
  const tr = useT();
  const host = useRef<HTMLDivElement | null>(null);
  const socket = useRef<TerminalSocket | null>(null);
  const [attachment, setAttachment] = useState<Attachment>("connecting");
  const [notice, setNotice] = useState<string | null>(null);
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [reconnect, setReconnect] = useState(0);
  useEffect(() => {
    const element = host.current;
    if (!element) return;
    const term = new Terminal({
      convertEol: false,
      cursorBlink: true,
      fontFamily: "var(--font-mono, ui-monospace), monospace",
      fontSize: 13,
      scrollback: 5000,
      allowProposedApi: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(element);
    fit.fit();
    const connection = attachTerminal(projectId, terminalId, wantInput, {
      onData: (bytes) => term.write(bytes),
      onGap: (reason) => {
        // Missing output cannot be rendered. Reset rather than draw a screen that
        // looks plausible but is wrong, and say so.
        term.reset();
        setNotice(`Output is missing (${reason}). The screen was reset.`);
      },
      onStatus: ({ input }) => {
        setAttachment(input ? "live" : "readonly");
      },
      onExit: (code) => {
        setExitCode(code);
        setAttachment("ended");
      },
      onDetach: (reason) => {
        setAttachment("detached");
        setNotice(reason);
      },
    });
    socket.current = connection;
    // Keystrokes travel as raw bytes; the application never interprets them, so
    // paste, control characters and escape sequences pass through unchanged.
    const input = term.onData((data) => connection.send(data));
    const resize = () => {
      fit.fit();
      connection.resize(term.rows, term.cols);
    };
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    connection.resize(term.rows, term.cols);
    return () => {
      observer.disconnect();
      input.dispose();
      // Closing the socket detaches this view. It never stops the PTY.
      connection.close();
      socket.current = null;
      term.dispose();
    };
  }, [projectId, terminalId, wantInput, reconnect]);
  const claimInput = useCallback(() => socket.current?.claimInput(), []);
  const interrupt = useCallback(() => socket.current?.interrupt(), []);
  const eof = useCallback(() => socket.current?.eof(), []);
  return (
    <section className="flex min-h-0 flex-col overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <header className="flex items-center justify-between gap-2 border-b border-[var(--color-border)] px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-xs font-medium">{title}</span>
          <span className="text-2xs text-[var(--color-foreground-muted)]">
            {processState === "exited"
              ? `exited ${processExitCode ?? exitCode ?? ""}`
              : processState === "lost"
                ? "process lost"
                : describe(attachment, exitCode)}
          </span>
        </div>
        <div className="flex items-center gap-1.5">
          {attachment === "detached" && processState === "running" ? (
            <button onClick={() => setReconnect((n) => n + 1)}>
              {tr("Reconnect view")}
            </button>
          ) : null}
          {attachment === "readonly" ? (
            <button
              type="button"
              onClick={claimInput}
              className="rounded-md border border-[var(--color-border)] px-2 py-1 text-2xs hover:bg-[var(--color-surface-muted)]"
            >
              {tr("Take input")}
            </button>
          ) : null}
          {attachment === "live" ? (
            <>
              <button
                type="button"
                onClick={interrupt}
                title={tr("Send an interrupt to the foreground process")}
                className="rounded-md border border-[var(--color-border)] px-2 py-1 text-2xs hover:bg-[var(--color-surface-muted)]"
              >
                Ctrl-C
              </button>
              <button
                type="button"
                onClick={eof}
                title={tr("Close the terminal's input")}
                className="rounded-md border border-[var(--color-border)] px-2 py-1 text-2xs hover:bg-[var(--color-surface-muted)]"
              >
                Ctrl-D
              </button>
            </>
          ) : null}
          {onClose ? (
            <button
              type="button"
              onClick={onClose}
              title={tr("Close this view. The terminal keeps running.")}
              className="rounded-md border border-[var(--color-border)] px-2 py-1 text-2xs hover:bg-[var(--color-surface-muted)]"
            >
              {tr("Close view")}
            </button>
          ) : null}
        </div>
      </header>
      {notice ? (
        <p className="border-b border-[var(--color-border)] px-3 py-1.5 text-2xs text-[var(--color-foreground-muted)]">
          {notice}
        </p>
      ) : null}
      <div ref={host} className="min-h-0 flex-1 px-2 py-1.5" />
    </section>
  );
}
function describe(attachment: Attachment, exitCode: number | null) {
  switch (attachment) {
    case "connecting":
      return "attaching";
    case "live":
      return "input";
    case "readonly":
      return "read-only";
    case "ended":
      return exitCode === null ? "ended" : `exited ${exitCode}`;
    case "detached":
      // The view is gone; whether the process is still running is unknown here.
      return "view detached";
  }
}
