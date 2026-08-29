// engines/mock.ts — an offline, deterministic engine adapter.
//
// dynamic-workflow treats `engine` as an open string with adapters supplied per
// run, so a fake CLI is a first-class engine rather than a test hack. This one
// spawns tools/mock-agent.ts, which answers each node from rules over the node's
// own input. That buys two things:
//
//   1. the whole pipeline runs with no credentials and no network, so the shape
//      of the graph can be reviewed before a single token is spent;
//   2. every run is byte-reproducible, so a change in output means a change in
//      the pipeline rather than model variance.
//
// Switching to a real CLI is `--engine pi`: same specs, same graph, same journal.

import { resolve } from "node:path";
import type { Engine, RunEvent } from "dynamic-workflow";
import { paths } from "../config.ts";

const SCRIPT = resolve(paths.root, "tools", "mock-agent.ts");

export const mock: Engine = {
  name: "mock",
  // It honors neither, and declaring that is what makes the kernel warn instead
  // of silently dropping a sampling/ephemeral request (no-silent-caps).
  supportsSampling: false,
  supportsEphemeral: true,

  buildArgv() {
    // `bun` resolves from node_modules/.bin, which the CLI prepends to PATH.
    return { cmd: ["bun", "run", SCRIPT], promptVia: "stdin" };
  },

  parseLine(line: string): RunEvent | null {
    const t = line.trim();
    if (!t) return null;
    let e: any;
    try {
      e = JSON.parse(t);
    } catch {
      return null;
    }
    switch (e.type) {
      case "session":
        return { kind: "start", sessionId: e.id };
      case "text":
        return { kind: "text", text: e.text ?? "" };
      case "error":
        return { kind: "error", message: e.message ?? "mock error" };
      case "end":
        return { kind: "end", status: e.status ?? "ok", tokens: e.tokens };
      default:
        return null;
    }
  },
};
