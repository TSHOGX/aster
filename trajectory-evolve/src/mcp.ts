#!/usr/bin/env bun
// mcp.ts — process entry for the host-side MCP server.
//
//   bun run src/mcp.ts              # stdio (spawned by a local client)
//   bun run src/mcp.ts --http 8104  # streamable-http for the sandbox

import "./bootstrap.ts";
import { serveHttp } from "./mcp/http.ts";
import { serveStdio } from "./mcp/stdio.ts";

const argv = process.argv.slice(2);
const httpIdx = argv.indexOf("--http");

if (httpIdx >= 0) {
  const port = Number(argv[httpIdx + 1] ?? process.env.TE_MCP_PORT ?? 8104);
  const hostname = process.env.TE_MCP_HOST ?? "0.0.0.0";
  const { url } = serveHttp(port, hostname);
  process.stderr.write(`trajectory-evolve MCP http ${url}\n`);
} else {
  await serveStdio();
}
