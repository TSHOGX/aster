import { writeFile } from "node:fs/promises";
import { expect, test, type Page } from "@playwright/test";
import { messages } from "./fixtures";

async function fixture(page: Page, count = 10000, simulatedRtt = 0) {
  const calls: string[] = [];
  const data = { a: messages(count, "a"), b: messages(count, "b") };
  await page.addInitScript(() => {
    const tasks: { start: number; duration: number }[] = [];
    new PerformanceObserver((list) => { for (const entry of list.getEntries()) tasks.push({ start: entry.startTime, duration: entry.duration }); }).observe({ type: "longtask", buffered: true });
    Object.defineProperty(window, "__longTasks", { value: tasks });
    class Source {
      static CLOSED = 2;
      readyState = 1;
      onopen?: () => void;
      onerror?: () => void;
      constructor() {
        setTimeout(() => this.onopen?.(), 10);
      }
      private listeners = new Map<string, (event: { data: string }) => void>();
      addEventListener(
        name: string,
        listener: (event: { data: string }) => void,
      ) {
        this.listeners.set(name, listener);
        (
          window as unknown as { __emit: (event: { type: string }) => void }
        ).__emit = (event) =>
          this.listeners.get(event.type)?.({ data: JSON.stringify(event) });
      }
      removeEventListener(name: string) {
        this.listeners.delete(name);
      }
      close() {}
    }
    Object.defineProperty(window, "EventSource", { value: Source });
  });
  await page.route("**/api/**", async (route) => {
    const u = new URL(route.request().url());
    calls.push(u.pathname);
    let body: unknown = {};
    if (u.pathname === "/api/me")
      body = { auth_required: false, user_id: "synthetic" };
    else if (u.pathname === "/api/healthz") body = { status: "ok" };
    else if (u.pathname === "/api/sessions")
      body = {
        sessions: ["a", "b"].map((id) => ({
          id,
          title: `Conversation ${id}`,
          mode: "agent",
          status: "running_idle",
          created_at: "2026-09-18T00:00:00Z",
          updated_at: "2026-09-18T00:00:00Z",
          access: "owner",
        })),
      };
    else if (u.pathname === "/api/deployment-capabilities")
      body = {
        schema_version: 1,
        message_pages: true,
        artifact_pages: true,
        default_mode: "agent",
        session_modes: [],
      };
    else if (u.pathname.endsWith("/runtime"))
      body = {
        session_id: u.pathname.split("/")[3],
        revision: 1,
        session_status: "running_idle",
        active_turn: null,
        queued_turns: [],
      };
    else if (u.pathname.endsWith("/messages/page")) {
      const id = u.pathname.split("/")[3] as "a" | "b";
      let list = data[id];
      const before = Number(u.searchParams.get("before_id")),
        after = Number(u.searchParams.get("after_id")),
        around = Number(u.searchParams.get("around_id"));
      if (around)
        list = list.filter((m) => m.id >= around - 20 && m.id < around + 20);
      else if (before) list = list.filter((m) => m.id < before).slice(-40);
      else if (after) list = list.filter((m) => m.id > after).slice(0, 40);
      else list = list.slice(-40);
      body = {
        messages: list,
        has_older: (list[0]?.id ?? 1) > 1,
        has_newer: (list.at(-1)?.id ?? count) < count,
        before_id: list[0]?.id ?? 0,
        after_id: list.at(-1)?.id ?? 0,
        latest_id: count,
        latest_final_id: count - 1,
        interruption: "",
      };
    } else if (u.pathname.endsWith("/artifacts/children"))
      body = {
        entries: Array.from({ length: 200 }, (_, i) => ({
          kind: "file",
          path: `file${i}.txt`,
          name: `file${i}.txt`,
          size: 100,
          file_count: 1,
          updated_at: "2026-09-18T00:00:00Z",
        })),
        has_more: false,
        next_cursor: "",
      };
    else if (u.pathname.endsWith("/mentioned-files")) body = { files: [] };
    else if (u.pathname === "/api/notifications") body = { notifications: [] };
    if (simulatedRtt)
      await new Promise((done) =>
        setTimeout(
          done,
          simulatedRtt + (Buffer.byteLength(JSON.stringify(body)) * 8) / 20000,
        ),
      );
    await route.fulfill({ json: body });
  });
  return calls;
}

test("long conversations use paged APIs and bound mounted rows across switches", async ({
  page,
}) => {
  const calls = await fixture(page);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await expect(page.locator('[data-message-id="10000"]')).toBeInViewport();
  expect(await page.locator("[data-message-id]").count()).toBeLessThan(40);
  await page
    .getByRole("button", { name: "Conversation b", exact: true })
    .click();
  await expect(page.locator("main header")).toContainText("Conversation b");
  await expect(page.locator('[data-message-id="10000"]')).toBeInViewport();
  await page
    .getByRole("button", { name: "Conversation a", exact: true })
    .click();
  await expect(page.locator("main header")).toContainText("Conversation a");
  expect(calls.some((path) => /\/messages$|\/artifacts$/.test(path))).toBe(
    false,
  );
  expect(errors).toEqual([]);
});

