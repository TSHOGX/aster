import { afterEach, describe, expect, it, vi } from "vitest";
import { api, APIError, command } from "./project-api";
afterEach(() => vi.unstubAllGlobals());
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
    await expect(api("/api/projects/p/files")).rejects.toBeInstanceOf(APIError);
  });
});
