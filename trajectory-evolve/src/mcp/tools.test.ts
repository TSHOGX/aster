import { expect, test } from "bun:test";
import { mkdirSync, writeFileSync, unlinkSync, existsSync } from "node:fs";
import { dirname } from "node:path";
import { handleRequest, TOOLS } from "./tools.ts";
import { serveHttp } from "./http.ts";
import { cycleLockPath } from "../jobs.ts";

test("tools/list exposes only sandbox-safe tools", async () => {
  const res = await handleRequest({ jsonrpc: "2.0", id: 1, method: "tools/list" });
  expect(res).not.toBeNull();
  expect(res!.error).toBeUndefined();
  const names = (res!.result as { tools: { name: string }[] }).tools.map((t) => t.name);
  expect(names).toEqual(["evolve_status", "evolve_run", "evolve_job", "kb_status", "kb_diff"]);
  expect(names).not.toContain("kb_approve");
  expect(names).not.toContain("ext_activate");
});

test("initialize returns server info", async () => {
  const res = await handleRequest({
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "t", version: "0" } },
  });
  expect((res!.result as { serverInfo: { name: string } }).serverInfo.name).toBe("trajectory-evolve");
});

test("unknown tool is a tool error, not a publish path", async () => {
  const res = await handleRequest({
    jsonrpc: "2.0",
    id: 2,
    method: "tools/call",
    params: { name: "kb_approve", arguments: { snapshot_id: "x" } },
  });
  expect(res!.error).toBeUndefined();
  const body = JSON.parse((res!.result as { content: { text: string }[]; isError: boolean }).content[0].text);
  expect((res!.result as { isError: boolean }).isError).toBe(true);
  expect(body.error).toBe("forbidden");
});

test("evolve_status returns JSON", async () => {
  const res = await handleRequest({
    jsonrpc: "2.0",
    id: 3,
    method: "tools/call",
    params: { name: "evolve_status", arguments: {} },
  });
  expect(res!.error).toBeUndefined();
  const parsed = JSON.parse((res!.result as { content: { text: string }[] }).content[0].text);
  expect(parsed).toHaveProperty("pains");
  expect(parsed).toHaveProperty("quadrants");
  expect(parsed).toHaveProperty("live");
});

test("evolve_run skips when cycle lock is held by a live pid", async () => {
  const lock = cycleLockPath();
  mkdirSync(dirname(lock), { recursive: true });
  writeFileSync(lock, `${process.pid}\n`);
  try {
    const res = await handleRequest({
      jsonrpc: "2.0",
      id: 4,
      method: "tools/call",
      params: { name: "evolve_run", arguments: {} },
    });
    const parsed = JSON.parse((res!.result as { content: { text: string }[] }).content[0].text);
    expect(parsed.status).toBe("skipped");
    expect(parsed.skipped).toBe(true);
    expect(parsed.reason).toBe("already_running");
  } finally {
    if (existsSync(lock)) unlinkSync(lock);
  }
});

test("TOOLS schemas never mention approve or activate", () => {
  const blob = JSON.stringify(TOOLS);
  expect(blob).not.toContain("kb_approve");
  expect(blob).not.toContain("ext_activate");
});

test("HTTP initialize + tools/list + evolve_status + healthz", async () => {
  const { url, stop } = serveHttp(0, "127.0.0.1");
  try {
    const health = await fetch(new URL("/healthz", url));
    expect(health.ok).toBe(true);
    expect(((await health.json()) as { server: string }).server).toBe("trajectory-evolve");

    const rpc = async (method: string, params?: unknown, id = 1) => {
      const r = await fetch(url, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ jsonrpc: "2.0", id, method, params }),
      });
      return r.json() as Promise<{ result?: unknown; error?: unknown }>;
    };

    const init = await rpc("initialize", {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: { name: "t", version: "0" },
    });
    expect((init.result as { serverInfo: { name: string } }).serverInfo.name).toBe("trajectory-evolve");

    const listed = await rpc("tools/list");
    const names = (listed.result as { tools: { name: string }[] }).tools.map((t) => t.name);
    expect(names).toContain("evolve_status");
    expect(names).not.toContain("kb_approve");

    const status = await rpc("tools/call", { name: "evolve_status", arguments: {} }, 3);
    const text = (status.result as { content: { text: string }[] }).content[0].text;
    expect(JSON.parse(text)).toHaveProperty("quadrants");
  } finally {
    stop();
  }
});
