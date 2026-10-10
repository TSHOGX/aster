import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import { conversationRoute } from "./conversation-fixture";

test("native receipt card reads bounded metadata and follows result and tombstone hints", async ({
  page,
}, info) => {
  await workspaceFixture(page);
  let title = "Match result";
  let state = "active",
    reads = 0;
  await conversationRoute(page, (route) =>
    route.fulfill({
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Check result",
          state: "idle",
          revision: 1,
          control_revision: 1,
          deletion: 0,
        },
        messages: [],
        turns: [],
        record_cursor: 1,
        has_earlier: false,
        timeline: [
          {
            id: "user",
            cursor: 1,
            turn_id: "t",
            kind: "user",
            content: "Check the result later",
          },
          {
            id: "tool",
            cursor: 2,
            turn_id: "t",
            kind: "tool",
            name: "schedules_create",
            state: "completed",
            result: JSON.stringify({
              content: [{ type: "text", text: "Saved task" }],
              details: {
                kind: "schedule_receipt",
                version: 1,
                project_id: "battery",
                schedule_id: "task",
                url: "https://evil.invalid",
              },
            }),
          },
          {
            id: "answer",
            cursor: 3,
            turn_id: "t",
            kind: "assistant",
            content: "Scheduled.",
          },
        ],
      }),
    }),
  );
  await page.route("**/schedules/task/metadata", (route) => {
    reads++;
    return route.fulfill({
      json: {
        id: "task",
        project_id: "battery",
        project_title: "Battery",
        title,
        state,
        trigger_kind: "at",
        run_at: "2099-01-01T00:00:00Z",
        timezone: "UTC",
        cron: "",
        revision: 1,
        ...(state === "active"
          ? { next_at: "2099-01-01T00:00:00Z" }
          : { last_state: "completed", last_session_id: "result" }),
      },
    });
  });
  await page.goto("/?project=battery&session=analysis");
  const card = page.locator('[data-schedule-id="task"]');
  await expect(card).toContainText("Match result");
  await page.setViewportSize({ width: 390, height: 844 });
  const controls = card.locator(".schedule-card-actions");
  await expect(controls.getByRole("link", { name: "View task" })).toBeVisible();
  expect(await controls.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await controls.getByRole("link", { name: "View task" }).focus();
  await expect(controls.getByRole("link", { name: "View task" })).toBeFocused();
  await page.screenshot({ path: info.outputPath("schedule-card-mobile.png"), fullPage: true });
  await expect(card.getByRole("link", { name: "View task" })).toHaveAttribute(
    "href",
    /schedule=task/,
  );
  expect(await card.locator('a[href^="https:"]').count()).toBe(0);
  title = "A very long scheduled research task title that should remain readable on a narrow mobile screen — " + "x".repeat(80);
  state = "exhausted";
  await page.evaluate(() =>
    window.dispatchEvent(
      new CustomEvent("fixture-resource-event", {
        detail: {
          resource_kind: "run.updated",
          resource_id: "task",
          payload: { project_id: "battery", schedule_id: "task" },
        },
      }),
    ),
  );
  await expect(card).toContainText("Completed");
  await expect(card).toContainText(title);
  expect(await card.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
  await expect(
    card.getByRole("link", { name: "Open conversation" }),
  ).toHaveAttribute("href", /session=result/);
  state = "deleted";
  await page.evaluate(() =>
    window.dispatchEvent(
      new CustomEvent("fixture-resource-event", {
        detail: {
          resource_kind: "schedule.deleted",
          resource_id: "task",
          payload: { project_id: "battery", schedule_id: "task" },
        },
      }),
    ),
  );
  await expect(card).toContainText("This task was deleted.");
  await expect(card.getByRole("button", { name: "Cancel task" })).toHaveCount(
    0,
  );
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  const badge = card.locator('.schedule-status[data-state="deleted"]');
  await expect(badge).toHaveText("Deleted");
  expect(await card.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await page.screenshot({ path: info.outputPath("schedule-card-deleted.png"), fullPage: true });
  expect(reads).toBeLessThan(8);
});
