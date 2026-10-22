import { expect, test } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { origin, settleOverview, provision, login, setPreferences, switchGroup } from "./fixture";
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
      await settleOverview(page); await page.goto(`/evolution-space/groups/${alpha.id}/?from=deep-link`);
      await page.getByRole("link", { name: text("Sign in", "登录"), exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`/evolution-space/groups/${alpha.id}/\\?from=deep-link`));
      const overview = page.getByRole("region", { name: text("Group overview", "团队概览"), exact: true });
      await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
      // Read-only access: management sections explain the missing grant.
      const sections = page.getByRole("navigation", { name: text("Group sections", "团队页面导航"), exact: true });
      await expect(sections.getByText(text("Evaluation profiles", "评估配置"), { exact: false })).toHaveAttribute("aria-disabled", "true");
      await sections.getByRole("link", { name: text("Permissions", "权限"), exact: true }).click();
      const matrix = page.getByRole("region", { name: text("Your permissions", "您的权限"), exact: true });
      await expect(matrix.getByRole("row", { name: `${text("Manage configuration", "管理配置")} ${text("Not granted", "未授权")}` })).toBeVisible();
      await expect(matrix.getByRole("row", { name: `${text("Edit and publish workflows", "编辑和发布工作流")} ${text("Not granted", "未授权")}` })).toBeVisible();
      const groups = await (await page.request.get("/evolution-space/api/v1/groups?limit=1")).json();
      expect(groups.items).toHaveLength(1); expect(groups.has_more).toBe(true);
      const next = await (await page.request.get(`/evolution-space/api/v1/groups?limit=1&after=${groups.next_after}`)).json();
      expect(next.items).toHaveLength(1); expect(next.has_more).toBe(false);
      expect((await page.request.get(`/evolution-space/api/v1/groups/${secret.id}`)).status()).toBe(403);
      expect((await page.request.get("/evolution-space/internal/v1/generation/admit")).status()).toBe(404);
      expect((await page.request.get("/evolution-space/api/v1/catalog/service")).status()).toBe(404);
      await page.getByRole("button", { name: text("Switch group", "切换团队"), exact: true }).click();
      const dialog = page.getByRole("dialog", { name: text("Switch group", "切换团队"), exact: true });
      await expect(dialog.getByRole("link", { name: /^Secret Team\b/ })).toHaveCount(0);
      await dialog.getByRole("link", { name: /^Team Beta\b/ }).click();
      await expect(dialog).toBeHidden();
      await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      await sections.getByRole("link", { name: text("Permissions", "权限"), exact: true }).click();
      await expect(matrix.getByRole("row", { name: `${text("Manage configuration", "管理配置")} ${text("Allowed", "允许")}` })).toBeVisible();
      await expect(matrix.getByRole("row", { name: `${text("Read source evidence", "读取源证据")} ${text("Not granted", "未授权")}` })).toBeVisible();
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/skills/`);
      await expect(page.getByRole("heading", { name: text("Workflow editor", "工作流编辑器"), exact: true })).toBeVisible();
      await expect(page.getByRole("alert").filter({ hasText: text("Workflow draft is unavailable", "工作流草稿不可用") })).toBeVisible();
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
      await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      await page.screenshot({ path: info.outputPath("group-overview.png"), fullPage: true });
      // The application continues in the last group; the brand opens the picker.
      await settleOverview(page); await page.goto("/evolution-space/");
      await expect(page).toHaveURL(new RegExp(`/evolution-space/groups/${beta.id}/$`));
      await page.getByRole("link", { name: text("Evolution Space", "演进空间"), exact: true }).click();
      await expect(page.getByRole("heading", { name: text("Choose a group", "选择团队"), exact: true })).toBeVisible();
      await page.getByLabel(text("Search groups", "搜索团队"), { exact: true }).fill("Secret Team");
      await page.getByRole("button", { name: text("Search", "搜索"), exact: true }).click();
      await expect(page.getByRole("heading", { name: text("No matching groups", "没有匹配的团队"), exact: true })).toBeVisible();
      await page.reload();
      await expect(page.getByLabel(text("Search groups", "搜索团队"), { exact: true })).toHaveValue("Secret Team");
      await expect(page.getByRole("heading", { name: text("No matching groups", "没有匹配的团队"), exact: true })).toBeVisible();
      await page.getByLabel(text("Search groups", "搜索团队"), { exact: true }).fill("");
      await page.getByRole("button", { name: text("Search", "搜索"), exact: true }).click();
      await page.getByRole("link", { name: /^Team Alpha\b/ }).click();
      await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
      await grant(alpha.id, "config.read", "deny", 1);
      await overview.locator(".page-header").getByRole("button", { name: text("Refresh", "刷新"), exact: true }).click();
      await expect(page.getByRole("heading", { name: text("You no longer have access to this group.", "您已无权访问此团队。"), exact: true })).toBeVisible();
      await expect(overview).toHaveCount(0);
      expect((await page.request.get(`/evolution-space/api/v1/groups/${alpha.id}`)).status()).toBe(403);
      const cookies = await context.cookies();
      expect(cookies.find(cookie => cookie.name === "__Secure-evolution_space_session")).toMatchObject({ path: "/evolution-space/", secure: true, httpOnly: true, sameSite: "Lax" });
      const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
      expect((await page.request.post("/evolution-space/auth/logout", { headers: { Origin: origin, "X-CSRF-Token": "bad" } })).status()).toBe(401);
      expect(session.csrf).toHaveLength(43);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.getByText(text("Account", "账户"), { exact: true }).click();
      await page.getByRole("button", { name: text("Sign out of this central session", "退出此主应用会话"), exact: true }).click();
      await expect(page.getByRole("link", { name: text("Sign in", "登录"), exact: true })).toBeVisible();
      expect((await page.request.get("/api/auth/session")).status()).toBe(401);
      expect((await page.request.get("/evolution-space/api/v1/session")).status()).toBe(401);
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

test("failed reads clear stale metadata and account language applies without local controls", async ({ page: admin, browser }) => {
  const { alpha, beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width: 390, height: 900 } });
  const page = await context.newPage();
  try {
    await login(page, user);
    await settleOverview(page); await page.goto(`/evolution-space/groups/${alpha.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    const overview = page.locator(".overview");
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
    const selectedPath = `**/evolution-space/api/v1/groups/${alpha.id}`;
    await page.route(selectedPath, route => route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"group_authority_unavailable"}' }));
    await overview.locator(".page-header").getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Group access is unavailable. Please retry.", exact: true })).toBeVisible();
    await expect(overview).toHaveCount(0);
    await page.unroute(selectedPath);
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toBeVisible();
    let release!: () => void;
    const released = new Promise<void>(resolve => { release = resolve; });
    let started!: () => void;
    const pending = new Promise<void>(resolve => { started = resolve; });
    await page.route(selectedPath, async route => {
      const response = await route.fetch(); started(); await released;
      await route.fulfill({ response }).catch(() => {});
    });
    await overview.locator(".page-header").getByRole("button", { name: "Refresh", exact: true }).click();
    await pending;
    await switchGroup(page, "Team Beta");
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    release(); await page.unroute(selectedPath);
    await expect(overview.getByRole("heading", { name: "Team Alpha", exact: true })).toHaveCount(0);
    // Language and appearance are edited only in Workbench settings.
    await expect(page.getByRole("combobox", { name: "Language", exact: true })).toHaveCount(0);
    await expect(page.getByRole("combobox", { name: "Theme mode", exact: true })).toHaveCount(0);
    await setPreferences(page, { language: "zh" });
    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
    await expect(page.getByRole("navigation", { name: "团队页面导航", exact: true }).getByRole("link", { name: "概览", exact: true })).toBeVisible();
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    await setPreferences(page, { language: "en" });
    await page.route("**/evolution-space/api/v1/session", route => route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"identity_unavailable"}' }));
    await page.reload();
    await expect(page.getByText("Identity is unavailable. Retry when the service is restored.", { exact: true })).toBeVisible();
    await expect(overview).toHaveCount(0);
    await page.unroute("**/evolution-space/api/v1/session");
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(overview.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    expect(beta.id).toBeTruthy();
  } finally { await context.close(); }
});

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`workflow editor preserves visual edits, invalid fields and conflicts ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { alpha, beta, user, grant } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message)); page.on("dialog", dialog => dialog.accept());
    try {
      await login(page, user); await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const workflow = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8"));
      workflow.id = `skills-${crypto.randomUUID().slice(0, 8)}`;
      const session = await (await page.request.get("/evolution-space/api/v1/session")).json() as { csrf: string };
      const draftPath = `/evolution-space/api/v1/groups/${beta.id}/workflows/${workflow.id}/draft`;
      const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
      const seeded = await page.request.put(draftPath, { headers, data: { id: workflow.id, expected_revision: 0, expected_layout_revision: 0, workflow, layout: { extract: { x: 12, y: 24 } } } });
      expect(seeded.status(), await seeded.text()).toBe(200);
      const foreignDraft = `/evolution-space/api/v1/groups/${alpha.id}/workflows/${workflow.id}/draft`;
      expect((await page.request.get(foreignDraft)).status()).toBe(404);
      expect((await page.request.get(`${foreignDraft}/revisions/1`)).status()).toBe(404);
      await grant(alpha.id, "workflow.edit");
      expect((await page.request.put(foreignDraft, { headers, data: { id: workflow.id, expected_revision: 1, expected_layout_revision: 1, workflow, layout: {} } })).status()).toBe(404);
      expect((await page.request.post(`${foreignDraft}/publish`, { headers, data: { revision: 1 } })).status()).toBe(404);
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/${workflow.id}/`);
      await page.getByRole("button", { name: /^extract\b/ }).click();
      const prompt = page.getByLabel(tr("Prompt text", "提示词文本"), { exact: true });
      await prompt.fill("Extract {{corpus}} with evidence");
      const ports = page.getByLabel(tr("Input ports JSON", "输入端口 JSON"), { exact: true });
      const save = page.getByRole("button", { name: tr("Save draft", "保存草稿"), exact: true });
      await ports.fill("{"); await expect(ports).toHaveAttribute("aria-invalid", "true"); await expect(save).toBeDisabled();
      await page.getByRole("button", { name: /^critic\b/ }).click();
      await page.getByRole("button", { name: /^extract\b/ }).click(); await expect(ports).toHaveValue("{");
      await page.getByRole("button", { name: tr("Save recovery", "保存恢复副本"), exact: true }).click();
      await page.reload(); await page.getByRole("button", { name: tr("Restore recovery", "恢复副本"), exact: true }).click();
      await expect(prompt).toHaveValue("Extract {{corpus}} with evidence"); await expect(ports).toHaveValue("{"); await expect(save).toBeDisabled();
      await ports.fill(JSON.stringify(workflow.nodes[0].input_ports));
      const validate = page.getByRole("button", { name: tr("Validate draft", "验证草稿"), exact: true });
      const digest = page.getByRole("status").filter({ hasText: tr("Executable digest", "可执行摘要") });
      await validate.click(); await expect(digest).toContainText("sha256:");
      await prompt.fill("Extract {{missing}} with evidence");
      await page.getByRole("button", { name: /^critic\b/ }).click();
      await validate.click();
      await expect(page.getByRole("alert").filter({ hasText: tr("unbound prompt variable", "提示词变量未绑定") })).toContainText(".prompt.text");
      await expect(prompt).toBeFocused(); await expect(prompt).toHaveValue("Extract {{missing}} with evidence");
      await prompt.fill("Extract {{corpus}} with evidence"); await validate.click(); await expect(digest).toContainText("sha256:");
      const originalDigest = await digest.innerText();
      await save.click(); await expect(page.getByRole("status").filter({ hasText: tr("Draft saved.", "草稿已保存。") })).toBeVisible();
      let retained = await (await page.request.get(draftPath)).json();
      const edited = retained.workflow.nodes.find((node: { id: string }) => node.id === "extract");
      expect(edited.prompt.text).toBe("Extract {{corpus}} with evidence");
      expect(edited.prompt.digest).toBe(`sha256:${createHash("sha256").update(edited.prompt.text).digest("hex")}`);
      const beforeLayout = structuredClone(retained);
      const publishButton = page.getByRole("button", { name: tr("Publish revision", "发布版本"), exact: true });
      const publishedReference = page.locator("section.card").filter({ has: page.getByRole("heading", { name: tr("Published workflow reference", "已发布工作流引用"), exact: true }) }).locator("pre");
      await publishButton.click(); await expect(publishedReference).toBeVisible();
      const publishedBeforeLayout = JSON.parse(await publishedReference.innerText());
      await page.getByLabel(tr("Canvas layout JSON", "画布布局 JSON"), { exact: true }).fill('{"extract":{"x":80,"y":120}}');
      await expect(digest).toHaveText(originalDigest); await save.click(); await expect(digest).toHaveText(originalDigest);
      retained = await (await page.request.get(draftPath)).json();
      expect(retained.revision).toBe(beforeLayout.revision);
      expect(retained.layout_revision).toBe(beforeLayout.layout_revision + 1);
      expect(retained.workflow).toEqual(beforeLayout.workflow);
      await publishButton.click(); await expect(publishedReference).toBeVisible();
      expect(JSON.parse(await publishedReference.innerText())).toEqual(publishedBeforeLayout);
      // Reformat the declarative view, return to visual editing and save/reload.
      // Whitespace and canvas movement cannot mint a new executable identity.
      await page.getByLabel(tr("Executable workflow JSON", "可执行工作流 JSON"), { exact: true }).fill(JSON.stringify(retained.workflow, null, 4));
      await page.getByRole("button", { name: /^extract\b/ }).click();
      await expect(prompt).toHaveValue("Extract {{corpus}} with evidence");
      await save.click(); await page.reload();
      await page.getByRole("button", { name: /^extract\b/ }).click();
      await expect(prompt).toHaveValue("Extract {{corpus}} with evidence");
      const roundTrip = await (await page.request.get(draftPath)).json();
      expect(roundTrip.revision).toBe(retained.revision);
      expect(roundTrip.layout_revision).toBe(retained.layout_revision);
      expect(roundTrip.workflow).toEqual(retained.workflow);
      await validate.click(); await expect(digest).toHaveText(originalDigest);
      await page.getByLabel(tr("Node ID", "节点 ID"), { exact: true }).fill("gather");
      await page.getByRole("button", { name: tr("Rename node", "重命名节点"), exact: true }).click();
      await expect(digest).toHaveCount(0);
      const executable = page.getByLabel(tr("Executable workflow JSON", "可执行工作流 JSON"), { exact: true });
      const renamed = JSON.parse(await executable.inputValue());
      expect(renamed.edges.some((edge: { from: string; to: string }) => edge.from.startsWith("extract.") || edge.to.startsWith("extract."))).toBe(false);
      await page.getByRole("button", { name: tr("Add agent", "添加 agent"), exact: true }).click();
      await page.getByLabel(tr("Prompt text", "提示词文本"), { exact: true }).fill("Prepare {{corpus}}");
      await page.getByLabel(tr("Prompt variables", "提示词变量"), { exact: true }).fill("corpus");
      await ports.fill(JSON.stringify(workflow.inputs));
      await page.getByLabel(tr("Output ports JSON", "输出端口 JSON"), { exact: true }).fill(JSON.stringify(workflow.inputs));
      const model = workflow.nodes[0].model_ref;
      await page.getByLabel(tr("Model revision", "模型版本"), { exact: true }).fill(model.id);
      await page.getByLabel(tr("Model version", "模型版本号"), { exact: true }).fill(String(model.revision));
      await page.getByLabel(tr("Model digest", "模型摘要"), { exact: true }).fill(model.digest);
      await page.getByRole("button", { name: `${tr("Remove connection", "删除连接")} $input.corpus → gather.corpus`, exact: true }).click();
      const connect = async (from: string, to: string) => { await page.getByLabel(tr("From node", "源节点"), { exact: true }).selectOption(from); await page.getByLabel(tr("To node", "目标节点"), { exact: true }).selectOption(to); await page.getByRole("button", { name: tr("Connect", "连接"), exact: true }).click(); };
      await connect("$input.corpus", "agent-node.corpus"); await connect("agent-node.corpus", "gather.corpus");
      await validate.click(); await expect(digest).toContainText("sha256:"); await save.click();
      await page.getByRole("button", { name: /^gather\b/ }).click();
      retained = await (await page.request.get(draftPath)).json();
      const concurrent = structuredClone(retained.workflow); concurrent.nodes.find((node: { id: string }) => node.id === "critic").timeout_seconds++;
      expect((await page.request.put(draftPath, { headers, data: { id: workflow.id, expected_revision: retained.revision, expected_layout_revision: retained.layout_revision, workflow: concurrent, layout: retained.layout } })).ok()).toBe(true);
      await prompt.fill("Local unsaved {{corpus}}"); await save.click();
      await expect(page.getByRole("alert").filter({ hasText: tr("The draft changed elsewhere. Reload before saving.", "草稿已在其他位置更改，请重新加载后保存。") })).toBeVisible();
      await expect(prompt).toHaveValue("Local unsaved {{corpus}}");
      await page.getByRole("button", { name: tr("Keep recovery and reload latest", "保留恢复副本并加载最新版本"), exact: true }).click();
      await page.getByRole("button", { name: tr("Restore recovery", "恢复副本"), exact: true }).click(); await expect(prompt).toHaveValue("Local unsaved {{corpus}}");
      await page.getByRole("button", { name: `${tr("Remove connection", "删除连接")} build.patch → $output.patch`, exact: true }).click();
      await save.click(); await expect(page.getByRole("status").filter({ hasText: tr("Draft saved.", "草稿已保存。") })).toBeVisible();
      await page.getByRole("button", { name: tr("Publish revision", "发布版本"), exact: true }).click();
      await expect(page.getByRole("alert").filter({ hasText: tr("Publishing requires workflow-edit permission and a valid saved draft.", "发布需要工作流编辑权限和有效的已保存草稿。") })).toBeVisible();
      await connect("build.patch", "$output.patch"); await save.click();
      await page.getByRole("button", { name: tr("Publish revision", "发布版本"), exact: true }).click();
      await expect(page.getByRole("status").filter({ hasText: tr("Published immutable workflow revision.", "已发布不可变工作流版本。") })).toBeVisible();
      const publishedDraft = await (await page.request.get(draftPath)).json();
      expect((await page.request.post(`${foreignDraft}/publish`, { headers, data: { revision: publishedDraft.revision } })).status()).toBe(404);
      await page.screenshot({ path: info.outputPath("visual-workflow-edits.png"), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true); expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

test("terminal run inspector retries into a linked immutable run", async ({ page: admin, browser }) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width: 390, height: 900 } });
  const page = await context.newPage();
  try {
    await login(page, user);
    await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.getByRole("region", { name: "Group overview", exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    const root = `/evolution-space/api/v1/groups/${beta.id}/runs`;
    const run = { id: "terminal-run", state: "completed", fence: 2, handoff: "submitted", evaluation_state: "not_requested", pins: { workflow: { id: "skills", revision: 1 }, service: { id: "native", revision: 1 }, configuration_revision: 1 }, created_at: new Date().toISOString() };
    const retry = { ...run, id: "retry-run", state: "queued", retry_of: "terminal-run" };
    await page.route(`${origin}${root}/terminal-run`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(run) }));
    await page.route(`${origin}${root}/retry-run`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(retry) }));
    for (const suffix of ["events?after=0&limit=100", "attempts?limit=100", "artifacts", "debugger"]) {
      await page.route(`${origin}${root}/terminal-run/${suffix}`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(suffix === "events?after=0&limit=100" ? { events: [], next_after: 0 } : suffix === "attempts?limit=100" || suffix === "artifacts" ? { items: [] } : { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 0, cost_micros: 0 } }, counts: { attempts: 0, events: 0, provider_observations: 0 }, next_after: "", has_more: false, provider_observations: [], opaque: [] }) }));
    }
    const requests: unknown[] = [];
    await page.route(`${origin}${root}/terminal-run/retry`, async route => {
      requests.push(route.request().postDataJSON());
      if (requests.length === 1) { await route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"reply_lost"}' }); return; }
      await route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify(retry) });
    });
    await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/runs/terminal-run`);
    await expect(page.getByRole("heading", { name: /Run inspector: terminal-run/, exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Retry as new run", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Retry as new run", exact: true }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Run retry is unavailable." })).toBeVisible();
    await page.reload();
    await page.getByRole("button", { name: "Retry as new run", exact: true }).click();
    await expect.poll(() => requests.length).toBe(2);
    expect(requests[1]).toEqual(requests[0]);
    await expect(page).toHaveURL(new RegExp(`/evolution-space/groups/${beta.id}/runs/retry-run/?$`));
    await expect(page.getByText("Retry source", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "terminal-run", exact: true })).toBeVisible();
  } finally { await context.close(); }
});

for (const variant of [
  { locale: "en-US", width: 1440, mode: "report_only" },
  { locale: "zh-CN", width: 390, mode: "required" },
]) {
  test(`evaluation report lifecycle and revocation ${variant.mode} ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user);
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const root = `/evolution-space/api/v1/groups/${beta.id}/evaluations/job-report`;
      const job = { id: "job-report", state: "running", fence: 1, policy_mode: variant.mode, profile: { ref: { id: "profile", revision: 2 } }, subject: { candidate: { tree_digest: "sha256:candidate" } }, created_at: new Date().toISOString() };
      const completion = { result: { state: "completed", outcome: "pass", subject: { dataset: { id: "held-out", digest: "sha256:dataset" }, scorer: { id: "scorer", revision: 1 }, runtime: { id: "runtime", revision: 1 }, model: { id: "model", revision: 1 }, tools: [] }, metrics: [{ name: "accuracy", unit: "ratio", value: 1, samples: 10, coverage: 1 }], reports: [{ id: "report-artifact", size: 12, digest: "sha256:report" }] }, usage: { spent: { tokens: 2, cost_micros: 3 }, held: { tokens: 0, cost_micros: 0 } } };
      let jobStatus = 200, resultStatus = 404;
      // These intercepted responses test UI lifecycle only. Worker assessment,
      // held-out authorization and merge policy have separate service tests.
      await page.route(`${origin}${root}`, route => route.fulfill({ status: jobStatus, contentType: "application/json", body: JSON.stringify(jobStatus === 200 ? job : { error: "access_denied" }) }));
      await page.route(`${origin}${root}/result`, route => route.fulfill({ status: resultStatus, contentType: "application/json", body: JSON.stringify(resultStatus === 200 ? completion : { error: "result_unavailable" }) }));
      await page.route(`${origin}${root}/cancel`, route => route.fulfill({ status: 403, contentType: "application/json", body: '{"error":"access_denied"}' }));
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/evaluations/job-report`);
      await expect(page.getByText(tr("Policy mode", "策略模式"), { exact: true })).toBeVisible();
      await expect(page.getByText(variant.mode === "required" ? tr("required", "必需") : tr("report_only", "仅报告"), { exact: true })).toBeVisible();
      const pending = page.getByText(tr("No settled report yet. The evaluator may still be running.", "尚无已结算报告，评估器可能仍在运行。"), { exact: true });
      const unavailable = page.getByText(tr("Settled report is temporarily unavailable. Retry after the evaluator or service recovers.", "已结算报告暂时不可用，请在评估器或服务恢复后重试。"), { exact: true });
      const refresh = page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true });
      const download = page.getByRole("link", { name: tr("Open bounded report", "打开有界报告"), exact: true });
      await expect(pending).toBeVisible();
      await page.getByRole("button", { name: tr("Cancel evaluation", "取消评估"), exact: true }).click();
      await expect(page.getByRole("alert").filter({ hasText: tr("Evaluation cancellation failed.", "评估取消失败。") })).toBeVisible();
      resultStatus = 200; job.state = "completed";
      for (const [outcome, zh] of [["pass", "通过"], ["fail", "未通过"], ["inconclusive", "无法确定"]]) {
        completion.result.outcome = outcome;
        await refresh.click();
        await expect(page.getByRole("heading", { name: `${tr("Outcome", "结果")}: ${tr(outcome, zh)}`, exact: true })).toBeVisible();
        await expect(download).toHaveAttribute("href", `${root}/reports/report-artifact?max_bytes=1048576`);
        await expect(pending).toHaveCount(0);
      }
      await expect(page.getByText(tr("Held-out answers never appear in reports or prompts; only retained report artifacts are downloadable.", "保留集答案不会出现在报告或提示词中；只能下载已保留的报告工件。"), { exact: true })).toBeVisible();
      await page.screenshot({ path: info.outputPath("evaluation-report.png"), fullPage: true });
      for (const status of [503, 403, 404]) {
        resultStatus = status;
        await refresh.click();
        await expect(unavailable).toBeVisible();
        await expect(download).toHaveCount(0);
        await expect(page.getByRole("heading", { name: /^Outcome:|^结果:/ })).toHaveCount(0);
        await expect(pending).toHaveCount(0); // A terminal job's absent report is not still running.
      }
      resultStatus = 200;
      await refresh.click(); await expect(download).toBeVisible();
      // An older successful result cannot restore evidence after a newer denial.
      let release!: () => void, started!: () => void, delivered!: () => void;
      const held = new Promise<void>(resolve => { release = resolve; });
      const waiting = new Promise<void>(resolve => { started = resolve; });
      const finished = new Promise<void>(resolve => { delivered = resolve; });
      await page.route(`${origin}${root}/result`, async route => {
        started(); await held;
        await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(completion) }).catch(() => {});
        delivered();
      }, { times: 1 });
      await refresh.click(); await waiting;
      jobStatus = 403;
      await refresh.click();
      await expect(page.getByText(tr("Evaluation report is unavailable. Retry or check group access.", "无法获取评估报告，请重试或检查团队访问权限。"), { exact: true })).toBeVisible();
      await expect(page.getByText("sha256:candidate", { exact: true })).toHaveCount(0);
      await expect(download).toHaveCount(0);
      release(); await finished;
      await expect(download).toHaveCount(0);
      jobStatus = 200;
      await page.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
      await expect(download).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

test("role matrix and keyboard skip link remain usable on a narrow viewport", async ({ page: admin, browser }) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width: 390, height: 900 } });
  const page = await context.newPage();
  try {
    await login(page, user);
    await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    await page.getByRole("navigation", { name: "Group sections", exact: true }).getByRole("link", { name: "Permissions", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Permissions", exact: true, level: 1 })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("heading", { name: "Permissions", exact: true, level: 1 })).toBeVisible();
    await page.keyboard.press("Tab");
    const skip = page.getByRole("link", { name: "Skip to main content", exact: true });
    await expect(skip).toBeFocused();
    await skip.press("Enter");
    await expect(page.locator("#main-content")).toBeFocused();
    const matrix = page.locator("section[aria-labelledby='permissions-heading']").getByRole("table");
    await expect(matrix.getByRole("row")).toHaveCount(6);
    await expect(matrix.getByRole("row", { name: "Read configuration Allowed" })).toBeVisible();
    await expect(matrix.getByRole("row", { name: "Manage configuration Allowed" })).toBeVisible();
    await expect(matrix.getByRole("row", { name: "Edit and publish workflows Allowed" })).toBeVisible();
    await expect(matrix.getByRole("row", { name: "Operate runs Allowed" })).toBeVisible();
    await expect(matrix.getByRole("row", { name: "Read source evidence Not granted" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  } finally { await context.close(); }
});

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`run inspector waits for cooperative pause ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user);
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const root = `/evolution-space/api/v1/groups/${beta.id}/runs/pause-run`;
      const run = { budget: { max_tokens: 100, max_cost_micros: 100, max_attempts: 10, timeout_seconds: 60, max_artifact_bytes: 1048576 }, id: "pause-run", state: "running", fence: 1, handoff: "not_ready", evaluation_state: "not_requested", pins: { workflow: { id: "skills", revision: 1 }, service: { id: "native", revision: 1 }, configuration_revision: 1 }, created_at: new Date().toISOString() };
      const controls: string[] = [];
      // UI transitions are intercepted; real draining and resume use native-process tests.
      await page.route(`${origin}${root}`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(run) }));
      await page.route(`${origin}${root}/**`, route => {
        const path = new URL(route.request().url()).pathname;
        let body: unknown = { items: [] };
        if (path.endsWith("/control")) {
          const control = route.request().postDataJSON() as { action: string; expected_fence: number };
          expect(control.expected_fence).toBe(run.fence);
          controls.push(control.action);
          run.state = control.action === "pause" ? "pause_requested" : "queued";
          body = { state: run.state, fence: run.fence };
        } else if (path.endsWith("/events")) body = { events: [], next_after: 0 };
        else if (path.endsWith("/debugger")) body = { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 10, cost_micros: 20 }, available: { tokens: 90, cost_micros: 80 } }, counts: { attempts: 0, events: 0, provider_observations: 0 }, next_after: "", has_more: false, provider_observations: [], opaque: [] };
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
      });
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/runs/pause-run`);
      await page.getByRole("button", { name: tr("Pause", "暂停"), exact: true }).click();
      await expect(page.getByRole("heading", { name: `${tr("Run status", "运行状态")}: ${tr("Pausing", "正在暂停")}`, exact: true })).toBeVisible();
      await expect(page.getByText(tr("Pause requested. Current work is finishing before the run pauses.", "已请求暂停。当前工作完成后，运行将暂停。"), { exact: true })).toBeVisible();
      await expect(page.getByRole("button", { name: tr("Pause", "暂停"), exact: true })).toBeDisabled();
      await expect(page.getByRole("button", { name: tr("Resume", "继续"), exact: true })).toBeDisabled();
      await expect(page.getByRole("button", { name: tr("Run from here", "从此处运行"), exact: true })).toBeDisabled();
      await expect(page.getByRole("button", { name: tr("Cancel", "取消"), exact: true })).toBeEnabled();
      await page.screenshot({ path: info.outputPath("pause-requested.png"), fullPage: true });
      run.state = "paused"; run.fence++;
      await page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true }).click();
      await expect(page.getByRole("button", { name: tr("Resume", "继续"), exact: true })).toBeEnabled();
      await page.getByRole("button", { name: tr("Resume", "继续"), exact: true }).click();
      await expect(page.getByRole("heading", { name: `${tr("Run status", "运行状态")}: ${tr("queued", "排队中")}`, exact: true })).toBeVisible();
      expect(controls).toEqual(["pause", "resume"]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`checkpoint experiment preview and lost response recovery ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user); await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const root = `/evolution-space/api/v1/groups/${beta.id}/runs`;
      const budget = { max_tokens: 100, max_cost_micros: 100, max_attempts: 20, timeout_seconds: 60, max_artifact_bytes: 1048576 };
      const run = { id: "experiment-source", state: "paused", fence: 2, handoff: "not_ready", evaluation_state: "not_requested", budget, pins: { workflow: { id: "skills", revision: 1 } }, created_at: new Date().toISOString() };
      const workflow = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8"));
      const requests: unknown[] = []; let created = ""; let frozenStatus = 200;
      await page.route(`${origin}${root}/**`, route => {
        const path = new URL(route.request().url()).pathname; let body: unknown = { items: [] };
        if (path.endsWith("/experiment-preview")) {
          const request = route.request().postDataJSON(); expect(request.start_node).toBe("critic"); expect(request.budget.max_tokens).toBe(30);
          body = { eligible: true, checkpoint: { id: "node-extract" }, dependencies: [{ id: "sealed-corpus" }, { id: "sealed-checkpoint" }], reused: [{ call: { id: "extract-attempt", instance: "root/extract" } }] };
        } else if (path.endsWith("/experiment")) {
          const request = route.request().postDataJSON(); requests.push(request); created = request.id;
          if (requests.length === 1) return route.fulfill({ status: 503, body: "lost response" });
          expect(requests[1]).toEqual(requests[0]); body = { id: created };
        } else if (path.endsWith("/workflow")) return route.fulfill({ status: frozenStatus, contentType: "application/json", body: JSON.stringify(frozenStatus === 200 ? { workflow } : { error: "source_denied" }) });
        else if (path.endsWith("/experiment-source")) body = run;
        else if (created && path.endsWith(`/${created}`)) body = { ...run, id: created, state: "queued", experiment_of: run.id };
        else if (path.endsWith("/events")) body = { events: [], next_after: 0 };
        else if (path.endsWith("/debugger")) body = { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 30, cost_micros: 100 } }, counts: { attempts: 0, events: 0, provider_observations: 0 }, next_after: "", has_more: false, provider_observations: [], opaque: [] };
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
      });
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/runs/experiment-source`);
      await page.getByRole("button", { name: tr("Run from here", "从此处运行"), exact: true }).click();
      await page.getByLabel(tr("Start node", "起始节点"), { exact: true }).selectOption("critic");
      await page.getByLabel(tr("Token budget", "令牌预算"), { exact: true }).fill("30");
      await expect(page.getByRole("button", { name: tr("Create experiment", "创建实验"), exact: true })).toBeDisabled();
      await page.getByRole("button", { name: tr("Preview checkpoint reuse", "预览检查点复用"), exact: true }).click();
      await expect(page.getByText(`root/extract · ${tr("Reused from checkpoint", "复用自检查点")}`, { exact: true })).toBeVisible();
      await page.screenshot({ path: info.outputPath("checkpoint-reuse-preview.png"), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.getByRole("button", { name: tr("Create experiment", "创建实验"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Checkpoint experiment", "检查点实验"), exact: true }).getByRole("alert")).toContainText(tr("Experiment creation is unavailable or the checkpoint is no longer eligible.", "实验创建不可用，或检查点已不再满足条件。"));
      await page.reload();
      frozenStatus = 403;
      await page.getByRole("button", { name: tr("Run from here", "从此处运行"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Checkpoint experiment", "检查点实验"), exact: true }).getByRole("alert")).toBeVisible();
      await expect(page.getByLabel(tr("Frozen experiment workflow", "冻结的实验工作流"), { exact: true })).toHaveValue("");
      await expect(page.getByRole("button", { name: tr("Recover experiment", "恢复实验"), exact: true })).toHaveCount(0);
      await page.getByRole("button", { name: tr("Close", "关闭"), exact: true }).click(); frozenStatus = 200;
      await page.getByRole("button", { name: tr("Run from here", "从此处运行"), exact: true }).click();
      await expect(page.getByLabel(tr("Start node", "起始节点"), { exact: true })).toHaveValue("critic");
      await page.getByRole("button", { name: tr("Recover experiment", "恢复实验"), exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`/runs/${created}/?$`));
      expect(requests).toHaveLength(2); expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`run inspector clears denied evidence and superseded reads ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user);
      const runs = `/evolution-space/api/v1/groups/${beta.id}/runs`; const root = `${runs}/sensitive-run`;
      const run = { id: "sensitive-run", state: "completed", fence: 1, handoff: "not_ready", evaluation_state: "not_requested", experiment_of: "source-run", pins: { workflow: { id: "skills", revision: 1 } }, created_at: new Date().toISOString() };
      const attempt = { call: { id: "attempt", instance: "root/extract", number: 1, node: { id: "extract", kind: "agent" }, inputs: { corpus: "sensitive-input" }, prompt: "sensitive-prompt" }, state: "succeeded", outputs: { result: "sensitive-output" } };
      const artifact = { id: "sensitive-artifact", size: 10, digest: "sha256:sensitive", media_type: "application/json" };
      const event = { id: "event", sequence: 1, kind: "attempt-succeeded", attempt_id: "attempt", timestamp: new Date().toISOString(), artifacts: [artifact] };
      const observation = { call: { id: "provider-001", idempotency_key: "provider-001", request: { id: artifact.id, digest: artifact.digest, size: artifact.size } }, outcome: { state: "completed", provider_request_id: "request", usage: { tokens: 1, cost_micros: 1 } } };
      const debug = { budget: { spent: { tokens: 2, cost_micros: 3 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 98, cost_micros: 97 } }, counts: { attempts: 2, events: 1, provider_observations: 2 }, next_after: "provider-001", has_more: true, provider_observations: [observation], opaque: [] };
      let status = 200, evidenceStatus = 200, previewStatus = 200; let truncated = false; let binary = false; let oversized = false; const cursors: string[] = [];
      await page.route(`${origin}${runs}/**`, route => {
        const url = new URL(route.request().url()); let body: unknown = run; let code = status;
        if (url.pathname.endsWith("/artifacts/sensitive-artifact")) return route.fulfill({ status: previewStatus, contentType: binary ? "application/octet-stream" : "text/plain", headers: { "X-Artifact-Truncated": String(truncated), "X-Artifact-Size": truncated ? "2097152" : "24" }, body: oversized ? "x".repeat(1048580) : truncated ? "bounded-prefix" : "<script>inert()</script>" });
        if (url.pathname.endsWith("/events")) { cursors.push(url.searchParams.get("after")!); body = { events: [event, event], next_after: 1 }; code = evidenceStatus; }
        else if (url.pathname.endsWith("/attempts/attempt")) { body = attempt; code = evidenceStatus; }
        else if (url.pathname.endsWith("/attempts")) { body = url.searchParams.has("after") ? { items: [{ ...attempt, call: { ...attempt.call, id: "attempt-two", instance: "root/critic" } }], next_after: "attempt-two", has_more: false } : { items: [attempt], next_after: "attempt", has_more: true }; code = evidenceStatus; }
        else if (url.pathname.endsWith("/artifacts")) { body = url.searchParams.has("after") ? { items: [], next_after: "", has_more: false } : { items: [artifact], next_after: "sensitive-artifact", has_more: true }; code = evidenceStatus; }
        else if (url.pathname.endsWith("/debugger")) { body = url.searchParams.has("after") ? { ...debug, has_more: false, next_after: "provider-002", provider_observations: [{ ...observation, call: { ...observation.call, id: "provider-002" } }] } : debug; code = evidenceStatus; }
        else if (url.pathname.endsWith("/source-run")) body = { ...run, id: "source-run", experiment_of: undefined };
        return route.fulfill({ status: code, contentType: "application/json", body: JSON.stringify(code === 200 ? body : { error: "unavailable" }) });
      });
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/runs/sensitive-run`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      const download = page.getByRole("link", { name: tr("Open bounded content", "打开有界内容"), exact: true });
      const refresh = page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true });
      await expect(download).toBeVisible();
      await expect(page.getByRole("button", { name: tr("Load more artifacts", "加载更多工件"), exact: true })).toBeVisible();
      await page.getByRole("button", { name: tr("Load more artifacts", "加载更多工件"), exact: true }).click();
      await expect(page.getByRole("button", { name: tr("Load more artifacts", "加载更多工件"), exact: true })).toHaveCount(0);
      await expect(page.getByText(tr("Load all attempt pages to compare execution paths.", "加载全部尝试页面后才能比较执行路径。"), { exact: true })).toBeVisible();
      await page.getByRole("button", { name: tr("Load more attempts", "加载更多尝试"), exact: true }).click();
      await expect(page.locator("summary").filter({ hasText: "root/critic" })).toBeVisible();
      await expect(page.getByText(tr("Load all attempt pages to compare execution paths.", "加载全部尝试页面后才能比较执行路径。"), { exact: true })).toHaveCount(0);
      await page.getByRole("button", { name: tr("Load more provider observations", "加载更多提供商观察记录"), exact: true }).click();
      await expect(page.getByText("provider-002", { exact: true })).toBeVisible();
      await download.click();
      const preview = page.getByRole("region", { name: `${tr("Artifact preview", "工件预览")}: sensitive-artifact`, exact: true });
      await expect(preview.getByRole("status")).toContainText(tr("Complete artifact content.", "完整工件内容。"));
      await expect(preview.getByText("<script>inert()</script>", { exact: true })).toBeVisible();
      truncated = true; await preview.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
      await expect(preview.getByRole("status")).toContainText(tr("Truncated preview.", "预览已截断"));
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.screenshot({ path: info.outputPath("artifact-preview-truncated.png"), fullPage: true });
      oversized = true; truncated = false;
      await preview.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
      await expect(preview.getByRole("status")).toContainText(tr("Truncated preview.", "预览已截断"));
      expect(await preview.locator("code").evaluate(element => element.textContent?.length)).toBe(1048576);
      oversized = false; truncated = true;
      for (const [code, message, chinese] of [[403, "Artifact access was denied.", "工件访问被拒绝。"], [404, "Artifact content is missing.", "工件内容缺失。"], [410, "Artifact content has expired.", "工件内容已过期。"], [503, "Artifact content is unavailable. Retry when access or service is restored.", "工件内容不可用，请在访问权限或服务恢复后重试。"]] as const) {
        previewStatus = code; await preview.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
        await expect(preview.getByRole("alert")).toHaveText(tr(message, chinese));
        await expect(preview.getByText("bounded-prefix", { exact: true })).toHaveCount(0);
      }
      previewStatus = 200; binary = true; await preview.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
      await expect(preview.getByRole("link", { name: tr("Download bounded bytes", "下载有界字节"), exact: true })).toBeVisible();
      await preview.getByRole("button", { name: tr("Close", "关闭"), exact: true }).click(); await expect(preview).toHaveCount(0);
      await page.locator("summary").filter({ hasText: "root/extract" }).click();
      await expect(page.locator("details").filter({ has: page.locator("summary").filter({ hasText: "root/extract" }) }).getByText("sensitive-prompt", { exact: true })).toBeVisible();
      await expect.poll(() => cursors.includes("1")).toBe(true);
      await expect(page.locator("time")).toHaveCount(1); // Duplicate delivery is one timeline event.
      for (const code of [403, 503]) {
        evidenceStatus = code; await refresh.click();
        await expect(page.getByText(tr("Run evidence is unavailable. Retry or check source access.", "运行证据不可用，请重试或检查源访问权限。"), { exact: true })).toBeVisible();
        await expect(download).toHaveCount(0); await expect(page.getByText("sensitive-prompt", { exact: true })).toHaveCount(0);
        await expect(page.getByRole("heading", { name: tr("Execution debugger", "执行调试器"), exact: true })).toHaveCount(0);
        await expect(page.getByRole("heading", { name: tr("Experiment comparison", "实验比较"), exact: true })).toHaveCount(0);
        evidenceStatus = 200; await refresh.click(); await expect(download).toBeVisible();
      }
      let release!: () => void, started!: () => void, delivered!: () => void;
      const held = new Promise<void>(resolve => { release = resolve; }); const waiting = new Promise<void>(resolve => { started = resolve; }); const finished = new Promise<void>(resolve => { delivered = resolve; });
      await page.route(`${origin}${root}/debugger`, async route => { started(); await held; await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(debug) }).catch(() => {}); delivered(); }, { times: 1 });
      await refresh.click(); await waiting;
      status = 403; await refresh.click();
      await expect(page.getByText(tr("Run detail is unavailable. Retry or check group access.", "无法获取运行详情，请重试或检查团队访问权限。"), { exact: true })).toBeVisible();
      release(); await finished; await expect(download).toHaveCount(0);
      await expect(page.getByRole("heading", { name: tr("Execution debugger", "执行调试器"), exact: true })).toHaveCount(0);
      status = 200; await page.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click(); await expect(download).toBeVisible();
      await page.screenshot({ path: info.outputPath("run-evidence-recovered.png"), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true); expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
 test(`draft test scopes and lost response recovery ${variant.locale}`, async ({ page: admin, browser }, info) => {
  const { beta, user, grant } = await provision(admin); await grant(beta.id, "source.read");
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
  const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  try {
   await login(page, user); await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
   await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
   await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true })).toBeVisible();
   const workflow = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8")); workflow.id = `trial-draft-${crypto.randomUUID().slice(0, 8)}`;
   const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
   const draftPath = `/evolution-space/api/v1/groups/${beta.id}/workflows/${workflow.id}/draft`;
   const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
   expect((await page.request.put(draftPath, { headers, data: { id: workflow.id, expected_revision: 0, expected_layout_revision: 0, workflow, layout: {} } })).status()).toBe(200);
   const budget = { max_tokens: 100, max_cost_micros: 100, max_attempts: 20, timeout_seconds: 60, max_artifact_bytes: 1048576 };
   const root = `/evolution-space/api/v1/groups/${beta.id}/runs`; let created = ""; const requests: Record<string, unknown>[] = [];
   await page.route(`${origin}${root}*`, route => route.fulfill({ json: { items: [{ id: "source", budget, state: "completed", handoff: "submitted", evaluation_state: "not_requested", created_at: "2026-10-17T00:00:00Z", pins: { template: { id: "skills", revision: 1 }, workflow: { id: workflow.id, revision: 1 }, base: { commit_id: "a".repeat(64) } } }], next_after: "source", has_more: false } }));
   await page.route(`${origin}${root}/**`, route => {
    const path = new URL(route.request().url()).pathname;
    const body = path.endsWith("/events") ? { events: [], next_after: 0 } : path.endsWith("/debugger") ? { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 0, cost_micros: 0 } }, counts: { attempts: 0, events: 0, provider_observations: 0 }, provider_observations: [], opaque: [] } : path.endsWith(`/${created}`) ? { id: created, state: "queued", fence: 0, handoff: "not_applicable", evaluation_state: "not_applicable", pins: {}, budget, created_at: new Date().toISOString(), draft_test: { kind: "node", node_id: "extract", source: { run_id: "source" }, draft: { id: workflow.id, revision: 1 } } } : { items: [], has_more: false };
    return route.fulfill({ json: body });
   });
   await page.route(`${origin}${draftPath}/tests`, route => { const input = route.request().postDataJSON(); requests.push(input); created = input.id; return requests.length === 1 ? route.fulfill({ status: 503, body: "lost response" }) : route.fulfill({ status: 201, json: { id: created } }); });
   await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/${workflow.id}/`);
   await page.getByRole("button", { name: tr("Test saved draft", "测试已保存草稿"), exact: true }).click();
   const form = page.getByRole("region", { name: tr("Draft test", "草稿测试"), exact: true });
   await form.getByLabel(tr("Source run", "源运行"), { exact: true }).selectOption("source");
   await form.getByLabel(tr("Test scope", "测试范围"), { exact: true }).selectOption("subgraph");
   await expect(form.getByRole("button", { name: tr("Start draft test", "开始草稿测试"), exact: true })).toBeDisabled();
   await form.getByLabel(tr("Test scope", "测试范围"), { exact: true }).selectOption("node");
   await form.getByLabel(tr("Test node", "测试节点"), { exact: true }).selectOption("extract");
   await form.getByLabel(tr("Token budget", "令牌预算"), { exact: true }).fill("101");
   await expect(form.getByRole("button", { name: tr("Start draft test", "开始草稿测试"), exact: true })).toBeDisabled();
   await form.getByLabel(tr("Token budget", "令牌预算"), { exact: true }).fill("30");
   await page.screenshot({ path: info.outputPath("draft-test.png"), fullPage: true });
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
   await form.getByRole("button", { name: tr("Start draft test", "开始草稿测试"), exact: true }).focus(); await form.getByRole("button", { name: tr("Start draft test", "开始草稿测试"), exact: true }).click();
   await expect(form.getByRole("button", { name: tr("Recover draft test", "恢复草稿测试"), exact: true })).toBeEnabled();
   await page.reload(); await grant(beta.id, "source.read", "deny", 1);
   await page.getByRole("button", { name: tr("Test saved draft", "测试已保存草稿"), exact: true }).click();
   await expect(form.getByRole("alert")).toBeVisible(); await expect(form.getByRole("button", { name: tr("Recover draft test", "恢复草稿测试"), exact: true })).toHaveCount(0);
   await grant(beta.id, "source.read", "allow", 2); await form.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
   await expect(form.getByLabel(tr("Token budget", "令牌预算"), { exact: true })).toHaveValue("30");
   await form.getByRole("button", { name: tr("Recover draft test", "恢复草稿测试"), exact: true }).click();
   await expect(page).toHaveURL(new RegExp(`/runs/${created}/?$`)); expect(requests).toHaveLength(2); expect(requests[1]).toEqual(requests[0]);
   await expect(page.getByRole("button", { name: tr("Run from here", "从此处运行"), exact: true })).toHaveCount(0); expect(errors).toEqual([]);
  } finally { await context.close(); }
 });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
 test(`draft creation and exact fork recover committed responses ${variant.locale}`, async ({ page: admin, browser }, info) => {
  const { alpha, beta, user, grant } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
  const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  try {
   await login(page, user); await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/`);
   await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
   const form = page.getByRole("region", { name: tr("Workflow drafts", "工作流草稿"), exact: true });
   await form.getByRole("button", { name: tr("New or fork draft", "新建或派生草稿"), exact: true }).click();
   const id = `created-${crypto.randomUUID().slice(0, 8)}`;
   await form.getByLabel(tr("New draft ID", "新草稿 ID"), { exact: true }).fill(id);
   await form.getByRole("button", { name: tr("Create draft", "创建草稿"), exact: true }).click();
   await expect(page).toHaveURL(new RegExp(`/workflows/${id}/$`));
   await page.getByRole("button", { name: tr("Add transform", "添加 transform"), exact: true }).click();
   await page.getByRole("button", { name: tr("Save draft", "保存草稿"), exact: true }).click();
   await expect(page.getByText(tr("Draft saved.", "草稿已保存。"), { exact: true })).toBeVisible();
   const root = `/evolution-space/api/v1/groups/${beta.id}/workflows`;
   const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
   const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
   const saved = await (await page.request.get(`${root}/${id}/draft`)).json();
   expect(saved.revision).toBe(2); expect(saved.workflow.nodes).toHaveLength(1);
   await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/`);
   await expect(form.getByRole("link", { name: id, exact: true })).toBeVisible();
   // Commit an exact historical fork, then simulate a lost response.
   const forkID = `fork-${crypto.randomUUID().slice(0, 8)}`;
   const requests: unknown[] = [];
   let drop = true;
   await page.route(`**${root}`, async route => {
    if (route.request().method() !== "POST") return route.continue();
    requests.push(route.request().postDataJSON());
    const response = await route.fetch(); expect(response.status()).toBe(201);
    if (drop) { drop = false; return route.fulfill({ status: 503, body: '{}' }); }
    return route.fulfill({ response });
   });
   await form.getByRole("button", { name: tr("New or fork draft", "新建或派生草稿"), exact: true }).click();
   await form.getByLabel(tr("New draft ID", "新草稿 ID"), { exact: true }).fill(forkID);
   await form.getByLabel(tr("Draft starting point", "草稿起点"), { exact: true }).selectOption("draft");
   await form.getByLabel(tr("Source draft ID", "源草稿 ID"), { exact: true }).fill(id);
   await form.getByLabel(tr("Source draft revision", "源草稿版本"), { exact: true }).fill("1");
   await form.getByRole("button", { name: tr("Create draft", "创建草稿"), exact: true }).click();
   await expect(form.getByRole("button", { name: tr("Recover draft creation", "恢复草稿创建"), exact: true })).toBeEnabled();
   const fork = await (await page.request.get(`${root}/${forkID}/draft`)).json();
   expect(fork.workflow.nodes ?? []).toHaveLength(0);
   // Later edits must survive replay of the original creation receipt.
   const edited = { ...saved.workflow, id: forkID };
   expect((await page.request.put(`${root}/${forkID}/draft`, { headers, data: { id: forkID, expected_revision: 1, expected_layout_revision: 1, workflow: edited, layout: {} } })).status()).toBe(200);
   await page.reload();
   await form.getByRole("button", { name: tr("New or fork draft", "新建或派生草稿"), exact: true }).click();
   await expect(form.getByLabel(tr("New draft ID", "新草稿 ID"), { exact: true })).toHaveValue(forkID);
   await expect(form.getByLabel(tr("New draft ID", "新草稿 ID"), { exact: true })).toBeDisabled();
   await grant(beta.id, "workflow.edit", "deny", 1);
   await form.getByRole("button", { name: tr("Recover draft creation", "恢复草稿创建"), exact: true }).click();
   await expect(form.getByLabel(tr("New draft ID", "新草稿 ID"), { exact: true })).toHaveValue("");
   expect(requests).toHaveLength(1);
   await grant(beta.id, "workflow.edit", "allow", 2);
   await form.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
   await form.getByRole("button", { name: tr("Recover draft creation", "恢复草稿创建"), exact: true }).click();
   await expect(page).toHaveURL(new RegExp(`/workflows/${forkID}/$`));
   expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
   const recovered = await (await page.request.get(`${root}/${forkID}/draft`)).json();
   expect(recovered.revision).toBe(2); expect(recovered.workflow.nodes).toHaveLength(1);
   await expect(page.getByRole("button", { name: /^transform-node\b/ })).toBeVisible();
   // Read authority filters the list, and source authority precedes run lookup.
   const foreignRoot = `/evolution-space/api/v1/groups/${alpha.id}/workflows`;
   expect((await (await page.request.get(foreignRoot)).json()).items).toEqual([]);
   await grant(alpha.id, "workflow.edit");
   expect((await page.request.post(foreignRoot, { headers, data: { id: `foreign-${crypto.randomUUID()}`, source_draft_id: id, source_revision: 1 } })).status()).toBe(404);
   expect((await page.request.post(root, { headers, data: { id: `run-${crypto.randomUUID()}`, source_run_id: "missing" } })).status()).toBe(403);
   await grant(beta.id, "source.read");
   expect((await page.request.post(root, { headers, data: { id: `run-${crypto.randomUUID()}`, source_run_id: "missing" } })).status()).toBe(404);
   await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/`);
   await expect(form.getByRole("link", { name: forkID, exact: true })).toBeVisible();
   await page.screenshot({ path: info.outputPath("draft-list.png"), fullPage: true });
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
   expect(errors).toEqual([]);
  } finally { await context.close(); }
 });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
 test(`inspector exact attempt links survive reload and history ${variant.locale}`, async ({ page: admin, browser }, info) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
  const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  try {
   await login(page, user);
   const root = `/evolution-space/api/v1/groups/${beta.id}/runs/nested`;
   const child = { call: { id: "z-child", instance: "root/map/attempt-1/item-2/loop/attempt-2/iteration-3/agent", parent: "parent", number: 2, node: { id: "agent", kind: "agent", model_ref: { id: "model", revision: 3 }, tools: [{ id: "tool", revision: 2 }] }, prompt: "exact nested prompt", inputs: { corpus: "frozen-input" } }, state: "failed", error_code: "node-timeout", outputs: { result: "retained-output" } };
   const parent = { call: { id: "parent", instance: "root/map/attempt-1/item-2/loop", number: 1, node: { id: "loop", kind: "loop" } }, state: "failed", outputs: {} };
   const paths: string[] = []; let selectedStatus = 200;
   // UI projection fixture; exact native records and authority are separately process-tested.
   await page.route(`${origin}${root}**`, route => {
    const url = new URL(route.request().url()); paths.push(url.pathname + url.search);
    let body: unknown = { id: "nested", state: "failed", handoff: "not_ready", evaluation_state: "not_requested", pins: { workflow: { id: "frozen", revision: 1 } } };
    if (url.pathname.endsWith("/attempts/z-child")) return route.fulfill({ status: selectedStatus, contentType: "application/json", body: JSON.stringify(selectedStatus === 200 ? child : { error: "unavailable" }) });
    if (url.pathname.endsWith("/attempts/parent")) body = parent;
    else if (url.pathname.includes("/attempts/")) return route.fulfill({ status: 404, body: '{}' });
    else if (url.pathname.endsWith("/attempts")) body = { items: [parent], next_after: "parent", has_more: true };
    else if (url.pathname.endsWith("/events")) body = { events: [{ id: "event", sequence: 1, kind: "attempt-failed", attempt_id: child.call.id, timestamp: new Date().toISOString() }], next_after: 1 };
    else if (url.pathname.endsWith("/artifacts")) body = { items: [], next_after: "", has_more: false };
    else if (url.pathname.endsWith("/debugger")) body = { budget: { spent: { tokens: 5, cost_micros: 10 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 10, cost_micros: 10 } }, counts: { attempts: 101, events: 1, provider_observations: 0 }, provider_observations: [], opaque: [] };
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
   });
   const url = `/evolution-space/groups/${beta.id}/runs/nested/?attempt=z-child`;
   await settleOverview(page); await page.goto(`/evolution-space/auth/login?return=${encodeURIComponent(url)}`);
   const detail = page.locator("#attempt-z-child"); const summary = detail.locator("summary");
   await expect(summary).toBeFocused(); await expect(detail).toHaveAttribute("open", "");
   await expect(detail.getByText("exact nested prompt", { exact: true })).toBeVisible();
   await expect(detail.getByText(tr("Execution depth", "执行深度"), { exact: true }).locator("..")).toContainText("2");
   await expect(detail.getByText(tr("Loop iteration", "循环迭代"), { exact: true }).locator("..")).toContainText("3");
   await expect(detail.getByText(tr("Map item", "映射项目"), { exact: true }).locator("..")).toContainText("2");
   await expect(detail.getByRole("link", { name: tr("Link to this attempt", "此尝试的链接"), exact: true })).toHaveAttribute("href", url);
   expect(paths.some(path => path.includes("/attempts?after="))).toBe(false);
   await page.reload(); await expect(summary).toBeFocused();
   await detail.getByRole("button", { name: "parent", exact: true }).click();
   await expect(page).toHaveURL(/attempt=parent$/); await expect(page.locator("#attempt-parent")).toHaveAttribute("open", "");
   await page.goBack(); await expect(page).toHaveURL(/attempt=z-child$/); await expect(summary).toBeFocused();
   await summary.press("Enter"); await expect(page).not.toHaveURL(/attempt=/);
   await page.getByRole("button", { name: "z-child", exact: true }).click();
   await expect(page).toHaveURL(/attempt=z-child$/); await expect(summary).toBeFocused();
   if (variant.width === 390) expect(await page.locator(".run-grid").evaluate(element => getComputedStyle(element).gridTemplateColumns.split(" ").length)).toBe(1);
   await page.screenshot({ path: info.outputPath("nested-attempt-link.png"), fullPage: true });
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
   const refresh = page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true });
   for (const status of [403, 503, 404]) {
    selectedStatus = status; await refresh.click();
    await expect(page.getByText("exact nested prompt", { exact: true })).toHaveCount(0);
    await expect(page.getByText(tr(status === 404 ? "Selected attempt is missing or was not retained." : "Run evidence is unavailable. Retry or check source access.", status === 404 ? "所选尝试不存在或未被保留。" : "运行证据不可用，请重试或检查源访问权限。"), { exact: true })).toBeVisible();
   }
   selectedStatus = 200; await refresh.click(); await expect(summary).toBeFocused();
   expect(errors).toEqual([]);
  } finally { await context.close(); }
 });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
 test(`node settings preserve recovery and focus graph validation errors ${variant.locale}`, async ({ page: admin, browser }, info) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
  const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message)); page.on("dialog", dialog => dialog.accept());
  try {
   await login(page, user); await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/`);
   await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
   await expect(page.getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
   const graph = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8")); graph.id = `settings-${crypto.randomUUID().slice(0, 8)}`;
   const root = `/evolution-space/api/v1/groups/${beta.id}/workflows/${graph.id}/draft`;
   const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
   const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
   expect((await page.request.put(root, { headers, data: { id: graph.id, expected_revision: 0, expected_layout_revision: 0, workflow: graph, layout: {} } })).status()).toBe(200);
   let releaseDraft!: () => void, draftRequested!: () => void;
   const held = new Promise<void>(resolve => { releaseDraft = resolve; });
   const requested = new Promise<void>(resolve => { draftRequested = resolve; });
   await page.route(`${origin}${root}`, async route => { draftRequested(); await held; await route.continue(); });
   await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/${graph.id}/`);
   try {
    await requested;
    await expect(page.getByText(tr("Loading workflow…", "正在加载工作流…"), { exact: true })).toBeVisible();
    await expect(page.getByLabel(tr("Executable workflow JSON", "可执行工作流 JSON"), { exact: true })).toHaveCount(0);
   } finally { releaseDraft(); }
   await expect(page.getByRole("button", { name: /^extract\b/ })).toBeVisible();
   await page.unroute(`${origin}${root}`);
   const executable = page.getByLabel(tr("Executable workflow JSON", "可执行工作流 JSON"), { exact: true });
   const validate = page.getByRole("button", { name: tr("Validate draft", "验证草稿"), exact: true });
   const checks: { field: string; focus: string; change: (value: typeof graph) => void }[] = [
    { field: "edges[0].from", focus: "workflow", change: value => { value.edges[0].from = "missing.port"; } },
    { field: "edges[0].to", focus: "workflow", change: value => { value.nodes[0].input_ports.corpus = `sha256:${"f".repeat(64)}`; } },
    { field: "edges", focus: "workflow", change: value => { value.nodes.find((node: { id: string }) => node.id === "build").output_ports.items = value.inputs.corpus; value.edges[0].from = "build.items"; } },
    { field: "extract.corpus", focus: "input_ports", change: value => { value.edges.shift(); } },
    { field: "nodes[0].inactive_inputs", focus: "inactive_inputs", change: value => { value.nodes[0].kind = "join"; value.nodes[0].inactive_inputs = "wait_forever"; } },
    { field: "nodes[0].max_iterations", focus: "max_iterations", change: value => { Object.assign(value.nodes[0], { kind: "loop", body: value.nodes[0].model_ref, predicate: "truthy", max_iterations: 0, on_limit: "fail" }); } },
    { field: "nodes[0].concurrency", focus: "concurrency", change: value => { Object.assign(value.nodes[0], { kind: "map", body: value.nodes[0].model_ref, item_inputs: value.nodes[0].input_ports, item_outputs: value.nodes[0].output_ports, max_items: 3, concurrency: 99, failure_mode: "fail_fast" }); } },
   ];
   for (const check of checks) {
    const invalid = structuredClone(graph); check.change(invalid);
    await executable.fill(JSON.stringify(invalid));
    const response = page.waitForResponse(response => response.url().endsWith("/draft/validate") && response.request().method() === "POST");
    await validate.click(); expect((await response).status()).toBe(422);
    await expect(page.locator("#workflow-field-error")).toContainText(check.field);
    await expect(page.locator(`[data-field="${check.focus}"]`)).toBeFocused();
   }
   await executable.fill(JSON.stringify(graph));
   await page.getByLabel(tr("Search node kinds", "搜索节点类型"), { exact: true }).fill("map");
   await expect(page.getByRole("button", { name: tr("Add agent", "添加 agent"), exact: true })).toHaveCount(0);
   const add = page.getByRole("button", { name: tr("Add map", "添加 map"), exact: true }); await add.focus(); await add.press("Enter");
   const body = page.getByLabel(tr("Body workflow revision JSON", "子工作流版本 JSON"), { exact: true });
   await body.fill(JSON.stringify(graph.nodes[0].model_ref));
   await page.getByLabel(tr("Maximum items", "最大项目数"), { exact: true }).fill("9");
   await page.getByLabel(tr("Concurrent items", "并发项目数"), { exact: true }).fill("2");
   await page.getByLabel(tr("Item failure handling", "项目失败处理"), { exact: true }).selectOption("collect_errors");
   const itemInputs = page.getByLabel(tr("Item input schemas JSON", "项目输入架构 JSON"), { exact: true });
   await itemInputs.fill("{"); await expect(itemInputs).toHaveAttribute("aria-invalid", "true");
   const save = page.getByRole("button", { name: tr("Save draft", "保存草稿"), exact: true }); await expect(save).toBeDisabled();
   await page.getByRole("button", { name: /^build\b/ }).click();
   await expect(page.getByLabel(tr("Implementation revision JSON", "实现版本 JSON"), { exact: true })).toBeVisible();
   await page.getByRole("button", { name: /^map-node\b/ }).click(); await expect(itemInputs).toHaveValue("{");
   await page.getByRole("button", { name: tr("Save recovery", "保存恢复副本"), exact: true }).click(); await page.reload();
   await page.getByRole("button", { name: tr("Restore recovery", "恢复副本"), exact: true }).click();
   await expect(itemInputs).toHaveValue("{"); await expect(save).toBeDisabled();
   await itemInputs.fill(JSON.stringify(graph.nodes[0].input_ports));
   await page.getByLabel(tr("Item output schemas JSON", "项目输出架构 JSON"), { exact: true }).fill(JSON.stringify(graph.nodes[0].output_ports));
   await save.click(); await expect(page.getByText(tr("Draft saved.", "草稿已保存。"), { exact: true })).toBeVisible();
   const saved = await (await page.request.get(root)).json();
   expect(saved.workflow.nodes.find((node: { id: string }) => node.id === "map-node")).toMatchObject({ kind: "map", body: graph.nodes[0].model_ref, max_items: 9, concurrency: 2, failure_mode: "collect_errors", item_inputs: graph.nodes[0].input_ports, item_outputs: graph.nodes[0].output_ports });
   await page.screenshot({ path: info.outputPath("map-node-settings.png"), fullPage: true });
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true); expect(errors).toEqual([]);
  } finally { await context.close(); }
 });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`profile publication, pagination and revoked reads ${variant.locale} ${variant.width}`, async ({ page: admin, browser }) => {
    const { alpha, beta, user, grant } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const text = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    const digest = (value: string) => `sha256:${createHash("sha256").update(value).digest("hex")}`;
    const ref = (id: string) => ({ id, revision: 1, digest: digest(id) });
    const profile = { ref: { id: `profile-${crypto.randomUUID()}`, revision: 1 }, dataset: { id: "dataset", group_id: beta.id, digest: digest("dataset"), schema_digest: digest("schema"), grant_id: "grant", policy_revision: 1, size: 4, media_type: "application/json" }, scorer: ref("scorer"), runtime: ref("runtime"), model: ref("model"), tools: [], thresholds: [{ name: "accuracy", unit: "ratio", operator: "gte", value: .9, minimum_samples: 10, minimum_coverage: .8 }], fixed_model_required: true };
    const root = `/evolution-space/api/v1/groups/${beta.id}/profiles`;
    try {
      await login(page, user);
      await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/profiles/`);
      await page.getByRole("link", { name: text("Sign in", "登录"), exact: true }).click();
      await page.getByText(text("Published profiles and JSON editor", "已发布配置与 JSON 编辑器"), { exact: true }).click();
      const editor = page.locator("section.card").filter({ has: page.getByRole("heading", { name: text("Evaluator profile editor", "评估器配置编辑器"), exact: true }) });
      const input = editor.getByRole("textbox", { name: text("Profile JSON", "配置 JSON"), exact: true });
      const select = editor.getByLabel(text("Existing profile", "现有配置"), { exact: true });
      const publish = editor.getByRole("button", { name: text("Publish profile revision", "发布配置版本"), exact: true });
      const refresh = editor.getByRole("button", { name: text("Refresh", "刷新"), exact: true });
      await expect(input).toBeEnabled();
      await expect(editor.getByRole("alert")).toHaveCount(0);
      await input.fill(JSON.stringify(profile));
      await publish.click();
      await expect(editor.getByRole("status")).toContainText(`${profile.ref.id} @1`);
      const canonical = JSON.parse(await input.inputValue());
      expect(canonical.ref.digest).toMatch(/^sha256:[a-f0-9]{64}$/);
      const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
      const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
      const first = await (await page.request.get(`${root}/${profile.ref.id}/1?digest=${canonical.ref.digest}`)).json();
      expect(first.thresholds[0].minimum_samples).toBe(10);
      // Lose the response only after the real server commits the revision.
      canonical.ref.revision = 2; canonical.thresholds[0].minimum_samples = 20;
      await input.fill(JSON.stringify(canonical));
      await page.route(`**${root}`, async route => {
        if (route.request().method() !== "POST") return route.continue();
        const response = await route.fetch(); expect(response.status()).toBe(201);
        await route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"lost_reply"}' });
      });
      await publish.click();
      await expect(editor.getByRole("alert")).toBeVisible();
      expect(JSON.parse(await input.inputValue()).ref.revision).toBe(2);
      await page.unroute(`**${root}`);
      await publish.click();
      await expect(editor.getByRole("status")).toContainText(`${profile.ref.id} @2`);
      expect((await (await page.request.get(`${root}/${profile.ref.id}/1?digest=${first.ref.digest}`)).json()).thresholds[0].minimum_samples).toBe(10);
      for (let i = 0; i < 51; i++) {
        const response = await page.request.post(root, { headers, data: { ...profile, ref: { id: `paged-${String(i).padStart(3, "0")}-${crypto.randomUUID()}`, revision: 1 } } });
        expect(response.status(), await response.text()).toBe(201);
      }
      await refresh.click();
      await expect(select.locator("option")).toHaveCount(51);
      const more = editor.getByRole("button", { name: text("Load more profiles", "加载更多评估配置"), exact: true });
      for (let pages = 0; pages < 10 && await more.count(); pages++) {
        await more.click();
        await expect(input).toBeEnabled();
      }
      await expect(more).toHaveCount(0);
      expect(await select.locator("option").count()).toBeGreaterThanOrEqual(54);
      const choices = await select.locator("option").evaluateAll(options => options.map(option => (option as HTMLOptionElement).value));
      expect(new Set(choices).size).toBe(choices.length);
      await select.selectOption(`${profile.ref.id}:2`);
      await expect(input).toHaveValue(/"minimum_samples": 20/);
      // A superseded detail response cannot replace a refreshed selection.
      let releaseDetail!: () => void;
      const held = new Promise<void>(resolve => { releaseDetail = resolve; });
      let detailArrived!: () => void;
      const arrived = new Promise<void>(resolve => { detailArrived = resolve; });
      let detailFinished!: () => void;
      const finished = new Promise<void>(resolve => { detailFinished = resolve; });
      await page.route(`**${root}/${profile.ref.id}/1?*`, async route => {
        const response = await route.fetch();
        detailArrived(); await held;
        await route.fulfill({ response }); detailFinished();
      });
      await select.selectOption(`${profile.ref.id}:1`); await arrived;
      await refresh.click(); await expect(input).not.toHaveValue("");
      const refreshed = await input.inputValue();
      releaseDetail(); await finished;
      await page.unroute(`**${root}/${profile.ref.id}/1?*`);
      await expect(input).toHaveValue(refreshed);
      for (let pages = 0; pages < 10 && await more.count(); pages++) {
        await more.click(); await expect(input).toBeEnabled();
      }
      await page.route(`**${root}/${profile.ref.id}/1?*`, route => route.fulfill({ status: 403, contentType: "application/json", body: '{}' }));
      await select.selectOption(`${profile.ref.id}:1`);
      await expect(input).toHaveValue("");
      await expect(select.locator("option")).toHaveCount(1);
      await expect(editor.getByRole("alert")).toContainText(text("Profile detail is unavailable", "评估配置详情不可用"));
      await page.unroute(`**${root}/${profile.ref.id}/1?*`);
      await refresh.click();
      await expect(input).not.toHaveValue("");
      await grant(beta.id, "config.manage", "deny", 1);
      await refresh.click();
      await expect(editor.getByRole("alert")).toContainText(text("Evaluator profiles are unavailable", "评估器配置不可用"));
      await expect(input).toHaveValue("");
      await expect(select.locator("option")).toHaveCount(1);
      expect((await page.request.get(root)).status()).toBe(403);
      expect((await page.request.get(`${root}/${profile.ref.id}/1?digest=${first.ref.digest}`)).status()).toBe(403);
      expect((await page.request.post(root, { headers, data: profile })).status()).toBe(403);
      expect((await page.request.get(`/evolution-space/api/v1/groups/${alpha.id}/profiles`)).status()).toBe(403);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      expect(errors).toEqual([]);
    } finally { await context.close(); }
  });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
 test(`guided template authoring from an exact published workflow ${variant.locale}`, async ({ page: admin, browser }, info) => {
  const { alpha, beta, user, grant } = await provision(admin);
  await grant(alpha.id, "workflow.edit");
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
  const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  try {
   await login(page, user); await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/workflows/`);
   await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
   const graph = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8")); graph.id = `template-graph-${crypto.randomUUID().slice(0, 8)}`;
   const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
   const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
   const draftPath = `/evolution-space/api/v1/groups/${beta.id}/workflows/${graph.id}/draft`;
   expect((await page.request.put(draftPath, { headers, data: { id: graph.id, expected_revision: 0, expected_layout_revision: 0, workflow: graph, layout: {} } })).status()).toBe(200);
   const drafts = page.getByRole("region", { name: tr("Workflow drafts", "工作流草稿"), exact: true });
   await drafts.getByRole("button", { name: tr("Refresh", "刷新"), exact: true }).click();
   const draftLink = drafts.getByRole("link", { name: graph.id, exact: true });
   const nextDrafts = drafts.getByRole("button", { name: tr("Next page", "下一页"), exact: true });
   for (let pages = 0; pages < 20; pages++) {
    await expect(draftLink.or(nextDrafts).first()).toBeVisible();
    if (await draftLink.count()) break;
    await nextDrafts.click();
   }
   await draftLink.click();
   await page.getByRole("button", { name: tr("Publish revision", "发布版本"), exact: true }).click();
   const published = page.locator("section.card").filter({ has: page.getByRole("heading", { name: tr("Published workflow reference", "已发布工作流引用"), exact: true }) });
   const workflow = JSON.parse(await published.locator("pre").innerText());
   expect(workflow).toMatchObject({ id: graph.id, revision: 1 });
   const before = await (await page.request.get(`/evolution-space/api/v1/groups/${beta.id}`)).json();
   // The published reference flows straight into a new template.
   await published.getByRole("link", { name: tr("Create goal template with this workflow", "使用此工作流创建目标模板"), exact: true }).click();
   await expect(page.getByRole("heading", { name: tr("New goal template", "新建目标模板"), exact: true })).toBeVisible();
   await expect(page.getByText(`${workflow.id} · 1`, { exact: true })).toBeVisible();
   for (const port of Object.keys(graph.inputs)) await expect(page.locator(".port-list code", { hasText: port }).first()).toBeVisible();
   const publish = page.getByRole("button", { name: tr("Publish template revision", "发布模板版本"), exact: true });
   await expect(publish).toBeDisabled();
   const id = `guided-template-${crypto.randomUUID().slice(0, 8)}`;
   await page.getByLabel(tr("Template ID", "模板 ID"), { exact: true }).fill(id);
   await page.getByLabel(tr("Goal", "目标"), { exact: true }).fill("Extract grounded skills");
   await page.getByLabel(tr("Suggested output directory", "建议输出目录"), { exact: true }).fill("skills");
   await page.getByText(tr("Template document", "模板文档"), { exact: true }).click();
   await expect(page.locator(".document-preview")).toContainText(`"id": "${id}"`);
   const root = `/evolution-space/api/v1/groups/${beta.id}/templates`;
   // Lose the reply after the server commits; the identical retry replays it.
   await page.route(`**${root}`, async route => {
    if (route.request().method() !== "POST") return route.continue();
    const response = await route.fetch(); expect(response.status()).toBe(201);
    await route.fulfill({ status: 503, contentType: "application/json", body: '{}' });
   });
   await publish.click(); await expect(page.locator(".template-form [role=alert]")).toBeVisible(); await page.unroute(`**${root}`);
   await publish.click();
   await expect(page.getByRole("status").filter({ hasText: `${id} @1` })).toBeVisible();
   await expect(page).toHaveURL(new RegExp(`/templates/${id}/1/\\?digest=`));
   const first = await (await page.request.get(`${root}/${id}/1?digest=${new URL(page.url()).searchParams.get("digest")}`)).json();
   expect(first.workflows).toEqual([workflow]);
   const use = page.getByRole("link", { name: tr("Use in configuration", "在配置中使用"), exact: true });
   await expect(use).toHaveAttribute("href", new RegExp(`/groups/${beta.id}/settings/configuration/\\?template_id=${id}&template_revision=1&template_digest=sha256`));
   // A new revision keeps the ID and leaves revision 1 unchanged.
   await page.getByRole("link", { name: tr("New revision", "新建版本"), exact: true }).click();
   await expect(page.getByLabel(tr("Template ID", "模板 ID"), { exact: true })).toHaveValue(id);
   await expect(page.getByLabel(tr("Revision", "修订版本"), { exact: true })).toHaveValue("2");
   await page.getByLabel(tr("Goal", "目标"), { exact: true }).fill("Extract grounded skills with citations");
   await publish.click();
   await expect(page.getByRole("status").filter({ hasText: `${id} @2` })).toBeVisible();
   expect((await (await page.request.get(`${root}/${id}/1?digest=${first.ref.digest}`)).json()).goal).toBe("Extract grounded skills");
   const next = { ...first, ref: { id, revision: 3 } };
   expect((await page.request.post(`/evolution-space/api/v1/groups/${alpha.id}/templates`, { headers, data: next })).status()).toBe(409);
   expect(await (await page.request.get(`/evolution-space/api/v1/groups/${beta.id}`)).json()).toEqual(before);
   await page.screenshot({ path: info.outputPath("published-template.png"), fullPage: true });
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
   await page.getByRole("link", { name: tr("New revision", "新建版本"), exact: true }).click();
   await expect(page.getByLabel(tr("Revision", "修订版本"), { exact: true })).toHaveValue("3");
   await grant(beta.id, "workflow.edit", "deny", 1);
   await publish.click();
   await expect(page.locator(".template-form [role=alert]")).toContainText(tr("Publishing templates requires permission to edit workflows.", "发布模板需要编辑工作流的权限。"));
   expect(errors).toEqual([]);
  } finally { await context.close(); }
 });
}

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
 test(`run and evaluation history preserve scoped filters and pages ${variant.locale}`, async ({ page: admin, browser }, info) => {
  const { beta, user, grant } = await provision(admin);
  const context = await browser.newContext({baseURL:origin,ignoreHTTPSErrors:true,locale:variant.locale,viewport:{width:variant.width,height:1000}});
  const page = await context.newPage(); const tr=(en:string,zh:string)=>variant.locale==="zh-CN"?zh:en;
  const errors:string[]=[];page.on("pageerror",error=>errors.push(error.message));
  const runsRoot=`/evolution-space/api/v1/groups/${beta.id}/runs`;
  const evaluationsRoot=`/evolution-space/api/v1/groups/${beta.id}/evaluations`;
  let failRuns=false,failEvaluations=false;
  const run=(id:string)=>({id,state:"completed",handoff:"pending",evaluation_state:"not_requested",created_at:"2026-10-17T02:00:00Z",pins:{template:{id:"skills",revision:1},workflow:{id:"workflow",revision:2},base:{commit_id:"a".repeat(64)}}});
  const requests:URLSearchParams[]=[];
  // Synthetic metadata pages exercise browser state; actual filtering and
  // group isolation are covered by the SQLite and browser-API regressions.
  await page.route(`${origin}${runsRoot}?*`,route=>{
   const query=new URL(route.request().url()).searchParams; requests.push(query);
   if(failRuns)return route.fulfill({status:503,json:{error:"unavailable"}});
   if(query.get("template")==="missing")return route.fulfill({json:{items:[],next_after:"",has_more:false}});
   const second=!!query.get("after");
   return route.fulfill({json:{items:[run(second?"run-26":"run-01")],next_after:second?"run-26":"run-25",has_more:!second}});
  });
  await page.route(`${origin}${evaluationsRoot}?*`,route=>{
   if(failEvaluations)return route.fulfill({status:503,json:{error:"unavailable"}});
   const second=!!new URL(route.request().url()).searchParams.get("after");
   return route.fulfill({json:{items:[{id:second?"check-26":"check-01",state:"completed",profile:{ref:{id:"profile",revision:1}}}],next_after:second?"check-26":"check-25",has_more:!second}});
  });
  try {
   await login(page,user);await settleOverview(page); await page.goto(`/evolution-space/groups/${beta.id}/runs/`);
   await page.getByRole("link",{name:tr("Sign in","登录"),exact:true}).click();
   const runs=page.getByRole("region",{name:tr("Run history","运行历史"),exact:true});
   const evaluations=page.getByRole("region",{name:tr("Evaluation history","评估历史"),exact:true});
   await expect(runs.getByRole("link",{name:"run-01",exact:true})).toBeVisible();
   await expect(runs.getByRole("columnheader",{name:tr("Execution state","执行状态"),exact:true})).toBeVisible();
   await expect(runs.getByRole("columnheader",{name:tr("Handoff","交接"),exact:true})).toBeVisible();
   await runs.locator("summary").press("Enter");
   await runs.getByLabel(tr("Template ID","模板 ID"),{exact:true}).fill("skills");
   await runs.getByLabel(tr("Execution state","执行状态"),{exact:true}).selectOption("completed");
   await runs.getByLabel(tr("Created from (inclusive)","创建时间起点（包含）"),{exact:true}).fill("2026-10-17T00:00:00+08:00");
   await runs.getByRole("button",{name:tr("Apply filters","应用筛选"),exact:true}).press("Enter");
   await expect(page).toHaveURL(/runs_template=skills/);
   await expect.poll(()=>requests.at(-1)?.get("from")).toBe("2026-10-17T00:00:00+08:00");
   await runs.getByRole("button",{name:tr("Next page","下一页"),exact:true}).press("Enter");
   await expect(runs.getByRole("link",{name:"run-26",exact:true})).toBeVisible();
   await expect(runs.getByRole("link",{name:"run-01",exact:true})).toHaveCount(0);
   await page.reload();
   await expect(runs.getByRole("link",{name:"run-26",exact:true})).toBeVisible();
   await expect(runs.getByLabel(tr("Template ID","模板 ID"),{exact:true})).toHaveValue("skills");
   await runs.getByRole("link",{name:"run-26",exact:true}).press("Enter");
   await expect(page).toHaveURL(/\/runs\/run-26\/\?/);
   await expect(page.getByRole("heading",{name:tr("Run inspector","运行检查器"),exact:true})).toBeVisible();
   await page.goBack();
   await expect(runs.getByRole("link",{name:"run-26",exact:true})).toBeVisible();
   expect(new URL(page.url()).searchParams.get("runs_after")).toBe("run-25");
   failRuns=true;await runs.getByRole("button",{name:tr("Refresh","刷新"),exact:true}).click();
   await expect(runs.getByRole("alert")).toBeVisible();await expect(runs.getByRole("table")).toHaveCount(0);
   await expect(runs.getByText(tr("No matching runs.","没有匹配的运行。"),{exact:true})).toHaveCount(0);
   failRuns=false;await runs.getByRole("button",{name:tr("Retry","重试"),exact:true}).click();
   await expect(runs.getByRole("link",{name:"run-26",exact:true})).toBeVisible();
   await runs.getByLabel(tr("Template ID","模板 ID"),{exact:true}).fill("missing");
   await runs.getByRole("button",{name:tr("Apply filters","应用筛选"),exact:true}).click();
   await expect(runs.getByText(tr("No matching runs.","没有匹配的运行。"),{exact:true})).toBeVisible();
   expect(new URL(page.url()).searchParams.has("runs_after")).toBe(false);
   await runs.getByRole("button",{name:tr("Clear filters","清除筛选"),exact:true}).click();
   await expect(runs.getByRole("link",{name:"run-01",exact:true})).toBeVisible();
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
   await page.screenshot({path:info.outputPath("execution-history.png"),fullPage:true});
   // Evaluation history pages independently on its own section.
   await page.getByRole("navigation",{name:tr("Group sections","团队页面导航"),exact:true}).getByRole("link",{name:tr("Evaluations","评估"),exact:true}).click();
   await evaluations.getByRole("button",{name:tr("Next page","下一页"),exact:true}).press("Enter");
   await expect(evaluations.getByRole("link",{name:"check-26",exact:true})).toBeVisible();
   await page.reload();
   await expect(evaluations.getByRole("link",{name:"check-26",exact:true})).toBeVisible();
   failEvaluations=true;await evaluations.getByRole("button",{name:tr("Refresh","刷新"),exact:true}).click();
   await expect(evaluations.getByRole("alert")).toBeVisible();await expect(evaluations.getByRole("link")).toHaveCount(0);
   failEvaluations=false;await evaluations.getByRole("button",{name:tr("Retry","重试"),exact:true}).click();
   await expect(evaluations.getByRole("link",{name:"check-26",exact:true})).toBeVisible();
   await grant(beta.id,"config.read","deny",1);
   await page.reload();
   await expect(runs).toHaveCount(0);await expect(evaluations).toHaveCount(0);
   expect((await page.request.get(`${runsRoot}?state=completed&after=run-25`)).status()).toBe(403);
   expect(errors).toEqual([]);
  } finally {await context.close()}
 });
}

test("account preferences apply without local controls and central logout synchronizes open tabs", async ({ page: admin, browser }, info) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width: 390, height: 900 } });
  const page = await context.newPage();
  try {
    await login(page, user);
    await page.goto(`/evolution-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.locator(".overview h1")).toHaveText("Team Beta");
    const other = await context.newPage();
    await other.goto(page.url());
    await expect(other.locator(".overview h1")).toHaveText("Team Beta");
    // No application strip or appearance editor; Workbench is one account-menu item.
    await expect(page.getByRole("navigation", { name: "Applications", exact: true })).toHaveCount(0);
    await expect(page.getByRole("combobox", { name: "Theme mode", exact: true })).toHaveCount(0);
    await page.getByText("Account", { exact: true }).click();
    await expect(page.getByRole("link", { name: "Back to Workbench", exact: true })).toHaveAttribute("href", "/");
    await page.keyboard.press("Escape");
    await setPreferences(page, { theme_mode: "dark", accent: "violet" });
    await other.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
    await expect(other.locator("html")).toHaveAttribute("data-theme", "dark");
    await expect(other.locator("html")).toHaveAttribute("data-accent", "violet");
    await setPreferences(page, { theme_mode: "system", accent: "neutral" });
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath("workspace-mobile.png"), fullPage: true });
    // End the actual central session, without relying on a local-storage signal.
    expect((await other.request.post("/api/auth/logout", { headers: { Origin: origin } })).ok()).toBeTruthy();
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(page.getByRole("link", { name: "Sign in", exact: true })).toBeVisible();
    await expect(page.locator(".overview")).toHaveCount(0);
  } finally { await context.close(); }
});

test("completed run handoff and return links retain branch and history context", async ({ page: admin, browser }) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US" });
  const page = await context.newPage();
  try {
    await login(page, user);
    await page.goto(`/evolution-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.locator(".overview h1")).toHaveText("Team Beta");
    const root = `/evolution-space/api/v1/groups/${beta.id}/runs/review-run`;
    let handoff = "submitted";
    await page.route(`${origin}${root}`, route => route.fulfill({ json: {
      id: "review-run", state: "completed", fence: 1, handoff, result_branch: "output-branch", evaluation_state: "not_requested",
      pins: { output: { library_id: "output-library", prefix: "skills" }, workflow: { id: "skills", revision: 1 } },
    } }));
    // Retained metadata and review navigation remain useful without source evidence.
    await page.route(`${origin}${root}/*`, route => route.fulfill({ status: 403, json: { error: "denied" } }));
    await page.goto(`/evolution-space/groups/${beta.id}/runs/review-run/?runs_state=completed&runs_after=prior&groups_search=Beta`);
    const review = page.getByRole("link", { name: "Open branch in Volume Studio", exact: true });
    await expect(review).toHaveAttribute("href", "/volumes-studio/libraries/output-library/?branch=output-branch");
    const back = page.getByRole("link", { name: "Back to runs", exact: true });
    const target = new URL((await back.getAttribute("href"))!, origin);
    expect(target.pathname).toBe(`/evolution-space/groups/${beta.id}/runs/`);
    expect(target.searchParams.get("runs_state")).toBe("completed");
    expect(target.searchParams.get("runs_after")).toBe("prior");
    expect(target.searchParams.get("groups_search")).toBe("Beta");
    handoff = "no_change";
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(page.getByText("This run produced no changes. No output branch needs review.", { exact: true })).toBeVisible();
    await expect(review).toHaveCount(0);
    handoff = "pending";
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(page.getByText(/Output transfer is pending/)).toBeVisible();
  } finally { await context.close(); }
});

test("a central account switch gates the previous derived workspace", async ({ page: admin, browser }) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true });
  const page = await context.newPage();
  try {
    await login(page, user);
    await page.goto(`/evolution-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.locator(".overview h1")).toHaveText("Team Beta");
    // Signing in another central account need not revoke the prior session's
    // scoped cookie. The shell must compare identities before rendering it.
    expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "evolution-space-admin", password: "browser-fixture-password" } })).ok()).toBeTruthy();
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(page.getByRole("link", { name: "Sign in", exact: true })).toBeVisible();
    await expect(page.locator(".overview")).toHaveCount(0);
  } finally { await context.close(); }
});

test("unsaved template edits survive identity outages and cancelled group switches", async ({ page: admin, browser }) => {
  const { alpha, beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US" });
  const page = await context.newPage();
  try {
    await login(page, user);
    await page.goto(`/evolution-space/groups/${beta.id}/templates/new/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    const goal = page.getByLabel("Goal", { exact: true });
    await expect(goal).toBeEnabled();
    await goal.fill("unsaved goal retained");
    await page.route("**/evolution-space/api/v1/session", route => route.fulfill({ status: 503, json: { error: "unavailable" } }));
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(page.getByRole("alert").filter({ hasText: "Identity is unavailable." })).toBeVisible();
    await expect(goal).toHaveValue("unsaved goal retained");
    await page.unroute("**/evolution-space/api/v1/session");
    await page.getByRole("alert").filter({ hasText: "Identity is unavailable." }).getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Identity is unavailable." })).toHaveCount(0);
    await expect(goal).toHaveValue("unsaved goal retained");
    const cancelled = page.waitForEvent("dialog").then(async dialog => { expect(dialog.type()).toBe("confirm"); await dialog.dismiss(); });
    await switchGroup(page, "Team Alpha");
    await cancelled;
    await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
    await expect(goal).toHaveValue("unsaved goal retained");
    expect(new URL(page.url()).pathname).toContain(beta.id);
    const accepted = page.waitForEvent("dialog").then(dialog => dialog.accept());
    await switchGroup(page, "Team Alpha");
    await accepted;
    await expect(page.locator(".overview h1")).toHaveText("Team Alpha");
    expect(new URL(page.url()).pathname).toContain(alpha.id);
  } finally { await context.close(); }
});

