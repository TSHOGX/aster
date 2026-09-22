import { expect, test } from "@playwright/test";
import { writeFileSync } from "node:fs";
import { workspaceFixture } from "./fixture";

test("healthy 60 and 120 second idle windows do no heavy reads across rotations", async ({
  page,
}) => {
  test.setTimeout(150000);
  const fixture = await workspaceFixture(page);
  const failures: string[] = [];
  page.on("pageerror", (e) => failures.push(e.message));
  await page.addInitScript(() => {
    let creates = 0;
    const create = URL.createObjectURL.bind(URL);
    URL.createObjectURL = (blob) => {
      creates++;
      return create(blob);
    };
    Object.defineProperty(window, "previewCreations", { get: () => creates });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "the research report" }).click();
  await expect(page.locator(".file-markdown")).toContainText(
    "Cycle performance report",
  );
  await page.waitForTimeout(600);
  const initialURLs = await page.evaluate(() =>
    Reflect.get(window, "previewCreations"),
  );
  const baseline = fixture.calls.length;
  const start = Date.now();
  const observations = [];
  for (const seconds of [60, 120]) {
    await page.waitForTimeout(Math.max(0, start + seconds * 1000 - Date.now()));
    const calls = fixture.calls.slice(baseline);
    const heavy = calls.filter(
      (c) =>
        c.method === "GET" &&
        (/\/view\/(pages|changes)$/.test(c.path) ||
          /\/files(?:\/content)?$/.test(c.path)),
    );
    const urls = await page.evaluate(() =>
      Reflect.get(window, "previewCreations"),
    );
    observations.push({
      seconds,
      elapsed_ms: Date.now() - start,
      requests: calls.length,
      heavy_reads: heavy.length,
      preview_creations: urls - initialURLs,
      heads: calls.filter((c) => c.path.endsWith("/view/head")).length,
    });
    expect(heavy).toEqual([]);
    expect(urls).toBe(initialURLs);
  }
  expect(failures).toEqual([]);
  writeFileSync(
    test.info().outputPath("idle.json"),
    JSON.stringify(
      {
        fixture: "deterministic resource events with real 25-second rotations",
        observations,
        failures,
      },
      null,
      2,
    ),
  );
});
