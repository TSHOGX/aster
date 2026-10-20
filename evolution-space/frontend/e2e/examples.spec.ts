import { expect, test } from "@playwright/test";
import { createHash } from "node:crypto";
import { origin, settleOverview, provision, login } from "./fixture";

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`built-in examples preview, copy into a draft and install as templates ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user);
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      // The overview explains how the concepts relate, in order.
      const flow = page.getByRole("region", { name: tr("How the pieces fit together", "各部分如何协同") });
      await expect(flow.getByRole("listitem")).toHaveCount(4);
      await expect(flow.getByText(tr("Configuration binding", "配置绑定"), { exact: true })).toBeVisible();

      await page.goto(`/evolution-space/groups/${beta.id}/workflows/`);
      const gallery = page.getByRole("region", { name: tr("Built-in examples", "内置示例") });
      await expect(gallery.getByRole("button")).toHaveCount(5);
      await gallery.getByRole("button", { name: /knowledge/ }).click();
      const drawer = page.getByRole("dialog");
      await expect(drawer.getByRole("heading", { name: "knowledge", exact: true })).toBeVisible();
      // The pipeline tab explains every node; selecting one reveals its prompt.
      await expect(drawer.locator(".node-explainer li")).toHaveCount(5);
      await drawer.locator(".node-explainer").getByRole("button", { name: /consolidate/ }).click();
      await expect(drawer.getByText(tr("Prompt", "提示词"), { exact: true })).toBeVisible();
      await drawer.getByRole("tab", { name: tr("Worked sample", "完整样例") }).click();
      await expect(drawer.getByText("knowledge/metrics/active-users.md")).toBeVisible();
      await expect(drawer.getByText(tr("Metric lacks formula, unit, grain or entity", "指标缺少公式、单位、粒度或实体"))).toBeVisible();
      await drawer.getByRole("tab", { name: tr("Baseline evaluation", "基线评估") }).click();
      await expect(drawer.getByRole("table", { name: tr("Baseline thresholds", "基线阈值") })).toContainText("definition_coverage");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: info.outputPath("example-drawer.png"), fullPage: false });

      // One registered model: copying installs the example, then forks a draft.
      const draft = `my-knowledge-${crypto.randomUUID().slice(0, 6)}`;
      await drawer.getByLabel(tr("New draft ID", "新草稿 ID"), { exact: true }).fill(draft);
      await drawer.getByRole("button", { name: tr("Copy to my drafts", "复制到我的草稿"), exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`/workflows/${draft}/$`));
      // The agent node shows the registered model selected and explains its kind.
      await page.getByRole("button", { name: tr("Graph", "图"), exact: true }).click().catch(() => {});
      await page.getByRole("button", { name: tr("List", "列表"), exact: true }).click();
      await page.locator(".node-list").getByRole("button", { name: /consolidate/ }).click();
      await expect(page.getByLabel(tr("Registered model", "已注册模型"), { exact: true })).toHaveValue("browser-model@1");
      await expect(page.locator(".inspector .hint").first()).toContainText(tr("Calls a model with a prompt", "使用提示词调用模型"));

      // The installed example is now an ordinary template.
      await page.goto(`/evolution-space/groups/${beta.id}/templates/`);
      const templates = page.getByRole("region", { name: tr("Built-in examples", "内置示例") });
      await expect(templates.getByRole("button", { name: /knowledge/ })).toContainText(tr("Installed", "已安装"));
      // Templates are a shared catalog, so each variant installs its own example.
      const target = variant.locale === "zh-CN" ? "episodes" : "facts";
      await templates.getByRole("button", { name: new RegExp(target) }).click();
      // Browsers share one fixture catalog; a previous browser may have installed it.
      const dialog = page.getByRole("dialog");
      const install = dialog.getByRole("button", { name: tr("Install template", "安装模板"), exact: true });
      const opened = dialog.getByRole("link", { name: tr("Open template", "打开模板"), exact: true });
      await expect(install.or(opened)).toBeVisible();
      await (await install.isVisible() ? install : opened).click();
      await expect(page).toHaveURL(new RegExp(`/templates/example-${target}/1/\\?digest=`));
      await expect(page.getByRole("link", { name: tr("Use in configuration", "在配置中使用"), exact: true })).toBeVisible();
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });

  test(`profile wizard uploads a dataset and publishes from a baseline ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    const digest = (value: string) => `sha256:${createHash("sha256").update(value).digest("hex")}`;
    try {
      await login(page, user);
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/profiles/?preset=example-facts`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      const wizard = page.getByRole("region", { name: tr("Create an evaluation profile", "创建评估配置") });
      await expect(wizard.getByLabel(tr("Baseline preset", "基线预设"), { exact: true })).toHaveValue("example-facts");
      await expect(wizard.getByLabel(`${tr("Metric", "指标")} 1`, { exact: true })).toHaveValue("answer_accuracy");
      const publish = wizard.getByRole("button", { name: tr("Publish evaluation profile", "发布评估配置"), exact: true });
      await expect(publish).toBeDisabled();
      const id = `qa-${crypto.randomUUID().slice(0, 8)}`;
      // The upload section starts open only while the group has no datasets.
      const file = wizard.getByLabel(tr("Dataset file", "数据集文件"), { exact: true });
      if (!await file.isVisible()) await wizard.getByText(tr("Upload a dataset", "上传数据集"), { exact: true }).click();
      await file.setInputFiles({ name: "facts.jsonl", mimeType: "application/x-ndjson", buffer: Buffer.from('{"question":"Where is the health check?","answer":"GET /healthz"}\n') });
      await wizard.getByLabel(tr("Dataset ID", "数据集 ID"), { exact: true }).fill(id);
      await wizard.getByRole("button", { name: tr("Upload and seal", "上传并封存"), exact: true }).click();
      await expect(wizard.getByLabel(tr("Dataset", "数据集"), { exact: true })).toHaveValue(id);
      for (const [label, value] of [["Scorer", "评分器"], ["Runtime", "运行时"], ["Model", "模型"]]) {
        const name = tr(label, value);
        await wizard.getByLabel(`${name}: ID`, { exact: true }).fill(label.toLowerCase());
        await wizard.getByLabel(`${name}: ${tr("Digest", "摘要")}`, { exact: true }).fill(digest(label));
      }
      await wizard.getByLabel(tr("Profile ID", "配置 ID"), { exact: true }).fill(`facts-${id}`);
      await publish.click();
      await expect(wizard.getByRole("status")).toContainText(`facts-${id} @1`);
      const profile = await (await page.request.get(`/evolution-space/api/v1/groups/${beta.id}/profiles?limit=50`)).json();
      expect(profile.revisions.some((ref: { id: string }) => ref.id === `facts-${id}`)).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: info.outputPath("profile-wizard.png"), fullPage: true });
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}
