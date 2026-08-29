// mcp/protocol.ts — JSON-RPC 2.0 helpers for a tiny MCP server.
//
// Implemented by hand on purpose: the contract this package exposes is five
// tools, not a framework. A dependency on an MCP SDK would couple the host
// process to a client library's versioning; the protocol is stable enough to
// speak directly.

export interface JsonRpcRequest {
  jsonrpc: "2.0";
  id?: string | number | null;
  method: string;
  params?: unknown;
}

export interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: string | number | null;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

export const PROTOCOL_VERSION = "2024-11-05";

export function ok(id: string | number | null, result: unknown): JsonRpcResponse {
  return { jsonrpc: "2.0", id, result };
}

export function fail(
  id: string | number | null,
  code: number,
  message: string,
  data?: unknown
): JsonRpcResponse {
  return { jsonrpc: "2.0", id, error: { code, message, ...(data !== undefined ? { data } : {}) } };
}

export function parseMessage(raw: string): JsonRpcRequest {
  const msg = JSON.parse(raw) as JsonRpcRequest;
  if (msg.jsonrpc !== "2.0" || typeof msg.method !== "string") {
    throw new Error("not a JSON-RPC 2.0 request");
  }
  return msg;
}

export interface McpTool {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

export function textResult(obj: unknown, isError = false): {
  content: Array<{ type: "text"; text: string }>;
  isError: boolean;
} {
  return {
    content: [{ type: "text", text: typeof obj === "string" ? obj : JSON.stringify(obj, null, 2) }],
    isError,
  };
}
