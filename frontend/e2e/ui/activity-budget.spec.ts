import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";
import {
  conversationRoute,
  invalidateConversation,
} from "./conversation-fixture";

for (const kind of ["tool", "thinking"]) {
  test(`truncated ${kind} downloads its versioned content only on request`, async ({
    page,
  }) => {
    const f = await workspaceFixture(page);
    await conversationRoute(page, (route) =>
      route.fulfill({
        body: JSON.stringify({
          session: { ...f.sessions[0], state: "running" },
          turns: [{ id: "t", state: "running" }],
          timeline: [
            {
              id: "detail",
              turn_id: "t",
              cursor: 1,
              kind,
              state: "running",
              name: "bash",
              content: "Short excerpt",
              result: "Short excerpt",
              message_id: "must-not-fetch-message",
              truncated: true,
              detail_version: "immutable-v1",
            },
          ],
        }),
      }),
    );
    const complete = "Full original result.\n".repeat(5000);
    const data = JSON.stringify({
      id: "detail",
      kind,
      ...(kind === "tool" ? { result: complete } : { content: complete }),
    });
    let reads = 0;
    await page.route("**/view/items/detail/detail?*", (route) => {
      reads++;
      const params = new URL(route.request().url()).searchParams;
      expect(params.get("version")).toBe("immutable-v1");
      const offset = Number(params.get("cursor") || 0),
        end = offset + 32768;
      return route.fulfill({
        json: {
          data: data.slice(offset, end),
          ...(end < data.length ? { next: String(end) } : {}),
        },
      });
    });
    await page.goto("/?project=battery&session=analysis");
    const group = page.locator(".activity-group");
    await expect(group).toBeVisible();
    expect(reads).toBe(0);
    if (kind === "thinking") await group.locator(".activity-summary").click();
    await group.locator("details > summary").click();
    await expect(group.locator(".activity-output")).toContainText(
      "Short excerpt",
    );
    expect(reads).toBe(0);
    const received = page.waitForEvent("download");
    await group
      .getByRole("button", { name: "Download full message", exact: true })
      .click();
    const download = await received;
    const path = await download.path();
    expect(readFileSync(path!, "utf8")).toBe(complete);
    expect(reads).toBeGreaterThan(1);
  });
}

async function activityFixture(page: Page) {
  await workspaceFixture(page);
  let phase = 0;
  const markdown = Array.from(
    { length: 12 },
    (_, i) =>
      `### Analysis ${i}\n\nInspect **capacity** and compare measurements.\n\n| Cell | Value |\n| --- | --- |\n| A | 100 |\n| B | 95 |\n\n`,
  ).join("");
  await conversationRoute(page, (route) =>
    route.fulfill({
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: "running",
          revision: phase + 1,
          control_revision: phase + 1,
        },
        turns: [
          { id: "t", state: "running", created_at: "2026-09-23T00:00:00Z" },
        ],
        turn_summaries: [
          {
            id: "t",
            state: "running",
            final_message_id: "ready",
            created_at: "2026-09-23T00:00:00Z",
            finished_at: "2026-09-23T00:00:05Z",
          },
        ],
        timeline: [
          {
            id: "u",
            turn_id: "t",
            cursor: 1,
            kind: "user",
            content: "Inspect forty independent measurements.",
          },
          {
            id: "ready",
            turn_id: "t",
            cursor: 2,
            kind: "assistant",
            message_id: "ready",
            content: "Ready",
          },
          ...(phase
            ? Array.from({ length: 40 }, (_, i) => [
                {
                  id: `thinking-${i}`,
                  turn_id: "t",
                  cursor: i * 2 + 3,
                  kind: "thinking",
                  state: "completed",
                  content: markdown,
                },
                {
                  id: `tool-${i}`,
                  turn_id: "t",
                  cursor: i * 2 + 4,
                  kind: "tool",
                  state: i >= 38 ? "running" : "completed",
                  name: "bash",
                  arguments: JSON.stringify({ command: `Inspect sample ${i}` }),
                  result: JSON.stringify({
                    content: [
                      {
                        type: "text",
                        text:
                          `Sample ${i} output\n` +
                          "Measured capacity remains stable.\n".repeat(60),
                      },
                    ],
                  }),
                },
              ]).flat()
            : []),
        ],
      }),
    }),
  );
  return async () => {
    phase++;
    await invalidateConversation(page);
  };
}

