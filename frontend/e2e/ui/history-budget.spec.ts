import { expect, test } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { historyFixture } from "./history-fixture";

test("large history preserves model copy, bounded pages and reader intent", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const fixture = await historyFixture(page);
  await page.goto("/?project=battery&session=analysis&message=answer-500");
  const scroll = page.locator(".conversation-scroll"),
    target = page.locator('[data-message-id="answer-500"]');
  await expect(target).toBeVisible();
  await expect(page.locator(".conversation-turn")).toHaveCount(10);
  await expect(page.locator(".turn-process")).toHaveCount(0);
  const turn = page.locator('[data-turn-id="turn-500"]');
  await turn.getByRole("button", { name: "Copy answer", exact: true }).click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe(fixture.answer(500));
  // Reposition to a visible item before height/font changes, then leave native
  // scroll anchoring and the application to preserve the reader's selected item.
  await target.evaluate((node) => node.scrollIntoView({ block: "start" }));
  const top = () =>
    target.evaluate(
      (node) =>
        node.getBoundingClientRect().top -
        document.querySelector(".conversation-scroll")!.getBoundingClientRect()
          .top,
    );
  await expect.poll(top).toBeLessThan(2);
  await page.addStyleTag({
    content:
      ".chat-message .markdown-body { font-size: 19px !important; line-height: 1.9; }",
  });
  await expect(target.locator(".markdown-body")).toHaveCSS("font-size", "19px");
  await page.waitForTimeout(250);
  expect(Math.abs(await top())).toBeLessThan(24);
  await fixture.stream();
  await page.waitForTimeout(250);
  expect(Math.abs(await top())).toBeLessThan(24);
  await scroll.dispatchEvent("wheel", { deltaY: -1 });
  await scroll.evaluate((node) => {
    node.scrollTop = 0;
    node.dispatchEvent(new Event("scroll"));
  });
  await expect(page.locator('[data-turn-id="turn-486"]')).toBeAttached();
  await expect(page.locator('[data-turn-id="turn-505"]')).toBeAttached();
  await scroll.dispatchEvent("wheel", { deltaY: 1 });
  await scroll.evaluate((node) => {
    node.scrollTop = node.scrollHeight;
    node.dispatchEvent(new Event("scroll"));
  });
  await expect(page.locator('[data-turn-id="turn-515"]')).toBeAttached();
  await page
    .getByRole("button", { name: "Return to latest", exact: true })
    .click();
  await expect(page.locator('[data-turn-id="turn-1000"]')).toBeAttached();
  const bottom = () =>
    scroll.evaluate(
      (node) => node.scrollHeight - node.scrollTop - node.clientHeight,
    );
  await expect.poll(bottom).toBeLessThan(3);
  await page.setViewportSize({ width: 1100, height: 760 });
  await expect.poll(bottom).toBeLessThan(3);
  await page.addStyleTag({
    content: ".chat-message .markdown-body { font-size: 21px !important; }",
  });
  await expect.poll(bottom).toBeLessThan(3);
  await page
    .locator(".agent-panel textarea")
    .fill("Retained draft\n".repeat(8));
  await expect.poll(bottom).toBeLessThan(3);
  await fixture.stream();
  await expect.poll(bottom).toBeLessThan(3);
  await scroll.dispatchEvent("wheel", { deltaY: -1 });
  await scroll.evaluate((node) => {
    node.scrollTop -= 350;
    node.dispatchEvent(new Event("scroll"));
  });
  const reading = await scroll.evaluate((node) => node.scrollTop);
  await fixture.stream();
  await page.waitForTimeout(250);
  expect(
    Math.abs((await scroll.evaluate((node) => node.scrollTop)) - reading),
  ).toBeLessThan(24);
  await expect(
    page.getByRole("button", { name: "Return to latest", exact: true }),
  ).toBeVisible();
});

