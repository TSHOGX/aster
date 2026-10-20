import { expect, test } from "@playwright/test";
import { origin, settleOverview, provision, login } from "./fixture";

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`administrators manage write-only model connections ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "evolution-space-admin", password: "browser-fixture-password" } })).ok()).toBeTruthy();
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/settings/models/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("heading", { name: tr("Model connections", "模型连接"), level: 1 })).toBeVisible();
      await page.getByRole("button", { name: tr("Add connection", "添加连接"), exact: true }).click();
      const id = `fixture-${crypto.randomUUID().slice(0, 6)}`;
      const form = page.getByRole("region", { name: tr("Add connection", "添加连接") });
      await form.getByLabel(tr("Connection ID", "连接 ID"), { exact: true }).fill(id);
      await form.getByLabel(tr("Display name", "显示名称"), { exact: true }).fill("Fixture chat");
      await form.getByLabel(tr("Base URL", "Base URL"), { exact: true }).fill("http://127.0.0.1:18597/v1");
      await form.getByLabel(tr("API key", "API 密钥"), { exact: true }).fill("sk-browser-fixture-key");
      await form.getByLabel(tr("Model", "模型"), { exact: true }).fill("fixture-chat");
      await form.getByRole("button", { name: tr("Test connection", "测试连接"), exact: true }).click();
      await expect(form.getByRole("status")).toContainText(tr("Connection works", "连接正常"));
      // A successful test loads the service's model list into a picker.
      await form.getByLabel(tr("Model", "模型"), { exact: true }).selectOption("fixture-chat");
      await form.getByRole("button", { name: tr("Save connection", "保存连接"), exact: true }).click();
      const table = page.getByRole("table", { name: tr("Model connections", "模型连接") });
      await expect(table.getByRole("row").filter({ hasText: id })).toContainText(tr("Enabled", "已启用"));
      expect(await page.content()).not.toContain("sk-browser-fixture-key");
      const models = await (await page.request.get(`/evolution-space/api/v1/groups/${beta.id}/models`)).json();
      expect(models.items.some((item: { id: string; label?: string }) => item.id === id && item.label?.includes("fixture-chat"))).toBe(true);
      // Editing without a key keeps the stored one; changing the model re-pins.
      await table.getByRole("row").filter({ hasText: id }).getByRole("button", { name: tr("Edit", "编辑"), exact: true }).click();
      const edit = page.getByRole("region", { name: tr("Edit connection", "编辑连接") });
      await edit.getByRole("button", { name: tr("Test connection", "测试连接"), exact: true }).click();
      await expect(edit.getByRole("status")).toContainText(tr("Connection works", "连接正常"));
      await edit.getByLabel(tr("Model", "模型"), { exact: true }).selectOption("fixture-reasoner");
      await edit.getByRole("button", { name: tr("Save connection", "保存连接"), exact: true }).click();
      await expect(table.getByRole("row").filter({ hasText: id }).getByRole("cell").nth(1)).toHaveText("2");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: info.outputPath("model-connections.png"), fullPage: true });
      expect(errors).toEqual([]);
    } finally { await context.close(); }
    // Group members see the section disabled and cannot read the API.
    const member = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale });
    const viewer = await member.newPage();
    try {
      await login(viewer, user); await settleOverview(viewer); await viewer.goto(`/evolution-space/groups/${beta.id}/`);
      await viewer.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(viewer.getByText(tr("Model connections", "模型连接"), { exact: false }).first()).toHaveAttribute("aria-disabled", "true");
      expect((await viewer.request.get("/evolution-space/api/v1/model-connections")).status()).toBe(403);
    } finally { await member.close(); }
  });
}