test("prepend preserves a visible message and switching restores it", async ({
  page,
}) => {
  await fixture(page, 500);
  await page.goto("/");
  await expect(page.locator('[data-message-id="500"]')).toBeInViewport();
  const scroller = page.locator("main [data-virtuoso-scroller]");
  await expect
    .poll(() => scroller.evaluate((el) => el.scrollTop))
    .toBeGreaterThan(1000);
  // Wait for the library's initial tail measurement to settle before simulating
  // a user's scroll; the initial probe row is already visible earlier.
  await expect
    .poll(() =>
      scroller.evaluate((el) =>
        Math.abs(el.scrollHeight - el.scrollTop - el.clientHeight),
      ),
    )
    .toBeLessThan(5);
  await page.evaluate(
    () =>
      new Promise<void>((done) =>
        requestAnimationFrame(() => requestAnimationFrame(() => done())),
      ),
  );
  await scroller.hover();
  await page.mouse.wheel(0, -20000);

  await expect(page.locator('[data-message-id="460"]')).toBeAttached();
  const anchorRow = page.locator('[data-message-id="461"]');
  await expect(anchorRow).toBeInViewport();
  await expect
    .poll(async () => Math.abs((await anchorRow.boundingBox())!.y - 50.5))
    .toBeLessThanOrEqual(8);
  const visible = { id: "461", top: (await anchorRow.boundingBox())!.y };
  await page
    .getByRole("button", { name: "Conversation b", exact: true })
    .click();
  await expect(page.locator("main header")).toContainText("Conversation b");
  await page
    .getByRole("button", { name: "Conversation a", exact: true })
    .click();
  const row = page.locator(`[data-message-id="${visible!.id}"]`);
  await expect(row).toBeInViewport();
  await expect
    .poll(async () => Math.abs((await row.boundingBox())!.y - visible!.top))
    .toBeLessThanOrEqual(8);
});

for (const count of [500, 10000])
  test(`performance: ${count} message history stays bounded over 30 switches`, async ({
    page,
  }, info) => {
    await fixture(page, count);
    await page.goto("/");
    await expect(page.locator(`[data-message-id="${count}"]`)).toBeInViewport();
    const durations: number[] = [];
    const switchesStart = await page.evaluate(() => performance.now());
    for (let i = 0; i < 30; i++) {
      const id = i % 2 ? "a" : "b";
      const start = await page.evaluate(() => performance.now());
      await page
        .getByRole("button", { name: `Conversation ${id}`, exact: true })
        .click();
      await expect(page.locator("main header")).toContainText(
        `Conversation ${id}`,
      );
      await expect(
        page.locator(`[data-message-id="${count}"]`),
      ).toBeInViewport();
      await page.evaluate(
        () => new Promise<void>((done) => requestAnimationFrame(() => done())),
      );
      const duration = await page.evaluate((since) => {
        const read = (
          window as unknown as {
            __workbenchPerformance: () => {
              stage: string;
              time: number;
              duration?: number;
            }[];
          }
        ).__workbenchPerformance;
        return read()
          .filter((s) => s.stage === "readable" && s.time >= since)
          .at(-1)?.duration;
      }, start);
      expect(duration).toBeDefined();
      durations.push(duration!);
      expect(await page.locator("[data-message-id]").count()).toBeLessThan(40);
    }
    const sorted = durations.slice().sort((a, b) => a - b);
    const tasks = await page.evaluate(() => (window as unknown as { __longTasks: { start: number; duration: number }[] }).__longTasks);
    const report = info.outputPath("performance.json");
    await writeFile(
      report,
      JSON.stringify({
        count,
        samples: durations,
        longTasks: tasks.filter((task) => task.start >= switchesStart),
        p95: sorted[28],
        production: process.env.HARNESS_PERF_PRODUCTION === "1",
      }),
    );
    await info.attach("performance.json", {
      path: report,
      contentType: "application/json",
    });
    if (process.env.HARNESS_PERF_PRODUCTION === "1")
      expect(sorted[28]).toBeLessThanOrEqual(250);
  });

test("a live message arriving during history load is retained", async ({
  page,
}) => {
  await fixture(page, 500);
  await page.goto("/");
  await expect(page.locator('[data-message-id="500"]')).toBeInViewport();
  await page.evaluate(() =>
    (window as unknown as { __emit: (event: unknown) => void }).__emit({
      type: "agent.message",
      session_id: "a",
      payload: {
        id: 501,
        session_id: "a",
        role: "assistant",
        content: "Concurrent committed reply",
        created_at: "2026-09-18T00:00:00Z",
        stop_reason: "stop",
      },
    }),
  );
  await expect(
    page.getByText("Concurrent committed reply", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Conversation b", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Conversation a", exact: true })
    .click();
  await expect(
    page.getByText("Concurrent committed reply", { exact: true }),
  ).toBeVisible();
});

