import { expect, test, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

const output = process.env.STARTUP_PROFILE_OUTPUT;
const origin = process.env.STARTUP_ORIGIN || "http://127.0.0.1:3107";
test.skip(!output, "Requires a new startup comparison directory");

async function beginUseful(page: Page, selector: string, text: string) {
  await page.evaluate(
    ({ selector, text }) => {
      Reflect.set(window, "localUsefulMS", -1);
      document.addEventListener(
        "pointerdown",
        () => {
          const started = performance.now();
          const check = () => {
            const node = document.querySelector(selector);
            if (
              node &&
              node.getBoundingClientRect().height &&
              node.textContent?.includes(text)
            )
              Reflect.set(window, "localUsefulMS", performance.now() - started);
            else if (performance.now() - started < 15000)
              requestAnimationFrame(check);
          };
          requestAnimationFrame(check);
        },
        { once: true, capture: true },
      );
    },
    { selector, text },
  );
}
async function endUseful(page: Page) {
  await expect
    .poll(() =>
      page.evaluate(() => Reflect.get(window, "localUsefulMS") as number),
    )
    .toBeGreaterThanOrEqual(0);
  return page.evaluate(() => Reflect.get(window, "localUsefulMS") as number);
}

test("startup-inclusive and initialized browser journeys use a fixed visible dataset", async ({
  browser,
}) => {
  test.setTimeout(300000);
  const setup: typeof workspaceFixture = process.env.STARTUP_FIXTURE
    ? (await import(process.env.STARTUP_FIXTURE)).workspaceFixture
    : workspaceFixture;
  const variants = [
    {
      name: process.env.STARTUP_FIXTURE ? "retained-baseline" : "current",
      setup,
      origin,
      buildID: readFileSync(
        resolve(process.env.STARTUP_BUILD_DIRECTORY || ".next", "BUILD_ID"),
        "utf8",
      ).trim(),
    },
  ];
  if (process.env.STARTUP_PAIRED_FIXTURE)
    variants.unshift({
      name: "retained-baseline",
      setup: (await import(process.env.STARTUP_PAIRED_FIXTURE))
        .workspaceFixture,
      origin: "http://127.0.0.1:3108",
      buildID: readFileSync(
        resolve(process.env.STARTUP_PAIRED_FIXTURE, "../../../.next/BUILD_ID"),
        "utf8",
      ).trim(),
    });
  const trials: unknown[] = [],
    failures: unknown[] = [];
  const report = {
    browser: browser.version(),
    builds: variants.map(({ name, origin, buildID }) => ({
      name,
      origin,
      buildID,
    })),
    paired_order:
      "alternates variant order within each trial when paired mode is enabled",
    variant: process.env.STARTUP_FIXTURE ? "retained-baseline" : "current",
    buildID: readFileSync(
      resolve(process.env.STARTUP_BUILD_DIRECTORY || ".next", "BUILD_ID"),
      "utf8",
    ).trim(),
    warmup: 1,
    samples: 30,
    trials,
    failures,
    method:
      "fresh context per trial; production server/OS warm; routed synthetic API and controlled events; startup from navigation time origin to useful conversation RAF; initialized journeys measured separately; no CPU profiler or network shaping",
  };
  mkdirSync(output!, { recursive: true });
  const save = () =>
    writeFileSync(
      resolve(output!, "startup.json"),
      JSON.stringify(report, null, 2),
    );
  for (let trial = -1; trial < 30; trial++) {
    for (const variant of trial % 2 ? variants : [...variants].reverse()) {
      const context = await browser.newContext({
        viewport: { width: 1600, height: 1000 },
      });
      try {
        const page = await context.newPage();
        const fixture = await variant.setup(page);
        const dataset = createHash("sha256")
          .update(
            JSON.stringify({
              messages: fixture.messages,
              files: fixture.files,
            }),
          )
          .digest("hex");
        const requests: Record<string, number> = {},
          statuses: Record<string, number> = {},
          failed: string[] = [],
          errors: string[] = [];
        let wireBytes = 0;
        page.on("pageerror", (error) => errors.push(error.message));
        page.on("request", (req) => {
          const path = new URL(req.url()).pathname;
          const kind = path.includes("/snapshot")
            ? "snapshot"
            : path.includes("/view/")
              ? "view"
              : path.includes("/files")
                ? "files"
                : path.startsWith("/api/")
                  ? "metadata"
                  : "assets";
          requests[kind] = (requests[kind] || 0) + 1;
        });
        page.on("response", (response) => {
          const status = String(response.status());
          statuses[status] = (statuses[status] || 0) + 1;
        });
        page.on("requestfinished", (request) => {
          void request
            .sizes()
            .then((size) => {
              wireBytes += size.responseBodySize;
            })
            .catch(() => {});
        });
        page.on("requestfailed", (req) =>
          failed.push(req.failure()?.errorText || "unknown"),
        );
        await page.addInitScript(() => {
          const state = { useful: -1, tasks: [] as number[], urls: 0 };
          Reflect.set(window, "startupMeasurement", state);
          new PerformanceObserver((list) =>
            state.tasks.push(...list.getEntries().map((e) => e.duration)),
          ).observe({ type: "longtask" });
          const original = URL.createObjectURL.bind(URL);
          URL.createObjectURL = (blob) => {
            state.urls++;
            return original(blob);
          };
          const inspect = () => {
            if (
              document
                .querySelector(".agent-panel")
                ?.textContent?.includes(
                  "The cells retain 95% of their initial capacity",
                )
            )
              state.useful = performance.now();
            else requestAnimationFrame(inspect);
          };
          requestAnimationFrame(inspect);
        });
        await page.goto(`${variant.origin}/?project=battery&session=analysis`);
        await expect
          .poll(() =>
            page.evaluate(
              () => Reflect.get(window, "startupMeasurement").useful as number,
            ),
          )
          .toBeGreaterThanOrEqual(0);
        const startup = await page.evaluate(
          () => Reflect.get(window, "startupMeasurement").useful as number,
        );
        const startupCounts = { ...requests };
        const link = page.getByRole("link", {
          name: "the research report",
          exact: true,
        });
        await beginUseful(page, ".file-markdown", "Cycle performance report");
        const previewStart = performance.now();
        await link.click();
        await expect(page.locator(".file-markdown")).toContainText(
          "Cycle performance report",
        );
        const preview = performance.now() - previewStart;
        const previewLocal = await endUseful(page);
        await page.locator('[data-session-link="report"]').click();
        await expect(page.locator('[data-session-id="report"]')).toContainText(
          "The report is ready",
        );
        await beginUseful(
          page,
          '[data-session-id="analysis"]',
          "The cells retain",
        );
        const returned = performance.now();
        await page.locator('[data-session-link="analysis"]').click();
        await expect(
          page.locator('[data-session-id="analysis"]'),
        ).toContainText("The cells retain");
        const cachedReturn = performance.now() - returned;
        const returnLocal = await endUseful(page);
        const cdp = await context.newCDPSession(page);
        await cdp.send("Performance.enable");
        await cdp.send("HeapProfiler.collectGarbage");
        const state = await page.evaluate(() =>
          Reflect.get(window, "startupMeasurement"),
        );
        const dom = await cdp.send("Memory.getDOMCounters"),
          metrics = await cdp.send("Performance.getMetrics");
        if (trial >= 0)
          trials.push({
            trial,
            variant: variant.name,
            host_load_at_checkpoint: readFileSync(
              "/proc/loadavg",
              "utf8",
            ).trim(),
            dataset,
            startup_ms: startup,
            preview_action_ms: preview,
            return_action_ms: cachedReturn,
            preview_local_ms: previewLocal,
            return_local_ms: returnLocal,
            startup_requests: startupCounts,
            requests,
            statuses,
            failed_requests: failed,
            errors,
            response_body_bytes: wireBytes,
            dom,
            heap: metrics.metrics.find((m) => m.name === "JSHeapUsedSize")
              ?.value,
            long_tasks: state.tasks,
            preview_urls: state.urls,
          });
        expect(errors).toEqual([]);
      } catch (error) {
        failures.push({ trial, variant: variant.name, error: String(error) });
        throw error;
      } finally {
        save();
        await context.close();
      }
    }
  }
  expect(trials).toHaveLength(30 * variants.length);
});

test("post-ready idle is separated from startup and initialized journeys", async ({
  browser,
}) => {
  test.setTimeout(160000);
  const setup: typeof workspaceFixture = process.env.STARTUP_FIXTURE
    ? (await import(process.env.STARTUP_FIXTURE)).workspaceFixture
    : workspaceFixture;
  const context = await browser.newContext({
    viewport: { width: 1600, height: 1000 },
  });
  const observations: unknown[] = [];
  mkdirSync(output!, { recursive: true });
  try {
    const page = await context.newPage();
    await setup(page);
    await page.addInitScript(() => {
      let urls = 0;
      const original = URL.createObjectURL.bind(URL);
      URL.createObjectURL = (blob) => {
        urls++;
        return original(blob);
      };
      Object.defineProperty(window, "idlePreviewURLs", { get: () => urls });
    });
    const requests: { path: string; method: string }[] = [];
    page.on("request", (req) => {
      const path = new URL(req.url()).pathname;
      if (path.startsWith("/api/"))
        requests.push({ path, method: req.method() });
    });
    await page.goto(`${origin}/?project=battery&session=analysis`);
    await page
      .getByRole("link", { name: "the research report", exact: true })
      .click();
    await expect(page.locator(".file-markdown")).toContainText(
      "Cycle performance report",
    );
    await page.waitForTimeout(1000);
    const before = requests.length,
      urls = await page.evaluate(
        () => Reflect.get(window, "idlePreviewURLs") as number,
      ),
      start = Date.now();
    await page.evaluate(() =>
      Reflect.set(
        window,
        "idlePreviewNode",
        document.querySelector(".file-markdown"),
      ),
    );
    for (const seconds of [60, 120]) {
      await page.waitForTimeout(
        Math.max(0, start + seconds * 1000 - Date.now()),
      );
      const calls = requests.slice(before);
      const counts = {
        snapshot: 0,
        view: 0,
        file: 0,
        directory: 0,
        head: 0,
        metadata: 0,
      };
      for (const call of calls) {
        if (call.path.endsWith("/snapshot")) counts.snapshot++;
        else if (/\/view\/(pages|changes)$/.test(call.path)) counts.view++;
        else if (call.path.endsWith("/files/content")) counts.file++;
        else if (call.path.endsWith("/files")) counts.directory++;
        else if (call.path.endsWith("/view/head")) counts.head++;
        else counts.metadata++;
      }
      const current = await page.evaluate(() => ({
        urls: Reflect.get(window, "idlePreviewURLs") as number,
        sameNode:
          Reflect.get(window, "idlePreviewNode") ===
          document.querySelector(".file-markdown"),
      }));
      observations.push({
        seconds,
        elapsed_ms: Date.now() - start,
        counts,
        new_urls: current.urls - urls,
        same_preview_node: current.sameNode,
      });
      writeFileSync(
        resolve(output!, "idle.json"),
        JSON.stringify(observations, null, 2),
      );
      if (!process.env.STARTUP_FIXTURE) {
        expect(
          counts.snapshot + counts.view + counts.file + counts.directory,
        ).toBe(0);
        expect(current.urls).toBe(urls);
        expect(current.sameNode).toBe(true);
      }
    }
  } finally {
    await context.close();
  }
});