test("inspecting process history leaves automatic following until latest is requested", async ({
  page,
}) => {
  const fixture = await historyFixture(page, 1, 1);
  await page.goto("/?project=battery&session=analysis");
  const turn = page.locator('[data-turn-id="turn-1"]');
  await expect(turn).toBeVisible();
  await turn.locator(".turn-work-toggle").click();
  await expect(
    page.getByRole("button", { name: "Return to latest", exact: true }),
  ).toBeVisible();
  await turn.locator(".activity-summary").click();
  await turn.locator(".thinking-trace > summary").click();
  const reading = await page
    .locator(".conversation-scroll")
    .evaluate((node) => node.scrollTop);
  await fixture.stream();
  await page.waitForTimeout(250);
  expect(
    Math.abs(
      (await page
        .locator(".conversation-scroll")
        .evaluate((node) => node.scrollTop)) - reading,
    ),
  ).toBeLessThan(24);
});

test("history CPU and composer feedback profile", async ({ page, context }) => {
  const output = process.env.HISTORY_PROFILE_OUTPUT;
  test.skip(!output, "Requires a new profile output directory");
  test.setTimeout(120000);
  await historyFixture(page);
  await page.addInitScript(() => {
    const feedback: number[] = [],
      tasks: number[] = [];
    document.addEventListener(
      "keydown",
      (event) => {
        if (!(event.target instanceof HTMLTextAreaElement)) return;
        const start = performance.now();
        requestAnimationFrame(() => feedback.push(performance.now() - start));
      },
      true,
    );
    new PerformanceObserver((list) =>
      tasks.push(...list.getEntries().map((e) => e.duration)),
    ).observe({ type: "longtask" });
    Reflect.set(window, "historySamples", { feedback, tasks });
  });
  await page.goto("/?project=battery&session=analysis&message=answer-500");
  await expect(page.locator('[data-message-id="answer-500"]')).toBeVisible();
  for (let i = 0; i < 5; i++) {
    const before = await page.locator(".conversation-turn").count();
    await page
      .locator(".conversation-scroll")
      .dispatchEvent("wheel", { deltaY: -1 });
    await page.locator(".conversation-scroll").evaluate((node) => {
      node.scrollTop = 0;
      node.dispatchEvent(new Event("scroll"));
    });
    await expect
      .poll(() => page.locator(".conversation-turn").count())
      .toBeGreaterThan(before);
  }
  const input = page.locator(".agent-panel textarea");
  await input.fill("Warmup");
  await input.press("a");
  await page.waitForTimeout(200);
  const cdp = await context.newCDPSession(page);
  if (process.env.HISTORY_CPU_RATE)
    await cdp.send("Emulation.setCPUThrottlingRate", {
      rate: Number(process.env.HISTORY_CPU_RATE),
    });
  await cdp.send("Profiler.enable");
  await cdp.send("Profiler.start");
  await page.evaluate(() => {
    const samples = Reflect.get(window, "historySamples");
    samples.feedback.length = 0;
    samples.tasks.length = 0;
  });
  for (let i = 0; i < 30; i++) {
    await input.press("a");
    await page.waitForTimeout(50);
  }
  const profile = await cdp.send("Profiler.stop");
  await cdp.send("HeapProfiler.collectGarbage");
  const dom = await cdp.send("Memory.getDOMCounters");
  const samples = await page.evaluate(() => ({
    ...Reflect.get(window, "historySamples"),
    turns: document.querySelectorAll(".conversation-turn").length,
    process: document.querySelectorAll(".turn-process").length,
  }));
  mkdirSync(output!, { recursive: true });
  writeFileSync(
    resolve(output!, "history.cpuprofile"),
    JSON.stringify(profile.profile),
  );
  writeFileSync(
    resolve(output!, "history.json"),
    JSON.stringify({ samples, dom }, null, 2),
  );
  expect(samples.feedback).toHaveLength(30);
  await cdp.detach();
});
