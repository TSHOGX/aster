import { expect, test } from "@playwright/test";
import { chooseValue } from "./picker";
import { ZH } from "../lib/i18n/messages";

test("native workflow authoring, testing, publication and selected group activation", async ({ page, context, baseURL }, info) => {
  const chinese = process.env.ADMIN_EVOLUTION_LOCALE?.startsWith("zh");
  const tr = (en: string, zh: string) => chinese ? zh : en;
  const admin = (text: string) => chinese ? ZH[text] || text : text;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  await context.addCookies([{ name: "project_login", value: process.env.ADMIN_EVOLUTION_TOKEN!, url: baseURL!, secure: true, httpOnly: true, sameSite: "Lax" }]);
  const group = "standard-users", workflow = "browser-upgrade-workflow";
  const groupAPI = `/api/admin/groups/${group}/optimization`;
  const state = await (await page.request.get(groupAPI)).json();
  const prior = await (await page.request.get(`${groupAPI}/revisions/${state.active_revision}`)).json();
  const editorPath = `/evaluation-space/groups/${group}/workflows/${workflow}/`;
  if (process.env.ADMIN_EVOLUTION_UPGRADE_PHASE === "publish") {
    await page.goto(`/evaluation-space/auth/login?return=${encodeURIComponent(editorPath)}`);
    await expect(page.getByRole("heading", { level: 1 })).toContainText(workflow);
    const canvas = page.getByRole("region", { name: tr("Workflow canvas", "工作流画布"), exact: true });
    const agent = canvas.locator("button").filter({ has: page.locator("strong", { hasText: /^draft$/ }) });
    await agent.focus(); await agent.press("Enter");
    const prompt = page.getByRole("textbox", { name: tr("Prompt text", "提示词文本"), exact: true });
    await prompt.fill(`${await prompt.inputValue()}\nKeep explicit source evidence.`);
    const draft = await (await page.request.get(`/evaluation-space/api/v1/groups/${group}/workflows/${workflow}/draft`)).json();
    const originalAgent = draft.workflow.nodes.find((node: { id: string }) => node.id === "draft");
    const add = page.getByRole("button", { name: tr("Add agent", "添加 agent"), exact: true });
    await add.focus(); await add.press("Enter");
    await page.getByRole("textbox", { name: tr("Node ID", "节点 ID"), exact: true }).fill("review");
    await page.getByRole("button", { name: tr("Rename node", "重命名节点"), exact: true }).click();
    await prompt.fill("Review this evidence-backed state: {{state}}. Preserve corpus and items; return the reviewed state.");
    await page.getByRole("textbox", { name: tr("Prompt variables", "提示词变量"), exact: true }).fill("state");
    await page.getByRole("textbox", { name: tr("Model revision", "模型版本"), exact: true }).fill(originalAgent.model_ref.id);
    await page.getByRole("spinbutton", { name: tr("Model version", "模型版本号"), exact: true }).fill(String(originalAgent.model_ref.revision));
    await page.getByRole("textbox", { name: tr("Model digest", "模型摘要"), exact: true }).fill(originalAgent.model_ref.digest);
    await page.getByRole("textbox", { name: tr("Tools JSON", "工具 JSON"), exact: true }).fill("[]");
    for (const [en, zh] of [["Input ports JSON", "输入端口 JSON"], ["Output ports JSON", "输出端口 JSON"]]) {
      await page.getByRole("textbox", { name: tr(en, zh), exact: true }).fill(JSON.stringify(originalAgent.output_ports));
    }
    await canvas.getByRole("button", { name: `${tr("Remove connection", "删除连接")} draft.state → revise.state`, exact: true }).click();
    for (const [from, to] of [["draft.state", "review.state"], ["review.state", "revise.state"]]) {
      await page.getByRole("combobox", { name: tr("From node", "源节点"), exact: true }).selectOption(from);
      await page.getByRole("combobox", { name: tr("To node", "目标节点"), exact: true }).selectOption(to);
      const connect = page.getByRole("button", { name: tr("Connect", "连接"), exact: true }); await connect.focus(); await connect.press("Enter");
    }
    await page.getByRole("button", { name: tr("Validate draft", "验证草稿"), exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "sha256:" })).toBeVisible();
    await page.getByRole("button", { name: tr("Save draft", "保存草稿"), exact: true }).click();
    await expect(page.getByRole("button", { name: tr("Test saved draft", "测试已保存草稿"), exact: true })).toBeEnabled();
    await page.getByRole("button", { name: tr("Test saved draft", "测试已保存草稿"), exact: true }).click();
    const form = page.getByRole("region", { name: tr("Draft test", "草稿测试"), exact: true });
    const source = form.getByRole("combobox", { name: tr("Source run", "源运行"), exact: true });
    for (let pages = 0; pages < 20; pages++) {
      await expect(source).toBeEnabled();
      if (await source.locator(`option[value="${process.env.ADMIN_EVOLUTION_SOURCE_RUN}"]`).count()) break;
      await form.getByRole("button", { name: tr("Load more source runs", "加载更多源运行"), exact: true }).click();
    }
    await source.selectOption(process.env.ADMIN_EVOLUTION_SOURCE_RUN!);
    await form.getByRole("button", { name: tr("Start draft test", "开始草稿测试"), exact: true }).click();
    await expect(page).toHaveURL(/\/runs\//);
    await expect(page.getByRole("heading", { name: tr("Run status: completed", "运行状态: 已完成"), exact: true })).toBeVisible({ timeout: 30000 });
    const testID = new URL(page.url()).pathname.split("/").filter(Boolean).at(-1)!;
    const run = await (await page.request.get(`/evaluation-space/api/v1/groups/${group}/runs/${testID}`)).json();
    expect(run.draft_test.source.run_id).toBe(process.env.ADMIN_EVOLUTION_SOURCE_RUN);
    const attempts = await (await page.request.get(`/evaluation-space/api/v1/groups/${group}/runs/${testID}/attempts?limit=100`)).json();
    expect(attempts.items.some((attempt: { call: { node: { id: string }; instance: string }; state: string }) => attempt.call.node.id === "review" && attempt.call.instance === "root/review" && attempt.state === "succeeded")).toBe(true);
    const nested = attempts.items.find((attempt: { call: { instance: string } }) => attempt.call.instance.includes("/iteration-0/critic"));
    expect(nested).toBeDefined(); expect(nested.call.parent).toBeTruthy();
    const detail = page.locator("details").filter({ has: page.locator("summary strong", { hasText: nested.call.instance }) });
    await detail.locator("summary").click();
    await expect(detail.locator("pre").first()).toHaveText(nested.call.prompt);
    await expect(detail).toContainText("native-model @1");
    await expect(detail.locator("pre").nth(1)).toHaveText(JSON.stringify(nested.call.inputs, null, 2));
    await expect(detail.locator("pre").nth(2)).toHaveText(JSON.stringify(nested.outputs, null, 2));
    await expect(page).toHaveURL(new RegExp(`attempt=${encodeURIComponent(nested.call.id)}`));
    await page.reload(); await expect(detail).toHaveAttribute("open", "");
    await detail.getByRole("button", { name: nested.call.parent, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`attempt=${encodeURIComponent(nested.call.parent)}`));
    await page.goBack(); await expect(detail).toHaveAttribute("open", "");
    expect(run.handoff).toBe("not_applicable"); expect(run.evaluation_state).toBe("not_applicable");
    await page.getByRole("link", { name: tr("Back to group", "返回团队"), exact: true }).click();
    await page.getByRole("region", { name: tr("Workflow drafts", "工作流草稿"), exact: true }).getByRole("link", { name: workflow, exact: true }).click();
    await page.getByRole("button", { name: tr("Publish revision", "发布版本"), exact: true }).click();
    const published = page.locator("section.published-workflow"); await expect(published).toBeVisible();
    const ref = JSON.parse(await published.locator("pre").innerText()); expect(ref).toMatchObject({ id: workflow, revision: 2 });
    await page.getByRole("link", { name: tr("Open goal templates", "打开目标模板"), exact: true }).click();
    const template = page.locator("section.catalog-editor").filter({ has: page.getByRole("heading", { name: tr("Goal template editor", "目标模板编辑器"), exact: true }) });
    await template.getByRole("combobox", { name: tr("Existing template", "现有模板"), exact: true }).selectOption("browser-upgrade-template:1");
    const input = template.getByRole("textbox", { name: tr("Template JSON", "模板 JSON"), exact: true });
    await expect(input).toBeEnabled();
    const definition = JSON.parse(await input.inputValue()); definition.ref.revision = 2; delete definition.ref.digest; definition.workflows.push(ref);
    await input.fill(JSON.stringify(definition, null, 2));
    await template.getByRole("button", { name: tr("Publish template revision", "发布模板版本"), exact: true }).click();
    await expect(template.getByRole("status")).toContainText("browser-upgrade-template @2");
    expect(await (await page.request.get(groupAPI)).json()).toEqual(state);
    await template.getByRole("link", { name: tr("Activate from configuration", "从配置中激活"), exact: true }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(admin("Automatic optimization"));
  } else {
    await page.goto(`/admin/groups/${group}/optimization`);
    const bindings = page.locator("article.evolution-binding");
    await expect(bindings).toHaveCount(prior.configuration.bindings.length);
    const skills = bindings.filter({ has: page.getByRole("heading", { name: "example-skills", exact: true }) });
    await chooseValue(skills.getByRole("combobox", { name: admin("Binding template revision"), exact: true }), { label: "browser-upgrade-template · 2" });
    await skills.locator("summary").filter({ hasText: admin("Workflow, inputs and execution limits") }).click();
    await chooseValue(skills.getByRole("combobox", { name: admin("Workflow revision"), exact: true }), { label: `${workflow} · 2` });
    for (const binding of await bindings.all()) await binding.getByRole("button", { name: admin("Use next reservation generation"), exact: true }).click();
    await page.getByRole("button", { name: admin("Save configuration draft"), exact: true }).click();
    await expect(page.getByRole("button", { name: admin("Preview activation"), exact: true })).toBeEnabled();
    await page.getByRole("button", { name: admin("Preview activation"), exact: true }).click();
    await page.getByRole("checkbox", { name: admin("I reviewed source scope, output ownership, budgets and evaluation changes."), exact: true }).check();
    await page.getByRole("button", { name: admin("Activate reviewed configuration"), exact: true }).click();
    await expect.poll(async () => (await (await page.request.get(groupAPI)).json()).active_revision).toBe(state.active_revision + 1);
    await page.getByRole("button", { name: admin("Reload server status"), exact: true }).click();
    const lifecycle = page.locator("section.panel").filter({ has: page.getByRole("heading", { name: admin("Configuration lifecycle"), exact: true }) });
    await expect(lifecycle).toContainText("studio"); await expect(lifecycle).toContainText("evaluation-space");
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath(`workflow-upgrade-${process.env.ADMIN_EVOLUTION_UPGRADE_PHASE}.png`), fullPage: true });
  expect(errors).toEqual([]);
});
