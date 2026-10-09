import { expect, test } from "@playwright/test";
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

// Opt-in profile: generate fixtures with e2e/preview-documents.py first.
const fixtures = process.env.PREVIEW_FIXTURES;
const output = process.env.PREVIEW_PROFILE_OUTPUT;
test.skip(
  !fixtures || !output,
  "Requires generated Office fixtures and a new evidence directory",
);
for (const name of [
  "small.docx",
  "large.docx",
  "small.xlsx",
  "large.xlsx",
  "small.pptx",
  "large.pptx",
]) {
  test(`Office CPU profile ${name}`, async ({ page, context }) => {
    const f = await workspaceFixture(page);
    f.messages.analysis[1].content = `[Measured document](/workspace/${name})`;
    await page.route("**/files/content?*", (route) =>
      route.fulfill({
        body: readFileSync(resolve(fixtures!, name)),
        contentType: "application/octet-stream",
      }),
    );
    await page.addInitScript(() => {
      const tasks: number[] = [];
      new PerformanceObserver((list) =>
        tasks.push(...list.getEntries().map((e) => e.duration)),
      ).observe({ type: "longtask" });
      Reflect.set(window, "previewLongTasks", tasks);
    });
    await page.goto("/?project=battery&session=analysis");
    await expect(
      page.getByRole("link", { name: "Measured document" }),
    ).toBeVisible();
    const cdp = await context.newCDPSession(page);
    await cdp.send("Performance.enable");
    await cdp.send("Profiler.enable");
    await cdp.send("Profiler.start");
    const start = performance.now();
    await page.getByRole("link", { name: "Measured document" }).click();
    const selector = name.endsWith("docx")
      ? ".docx"
      : name.endsWith("xlsx")
        ? ".x-spreadsheet"
        : ".pptx-preview-slide-wrapper-0";
    await expect(
      name.endsWith("pptx") ? page.frameLocator(".office-frame").locator(selector).first() : page.locator(".office-reading-row").first(),
    ).toBeVisible({
      timeout: 20000,
    });
    const useful = performance.now() - start;
    const readiness = page.locator(".office-preview[data-preview-phase]");
    if (await readiness.count())
      await expect(readiness).toHaveAttribute("data-preview-phase", "ready");
    const complete = performance.now() - start;
    await page.waitForTimeout(500);
    const profile = await cdp.send("Profiler.stop");
    const metrics = await cdp.send("Performance.getMetrics");
    const resources = await cdp.send("Memory.getDOMCounters");
    const longTasks = await page.evaluate(() =>
      Reflect.get(window, "previewLongTasks"),
    );
    mkdirSync(output!, { recursive: true });
    writeFileSync(
      resolve(output!, `${name}.json`),
      JSON.stringify(
        { useful, complete, metrics, resources, longTasks },
        null,
        2,
      ),
    );
    writeFileSync(
      resolve(output!, `${name}.cpuprofile`),
      JSON.stringify(profile.profile),
    );
    await cdp.detach();
  });
}
