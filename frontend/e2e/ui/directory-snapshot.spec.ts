import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("an expired directory continuation resets one bounded page with a truthful notice", async ({
  page,
}) => {
  await workspaceFixture(page);
  let expired = false;
  let firstPageReads = 0;
  await page.route("**/api/projects/battery/files?**", (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("path")) return route.fallback();
    const cursor = url.searchParams.get("after");
    if (!cursor) firstPageReads++;
    if (cursor === "expired") {
      expired = true;
      return route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          code: "resync_required",
          error: "resource requires resync",
        }),
      });
    }
    const start = cursor === "fresh" ? 100 : 0;
    const entries = Array.from({ length: 100 }, (_, n) => ({
      name: `entry-${String(start + n).padStart(3, "0")}.txt`,
      kind: "file",
      size: 0,
    }));
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        entries,
        ...(!cursor ? { next: expired ? "fresh" : "expired" } : {}),
      }),
    });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "entry-099.txt", exact: true }),
  ).toBeVisible();
  await page
    .locator(".files-panel")
    .getByRole("button", { name: "Next page", exact: true })
    .click();
  await expect(
    page.getByText("Directory changed. Showing the first page.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "entry-000.txt", exact: true }),
  ).toHaveCount(1);
  const beforeRefresh = firstPageReads;
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("fixture-resource-event", {
    detail: { resource_kind: "files", payload: { reset: true, reason: "changed" } },
  })));
  await expect.poll(() => firstPageReads).toBeGreaterThan(beforeRefresh);
  await expect(page.getByText("Directory changed. Showing the first page.", { exact: true })).toBeVisible();
  await page
    .locator(".files-panel")
    .getByRole("button", { name: "Next page", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "entry-199.txt", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "entry-000.txt", exact: true }),
  ).toHaveCount(0);
  await expect(page.locator(".file-row")).toHaveCount(100);
  await expect(page.locator(".files-panel")).not.toContainText(
    "resource requires resync",
  );
});
