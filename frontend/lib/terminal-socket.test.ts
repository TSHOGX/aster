import { describe, expect, it, vi } from "vitest";

import { attachTerminal, terminalSocketUrl } from "./terminal-socket";

type Handler = ((event: unknown) => void) | null;

class FakeSocket {
  static last: FakeSocket | null = null;
  static OPEN = 1;
  readyState = 1;
  binaryType = "blob";
  sent: Array<string | Uint8Array> = [];
  closed = false;
  onmessage: Handler = null;
  onclose: Handler = null;
  onerror: Handler = null;
  constructor(readonly url: string) {
    FakeSocket.last = this;
  }
  send(data: string | Uint8Array) {
    this.sent.push(data);
  }
  close() {
    this.closed = true;
  }
  deliver(data: unknown) {
    this.onmessage?.({ data });
  }
}

function install() {
  vi.stubGlobal("WebSocket", FakeSocket as unknown as typeof WebSocket);
  vi.stubGlobal("window", { location: { origin: "https://app.example" } });
}

function handlers() {
  return {
    onData: vi.fn(),
    onGap: vi.fn(),
    onStatus: vi.fn(),
    onExit: vi.fn(),
    onDetach: vi.fn()
  };
}

describe("terminal socket url", () => {
  it("is same-origin and upgrades the scheme", () => {
    install();
    expect(terminalSocketUrl("p", "t", true)).toBe(
      "wss://app.example/api/projects/p/terminals/t/attach?input=1"
    );
    // A read-only attachment does not ask for the input claim at all.
    expect(terminalSocketUrl("p", "t", false)).toBe(
      "wss://app.example/api/projects/p/terminals/t/attach"
    );
  });

  it("escapes identifiers rather than interpolating them into the path", () => {
    install();
    expect(terminalSocketUrl("a/b", "c d", false)).toContain("/projects/a%2Fb/terminals/c%20d/");
  });
});

describe("terminal socket protocol", () => {
  it("passes raw bytes through untouched", () => {
    install();
    const h = handlers();
    attachTerminal("p", "t", true, h);
    const socket = FakeSocket.last!;
    const payload = new Uint8Array([0x1b, 0x5b, 0x31, 0x6d, 0xe4, 0xb8, 0x96, 0x07, 0x00]);
    socket.deliver(payload.buffer);
    expect(h.onData).toHaveBeenCalledTimes(1);
    expect(Array.from(h.onData.mock.calls[0][0] as Uint8Array)).toEqual(Array.from(payload));
  });

  it("only sends input while this view holds the claim", () => {
    install();
    const h = handlers();
    const connection = attachTerminal("p", "t", true, h);
    const socket = FakeSocket.last!;

    // Before any status frame the view holds nothing, so keystrokes are dropped
    // rather than sent somewhere they would be refused.
    connection.send("ls\r");
    expect(socket.sent).toHaveLength(0);

    socket.deliver(JSON.stringify({ kind: "status", rows: 24, cols: 80, input: false }));
    expect(h.onStatus).toHaveBeenLastCalledWith({ rows: 24, cols: 80, input: false });
    connection.send("ls\r");
    expect(socket.sent).toHaveLength(0);

    socket.deliver(JSON.stringify({ kind: "status", rows: 24, cols: 80, input: true }));
    connection.send("ls\r");
    expect(socket.sent).toHaveLength(1);
    expect(Array.from(socket.sent[0] as Uint8Array)).toEqual([108, 115, 13]);
  });

  it("reports a gap so the renderer can reset instead of drawing missing output", () => {
    install();
    const h = handlers();
    attachTerminal("p", "t", true, h);
    FakeSocket.last!.deliver(JSON.stringify({ kind: "gap", reason: "replay ring exceeded" }));
    expect(h.onGap).toHaveBeenCalledWith("replay ring exceeded");
  });

  it("distinguishes an exit from a detached view", () => {
    install();
    const exited = handlers();
    attachTerminal("p", "t", true, exited);
    const first = FakeSocket.last!;
    first.deliver(JSON.stringify({ kind: "exit", exit_code: 130 }));
    expect(exited.onExit).toHaveBeenCalledWith(130);
    // A close after a settled exit is not a second, contradictory event.
    first.onclose?.({});
    expect(exited.onDetach).not.toHaveBeenCalled();

    const dropped = handlers();
    attachTerminal("p", "t", true, dropped);
    FakeSocket.last!.onclose?.({});
    expect(dropped.onDetach).toHaveBeenCalledWith("disconnected");
    // A dropped socket says nothing about whether the process ended.
    expect(dropped.onExit).not.toHaveBeenCalled();
  });

  it("reports a lost placement as a detachment with its reason, not an exit", () => {
    install();
    const h = handlers();
    attachTerminal("p", "t", true, h);
    FakeSocket.last!.deliver(JSON.stringify({ kind: "lost", reason: "Project paused" }));
    expect(h.onDetach).toHaveBeenCalledWith("Project paused");
    expect(h.onExit).not.toHaveBeenCalled();
  });

  it("expresses only the supported control frames", () => {
    install();
    const connection = attachTerminal("p", "t", true, handlers());
    const socket = FakeSocket.last!;
    connection.resize(40, 120);
    connection.claimInput();
    connection.eof();
    connection.interrupt();
    connection.signal(15);
    // Ctrl-C is its own frame, not a directed signal: the application writes the
    // interrupt byte to the PTY so the shell itself survives it.
    expect(socket.sent.map((frame) => JSON.parse(String(frame)))).toEqual([
      { type: "resize", rows: 40, cols: 120 },
      { type: "claim_input" },
      { type: "eof" },
      { type: "interrupt" },
      { type: "signal", signal: 15 }
    ]);
  });

  it("ignores a malformed control frame rather than throwing into the renderer", () => {
    install();
    const h = handlers();
    attachTerminal("p", "t", true, h);
    FakeSocket.last!.deliver("not json");
    FakeSocket.last!.deliver(JSON.stringify({ kind: "unknown-kind" }));
    expect(h.onData).not.toHaveBeenCalled();
    expect(h.onGap).not.toHaveBeenCalled();
    expect(h.onDetach).not.toHaveBeenCalled();
  });
});
