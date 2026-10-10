import { afterEach, describe, expect, it, vi } from "vitest";
import { api, APIError, command } from "./project-api";
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });
describe("Project command outcomes", () => {
  it("does not repeat a mutation when the reply is lost", async () => {
    const fetch = vi.fn().mockRejectedValue(new TypeError("connection reset"));
    vi.stubGlobal("fetch", fetch);
    await expect(
      command("/api/projects/p/files", {
        id: "original",
        operation: "remove",
        path: "result",
      }),
    ).rejects.toMatchObject({ status: 0, outcome: { state: "unknown" } });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it("does not mistake a truncated success response for confirmed completion", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("{", { status: 200 })),
    );
    await expect(command("/api/projects/p/files", {})).rejects.toMatchObject({
      outcome: { state: "unknown" },
    });
  });
  it("accepts asynchronous controls while pending reads stay distinguishable", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation(() =>
          Promise.resolve(
            new Response('{"status":"pending"}', { status: 202 }),
          ),
        ),
    );
    await expect(command("/api/projects/p/resume")).resolves.toEqual({
      status: "pending",
    });
    const pending = api("/api/projects/p/files").catch(error => error);
    await vi.runAllTimersAsync();
    expect(await pending).toBeInstanceOf(APIError);
  });
});

it("retains exact quota quantities and distinguishes them from read pressure",async()=>{
  vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({code:"quota_exceeded",quota:{dimension:"transfer_bytes",used:"0",reserved:"0",requested_delta:"9007199254740993",limit:"3"}}),{status:429})));
  await expect(api("/api/projects/p/files/content?path=file")).rejects.toMatchObject({status:429,message:expect.stringContaining("9,007,199,254,740,993"),outcome:{code:"quota_exceeded"}});
});
