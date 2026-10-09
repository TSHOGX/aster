import { expect, test } from "@playwright/test";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

const fixtures = process.env.PREVIEW_FIXTURES;
const output = process.env.OFFICE_LATENCY_OUTPUT;
test.skip(!fixtures || !output, "Requires immutable Office fixtures and an evidence directory");

for (const cpuRate of [1, 4]) {
  for (const name of ["small.docx", "small.xlsx", "small.pptx"]) {
    test(`thirty independent Office journeys ${name} cpu${cpuRate}`, async ({ browser }) => {
      test.setTimeout(300000);
      const body = readFileSync(resolve(fixtures!, name));
      const samples: number[] = [], failures: unknown[] = [], tasks: unknown[] = [], errors: string[] = [];
      const report = { buildID: readFileSync(resolve(".next/BUILD_ID"), "utf8").trim(), name, cpuRate, bytes: body.length, sha256: createHash("sha256").update(body).digest("hex"), browser: browser.version(), warmup: 1, measuredTrials: 30, samples, failures, tasks, errors, method: "fresh context per trial; application initialized before pointerdown; first useful Office DOM/canvas plus adapter ready on RAF; routed body, warm production server, no CPU profiler; not network/paint/field INP" };
      mkdirSync(output!, { recursive: true });
      const save = () => writeFileSync(resolve(output!, `${name}-cpu${cpuRate}.json`), JSON.stringify(report, null, 2));
      for (let trial = -1; trial < 30; trial++) {
        const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
        const page = await context.newPage();
        try {
          page.on("pageerror", error => errors.push(error.message));
          const fixture = await workspaceFixture(page);
          fixture.messages.analysis[1].content = `[Measured Office](/workspace/${name})`;
          await page.route("**/files/content?*", route => route.fulfill({ body, contentType: "application/octet-stream" }));
          await page.addInitScript(() => {
            const longTasks: number[] = [];
            new PerformanceObserver(list => longTasks.push(...list.getEntries().map(entry => entry.duration))).observe({ type: "longtask" });
            Reflect.set(window, "officeLatencyTasks", longTasks);
            const original = CanvasRenderingContext2D.prototype.fillText;
            CanvasRenderingContext2D.prototype.fillText = function(text, x, y, width) {
              if (String(text).includes("Cell 0:0")) Reflect.set(window, "officeCellDrawn", true);
              return width === undefined ? original.call(this, text, x, y) : original.call(this, text, x, y, width);
            };
          });
          await page.goto("http://127.0.0.1:3107/?project=battery&session=analysis");
          const link = page.getByRole("link", { name: "Measured Office", exact: true });
          await expect(link).toBeVisible();
          const cdp = await context.newCDPSession(page);
          await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpuRate });
          await page.evaluate(name => {
            Reflect.set(window, "officeUsefulMS", -1);
            Reflect.get(window, "officeLatencyTasks").length = 0;
            document.addEventListener("pointerdown", () => {
              const started = performance.now();
              const inspect = () => {
                const frame = document.querySelector<HTMLIFrameElement>(".office-frame");
                const doc = frame?.contentDocument;
                const content = name.endsWith("pptx") ? doc?.body?.textContent?.includes("Measured slide 0")
                  : document.querySelector(".office-reading-viewport")?.textContent?.includes(name.endsWith("docx") ? "Paragraph 0" : "Cell 0:0");
                if (content && document.querySelector('[data-preview-phase="ready"]')) {
                  Reflect.set(window, "officeUsefulMS", performance.now() - started);
                } else if (performance.now() - started < 20000) requestAnimationFrame(inspect);
              };
              requestAnimationFrame(inspect);
            }, { once: true, capture: true });
          }, name);
          await link.click();
          await expect.poll(() => page.evaluate(() => Reflect.get(window, "officeUsefulMS") as number), { timeout: 21000 }).toBeGreaterThanOrEqual(0);
          const value = await page.evaluate(() => Reflect.get(window, "officeUsefulMS") as number);
          if (trial >= 0) {
            samples.push(value);
            tasks.push(await page.evaluate(() => {
              const frame = document.querySelector<HTMLIFrameElement>(".office-frame");
              return { parent: Reflect.get(window, "officeLatencyTasks"), renderer: frame?.contentWindow ? Reflect.get(frame.contentWindow, "officeLatencyTasks") : [] };
            }));
          }
        } catch (error) {
          failures.push({ trial, error: String(error), observation: await page.evaluate(() => ({ phase: document.querySelector(".office-preview")?.getAttribute("data-preview-phase"), frames: document.querySelectorAll(".office-frame").length, frameTextBytes: document.querySelector<HTMLIFrameElement>(".office-frame")?.contentDocument?.body?.textContent?.length, status: document.querySelector(".selected-file")?.textContent, timing: Reflect.get(window, "officeUsefulMS") })) });
          throw error;
        } finally {
          save();
          await context.close();
        }
      }
      const sorted = [...samples].sort((a,b) => a-b);
      const stats = { p50: sorted[14], p95: sorted[28], max: sorted[29], quantile: "nearest-rank" };
      writeFileSync(resolve(output!, `${name}-cpu${cpuRate}.json`), JSON.stringify({ ...report, stats }, null, 2));
      expect(samples).toHaveLength(30);
      expect(failures).toHaveLength(0);
      expect(errors).toHaveLength(0);
      // Original ordinary local Office target. CPU-throttled observations remain
      // separate evidence rather than silently redefining that target.
      if (cpuRate === 1) expect(stats.p95).toBeLessThan(3000);
    });
  }
}
