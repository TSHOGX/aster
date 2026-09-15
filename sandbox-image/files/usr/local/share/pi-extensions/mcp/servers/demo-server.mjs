#!/usr/bin/env node
/**
 * Minimal stdio MCP server for harness/lab smoke tests.
 * Tools: get_current_time, echo, add_numbers
 */
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";

const server = new Server(
  { name: "harness-demo-mcp", version: "1.0.0" },
  { capabilities: { tools: {} } },
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: "get_current_time",
      description: "Return the current UTC time as ISO-8601.",
      inputSchema: { type: "object", properties: {}, additionalProperties: false },
    },
    {
      name: "echo",
      description: "Echo back a message. Useful to verify MCP tool wiring.",
      inputSchema: {
        type: "object",
        required: ["message"],
        properties: { message: { type: "string", description: "Text to echo" } },
        additionalProperties: false,
      },
    },
    {
      name: "add_numbers",
      description: "Add two numbers and return the sum.",
      inputSchema: {
        type: "object",
        required: ["a", "b"],
        properties: {
          a: { type: "number" },
          b: { type: "number" },
        },
        additionalProperties: false,
      },
    },
  ],
}));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args = {} } = request.params;
  if (name === "get_current_time") {
    return { content: [{ type: "text", text: new Date().toISOString() }] };
  }
  if (name === "echo") {
    return { content: [{ type: "text", text: String(args.message ?? "") }] };
  }
  if (name === "add_numbers") {
    const sum = Number(args.a) + Number(args.b);
    return { content: [{ type: "text", text: String(sum) }] };
  }
  return {
    isError: true,
    content: [{ type: "text", text: `Unknown tool: ${name}` }],
  };
});

const transport = new StdioServerTransport();
await server.connect(transport);
