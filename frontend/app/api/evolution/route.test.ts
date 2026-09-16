import { NextRequest } from "next/server";
import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => { vi.unstubAllEnvs(); vi.unstubAllGlobals(); vi.resetModules(); });

async function load(configured = true, viewers = "reviewer") {
  vi.resetModules();
  vi.stubEnv("EVOLUTION_STATE_ROOT", configured ? "/synthetic/evolution" : "");
  vi.stubEnv("EVOLUTION_PROJECT_ROOT", configured ? "/synthetic/runner" : "");
  vi.stubEnv("EVOLUTION_MEMORY_DB", configured ? "/synthetic/memory.db" : "");
  vi.stubEnv("EVOLUTION_VIEWERS", viewers);
  return import("./[...path]/route");
}

describe("optional external evolution boundary", () => {
  it("returns unavailable without configuration and does not touch the backend", async () => {
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    const { GET } = await load(false);
    const r = await GET(new NextRequest("http://localhost/api/evolution/pending"), { params: Promise.resolve({ path: ["pending"] }) });
    expect(r.status).toBe(503); expect(fetch).not.toHaveBeenCalled();
  });
  it("denies an authenticated user without an explicit allowlist", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ user_id: "reviewer" })));
    const { GET } = await load(true, "");
    const r = await GET(new NextRequest("http://localhost/api/evolution/pending"), { params: Promise.resolve({ path: ["pending"] }) });
    expect(r.status).toBe(403);
  });
  it("returns empty real state rather than a business demo when configured files are absent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ user_id: "reviewer" })));
    const { GET } = await load();
    const r = await GET(new NextRequest("http://localhost/api/evolution/pending"), { params: Promise.resolve({ path: ["pending"] }) });
    expect(r.status).toBe(200);
    const body = await r.json();
    expect(body.pending).toBeNull(); expect(body.detail.tables).toEqual([]); expect(body.detail.condition_plans).toEqual([]);
    expect(body.report).toBe("No report is available.");
  });
});
