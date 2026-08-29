// mcp/tools.ts — the sandbox-visible surface.
//
// Deliberately smaller than the CLI. A tool an agent can call is a tool an
// agent can abuse; approve / reject / ext-activate would let a sandbox publish
// knowledge into every future session, so they stay expert-CLI-only.
//
// Architecture independence is the point: pi today prefixes these as
// `mcp_trajectory_evolve_<name>`; a future agent that speaks MCP will call the
// same five names. The skill in /agents tells whoever is reading WHEN to call.

import { getJob, startCycleJob } from "../jobs.ts";
import { opKbDiff, opKbStatus, opStatus, parseTrack } from "../ops.ts";
import { fail, ok, textResult, type JsonRpcRequest, type JsonRpcResponse, type McpTool } from "./protocol.ts";

export const TOOLS: McpTool[] = [
  {
    name: "evolve_status",
    description:
      "Read-only snapshot of the self-evolution pipeline: live/pending KB digest, " +
      "quality quadrants (miss/noise/unused/unusable), cross-session query pains, " +
      "retrieval gaps, extractor versions. Call this first. Zero LLM cost. " +
      "If pains is empty AND miss=0, a cycle would be a no-op — do not start one.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "evolve_run",
    description:
      "Start a self-evolution cycle on the HOST (not in this sandbox). Returns a " +
      "job id immediately — poll evolve_job. The cycle STAGES a KB snapshot; it " +
      "does NOT publish. This session will not see the new snapshot; only NEW " +
      "sessions after an expert approves. Never invent approve/publish tools: they " +
      "do not exist on this server. Default: ingest+observe+harvest+all tracks.",
    inputSchema: {
      type: "object",
      additionalProperties: false,
      properties: {
        track: {
          type: "string",
          enum: ["kb", "tables", "extractors", "all"],
          description: "Which evolution track. Default all.",
        },
        ingest: { type: "boolean", description: "Re-read trajectory files. Default true." },
        observe: { type: "boolean", description: "Rebuild observations/pains. Default true." },
        harvest: { type: "boolean", description: "Re-run extraction lenses. Default true." },
      },
    },
  },
  {
    name: "evolve_job",
    description:
      "Poll a job started by evolve_run. status is queued|running|done|error|skipped. " +
      "skipped means a nightly/other cycle holds the lock — do not retry-spam; tell the user.",
    inputSchema: {
      type: "object",
      required: ["job_id"],
      additionalProperties: false,
      properties: { job_id: { type: "string" } },
    },
  },
  {
    name: "kb_status",
    description:
      "Live vs pending KB digest. pending != null means a snapshot is waiting for " +
      "expert approval (`harness-ctl` / `cli kb approve`). This session is pinned to live.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "kb_diff",
    description:
      "File-level diff between the live snapshot and the pending one. Empty if nothing is pending.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
];

const FORBIDDEN = new Set([
  "kb_approve",
  "kb_reject",
  "approve",
  "publish",
  "ext_activate",
  "activate",
]);

export async function handleRequest(msg: JsonRpcRequest): Promise<JsonRpcResponse | null> {
  const id = msg.id ?? null;
  // Notifications (no id) are acknowledged by silence.
  if (msg.id === undefined && msg.method.startsWith("notifications/")) return null;

  switch (msg.method) {
    case "initialize":
      return ok(id, {
        protocolVersion: "2024-11-05",
        capabilities: { tools: { listChanged: false } },
        serverInfo: { name: "trajectory-evolve", version: "0.1.0" },
        instructions:
          "Host-side self-evolution. Call evolve_status before evolve_run. " +
          "Cycles STAGE only; there is no approve tool. Poll evolve_job. " +
          "See /agents/skills/self-evolve/SKILL.md.",
      });
    case "notifications/initialized":
    case "initialized":
      return null;
    case "ping":
      return ok(id, {});
    case "tools/list":
      return ok(id, { tools: TOOLS });
    case "tools/call":
      return ok(id, await callTool(msg.params));
    default:
      if (FORBIDDEN.has(msg.method) || (typeof (msg.params as { name?: string })?.name === "string" && FORBIDDEN.has((msg.params as { name: string }).name))) {
        return fail(id, -32601, "that operation is expert-CLI-only; this MCP server cannot publish or activate");
      }
      return fail(id, -32601, `method not found: ${msg.method}`);
  }
}

async function callTool(params: unknown): Promise<ReturnType<typeof textResult>> {
  const p = (params ?? {}) as { name?: string; arguments?: Record<string, unknown> };
  const name = p.name ?? "";
  const args = p.arguments ?? {};
  if (FORBIDDEN.has(name)) {
    return textResult(
      { error: "forbidden", message: `${name} is expert-CLI-only; sandbox agents cannot publish the shared KB` },
      true
    );
  }
  try {
    switch (name) {
      case "evolve_status":
        return textResult(opStatus());
      case "kb_status":
        return textResult(opKbStatus());
      case "kb_diff":
        return textResult(opKbDiff());
      case "evolve_run": {
        const track = parseTrack(typeof args.track === "string" ? args.track : "all");
        const job = startCycleJob({
          track,
          ingest: args.ingest !== false,
          observe: args.observe !== false,
          harvest: args.harvest !== false,
        });
        return textResult({
          job_id: job.id,
          status: job.status,
          skipped: job.status === "skipped",
          reason: job.status === "skipped" ? "already_running" : undefined,
          error: job.error || undefined,
          poll: "evolve_job",
          note: job.status === "skipped"
            ? "another cycle holds the lock; do not retry-spam"
            : "poll evolve_job until done/error/skipped. Result is STAGED, not live.",
        });
      }
      case "evolve_job": {
        const jobId = String(args.job_id ?? "");
        if (!jobId) return textResult({ error: "job_id required" }, true);
        const job = getJob(jobId);
        if (!job) return textResult({ error: `unknown job ${jobId}` }, true);
        return textResult(job);
      }
      default:
        return textResult({ error: `unknown tool ${name}`, known: TOOLS.map((t) => t.name) }, true);
    }
  } catch (err) {
    return textResult({ error: err instanceof Error ? err.message : String(err) }, true);
  }
}
