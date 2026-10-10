import { expect, test } from "@playwright/test";

test("real owner authenticates both builds and preserves administrative request identity", async ({ page, browser, baseURL }) => {
  await page.goto("/admin/users");
  await page.waitForURL(url => url.pathname === "/" && url.searchParams.get("return_to") === "/admin/users");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await expect(page.getByRole("heading", { name: "Users", exact: true })).toBeVisible();
  let loseReply = true;
  await page.route("**/api/admin/users", async route => {
    if (route.request().method() === "POST" && loseReply) {
      loseReply = false;
      const response = await route.fetch(); expect(response.status()).toBe(201);
      await response.dispose(); await route.abort("failed"); return;
    }
    await route.continue();
  });
  await page.getByRole("link", { name: "Create user", exact: true }).click();
  await page.getByLabel("Username", { exact: true }).fill("browser-user");
  await page.getByLabel("Display name", { exact: true }).fill("Browser user");
  await page.getByRole("combobox", { name: "Membership", exact: true }).selectOption("none");
  await page.getByRole("button", { name: "Create account", exact: true }).click();
  await page.getByRole("button", { name: "Check original request", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("password cannot be shown again");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const listing = await (await page.request.get("/api/admin/users?search=browser-user")).json();
  expect(listing.users).toHaveLength(1); expect(listing.users[0].groups).toEqual([]);
  const userID = listing.users[0].id as string;
  expect(userID).not.toBe("browser-user");
  await page.getByRole("link", { name: "Open this account", exact: true }).click();
  await page.waitForURL(url => url.pathname === `/admin/users/${userID}`);
  await page.getByLabel("Display name", { exact: true }).fill("Browser user edited");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Browser user edited");
  await page.getByRole("button", { name: "Reset password", exact: true }).click();
  const dialog = page.getByRole("dialog"); await expect(dialog).toBeVisible();
  const temporary = await dialog.getByLabel("Temporary password", { exact: true }).inputValue();
  expect(temporary).toHaveLength(24);
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await page.getByRole("link", { name: "View account audit", exact: true }).click();
  await expect(page.getByText("account.password.reset", { exact: true })).toBeVisible();
  const audit = await (await page.request.get(`/api/admin/audit-events?target=${userID}`)).json();
  expect(JSON.stringify(audit)).not.toContain(temporary); expect(JSON.stringify(audit)).not.toContain("password_hash");
  const identity = await (await page.request.get("/api/auth/session")).json();
  const demote = await page.request.patch(`/api/admin/users/${identity.id}`, { data: { request_id: crypto.randomUUID(), expected_revision: identity.revision, role: "user" } });
  expect(demote.status()).toBe(409); expect((await demote.json()).code).toBe("last_admin");
  const project = await page.request.post("/api/projects", { data: { id: "admin-owned-project", title: "Private project" } }); expect(project.status()).toBe(201);
  const ordinaryContext = await browser.newContext({ baseURL, locale: "en-US" });
  try {
    const ordinary = await ordinaryContext.newPage();
    await ordinary.goto("/admin");
    await ordinary.getByLabel("Username", { exact: true }).fill("browser-user");
    await ordinary.getByLabel("Password", { exact: true }).fill(temporary);
    await ordinary.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(ordinary.getByRole("heading", { name: "Set your password", exact: true })).toBeVisible();
    expect((await ordinary.request.get("/api/admin/users")).status()).toBe(401);
    expect((await ordinary.request.get("/api/projects")).status()).toBe(401);
    await ordinary.getByLabel("New password", { exact: true }).fill("browser-user-permanent-password");
    await ordinary.getByRole("button", { name: "Save password", exact: true }).click();
    await expect(ordinary.getByRole("heading", { name: "Access denied", exact: true })).toBeVisible();
    expect((await ordinary.request.get("/api/admin/users")).status()).toBe(403);
    expect((await ordinary.request.get("/api/projects/admin-owned-project")).status()).toBe(404);
    await ordinary.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(ordinary.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  } finally { await ordinaryContext.close(); }
});