test("public paged view stays provider-free and loads full bodies on demand", async ({
  page,
}) => {
  const rows = messages(40, "shared");
  rows[39] = {
    ...rows[39],
    content: "Large message preview",
    content_truncated: true,
  } as (typeof rows)[number];
  const paths: string[] = [];
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    paths.push(path);
    let body: unknown = {};
    if (path === "/api/shared/synthetic")
      body = {
        id: "shared",
        title: "Shared",
        mode: "agent",
        message_pages: true,
        artifact_pages: true,
        frozen: true,
      };
    else if (path.endsWith("/messages/page"))
      body = {
        messages: rows,
        has_older: false,
        has_newer: false,
        before_id: 1,
        after_id: 40,
        latest_id: 40,
        latest_final_id: 40,
        interruption: "",
      };
    else if (path.endsWith("/content"))
      body = {
        content: "Complete message body",
        next_offset: 21,
        has_more: false,
        total: 21,
      };
    else if (path.endsWith("/mentioned-files")) body = { files: [] };
    else if (path.endsWith("/search"))
      body = { entries: [], has_more: false, next_cursor: "" };
    await route.fulfill({ json: body });
  });
  await page.goto("/s/synthetic");
  await expect(
    page.getByText("Large message preview", { exact: true }),
  ).toBeVisible();
  expect(paths.some((p) => p.endsWith("/content"))).toBe(false);
  await page.getByRole("button", { name: /Read full message/ }).click();
  await expect(page.getByRole("dialog")).toContainText("Complete message body");
  expect(paths.every((p) => p.startsWith("/api/shared/"))).toBe(true);
});

test("performance: 100 cached switches do not accumulate transcript rows or heap", async ({
  page,
}, info) => {
  test.setTimeout(120000);
  await fixture(page, 10000);
  const cdp = await page.context().newCDPSession(page);
  await page.goto("/");
  await expect(page.locator('[data-message-id="10000"]')).toBeInViewport();
  for (let i = 0; i < 6; i++)
    await page
      .getByRole("button", {
        name: `Conversation ${i % 2 ? "a" : "b"}`,
        exact: true,
      })
      .click();
  await cdp.send("HeapProfiler.collectGarbage");
  const initial = await cdp.send("Runtime.getHeapUsage");
  for (let i = 0; i < 100; i++) {
    const id = i % 2 ? "a" : "b";
    await page
      .getByRole("button", { name: `Conversation ${id}`, exact: true })
      .click();
    await expect(page.locator("main header")).toContainText(
      `Conversation ${id}`,
    );
  }
  await cdp.send("HeapProfiler.collectGarbage");
  const final = await cdp.send("Runtime.getHeapUsage");
  expect(await page.locator("[data-message-id]").count()).toBeLessThan(40);
  expect(final.usedSize - initial.usedSize).toBeLessThan(20 * 1024 * 1024);
  await info.attach("heap.json", {
    body: JSON.stringify({
      initial: initial.usedSize,
      final: final.usedSize,
      delta: final.usedSize - initial.usedSize,
    }),
    contentType: "application/json",
  });
});

test("performance: cold reads with simulated 100ms RTT and 20Mbps", async ({
  page,
}, info) => {
  test.setTimeout(120000);
  await fixture(page, 10000, 100);
  const durations: number[] = [];
  for (let i = 0; i < 30; i++) {
    const started = Date.now();
    await page.goto("/");
    await expect(page.locator('[data-message-id="10000"]')).toBeInViewport();
    durations.push(Date.now() - started);
  }
  const sorted = durations.slice().sort((a, b) => a - b);
  const report = info.outputPath("cold-performance.json");
  await writeFile(
    report,
    JSON.stringify({
      samples: durations,
      p95: sorted[28],
      simulatedRtt: 100,
      simulatedMbps: 20,
      backend: "mocked",
      browserCache: "warm assets, cold application cache",
    }),
  );
  await info.attach("cold-performance.json", {
    path: report,
    contentType: "application/json",
  });
  if (process.env.HARNESS_PERF_PRODUCTION === "1")
    expect(sorted[28]).toBeLessThanOrEqual(1500);
});

test("four-times CPU slowdown keeps the 10000-message view bounded", async ({
  page,
}) => {
  await fixture(page, 10000);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
  await page.goto("/");
  await expect(page.locator('[data-message-id="10000"]')).toBeInViewport();
  await page
    .getByRole("button", { name: "Conversation b", exact: true })
    .click();
  await expect(page.locator("main header")).toContainText("Conversation b");
  expect(await page.locator("[data-message-id]").count()).toBeLessThan(40);
});

test("a stale initial HTTP page cannot erase a newer SSE commit", async ({ page }) => {
  await fixture(page, 500);
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  let waiting = false;
  await page.route("**/api/sessions/a/messages/page", async (route) => {
    waiting = true;
    await pending;
    await route.fallback();
  });
  await page.goto("/");
  await expect.poll(() => waiting).toBe(true);
  await page.evaluate(() => (window as unknown as { __emit: (event: unknown) => void }).__emit({ type: "agent.message", session_id: "a", payload: { id: 501, session_id: "a", role: "assistant", content: "Commit while GET pending", created_at: "2026-09-18T00:00:00Z", stop_reason: "stop" } }));
  release();
  await expect(page.getByText("Commit while GET pending", { exact: true })).toBeVisible();
});
