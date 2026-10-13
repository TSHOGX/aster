import { expect, test } from "@playwright/test";
import { displayName, displayInitial } from "../../../web/brand";
import { workspaceFixture } from "./fixture";

test("deployment display name appears on sign-in, navigation and public shares", async ({ page }) => {
  await page.route("**/api/auth/session", route => route.fulfill({
    status: 401, json: { error: "Sign in required" },
  }));
  await page.goto("/");
  await expect(page.locator(".project-login h1")).toHaveText(displayName);
  await expect(page.locator(".brand-mark")).toHaveText(displayInitial);
  await expect(page).toHaveTitle(displayName);
  await expect(page.locator('meta[name="application-name"]')).toHaveAttribute("content", displayName);

  await page.unroute("**/api/auth/session");
  await workspaceFixture(page);
  await page.reload();
  await expect(page.locator(".project-brand strong")).toHaveText(displayName);
  await expect(page.locator(".project-brand .brand-mark")).toHaveText(displayInitial);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator(".mobile-navigation > span")).toHaveText(displayName);
  await expect(page.locator(".mobile-navigation > span")).toBeVisible();

  await page.route("**/api/shared/display-fixture", route => route.fulfill({
    json: { title: "Shared example", files: [] },
  }));
  await page.route("**/api/shared/display-fixture/messages?*", route => route.fulfill({
    json: { messages: [] },
  }));
  await page.goto("/s/display-fixture");
  await expect(page.locator(".public-share-brand > small")).toContainText(displayName);
  await expect(page).toHaveTitle(displayName);
  await expect(page.locator('meta[name="application-name"]')).toHaveAttribute("content", displayName);
});
