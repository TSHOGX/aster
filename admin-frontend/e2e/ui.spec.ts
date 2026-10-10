import { expect, test } from "@playwright/test";
const account = { id: "admin-id", username: "admin", display_name: "Administrator", role: "admin", status: "active", revision: 1, session_revision: 1, must_change_password: false };
test("production admin deep links keep every script under the admin base path", async ({ page }) => {
  await page.route("**/api/auth/session", route => route.fulfill({ json: account }));
  await page.goto("/admin/users/example");
  await expect(page.getByRole("heading", { level: 1 })).toContainText("administration");
  await expect(page.getByRole("status")).toHaveCount(0);
  const scripts = await page.locator("script[src]").evaluateAll(nodes => nodes.map(node => node.getAttribute("src")!));
  expect(scripts.filter(src => src.includes("/_next/static/")).every(src => src.startsWith("/admin/_next/static/"))).toBe(true);
  await page.reload();
  await expect(page.getByRole("status")).toHaveCount(0);
});
test("non-admin access is denied without a login redirect loop", async ({ page }) => {
  const privileged: string[] = [];
  page.on("request", request => { if (request.url().includes("/api/admin/")) privileged.push(request.url()); });
  await page.route("**/api/auth/session", route => route.fulfill({ json: { ...account, role: "user" } }));
  await page.goto("/admin/users");
  await expect(page.getByRole("heading", { name: "Access denied" })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/admin/users");
  expect(privileged).toEqual([]);
  await expect(page.getByRole("link", { name: "Return to workbench" })).toHaveAttribute("href", "/");
});
for (const restricted of [false, true]) test(`login and password setup return to the requested admin page (${restricted})`, async ({ page }) => {
  await page.route("**/api/auth/session", route => route.fulfill(restricted ? { json: { ...account, must_change_password: true } } : { status: 401, json: { error: "login required" } }));
  await page.goto("/admin/users/example?tab=security");
  await page.waitForURL(url => url.pathname === "/" && url.searchParams.get("return_to") === "/admin/users/example?tab=security");
});
