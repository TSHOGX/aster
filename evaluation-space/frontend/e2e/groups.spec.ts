import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
const origin = "https://127.0.0.1:18543";
async function provision(admin: Page) {
  expect((await admin.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "evaluation-admin", password: "browser-fixture-password" } })).ok()).toBeTruthy();
  const groups = (await (await admin.request.get("/api/admin/groups?limit=50")).json()).groups as { id: string; name: string }[];
  const alpha = groups.find(group => group.name === "Team Alpha")!;
  const beta = groups.find(group => group.name === "Team Beta")!;
  const secret = groups.find(group => group.name === "Secret Team")!;
  expect(alpha && beta && secret).toBeTruthy();
  const created = await admin.request.post("/api/admin/users", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), username: `viewer-${crypto.randomUUID()}`, display_name: "Group viewer", role: "user", group_ids: [alpha.id, beta.id] } });
  expect(created.status(), await created.text()).toBe(201);
  const user = await created.json();
  const grant = async (group: string, action: string, effect = "allow", revision = 0) => {
    const response = await admin.request.put(`/api/admin/groups/${group}/optimization/grants`, { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), account_id: user.user.id, action, effect, expected_revision: revision } });
    expect(response.ok(), await response.text()).toBeTruthy();
  };
  await grant(alpha.id, "config.read"); await grant(beta.id, "config.read");
  await grant(beta.id, "config.manage"); await grant(beta.id, "run.start");
  await grant(beta.id, "workflow.edit");
  // An allow without membership must not reveal even group metadata.
  await grant(secret.id, "config.read");
  return { alpha, beta, secret, user, grant };
}
async function login(page: Page, user: { user: { username: string }; temporary_password: string }) {
  expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: user.user.username, password: user.temporary_password } })).ok()).toBeTruthy();
  expect((await page.request.post("/api/auth/password/setup", { headers: { Origin: origin }, data: { new_password: "permanent-viewer-fixture-password" } })).ok()).toBeTruthy();
}
for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`scoped group switching, revocation and shared logout ${variant.locale} ${variant.width}`, async ({ page: admin, browser }, info) => {
    const { alpha, beta, secret, user, grant } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const zh = variant.locale === "zh-CN";
    const text = (en: string, cn: string) => zh ? cn : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user);
      await page.goto(`/evaluation-space/groups/${alpha.id}/?from=deep-link`);
      await page.getByRole("link", { name: text("Sign in", "登录"), exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`/evaluation-space/groups/${alpha.id}/\\?from=deep-link`));
      const overview = page.getByRole("region", { name: text("Group overview", "团队概览"), exact: true });
      await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
      await expect(overview.getByText(text("Manage configuration", "管理配置"), { exact: true }).locator("..")).toContainText(text("Not granted", "未授权"));
      await expect(overview.getByText(text("Edit and publish workflows", "编辑和发布工作流"), { exact: true }).locator("..")).toContainText(text("Not granted", "未授权"));
      await expect(page.getByRole("link", { name: "Secret Team", exact: true })).toHaveCount(0);
      const groups = await (await page.request.get("/evaluation-space/api/v1/groups?limit=1")).json();
      expect(groups.items).toHaveLength(1); expect(groups.has_more).toBe(true);
      const next = await (await page.request.get(`/evaluation-space/api/v1/groups?limit=1&after=${groups.next_after}`)).json();
      expect(next.items).toHaveLength(1); expect(next.has_more).toBe(false);
      expect((await page.request.get(`/evaluation-space/api/v1/groups/${secret.id}`)).status()).toBe(403);
      expect((await page.request.get("/evaluation-space/internal/v1/generation/admit")).status()).toBe(404);
      expect((await page.request.get("/evaluation-space/api/v1/catalog/service")).status()).toBe(404);
      await page.getByRole("link", { name: "Team Beta", exact: true }).click();
      await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      await expect(overview.getByText(text("Manage configuration", "管理配置"), { exact: true }).locator("..")).toContainText(text("Allowed", "允许"));
      await expect(overview.getByText(text("Edit and publish workflows", "编辑和发布工作流"), { exact: true }).locator("..")).toContainText(text("Allowed", "允许"));
      await expect(overview.getByText(text("Read source evidence", "读取源证据"), { exact: true }).locator("..")).toContainText(text("Not granted", "未授权"));
      await page.goto(`/evaluation-space/groups/${beta.id}/workflows/skills/`);
      await expect(page.getByRole("heading", { name: text("Workflow editor", "工作流编辑器"), exact: true })).toBeVisible();
      await expect(page.getByRole("alert").filter({ hasText: text("Workflow draft is unavailable", "工作流草稿不可用") })).toBeVisible();
      await page.goto(`/evaluation-space/groups/${beta.id}/`);
      await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      await page.screenshot({ path: info.outputPath("group-overview.png"), fullPage: true });
      await page.getByLabel(text("Search groups", "搜索团队"), { exact: true }).fill("Secret Team");
      await page.getByRole("button", { name: text("Search", "搜索"), exact: true }).click();
      await expect(page.getByRole("heading", { name: text("No matching groups", "没有匹配的团队"), exact: true })).toBeVisible();
      await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      await page.getByLabel(text("Search groups", "搜索团队"), { exact: true }).fill("");
      await page.getByRole("button", { name: text("Search", "搜索"), exact: true }).click();
      await page.getByRole("link", { name: "Team Alpha", exact: true }).click();
      await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
      await grant(alpha.id, "config.read", "deny", 1);
      await overview.getByRole("button", { name: text("Refresh", "刷新"), exact: true }).click();
      await expect(overview.getByRole("heading", { name: text("You no longer have access to this group.", "您已无权访问此团队。"), exact: true })).toBeVisible();
      await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toHaveCount(0);
      expect((await page.request.get(`/evaluation-space/api/v1/groups/${alpha.id}`)).status()).toBe(403);
      const cookies = await context.cookies();
      expect(cookies.find(cookie => cookie.name === "__Secure-evaluation_space_session")).toMatchObject({ path: "/evaluation-space/", secure: true, httpOnly: true, sameSite: "Lax" });
      const session = await (await page.request.get("/evaluation-space/api/v1/session")).json();
      expect((await page.request.post("/evaluation-space/auth/logout", { headers: { Origin: origin, "X-CSRF-Token": "bad" } })).status()).toBe(401);
      expect(session.csrf).toHaveLength(43);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.getByRole("button", { name: text("Sign out of this central session", "退出此主应用会话"), exact: true }).click();
      await expect(page.getByRole("link", { name: text("Sign in", "登录"), exact: true })).toBeVisible();
      expect((await page.request.get("/api/auth/session")).status()).toBe(401);
      expect((await page.request.get("/evaluation-space/api/v1/session")).status()).toBe(401);
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

