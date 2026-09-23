import { expect, test, type Page, type Route } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

const output = process.env.READ_LATENCY_OUTPUT;
test.skip(!output, "Requires a new initialized-read evidence directory");
async function begin(page: Page, selector: string, text: string) {
  await page.evaluate(
    ({ selector, text }) => {
      Reflect.set(window, "readUsefulMS", -1);
      document.addEventListener(
        "pointerdown",
        () => {
          const start = performance.now();
          const inspect = () => {
            const node = document.querySelector(selector);
            if (
              node &&
              node.getBoundingClientRect().height &&
              node.textContent?.includes(text)
            )
              Reflect.set(window, "readUsefulMS", performance.now() - start);
            else if (performance.now() - start < 10000)
              requestAnimationFrame(inspect);
          };
          requestAnimationFrame(inspect);
        },
        { once: true, capture: true },
      );
    },
    { selector, text },
  );
}
async function end(page: Page) {
  await expect
    .poll(() =>
      page.evaluate(() => Reflect.get(window, "readUsefulMS") as number),
    )
    .toBeGreaterThanOrEqual(0);
  return page.evaluate(() => Reflect.get(window, "readUsefulMS") as number);
}
for (const shaped of [false, true])
  test(`thirty independent initialized reads shaped=${shaped}`, async ({
    browser,
  }) => {
    test.setTimeout(300000);
    const samples: {
        conversation: number;
        preview: number;
        cachedReturn: number;
      }[] = [],
      failures: unknown[] = [];
    const report = {
      shaped,
      buildID: readFileSync(resolve(".next/BUILD_ID"), "utf8").trim(),
      browser: browser.version(),
      warmup: 1,
      trials: 30,
      samples,
      failures,
      method: shaped
        ? "Synthetic API response delay:100ms plus encoded bytes/1250000 bytes per second; responses materialized before delay,not packet shaping. Static assets loopback; application initialized before measured intent."
        : "Loopback static assets and routed API; application initialized before measured intent; fresh context each trial.",
      timing: "pointerdown to useful DOM on RAF; not raster paint or field INP",
    };
    mkdirSync(output!, { recursive: true });
    const save = () =>
      writeFileSync(
        resolve(output!, shaped ? "shaped.json" : "loopback.json"),
        JSON.stringify(report, null, 2),
      );
    for (let trial = -1; trial < 30; trial++) {
      const context = await browser.newContext({
        viewport: { width: 1600, height: 1000 },
      });
      try {
        const page = await context.newPage();
        if (shaped) {
          const original = page.route.bind(page);
          page.route = async (pattern, handler, options) =>
            original(
              pattern,
              (route, request) => {
                const proxy = new Proxy(route, {
                  get(target, key) {
                    if (key === "fulfill")
                      return async (
                        response: Parameters<Route["fulfill"]>[0],
                      ) => {
                        const body =
                          response?.body ??
                          (response?.json === undefined
                            ? ""
                            : JSON.stringify(response.json));
                        const bytes =
                          typeof body === "string"
                            ? Buffer.byteLength(body)
                            : body.length;
                        await new Promise((resolve) =>
                          setTimeout(resolve, 100 + bytes / 1250),
                        );
                        return target.fulfill(response);
                      };
                    const value = Reflect.get(target, key);
                    return typeof value === "function"
                      ? value.bind(target)
                      : value;
                  },
                });
                return handler(proxy, request);
              },
              options,
            );
        }
        await workspaceFixture(page);
        await page.goto(
          "http://127.0.0.1:3107/?project=battery&session=analysis",
        );
        await expect(
          page.getByRole("link", { name: "the research report" }),
        ).toBeVisible();
        await begin(page, '[data-session-id="report"]', "The report is ready");
        await page.locator('[data-session-link="report"]').click();
        const conversation = await end(page);
        await begin(page, '[data-session-id="analysis"]', "The cells retain");
        await page.locator('[data-session-link="analysis"]').click();
        const cachedReturn = await end(page);
        await begin(page, ".file-markdown", "Cycle performance report");
        await page.getByRole("link", { name: "the research report" }).click();
        const preview = await end(page);
        if (trial >= 0) samples.push({ conversation, cachedReturn, preview });
      } catch (error) {
        failures.push({ trial, error: String(error) });
        throw error;
      } finally {
        save();
        await context.close();
      }
    }
    const p95 = (key: keyof (typeof samples)[number]) =>
      [...samples].map((s) => s[key]).sort((a, b) => a - b)[28];
    const stats = {
      conversation_p95: p95("conversation"),
      preview_p95: p95("preview"),
      cached_return_p95: p95("cachedReturn"),
      quantile: "nearest-rank",
    };
    writeFileSync(
      resolve(output!, shaped ? "shaped.json" : "loopback.json"),
      JSON.stringify({ ...report, stats }, null, 2),
    );
    expect(samples).toHaveLength(30);
    expect(stats.conversation_p95).toBeLessThan(shaped ? 1500 : 800);
    expect(stats.cached_return_p95).toBeLessThan(150);
    if (!shaped) expect(stats.preview_p95).toBeLessThan(800);
  });