test("closed details release bodies while active rows and open scroll state survive updates", async ({
  page,
}) => {
  const update = await activityFixture(page);
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".chat-message.assistant")).toContainText("Ready");
  await update();
  const group = page.locator(".activity-group"),
    last = group.locator('[data-timeline-id="tool-39"]');
  await expect(last).toBeVisible();
  await expect(group.locator(".tool-trace:visible")).toHaveCount(2);
  await expect(group.locator(".activity-output")).toHaveCount(0);
  await group.locator(".activity-summary").click();
  await expect(group.locator(".activity-output")).toHaveCount(0);
  const thought = group.locator('[data-timeline-id="thinking-0"]');
  await thought.locator("summary").click();
  await expect(thought.locator(".markdown-body")).toContainText("Analysis 11");
  await expect(group.locator(".activity-output")).toHaveCount(1);
  await thought.locator("summary").click();
  await expect(group.locator(".activity-output")).toHaveCount(0);
  const tool = group.locator('[data-timeline-id="tool-0"]');
  await tool.locator("summary").click();
  const output = tool.locator(".activity-output");
  await expect(output).toContainText("Sample 0 output");
  await output.evaluate((node) => {
    node.scrollTop = 200;
    node.dispatchEvent(new Event("scroll"));
  });
  await update();
  await expect.poll(() => output.evaluate((node) => node.scrollTop)).toBe(200);
  await group.locator(".activity-summary").click();
  await expect(group.locator(".activity-output")).toHaveCount(0);
  await group.locator(".activity-summary").click();
  await expect(tool).toHaveAttribute("open", "");
  await expect.poll(() => output.evaluate((node) => node.scrollTop)).toBe(200);
  await expect(group.locator(".activity-output")).toHaveCount(1);
});

test("active turn collapsed-detail CPU profile", async ({ page, context }) => {
  const output = process.env.ACTIVITY_PROFILE_OUTPUT;
  test.skip(!output, "Requires a new profile output directory");
  const activate = await activityFixture(page);
  await page.addInitScript(() => {
    const tasks: number[] = [];
    new PerformanceObserver((list) =>
      tasks.push(...list.getEntries().map((e) => e.duration)),
    ).observe({ type: "longtask" });
    Reflect.set(window, "activityTasks", tasks);
  });
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".chat-message.assistant")).toContainText("Ready");
  const cdp = await context.newCDPSession(page);
  if (process.env.ACTIVITY_CPU_RATE)
    await cdp.send("Emulation.setCPUThrottlingRate", {
      rate: Number(process.env.ACTIVITY_CPU_RATE),
    });
  await cdp.send("Profiler.enable");
  await cdp.send("Profiler.start");
  await page.evaluate(() => {
    Reflect.get(window, "activityTasks").length = 0;
  });
  const start = performance.now();
  await activate();
  await expect(
    page.locator('[data-timeline-id="tool-39"] > summary'),
  ).toContainText("Running bash");
  const useful = performance.now() - start;
  await page.waitForTimeout(300);
  const profile = await cdp.send("Profiler.stop");
  await cdp.send("HeapProfiler.collectGarbage");
  const dom = await cdp.send("Memory.getDOMCounters");
  const samples = await page.evaluate(() => ({
    longTasks: Reflect.get(window, "activityTasks"),
    outputs: document.querySelectorAll(".activity-output").length,
    markdown: document.querySelectorAll(".activity-output .markdown-body")
      .length,
  }));
  mkdirSync(output!, { recursive: true });
  writeFileSync(
    resolve(output!, "activity.json"),
    JSON.stringify({ useful, dom, samples }, null, 2),
  );
  writeFileSync(
    resolve(output!, "activity.cpuprofile"),
    JSON.stringify(profile.profile),
  );
  await cdp.detach();
});
