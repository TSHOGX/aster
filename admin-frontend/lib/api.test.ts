import { afterEach, expect, it, vi } from "vitest";
import { api, APIError, mutate } from "./api";
afterEach(() => vi.unstubAllGlobals());
it("does not replay an uncertain administrative mutation", async () => {
  const fetch = vi.fn().mockRejectedValue(new TypeError("connection lost")); vi.stubGlobal("fetch", fetch);
  await expect(mutate("/api/admin/users", { request_id: "original", username: "user" })).rejects.toMatchObject({ status: 0, code: "outcome_unknown" });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0][1]).toMatchObject({ credentials: "same-origin", cache: "no-store" });
});
it("preserves conflict codes and distinguishes reads from uncertain writes", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "revision_conflict", error: "changed" }), { status: 409 })));
  await expect(api("/api/admin/users")).rejects.toEqual(new APIError(409, "revision_conflict", "changed"));
  await expect(api("https://foreign/api/admin/users")).rejects.toThrow("same-origin");
});
