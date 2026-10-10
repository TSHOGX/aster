import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

for (const width of [390, 1600]) {
  test(`temporary account completes password setup at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await workspaceFixture(page);
    const identity = {
      id: "researcher", username: "researcher", display_name: "researcher",
      status: "active", role: "user", revision: 1, session_revision: 1,
      must_change_password: true,
    };
    let restricted = true;
    const productRequests: string[] = [];
    page.on("request", request => {
      const path = new URL(request.url()).pathname;
      if (restricted && path.startsWith("/api/") && !path.startsWith("/api/auth/")) productRequests.push(path);
    });
    await page.route("**/api/auth/session", route => route.fulfill({ json: identity }));
    await page.route("**/api/auth/password/setup", async route => {
      expect(route.request().postDataJSON()).toEqual({ new_password: "my-permanent-password" });
      restricted = false;
      await route.fulfill({ json: { ...identity, must_change_password: false, revision: 2, session_revision: 2 } });
    });
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Set your password" })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("heading", { name: "Set your password" })).toBeVisible();
    expect(productRequests).toEqual([]);
    await page.getByLabel("New password").fill("my-permanent-password");
    await page.getByRole("button", { name: "Save password" }).click();
    await expect(page.getByRole("heading", { name: "Set your password" })).toHaveCount(0);
    await expect(page.locator(".project-shell")).toBeVisible();
  });
}
