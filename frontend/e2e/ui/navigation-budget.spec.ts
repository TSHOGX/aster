import { expect, test } from "@playwright/test";
import { historyFixture } from "./history-fixture";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

test("the turn rail pages both ways within a bounded window and keeps the current marker", async ({
  page,
}) => {
  const fixture = await historyFixture(page, 1000, 1);
  await page.goto("/?project=battery&session=analysis");
  const rail = page.locator(".turn-rail");
  await expect(rail.locator(".turn-tick")).toHaveCount(100);
  const reads = () => fixture.calls.filter(url => url.includes("/turn-index?before=") && !url.includes("before=0&")).length;
  const beforeScroll = reads();
  // Scroll restoration and click auto-scrolling are not new paging intent.
  await rail.evaluate(async node => {
    node.scrollTop = 0;
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  });
  expect(reads()).toBe(beforeScroll);
  for (const earliest of [801, 701, 601, 501, 401]) {
    await page
      .getByRole("button", { name: "Earlier turns", exact: true })
      .click();
    await expect(
      rail.locator(`[data-nav-turn="turn-${earliest}"]`),
    ).toBeAttached();
    expect(await rail.locator(".turn-tick").count()).toBeLessThanOrEqual(301);
  }
  await expect(rail.locator('[data-nav-turn="turn-1000"]')).toHaveAttribute(
    "aria-current",
    "step",
  );
  await fixture.stream();
  await expect(rail.locator('[data-nav-turn="turn-1001"]')).toBeAttached();
  await page.waitForTimeout(900); // Cross the explicit revision coalescing interval.
  await expect(rail.locator('[data-nav-turn="turn-401"]')).toBeAttached();
  await expect(rail.locator('[data-nav-turn="turn-901"]')).toHaveCount(0);
  expect(await rail.locator(".turn-tick").count()).toBeLessThanOrEqual(301);
  for (const latest of [800, 900, 999]) {
    await page
      .getByRole("button", { name: "Later turns", exact: true })
      .click();
    await expect(
      rail.locator(`[data-nav-turn="turn-${latest}"]`),
    ).toBeAttached();
    expect(await rail.locator(".turn-tick").count()).toBeLessThanOrEqual(301);
  }
  // One newly streamed turn can occupy the next page after the old latest edge.
  if (await page.getByRole("button", { name: "Later turns", exact: true }).count())
    await page.getByRole("button", { name: "Later turns", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Later turns", exact: true }),
  ).toHaveCount(0);
  await rail.locator('[data-nav-turn="turn-750"]').click();
  await expect(page.locator('[data-turn-id="turn-750"]')).toBeVisible();
  await expect(rail.locator('[data-nav-turn="turn-750"]')).toHaveAttribute(
    "aria-current",
    "step",
  );
  await expect(page.locator(".agent-panel").getByRole("alert")).toHaveCount(0);
});

test("maximal transcript traversal retains bounded pages and both directions", async ({
  page,
  context,
}) => {
  const output = process.env.HISTORY_WINDOW_OUTPUT;
  test.skip(!output, "Requires a new window profile output directory");
  test.setTimeout(180000);
  await historyFixture(page, 1000, 12);
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator('[data-turn-id="turn-1000"]')).toBeAttached();
  const cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");
  const scroll = page.locator(".conversation-scroll");
  const observations: unknown[] = [];
  for (let i = 1; i <= 20; i++) {
    const first = 991 - i * 10;
    await scroll.dispatchEvent("wheel", { deltaY: -1 });
    await scroll.evaluate((el) => {
      el.scrollTop = 0;
      el.dispatchEvent(new Event("scroll"));
    });
    await expect(page.locator(`[data-turn-id="turn-${first}"]`)).toBeAttached();
    const count = await page.locator(".conversation-turn").count();
    expect(count).toBeLessThanOrEqual(160);
    if (i % 5 === 0) {
      await cdp.send("HeapProfiler.collectGarbage");
      observations.push({
        pagesTraversed: i + 1,
        turns: count,
        dom: await cdp.send("Memory.getDOMCounters"),
        metrics: await cdp.send("Performance.getMetrics"),
      });
    }
  }
  if (process.env.HISTORY_SCROLL_PROFILE === "1") {
    const rate = Number(process.env.HISTORY_CPU_RATE || "1");
    await cdp.send("Emulation.setCPUThrottlingRate", { rate });
    await scroll.evaluate(node => {
      node.scrollTop = (node.scrollHeight - node.clientHeight) / 2;
    });
    await page.waitForTimeout(300);
    await scroll.evaluate(node => {
      const feedback: number[] = [], tasks: number[] = [];
      node.addEventListener("wheel", () => {
        const start = performance.now();
        requestAnimationFrame(() => feedback.push(performance.now() - start));
      }, { passive: true });
      new PerformanceObserver(list => tasks.push(...list.getEntries().map(entry => entry.duration))).observe({ type: "longtask" });
      Reflect.set(window, "maximalScrollSamples", { feedback, tasks });
    });
    await cdp.send("Profiler.enable");
    await cdp.send("Profiler.start");
    await scroll.hover();
    for (let sample = 0; sample < 30; sample++) {
      await page.mouse.wheel(0, sample % 2 ? -180 : 180);
      await page.waitForTimeout(100);
    }
    const profile = await cdp.send("Profiler.stop");
    const samples = await page.evaluate(() => Reflect.get(window, "maximalScrollSamples") as { feedback: number[]; tasks: number[] });
    mkdirSync(output!, { recursive: true });
    writeFileSync(resolve(output!, "maximal-scroll.cpuprofile"), JSON.stringify(profile.profile));
    const sorted = [...samples.feedback].sort((a,b) => a-b);
    writeFileSync(resolve(output!, "maximal-scroll.json"), JSON.stringify({ rate, samples, p95: sorted[Math.ceil(sorted.length * .95)-1], turns: await page.locator(".conversation-turn").count(), dom: await cdp.send("Memory.getDOMCounters"), method: "wheel event to next RAF; separate profiled run at maximal retained transcript window; not paint or field INP" }, null, 2));
    expect(samples.feedback).toHaveLength(30);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 1 });
  }
  const before = await page
    .locator(".conversation-turn")
    .last()
    .getAttribute("data-turn-id");
  await scroll.dispatchEvent("wheel", { deltaY: 1 });
  await scroll.evaluate((el) => {
    el.scrollTop = el.scrollHeight;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect
    .poll(() =>
      page.locator(".conversation-turn").last().getAttribute("data-turn-id"),
    )
    .not.toBe(before);
  expect(await page.locator(".conversation-turn").count()).toBeLessThanOrEqual(
    160,
  );
  await page
    .getByRole("button", { name: "Return to latest", exact: true })
    .click();
  await expect(page.locator('[data-turn-id="turn-1000"]')).toBeAttached();
  await expect(page.locator(".conversation-turn")).toHaveCount(10);
  mkdirSync(output!, { recursive: true });
  writeFileSync(
    resolve(output!, "window.json"),
    JSON.stringify(observations, null, 2),
  );
  await cdp.detach();
});