for (const width of [1440, 390]) test(`graph canvas edits layout without changing the executable workflow at ${width}px`, async ({ page: admin, browser }, info) => {
  const { beta, user } = await provision(admin);
  const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: "en-US", viewport: { width, height: 900 } });
  const page = await context.newPage();
  // React Flow observes canvas size; WebKit reports a deferred resize notice
  // as a page error. It is not an application failure.
  const errors: string[] = []; page.on("pageerror", error => { if (!error.message.startsWith("ResizeObserver loop")) errors.push(error.message); });
  try {
    await login(page, user);
    await page.context().addInitScript(() => localStorage.setItem("evolution-space:canvas-view", "graph"));
    const graph = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8")); graph.id = `canvas-${crypto.randomUUID().slice(0, 8)}`;
    // Open the editor directly, then create the draft and retry in place, so no
    // document navigation interrupts in-flight reads (WebKit reports those).
    await page.goto(`/evolution-space/groups/${beta.id}/workflows/${graph.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Workflow draft is unavailable" })).toBeVisible();
    const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
    const draftPath = `/evolution-space/api/v1/groups/${beta.id}/workflows/${graph.id}/draft`;
    expect((await page.request.put(draftPath, { headers: { Origin: origin, "X-CSRF-Token": session.csrf }, data: { id: graph.id, expected_revision: 0, expected_layout_revision: 0, workflow: graph, layout: {} } })).status()).toBe(200);
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    const canvas = page.getByRole("group", { name: "Workflow graph", exact: true });
    await expect(canvas).toBeVisible();
    for (const node of graph.nodes) await expect(canvas.locator(".flow-node header strong", { hasText: new RegExp(`^${node.id}$`) })).toBeVisible();
    await expect(canvas.getByText("Workflow inputs", { exact: true })).toBeVisible();
    await canvas.locator(".react-flow__node", { hasText: "critic" }).click();
    await expect(page.getByRole("complementary").filter({ has: page.getByRole("heading", { name: "Node inspector" }) }).getByLabel("Node ID", { exact: true })).toHaveValue("critic");
    const before = await (await page.request.get(draftPath)).json();
    await page.getByRole("button", { name: "Auto-layout", exact: true }).click();
    await expect(page.getByText("Unsaved changes", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Save draft", exact: true }).click();
    await expect(page.getByText("Draft saved.", { exact: true })).toBeVisible();
    const after = await (await page.request.get(draftPath)).json();
    expect(after.revision).toBe(before.revision);
    expect(after.layout_revision).toBe(before.layout_revision + 1);
    expect(after.workflow).toEqual(before.workflow);
    for (const node of graph.nodes) expect(Number.isFinite(after.layout[node.id].x)).toBe(true);
    // The ordered list remains a complete keyboard alternative.
    await page.getByRole("button", { name: "List", exact: true }).click();
    await expect(canvas).toHaveCount(0);
    await page.getByRole("button", { name: /^extract\b/ }).click();
    await expect(page.getByLabel("Node ID", { exact: true })).toHaveValue("extract");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.getByRole("button", { name: "Graph", exact: true }).click();
    await page.screenshot({ path: info.outputPath(`canvas-${width}.png`), fullPage: true });
    await page.getByRole("group", { name: "Workflow graph", exact: true }).screenshot({ path: info.outputPath(`canvas-element-${width}.png`) });
    expect(errors).toEqual([]);
  } finally { await context.close(); }
});
