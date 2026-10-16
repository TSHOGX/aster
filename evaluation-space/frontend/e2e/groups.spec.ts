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

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`workflow editor preserves visual edits, invalid fields and conflicts ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { alpha, beta, user, grant } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage(); const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message)); page.on("dialog", dialog => dialog.accept());
    try {
      await login(page, user); await page.goto(`/evaluation-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const workflow = JSON.parse(fs.readFileSync(path.resolve(process.cwd(), "../../contracts/evolution/testdata/skills.valid.json"), "utf8"));
      workflow.id = `skills-${crypto.randomUUID().slice(0, 8)}`;
      const session = await (await page.request.get("/evaluation-space/api/v1/session")).json() as { csrf: string };
      const draftPath = `/evaluation-space/api/v1/groups/${beta.id}/workflows/${workflow.id}/draft`;
      const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
      const seeded = await page.request.put(draftPath, { headers, data: { id: workflow.id, expected_revision: 0, expected_layout_revision: 0, workflow, layout: { extract: { x: 12, y: 24 } } } });
      expect(seeded.status(), await seeded.text()).toBe(200);
      const foreignDraft = `/evaluation-space/api/v1/groups/${alpha.id}/workflows/${workflow.id}/draft`;
      expect((await page.request.get(foreignDraft)).status()).toBe(404);
      expect((await page.request.get(`${foreignDraft}/revisions/1`)).status()).toBe(404);
      await grant(alpha.id, "workflow.edit");
      expect((await page.request.put(foreignDraft, { headers, data: { id: workflow.id, expected_revision: 1, expected_layout_revision: 1, workflow, layout: {} } })).status()).toBe(404);
      expect((await page.request.post(`${foreignDraft}/publish`, { headers, data: { revision: 1 } })).status()).toBe(404);
      await page.goto(`/evaluation-space/groups/${beta.id}/workflows/${workflow.id}/`);
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
      await page.getByLabel(tr("Canvas layout JSON", "画布布局 JSON"), { exact: true }).fill('{"extract":{"x":80,"y":120}}');
      await expect(digest).toHaveText(originalDigest); await save.click(); await expect(digest).toHaveText(originalDigest);
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
    await page.goto(`/evaluation-space/groups/${beta.id}/`);
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.getByRole("region", { name: "Group overview", exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
    const root = `/evaluation-space/api/v1/groups/${beta.id}/runs`;
    const run = { id: "terminal-run", state: "completed", fence: 2, handoff: "submitted", evaluation_state: "not_requested", pins: { workflow: { id: "skills", revision: 1 }, service: { id: "native", revision: 1 }, configuration_revision: 1 }, created_at: new Date().toISOString() };
    const retry = { ...run, id: "retry-run", state: "queued", retry_of: "terminal-run" };
    await page.route(`${origin}${root}/terminal-run`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(run) }));
    await page.route(`${origin}${root}/retry-run`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(retry) }));
    for (const suffix of ["events?after=0&limit=100", "attempts?limit=100", "artifacts", "debugger"]) {
      await page.route(`${origin}${root}/terminal-run/${suffix}`, route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(suffix === "events?after=0&limit=100" ? { events: [], next_after: 0 } : suffix === "attempts?limit=100" || suffix === "artifacts" ? { items: [] } : { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 0, cost_micros: 0 } }, attempts: [], events: [], provider_observations: [], opaque: [] }) }));
    }
    const requests: unknown[] = [];
    await page.route(`${origin}${root}/terminal-run/retry`, async route => {
      requests.push(route.request().postDataJSON());
      if (requests.length === 1) { await route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"reply_lost"}' }); return; }
      await route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify(retry) });
    });
    await page.goto(`/evaluation-space/groups/${beta.id}/runs/terminal-run`);
    await expect(page.getByRole("heading", { name: /Run inspector: terminal-run/, exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Retry as new run", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Retry as new run", exact: true }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Run retry is unavailable." })).toBeVisible();
    await page.reload();
    await page.getByRole("button", { name: "Retry as new run", exact: true }).click();
    await expect.poll(() => requests.length).toBe(2);
    expect(requests[1]).toEqual(requests[0]);
    await expect(page).toHaveURL(new RegExp(`/evaluation-space/groups/${beta.id}/runs/retry-run/?$`));
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
      await page.goto(`/evaluation-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const root = `/evaluation-space/api/v1/groups/${beta.id}/evaluations/job-report`;
      const job = { id: "job-report", state: "running", fence: 1, policy_mode: variant.mode, profile: { ref: { id: "profile", revision: 2 } }, subject: { candidate: { tree_digest: "sha256:candidate" } }, created_at: new Date().toISOString() };
      const completion = { result: { state: "completed", outcome: "pass", subject: { dataset: { id: "held-out", digest: "sha256:dataset" }, scorer: { id: "scorer", revision: 1 }, runtime: { id: "runtime", revision: 1 }, model: { id: "model", revision: 1 }, tools: [] }, metrics: [{ name: "accuracy", unit: "ratio", value: 1, samples: 10, coverage: 1 }], reports: [{ id: "report-artifact", size: 12, digest: "sha256:report" }] }, usage: { spent: { tokens: 2, cost_micros: 3 }, held: { tokens: 0, cost_micros: 0 } } };
      let jobStatus = 200, resultStatus = 404;
      // These intercepted responses test UI lifecycle only. Worker assessment,
      // held-out authorization and merge policy have separate service tests.
      await page.route(`${origin}${root}`, route => route.fulfill({ status: jobStatus, contentType: "application/json", body: JSON.stringify(jobStatus === 200 ? job : { error: "access_denied" }) }));
      await page.route(`${origin}${root}/result`, route => route.fulfill({ status: resultStatus, contentType: "application/json", body: JSON.stringify(resultStatus === 200 ? completion : { error: "result_unavailable" }) }));
      await page.route(`${origin}${root}/cancel`, route => route.fulfill({ status: 403, contentType: "application/json", body: '{"error":"access_denied"}' }));
      await page.goto(`/evaluation-space/groups/${beta.id}/evaluations/job-report`);
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

for (const variant of [{ locale: "en-US", width: 1440 }, { locale: "zh-CN", width: 390 }]) {
  test(`run inspector waits for cooperative pause ${variant.locale}`, async ({ page: admin, browser }, info) => {
    const { beta, user } = await provision(admin);
    const context = await browser.newContext({ baseURL: origin, ignoreHTTPSErrors: true, locale: variant.locale, viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    const tr = (en: string, zh: string) => variant.locale === "zh-CN" ? zh : en;
    const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
    try {
      await login(page, user);
      await page.goto(`/evaluation-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const root = `/evaluation-space/api/v1/groups/${beta.id}/runs/pause-run`;
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
        else if (path.endsWith("/debugger")) body = { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 10, cost_micros: 20 }, available: { tokens: 90, cost_micros: 80 } }, attempts: [], events: [], provider_observations: [], opaque: [] };
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
      });
      await page.goto(`/evaluation-space/groups/${beta.id}/runs/pause-run`);
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
      await login(page, user); await page.goto(`/evaluation-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const root = `/evaluation-space/api/v1/groups/${beta.id}/runs`;
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
        else if (path.endsWith("/debugger")) body = { budget: { spent: { tokens: 0, cost_micros: 0 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 30, cost_micros: 100 } }, attempts: [], events: [], provider_observations: [], opaque: [] };
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
      });
      await page.goto(`/evaluation-space/groups/${beta.id}/runs/experiment-source`);
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
      await login(page, user); await page.goto(`/evaluation-space/groups/${beta.id}/`);
      await page.getByRole("link", { name: tr("Sign in", "登录"), exact: true }).click();
      await expect(page.getByRole("region", { name: tr("Group overview", "团队概览"), exact: true }).getByRole("heading", { name: "Team Beta", exact: true })).toBeVisible();
      const runs = `/evaluation-space/api/v1/groups/${beta.id}/runs`; const root = `${runs}/sensitive-run`;
      const run = { id: "sensitive-run", state: "completed", fence: 1, handoff: "not_ready", evaluation_state: "not_requested", experiment_of: "source-run", pins: { workflow: { id: "skills", revision: 1 } }, created_at: new Date().toISOString() };
      const attempt = { call: { id: "attempt", instance: "root/extract", number: 1, node: { id: "extract", kind: "agent" }, inputs: { corpus: "sensitive-input" }, prompt: "sensitive-prompt" }, state: "succeeded", outputs: { result: "sensitive-output" } };
      const artifact = { id: "sensitive-artifact", size: 10, digest: "sha256:sensitive", media_type: "application/json" };
      const event = { id: "event", sequence: 1, kind: "attempt-succeeded", attempt_id: "attempt", timestamp: new Date().toISOString(), artifacts: [artifact] };
      const debug = { budget: { spent: { tokens: 2, cost_micros: 3 }, held: { tokens: 0, cost_micros: 0 }, available: { tokens: 98, cost_micros: 97 } }, attempts: [attempt], events: [event], provider_observations: [], opaque: [] };
      let status = 200, evidenceStatus = 200; const cursors: string[] = [];
      await page.route(`${origin}${runs}/**`, route => {
        const url = new URL(route.request().url()); let body: unknown = run; let code = status;
        if (url.pathname.endsWith("/events")) { cursors.push(url.searchParams.get("after")!); body = { events: [event, event], next_after: 1 }; code = evidenceStatus; }
        else if (url.pathname.endsWith("/attempts")) { body = { items: [attempt] }; code = evidenceStatus; }
        else if (url.pathname.endsWith("/artifacts")) { body = url.searchParams.has("after") ? { items: [], next_after: "", has_more: false } : { items: [artifact], next_after: "sensitive-artifact", has_more: true }; code = evidenceStatus; }
        else if (url.pathname.endsWith("/debugger")) { body = debug; code = evidenceStatus; }
        else if (url.pathname.endsWith("/source-run")) body = { ...run, id: "source-run", experiment_of: undefined };
        return route.fulfill({ status: code, contentType: "application/json", body: JSON.stringify(code === 200 ? body : { error: "unavailable" }) });
      });
      await page.goto(`/evaluation-space/groups/${beta.id}/runs/sensitive-run`);
      const download = page.getByRole("link", { name: tr("Open bounded content", "打开有界内容"), exact: true });
      const refresh = page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true });
      await expect(download).toBeVisible();
      await expect(page.getByRole("button", { name: tr("Load more artifacts", "加载更多工件"), exact: true })).toBeVisible();
      await page.getByRole("button", { name: tr("Load more artifacts", "加载更多工件"), exact: true }).click();
      await expect(page.getByRole("button", { name: tr("Load more artifacts", "加载更多工件"), exact: true })).toHaveCount(0);
      await page.locator("summary").filter({ hasText: "root/extract" }).click();
      await expect(page.getByText("sensitive-prompt", { exact: true })).toBeVisible();
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
