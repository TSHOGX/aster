import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";
import {
  conversationRoute,
  invalidateConversation,
} from "./conversation-fixture";

test("parallel calls stay visible from preparation to partial output and completion", async ({
  page,
}) => {
  await workspaceFixture(page);
  let phase = 0;
  await conversationRoute(page, (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: phase === 4 ? "idle" : "running",
          revision: phase + 1,
          control_revision: 1,
          deletion: 0,
        },
        messages: [],
        turns: phase === 4 ? [] : [{ id: "t", state: "running" }],
        record_cursor: phase + 1,
        has_earlier: false,
        timeline: [
          {
            id: "u",
            cursor: 1,
            turn_id: "t",
            kind: "user",
            content:
              "Inspect the measurements and run the analysis in parallel.",
          },
          ...Array.from({ length: 16 }, (_, n) => ({
            id: `history-${n}`,
            cursor: n + 2,
            turn_id: "t",
            kind: "tool",
            name: "read",
            arguments: JSON.stringify({
              path: `/workspace/measurements/cell-${n}.csv`,
            }),
            state: "completed",
            result: JSON.stringify({
              content: [{ type: "text", text: "Read 200 measurements." }],
            }),
          })),
          {
            id: "a",
            cursor: 18,
            turn_id: "t",
            run_id: "t",
            kind: "tool",
            name: "bash",
            arguments: JSON.stringify({
              command:
                "python /workspace/analyze.py --compare " +
                "measurements ".repeat(25),
            }),
            state:
              phase === 0 ? "pending" : phase === 3 ? "completed" : "running",
            result:
              phase >= 2
                ? JSON.stringify({
                    content: [
                      {
                        type: "text",
                        text: Array.from(
                          { length: 60 },
                          (_, n) => `Sample ${n}: capacity retention 95%`,
                        ).join("\n"),
                      },
                    ],
                  })
                : undefined,
          },
          {
            id: "b",
            cursor: 19,
            turn_id: "t",
            run_id: "t",
            kind: "tool",
            name: "read",
            arguments: JSON.stringify({
              path: "/workspace/reports/summary.md",
            }),
            state:
              phase === 0
                ? "pending"
                : phase >= 2 && phase < 4
                  ? "completed"
                  : "running",
            result:
              phase >= 2 && phase < 4
                ? JSON.stringify({
                    content: [{ type: "text", text: "Report ready" }],
                  })
                : undefined,
          },
        ],
      }),
    }),
  );
  await page.goto("/?project=battery&session=analysis");
  const group = page.locator(".activity-group");
  const toggle = group.locator(".activity-summary");
  const a = group.locator('[data-timeline-id="a"]');
  const b = group.locator('[data-timeline-id="b"]');
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(a).toBeVisible();
  await expect(b).toBeVisible();
  await expect(a.locator("summary")).toContainText("Preparing bash");
  await expect(b.locator("summary")).toContainText("Preparing read");
  await expect(group.locator(".tool-trace:visible")).toHaveCount(2);
  await a.locator("summary").click();
  await expect(a.locator(".tool-panel")).toContainText("Waiting to execute…");
  phase = 1;
  await invalidateConversation(page);
  await expect(a.locator("summary")).toContainText("Running bash");
  await expect(b.locator("summary")).toContainText("Running read");
  await expect(a).toHaveAttribute("open", "");
  await expect(a.locator(".tool-panel")).toContainText(
    "Waiting for tool result…",
  );
  for (const row of [a, b])
    await expect(row.locator(".activity-line")).toHaveCSS(
      "animation-name",
      "activity-ripple",
    );
  await a.locator("summary").click();
  const evidence = resolve(
    __dirname,
    "../../test-results/ui/screenshots",
  );
  mkdirSync(evidence, { recursive: true });
  await page.screenshot({
    path: resolve(evidence, "tools-parallel-running.png"),
  });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(a.locator(".activity-line")).toHaveCSS("animation-name", "none");
  await expect(b.locator(".activity-line")).not.toHaveCSS(
    "color",
    "rgba(0, 0, 0, 0)",
  );
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await toggle.click();
  await expect(group.locator(".tool-trace:visible")).toHaveCount(18);
  const list = group.locator(".activity-details");
  expect(
    await list.evaluate(
      (n) => n.scrollHeight > n.clientHeight && n.clientHeight <= 360,
    ),
  ).toBe(true);
  const first = group.locator('[data-timeline-id="history-0"]');
  expect(await first.evaluate((n) => n.getBoundingClientRect().x)).toBe(
    await toggle.evaluate((n) => n.getBoundingClientRect().x),
  );
  await a.locator("summary").click();
  phase = 2;
  await invalidateConversation(page);
  await expect(a.locator(".activity-output")).toContainText(
    "Sample 59: capacity retention 95%",
  );
  await expect(b).toHaveAttribute("data-state", "completed");
  await expect(b.locator(".activity-line")).toHaveCSS("animation-name", "none");
  await expect(a.locator(".activity-line")).toHaveCSS(
    "animation-name",
    "activity-ripple",
  );
  const output = a.locator(".activity-output");
  expect(
    await output.evaluate(
      (n) => n.scrollHeight > n.clientHeight && n.clientHeight <= 240,
    ),
  ).toBe(true);
  await a.locator(".tool-panel").scrollIntoViewIfNeeded();
  await page.screenshot({
    path: resolve(evidence, "tools-expanded-output.png"),
  });
  await output.evaluate((n) => {
    n.scrollTop = n.scrollHeight;
  });
  expect(await output.evaluate((n) => n.scrollTop)).toBeGreaterThan(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(a.locator("summary")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({ path: resolve(evidence, "tools-mobile.png") });
  phase = 3;
  await invalidateConversation(page);
  await expect(a).toHaveAttribute("data-state", "completed");
  await expect(a).toHaveAttribute("open", "");
  await expect(group).not.toHaveClass(/is-live/);
  await toggle.click();
  await expect(group.locator(".tool-trace:visible")).toHaveCount(0);
  phase = 4;
  await invalidateConversation(page);
  await toggle.click();
  await expect(a).toHaveAttribute("data-state", "unknown");
  await expect(b).toHaveAttribute("data-state", "unknown");
  await expect(a.locator("summary")).toContainText("Outcome unknown");
  await expect(group.locator(".activity-row.is-active")).toHaveCount(0);
});
