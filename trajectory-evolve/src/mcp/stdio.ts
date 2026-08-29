// mcp/stdio.ts — JSON-RPC over newline-delimited stdin/stdout.
//
// For local clients (Cursor, Claude Code, a host-side pi) that spawn this
// process. Must not write anything except JSON-RPC to stdout — logs go to stderr.

import { handleRequest } from "./tools.ts";
import { fail, parseMessage } from "./protocol.ts";

export async function serveStdio(): Promise<void> {
  const decoder = new TextDecoder();
  let buf = "";
  for await (const chunk of Bun.stdin.stream()) {
    buf += decoder.decode(chunk, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      await handleLine(line);
    }
  }
  if (buf.trim()) await handleLine(buf.trim());
}

async function handleLine(line: string): Promise<void> {
  try {
    const msg = parseMessage(line);
    const res = await handleRequest(msg);
    if (res === null) return;
    process.stdout.write(JSON.stringify(res) + "\n");
  } catch (err) {
    process.stdout.write(
      JSON.stringify(fail(null, -32700, err instanceof Error ? err.message : String(err))) + "\n"
    );
  }
}
