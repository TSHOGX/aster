import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

test("a node-heavy HTML file opens a bounded simplified preview with complete source", async ({ page }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/large.html)";
  const body = `<html><body>${Array.from({ length: 40_000 }, (_, i) => `<p>Row ${i}</p>`).join("")}</body></html>`;
  await page.route("**/files/content?*", route => route.fulfill({ body }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  const simplified = page.getByRole("region", { name: "Simplified HTML preview large.html" });
  await expect(simplified).toContainText("Row 0");
  await expect(simplified).toContainText("Showing 2000 of 40000 text blocks");
  expect(await simplified.locator("article p").count()).toBe(2_000);
  await expect(page.locator(".html-preview")).toHaveCount(0);
  await page.getByRole("group", { name: "File view" }).getByRole("button", { name: "Source" }).click();
  await page.getByRole("button", { name: "Find in file" }).click();
  await page.locator(".cm-search input[name=search]").fill("Row 39999");
  await page.locator(".cm-search").getByRole("button", { name: "next" }).click();
  await expect(page.locator(".source-editor .cm-line").filter({ hasText: "Row 39999" })).toBeVisible();
});

test("private node-heavy HTML sample remains recoverable", async ({ page }) => {
  test.skip(!process.env.ACTUAL_PREVIEW_SAMPLES, "Requires the private captured corpus");
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/html_lg_001.html)";
  const body = readFileSync(resolve(process.env.ACTUAL_PREVIEW_SAMPLES!, "html_lg_001.html"));
  await page.route("**/files/content?*", route => route.fulfill({ body }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect(page.getByRole("region", { name: "Simplified HTML preview html_lg_001.html" })).toBeVisible();
  await page.getByRole("group", { name: "File view" }).getByRole("button", { name: "Source" }).click();
  await expect(page.locator(".source-preview")).toBeVisible();
});
