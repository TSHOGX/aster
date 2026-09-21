/**
 * Terminal transport client.
 *
 * One WebSocket per attached Terminal. Binary frames are raw PTY bytes in both
 * directions; text frames are the small JSON control protocol. Nothing here
 * interprets terminal output — that is the emulator's job — and nothing here
 * replays unacknowledged input after a reconnect: repeating keystrokes into a
 * live shell is a new side effect, not a recovery.
 */

export type TerminalStatusFrame = {
  kind: "status";
  rows?: number;
  cols?: number;
  input?: boolean;
};

export type TerminalGapFrame = { kind: "gap"; reason?: string };
export type TerminalExitFrame = { kind: "exit"; exit_code?: number };
export type TerminalLostFrame = { kind: "lost"; reason?: string };
export type TerminalErrorFrame = { kind: "error"; reason?: string };

export type TerminalControlFrame =
  | TerminalStatusFrame
  | TerminalGapFrame
  | TerminalExitFrame
  | TerminalLostFrame
  | TerminalErrorFrame;

export type TerminalSocketHandlers = {
  /** Raw PTY bytes. Write them to the emulator unchanged. */
  onData: (bytes: Uint8Array) => void;
  /**
   * Output is missing. The renderer must reset rather than draw a screen out of
   * bytes it never received.
   */
  onGap: (reason: string) => void;
  onStatus: (status: { rows?: number; cols?: number; input: boolean }) => void;
  /** The process ended, with the exit code its terminal frame reported. */
  onExit: (exitCode: number | null) => void;
  /**
   * The attachment ended without an exit: the socket closed, or the placement
   * that held the PTY is gone. The process may or may not still be running, so
   * this is deliberately distinct from onExit.
   */
  onDetach: (reason: string) => void;
};

export type TerminalSocket = {
  /** Send raw keystrokes. Ignored while this view is read-only. */
  send: (data: string) => void;
  resize: (rows: number, cols: number) => void;
  /** Ask for the single input claim. Succeeds only when no live view holds it. */
  claimInput: () => void;
  /** Ctrl-D equivalent: closes the PTY's input. The shell decides whether to exit. */
  eof: () => void;
  /**
   * Ctrl-C. The application writes the interrupt character to the PTY so the tty
   * line discipline signals the foreground process group; the shell itself keeps
   * running. This is not a directed signal.
   */
  interrupt: () => void;
  /** Termination only. Interrupting is the `interrupt` frame. */
  signal: (signal: 15 | 9) => void;
  close: () => void;
};

export function terminalSocketUrl(projectId: string, terminalId: string, input: boolean) {
  const base = typeof window !== "undefined" ? window.location.origin : "http://127.0.0.1:8090";
  const url = new URL(
    `/api/projects/${encodeURIComponent(projectId)}/terminals/${encodeURIComponent(terminalId)}/attach`,
    base
  );
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  if (input) url.searchParams.set("input", "1");
  return url.toString();
}

export function attachTerminal(
  projectId: string,
  terminalId: string,
  wantInput: boolean,
  handlers: TerminalSocketHandlers
): TerminalSocket {
  const socket = new WebSocket(terminalSocketUrl(projectId, terminalId, wantInput));
  socket.binaryType = "arraybuffer";
  let holdsInput = false;
  let settled = false;

  const control = (message: Record<string, unknown>) => {
    if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
  };

  socket.onmessage = (event) => {
    if (event.data instanceof ArrayBuffer) {
      handlers.onData(new Uint8Array(event.data));
      return;
    }
    let frame: TerminalControlFrame;
    try {
      frame = JSON.parse(String(event.data)) as TerminalControlFrame;
    } catch {
      return;
    }
    switch (frame.kind) {
      case "status":
        holdsInput = frame.input === true;
        handlers.onStatus({ rows: frame.rows, cols: frame.cols, input: holdsInput });
        break;
      case "gap":
        handlers.onGap(frame.reason || "output is missing");
        break;
      case "exit":
        settled = true;
        handlers.onExit(typeof frame.exit_code === "number" ? frame.exit_code : null);
        break;
      case "lost":
        settled = true;
        handlers.onDetach(frame.reason || "the sandbox holding this terminal was replaced");
        break;
      case "error":
        handlers.onGap(frame.reason || "terminal stream error");
        break;
    }
  };

  // A closed socket is a detached view, not a stopped process. Only an exit frame
  // establishes that the PTY ended.
  socket.onclose = () => {
    holdsInput = false;
    if (!settled) handlers.onDetach("disconnected");
  };
  socket.onerror = () => {
    if (!settled) handlers.onDetach("connection failed");
  };

  return {
    send: (data: string) => {
      if (!holdsInput || socket.readyState !== WebSocket.OPEN) return;
      socket.send(new TextEncoder().encode(data));
    },
    resize: (rows: number, cols: number) => control({ type: "resize", rows, cols }),
    claimInput: () => control({ type: "claim_input" }),
    eof: () => control({ type: "eof" }),
    interrupt: () => control({ type: "interrupt" }),
    signal: (signal) => control({ type: "signal", signal }),
    close: () => socket.close()
  };
}