test("failed reads clear stale metadata and language changes preserve navigation", async ({ page: admin, browser }) => {
  const { alpha, beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width: 390, height: 900 } });
  const page = await context.newPage();
  try {
    await login(page, user);
    await page.goto(`/evaluation-space/groups/${alpha.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    const overview = page.locator(".overview");
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
    const selectedPath = `**/evaluation-space/api/v1/groups/${alpha.id}`;
    await page.route(selectedPath, route => route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"group_authority_unavailable"}' }));
    await overview.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(overview.getByRole("heading", { name: "Group access is unavailable. Please retry.", exact: true })).toBeVisible();
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toHaveCount(0);
    await page.unroute(selectedPath);
    await overview.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
    let release!: () => void;
    const released = new Promise<void>(resolve => { release = resolve; });
    let started!: () => void;
    const pending = new Promise<void>(resolve => { started = resolve; });
    await page.route(selectedPath, async route => {
      const response = await route.fetch(); started(); await released;
      await route.fulfill({ response }).catch(() => {});
    });
    await overview.getByRole("button", { name: "Refresh", exact: true }).click();
    await pending;
    await page.getByRole("link", { name: "Team Beta", exact: true }).click();
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    release(); await page.unroute(selectedPath);
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toHaveCount(0);
    await page.getByRole("combobox", { name: "Language", exact: true }).click();
    await page.getByRole("option", { name: "Chinese (Simplified)", exact: true }).click();
    await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
    await expect(overview.getByRole("heading", { name: "您的权限", exact: true })).toBeVisible();
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    await expect.poll(async () => (await (await page.request.get("/api/me/preferences")).json()).language).toBe("zh");
    await page.reload();
    await expect(overview.getByRole("heading", { name: "您的权限", exact: true })).toBeVisible();
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    await page.route("**/evaluation-space/api/v1/session", route => route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"identity_unavailable"}' }));
    await page.reload();
    await expect(page.getByText("Identity is unavailable. Retry when the service is restored.", { exact: true })).toBeVisible();
    await expect(overview).toHaveCount(0);
    await page.unroute("**/evaluation-space/api/v1/session");
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
  } finally { await context.close(); }
});

test("workflow workbench edits, validates, publishes and preserves layout identity", async ({ page: admin, browser }) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  try {
    await login(page, user);
    await page.goto(`/evaluation-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    const workflow = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8"));
    workflow.id = `skills-${crypto.randomUUID().slice(0, 8)}`;
    const session = await (await page.request.get("/evaluation-space/api/v1/session")).json() as { csrf: string };
    const draftPath = `/evaluation-space/api/v1/groups/${beta.id}/workflows/${workflow.id}/draft`;
    const seeded = await page.request.put(draftPath, { headers: { Origin: origin, "X-CSRF-Token": session.csrf }, data: { id: workflow.id, expected_revision: 0, expected_layout_revision: 0, workflow, layout: { extract: { x: 12, y: 24 } } } });
    expect(seeded.status(), await seeded.text()).toBe(200);
    await page.goto(`/evaluation-space/groups/${beta.id}/workflows/${workflow.id}/`);
    await expect(page.getByRole("heading", { name: new RegExp(`Workflow editor: ${workflow.id}`), exact: true })).toBeVisible();
    await page.getByRole("button", { name: /^extract\b/ }).click();
    const executable = page.getByLabel("Executable workflow JSON", { exact: true });
    const edited = JSON.parse(await executable.inputValue());
    edited.nodes.find((node: { id: string }) => node.id === "extract").prompt.text = "Extract {{corpus}} with evidence";
    const extract = edited.nodes.find((node: { id: string }) => node.id === "extract");
    extract.prompt.digest = `sha256:${createHash("sha256").update(extract.prompt.text).digest("hex")}`;
    await executable.fill(JSON.stringify(edited, null, 2));
    await page.getByRole("button", { name: "Validate draft", exact: true }).click();
    await page.waitForTimeout(200);
    await expect(page.getByRole("status").filter({ hasText: "Executable digest" })).toContainText("sha256:");
    const digest = await page.getByRole("status").filter({ hasText: "Executable digest" }).innerText();
    await page.getByRole("button", { name: "Save draft", exact: true }).click();
    await expect(page.getByRole("status", { name: "" }).filter({ hasText: "Draft saved." })).toBeVisible();
    await page.getByLabel("Canvas layout JSON", { exact: true }).fill('{"extract":{"x":80,"y":120}}');
    await page.getByRole("button", { name: "Save draft", exact: true }).click();
    await expect(page.getByText("Draft revision 2", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "Publish revision", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "Published immutable workflow revision." })).toBeVisible();
    expect(digest).toContain("sha256:");
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  } finally { await context.close(); }
});
