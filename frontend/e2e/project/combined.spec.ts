import { expect, test, type Page, type Locator } from "@playwright/test";
import { existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";

test.use({ actionTimeout: 15000, navigationTimeout: 30000 });
const output = process.env.PROJECT_COMBINED_OUTPUT;
test.skip(!output, "Requires the disposable combined workload coordinator");

async function login(page: Page, project: string, session: string, authenticate = false) {
  await page.goto(`/?project=${project}&session=${session}`);
  if (authenticate) {
    await expect(page.getByLabel("Username", { exact: true })).toBeVisible();
    await page.getByLabel("Username", { exact: true }).fill("researcher");
    await page.getByLabel("Password", { exact: true }).fill("isolated-browser-fixture");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
  }
  await expect(page.locator(`[data-session-id="${session}"] .conversation-turn`).first()).toBeVisible();
}

// Timing begins on the browser's pointerdown and ends at the first animation
// frame with useful DOM. This excludes Playwright selector/action preparation;
// it is neither paint completion nor field INP.
async function useful(page: Page, target: Locator, selector: string, text = "") {
  await page.evaluate(({ selector, text }) => {
    const state = { start: 0, elapsed: -1 };
    Reflect.set(window, "combinedIntent", state);
    document.addEventListener("pointerdown", () => {
      state.start = performance.now();
      const check = () => {
        const node = document.querySelector(selector);
        if (node && node.getBoundingClientRect().height > 0 && node.textContent?.includes(text)) {
          state.elapsed = performance.now() - state.start;
        } else if (performance.now() - state.start < 15000) requestAnimationFrame(check);
      };
      requestAnimationFrame(check);
    }, { once: true, capture: true });
  }, { selector, text });
  await target.click();
  await expect.poll(() => page.evaluate(() => Reflect.get(window, "combinedIntent").elapsed), { timeout: 16000 }).toBeGreaterThanOrEqual(0);
  return page.evaluate(() => Reflect.get(window, "combinedIntent").elapsed as number);
}
function summarize(samples: number[]) {
  const sorted = [...samples].sort((a, b) => a - b);
  const rank = (p: number) => sorted[Math.max(0, Math.ceil(sorted.length * p) - 1)];
  return { count: sorted.length, p50: rank(.5), p95: rank(.95), max: rank(1), samples };
}

test("browser reading and edit state survive streaming load and gated owner update", async ({ page, context }) => {
  test.setTimeout(570000);
  const errors: string[] = [];
  let heavyReads = 0, headReads = 0;
  const requests: Record<string, number> = {};
  const statuses: Record<string, number> = {};
  const failedRequests: string[] = [];
  const report: Record<string, unknown> = { profiles: {}, errors, requests, statuses, failedRequests, quantile: "nearest-rank", scope: "two tabs; warmed sequential journeys; real Go HTTP and SSE", timing: "pointerdown to first RAF with useful DOM" };
  const profiles = report.profiles as Record<string, unknown>;
  const pages = [page, await context.newPage()];
  for (const observed of pages) {
    observed.on("pageerror", err => errors.push(err.message));
    observed.on("request", req => {
      const path = new URL(req.url()).pathname;
      if (req.method() === "GET" && (/\/view\/(pages|changes)$/.test(path) || /\/files(?:\/content)?$/.test(path))) heavyReads++;
      if (path.endsWith("/view/head")) headReads++;
      const kind = path.includes("/view/") ? "conversation" : path.includes("/files/") ? "file" : path.endsWith("/resources/events") ? "events" : path.startsWith("/api/") ? "metadata" : "assets";
      requests[kind] = (requests[kind] ?? 0) + 1;
    });
    observed.on("response", res => { const status = String(res.status()); statuses[status] = (statuses[status] ?? 0) + 1; });
    observed.on("requestfailed", req => failedRequests.push(req.failure()?.errorText ?? "unknown"));
    await observed.addInitScript(() => {
      let creates = 0;
      const create = URL.createObjectURL.bind(URL);
      URL.createObjectURL = blob => { creates++; return create(blob); };
      Object.defineProperty(window, "combinedURLCreates", { get: () => creates });
      const tasks: number[] = [];
      new PerformanceObserver(list => { for (const entry of list.getEntries()) { tasks.push(entry.duration); if (tasks.length > 2048) tasks.shift(); } }).observe({ type: "longtask" });
      Reflect.set(window, "combinedLongTasks", tasks);
    });
  }
  try {
    await login(page, "mixed-0", "mixed-agent-0-0", true);
    console.log("combined primary tab ready");
    await login(pages[1], "mixed-1", "mixed-agent-1-0");
    console.log("combined second tab ready");
    const composer = page.locator('[data-session-id="mixed-agent-0-0"] .agent-composer textarea');
    await composer.fill("Unsaved combined draft survives owner update");
    console.log("combined draft filled");
    await page.getByRole("button", { name: "File preview", exact: true }).click();
    console.log("combined file panel opened");
    await page.getByRole("button", { name: "preview-0.txt", exact: true }).click();
    console.log("combined preview selected");
    await expect(page.getByRole("region", { name: "File preview-0.txt", exact: true })).toContainText("Combined preview 0");
    await page.getByRole("button", { name: "File actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
    await page.getByLabel("Edit preview-0.txt", { exact: true }).fill("Combined saved edit\n");
    await page.getByRole("button", { name: "Save file", exact: true }).click();
    await expect(page.getByLabel("Edit preview-0.txt", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "File preview-0.txt", exact: true })).toContainText("Combined saved edit");
    const cdps = await Promise.all(pages.map(p => context.newCDPSession(p)));
    for (const cdp of cdps) await cdp.send("Performance.enable");
    writeFileSync(join(output!, "browser-ready"), "ready\n");
    console.log("combined edit saved and browser ready");
    await expect.poll(() => existsSync(join(output!, "workload-started")), { timeout: 60000 }).toBe(true);
    for (const profile of ["loopback", "latency100ms-10Mbps"]) {
      for (const cdp of cdps) await cdp.send("Network.emulateNetworkConditions", { offline: false, latency: profile === "loopback" ? 0 : 100, downloadThroughput: profile === "loopback" ? -1 : 1250000, uploadThroughput: profile === "loopback" ? -1 : 1250000 });
      const selection: number[] = [], cachedReturn: number[] = [], preview: number[] = [];
      const checkpoints: unknown[] = [];
      profiles[profile] = { selection, cachedReturn, preview, checkpoints, network: "CDP request latency and per-target bandwidth; not packet RTT shaping", warmup: 1 };
      for (let trial = -1; trial < 30; trial++) {
        const forward = await useful(page, page.locator('[data-session-link="mixed-agent-0-1"]'), '[data-session-id="mixed-agent-0-1"] .conversation-turn');
        const back = await useful(page, page.locator('[data-session-link="mixed-agent-0-0"]'), '[data-session-id="mixed-agent-0-0"] .conversation-turn');
        await expect(composer).toHaveValue("Unsaved combined draft survives owner update");
        const file = trial % 2 === 0 ? 0 : 1;
        const content = file === 0 ? "Combined saved edit" : "Combined preview 1";
        const opened = await useful(page, page.getByRole("button", { name: `preview-${file}.txt`, exact: true }), `section[aria-label="File preview-${file}.txt"]`, content);
        await expect(pages[1].locator('[data-session-id="mixed-agent-1-0"] .conversation-turn').first()).toBeVisible();
        if (trial >= 0) { selection.push(forward); cachedReturn.push(back); preview.push(opened); }
        writeFileSync(join(output!, "combined-browser.json"), JSON.stringify(report, null, 2));
        if (trial === -1 || trial === 14 || trial === 29) {
          await cdps[0].send("HeapProfiler.collectGarbage");
          checkpoints.push({ trial, dom: await cdps[0].send("Memory.getDOMCounters"), metrics: await cdps[0].send("Performance.getMetrics") });
        }
      }
      profiles[profile] = { ...profiles[profile] as object, selection: summarize(selection), cachedReturn: summarize(cachedReturn), preview: summarize(preview) };
    }
    // Keep both browser resource transports mounted throughout the owner update.
    await expect.poll(() => existsSync(join(output!, "workload-finished")), { timeout: 330000, intervals: [1000] }).toBe(true);
    await expect(composer).toHaveValue("Unsaved combined draft survives owner update");
    const result = await context.request.get("/api/projects/mixed-0/files/content?path=preview-0.txt");
    expect(result.ok()).toBe(true);
    expect(await result.text()).toBe("Combined saved edit\n");
    for (const p of pages) {
      await expect(p.locator(".conversation-turn").last()).toBeVisible();
    }
    // Separate post-ready windows from the preceding active/transition traffic.
    // PTYs and sibling schedules remain alive, but selected resources are quiet.
    await page.waitForTimeout(2000);
    const heavyBefore = heavyReads, headsBefore = headReads;
    const initialURLs = await page.evaluate(() => Reflect.get(window, "combinedURLCreates") as number);
    await page.evaluate(() => Reflect.set(window, "combinedPreviewNode", document.querySelector(".source-editor")));
    const idleStart = Date.now(), idle: unknown[] = [];
    report.idle = idle;
    for (const seconds of [60, 120]) {
      await page.waitForTimeout(Math.max(0, idleStart + seconds * 1000 - Date.now()));
      const sameNode = await page.evaluate(() => Reflect.get(window, "combinedPreviewNode") === document.querySelector(".source-editor"));
      const urls = await page.evaluate(() => Reflect.get(window, "combinedURLCreates") as number);
      idle.push({ seconds, elapsed_ms: Date.now() - idleStart, heavy_reads: heavyReads - heavyBefore, heads: headReads - headsBefore, new_urls: urls - initialURLs, same_preview_node: sameNode });
      writeFileSync(join(output!, "combined-browser.json"), JSON.stringify(report, null, 2));
      expect(heavyReads - heavyBefore).toBe(0);
      expect(urls).toBe(initialURLs);
      expect(sameNode).toBe(true);
    }
    report.longTasks = await Promise.all(pages.map(p => p.evaluate(() => Reflect.get(window, "combinedLongTasks"))));
    report.finalResources = await Promise.all(cdps.map(async cdp => { await cdp.send("HeapProfiler.collectGarbage"); return { dom: await cdp.send("Memory.getDOMCounters"), metrics: await cdp.send("Performance.getMetrics") }; }));
    expect(errors).toEqual([]);
    report.success = true;
  } catch (error) {
    report.success = false;
    report.failure = String(error);

    throw error;
  } finally {
    writeFileSync(join(output!, "combined-browser.json"), JSON.stringify(report, null, 2));
    await pages[1].close();
  }
});
