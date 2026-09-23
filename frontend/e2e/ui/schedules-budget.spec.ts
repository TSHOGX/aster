import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

const summary = (n: number) => ({ id: `task-${n}`, project_id: "battery", project_title: "Battery research", title: `Task ${n}`, timezone: "UTC", enabled: true, revision: 1 });

test("schedule reads are explicit, single-flight and replace bounded pages", async ({ page }) => {
  await workspaceFixture(page);
  let calls = 0, release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/schedules?*", async route => {
    calls++;
    if (calls === 1) await pending;
    const url = new URL(route.request().url());
    const start = Number(url.searchParams.get("after") || "0");
    await route.fulfill({ json: { schedules: Array.from({ length: 50 }, (_, i) => summary(start + i)), next: String(start + 50) } });
  });
  await page.clock.install();
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "Scheduled", exact: true }).click();
  try {
    await expect(page.locator(".schedules-page")).toHaveAttribute("aria-busy", "true");
    await expect(page.getByText("No scheduled tasks yet.", { exact: true })).toHaveCount(0);
    await page.clock.runFor(30000);
    expect(calls).toBe(1);
    release();
    await expect(page.locator(".schedule-row")).toHaveCount(50);
    await page.clock.runFor(30000);
    expect(calls).toBe(1);
    for (const start of [50, 100, 150]) {
      await page.getByRole("button", { name: "Next page", exact: true }).click();
      await expect(page.locator(".schedule-row").first()).toContainText(`Task ${start}`);
      await expect(page.locator(".schedule-row")).toHaveCount(50);
    }
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(page.locator(".schedules-page")).toHaveAttribute("aria-busy", "false");
    expect(calls).toBe(5);
    await expect(page.locator(".schedule-row").first()).toContainText("Task 150");
    await page.getByRole("button", { name: "First page", exact: true }).click();
    await expect(page.locator(".schedule-row").first()).toContainText("Task 0");
  } finally { release(); }
});

test("schedule filter changes fence late replies and full prompts load only for editing", async ({ page }) => {
  await workspaceFixture(page);
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  let first = true, details = 0, delivered = false;
  await page.route("**/api/schedules?*", async route => {
    if (first) { first = false; await pending; await route.fulfill({ json: { schedules: [summary(0)] } }); delivered = true; return; }
    await route.fulfill({ json: { schedules: [{ ...summary(1), project_title: "Battery research" }] } });
  });
  await page.route("**/api/projects/battery/schedules/task-1", route => {
    details++;
    return route.fulfill({ json: { ...summary(1), prompt: "The complete private scheduled prompt", cron: "", deleted: false } });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "Scheduled", exact: true }).click();
  try {
    await page.getByRole("button", { name: "Filter by Project", exact: true }).click();
    await page.getByRole("option", { name: "Battery research", exact: true }).click();
    await expect(page.locator(".schedule-row")).toContainText("Task 1");
    release();
    await expect.poll(() => delivered).toBe(true);
    await expect(page.locator(".schedule-row")).not.toContainText("Task 0");
    expect(details).toBe(0);
    await page.locator(".schedule-description").click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByRole("dialog").locator("textarea")).toHaveValue("The complete private scheduled prompt");
    expect(details).toBe(1);
  } finally { release(); }
});

test("schedule run history replaces each page while preserving the editor draft", async ({ page }) => {
  await workspaceFixture(page);
  await page.route("**/api/schedules?*", route => route.fulfill({ json: { schedules: [summary(1)] } }));
  await page.route("**/api/projects/battery/schedules/task-1", route => route.fulfill({ json: { ...summary(1), prompt: "Original prompt", cron: "", deleted: false } }));
  let historyPages = 0;
  await page.route("**/api/projects/battery/schedules/task-1/runs?*", route => {
    historyPages++;
    const after = new URL(route.request().url()).searchParams.get("after");
    const start = after ? Number(after) + 1 : 0;
    return route.fulfill({ json: { runs: Array.from({ length: 50 }, (_, i) => ({ id: String(start + i), project_id: "battery", schedule_id: "task-1", session_id: "analysis", turn_id: `turn-${start + i}`, state: "completed", created_at: "2026-09-23T00:00:00Z" })) } });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "Scheduled", exact: true }).click();
  await page.locator(".schedule-description").click();
  const dialog = page.getByRole("dialog");
  await dialog.locator("textarea").fill("Unsaved scheduled draft");
  await dialog.getByRole("button", { name: "Run history", exact: true }).click();
  await expect(dialog.locator(".run-history button")).toHaveCount(51);
  await dialog.locator(".run-history").getByRole("button", { name: "Next page", exact: true }).click();
  await expect(dialog.locator(".run-history button")).toHaveCount(51);
  await expect.poll(() => historyPages).toBe(2);
  await expect(dialog.locator(".run-history").getByRole("button", { name: "Next page", exact: true })).toBeEnabled();
  await expect(dialog.locator(".run-history button")).toHaveCount(51);
  await expect(dialog.locator("textarea")).toHaveValue("Unsaved scheduled draft");
});
