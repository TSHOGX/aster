// mcp/http.ts — streamable-http transport (the one the sandbox pi-mcp client uses).
//
// POST /mcp  JSON-RPC  → JSON-RPC
// GET  /healthz        → ok
//
// Stateless: every request is a complete JSON-RPC message. Session ids are not
// required; this server holds no per-sandbox secret. Bind on the host (0.0.0.0)
// and project harness-evolve.internal via data_proxies, same pattern as the
// dingpan MCP gateway — except there is no upstream token to inject.

import { handleRequest } from "./tools.ts";
import { fail, parseMessage, type JsonRpcResponse } from "./protocol.ts";

export function serveHttp(port: number, hostname = "0.0.0.0"): { url: string; stop: () => void } {
  const server = Bun.serve({
    port,
    hostname,
    async fetch(req) {
      const url = new URL(req.url);
      if (req.method === "GET" && (url.pathname === "/healthz" || url.pathname === "/")) {
        return Response.json({ ok: true, server: "trajectory-evolve" });
      }
      if (req.method === "POST" && (url.pathname === "/mcp" || url.pathname === "/")) {
        const raw = await req.text();
        try {
          const msg = parseMessage(raw);
          const res = await handleRequest(msg);
          if (res === null) return new Response(null, { status: 204 });
          return Response.json(res);
        } catch (err) {
          const body: JsonRpcResponse = fail(null, -32700, err instanceof Error ? err.message : String(err));
          return Response.json(body, { status: 400 });
        }
      }
      if (req.method === "OPTIONS") {
        return new Response(null, {
          status: 204,
          headers: {
            "Access-Control-Allow-Origin": "*",
            "Access-Control-Allow-Headers": "Content-Type, Mcp-Session-Id, Mcp-Protocol-Version",
            "Access-Control-Allow-Methods": "POST, GET, OPTIONS",
          },
        });
      }
      return new Response("not found", { status: 404 });
    },
  });
  return {
    url: `http://${hostname === "0.0.0.0" ? "127.0.0.1" : hostname}:${server.port}/mcp`,
    stop: () => server.stop(true),
  };
}
