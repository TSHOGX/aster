import { afterEach, expect, it, vi } from "vitest";
import { fetchFileRead } from "./file-read";

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
it("retries temporary pressure and a changing capture, then returns bytes", async () => {
  vi.useFakeTimers();
  const fetch = vi.fn()
    .mockResolvedValueOnce(new Response('{"error":"capacity exhausted"}', {status:503}))
    .mockResolvedValueOnce(new Response('{"code":"changed_during_read"}', {status:409}))
    .mockResolvedValueOnce(new Response("contents"));
  vi.stubGlobal("fetch", fetch);
  const pending = fetchFileRead("/files");
  await vi.runAllTimersAsync();
  expect(await (await pending).text()).toBe("contents");
  expect(fetch).toHaveBeenCalledTimes(3);
});
it("bounds retries and never retries authorization, missing files, stale cursors or writes", async () => {
  vi.useFakeTimers();
  for (const status of [401,403,404,409]) {
    const fetch = vi.fn(async () => new Response('{}', {status}));
    vi.stubGlobal("fetch", fetch);
    expect((await fetchFileRead("/files")).status).toBe(status);
    expect(fetch).toHaveBeenCalledTimes(1);
  }
  const fetch = vi.fn(async () => new Response('{}', {status:503}));
  vi.stubGlobal("fetch", fetch);
  const pending = fetchFileRead("/files");
  await vi.runAllTimersAsync();
  expect((await pending).status).toBe(503);
  expect(fetch).toHaveBeenCalledTimes(7);
  await expect(fetchFileRead("/files", {method:"PUT"})).rejects.toThrow("cannot submit");
  expect(fetch).toHaveBeenCalledTimes(7);
});
it("canceling a view cancels its delayed retry", async () => {
  vi.useFakeTimers();
  const fetch = vi.fn(async () => new Response('{}', {status:503}));
  vi.stubGlobal("fetch", fetch);
  const abort = new AbortController();
  const result = fetchFileRead("/files", {signal:abort.signal}).catch(e => e.name);
  await vi.advanceTimersByTimeAsync(1);
  abort.abort();
  expect(await result).toBe("AbortError");
  await vi.runAllTimersAsync();
  expect(fetch).toHaveBeenCalledTimes(1);
});
