import { expect, test } from "@playwright/test";

type Attempt = { call: { id: string; instance: string; number: number }; state: string; reused_from?: { run_id: string; checkpoint_id: string; attempt_id: string } };

test("native checkpoint experiment, comparison and connection recovery", async ({ page, context, baseURL }, info) => {
  const chinese = process.env.ADMIN_EVOLUTION_LOCALE?.startsWith("zh");
  const tr = (en: string, zh: string) => chinese ? zh : en;
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  await context.addCookies([{ name: "project_login", value: process.env.ADMIN_EVOLUTION_TOKEN!, url: baseURL!, secure: true, httpOnly: true, sameSite: "Lax" }]);
  const sourceID = process.env.ADMIN_EVOLUTION_SOURCE_RUN!;
  const root = "/evolution-space/api/v1/groups/standard-users/runs/";
  const sourcePath = `/evolution-space/groups/standard-users/runs/${sourceID}/`;
  await page.goto(`/evolution-space/auth/login?return=${encodeURIComponent(sourcePath)}`);
  await expect(page.getByRole("heading", { name: tr("Run status: paused", "运行状态: 已暂停"), exact: true })).toBeVisible();
  const sourceBefore = await (await page.request.get(root + sourceID)).json();
  await page.getByRole("button", { name: tr("Run from here", "从此处运行"), exact: true }).click();
  const form = page.getByRole("region", { name: tr("Checkpoint experiment", "检查点实验"), exact: true });
  const workflowInput = form.getByRole("textbox", { name: tr("Frozen experiment workflow", "冻结的实验工作流"), exact: true });
  await expect(workflowInput).not.toHaveValue("");
  const workflow = JSON.parse(await workflowInput.inputValue());
  workflow.nodes.find((node: { id: string }) => node.id === "revise").timeout_seconds--;
  await workflowInput.fill(JSON.stringify(workflow, null, 2));
  await form.getByRole("combobox", { name: tr("Start node", "起始节点"), exact: true }).selectOption("revise");
  await form.getByRole("button", { name: tr("Preview checkpoint reuse", "预览检查点复用"), exact: true }).click();
  const preview = form.getByRole("status");
  await expect(preview).toContainText(tr("Eligible dependencies", "符合条件的依赖"));
  await expect(preview).toContainText("root/draft");
  const create = form.getByRole("button", { name: tr("Create experiment", "创建实验"), exact: true });
  const submitted = page.waitForRequest(request => request.method() === "POST" && request.url().endsWith(`/runs/${sourceID}/experiment`));
  await create.focus(); await create.press("Enter");
  const experimentRequest = (await submitted).postData()!;
  await expect(page).toHaveURL(/\/runs\/(?!browser-native-retry)[^/]+\/?$/);
  const experimentID = new URL(page.url()).pathname.split("/").filter(Boolean).at(-1)!;
  await expect(page.getByRole("heading", { name: tr("Run status: completed", "运行状态: 已完成"), exact: true })).toBeVisible({ timeout: 30000 });
  await expect.poll(async () => (await (await page.request.get(root + experimentID)).json()).handoff).toBe("submitted");
  const comparison = page.locator("section.card").filter({ has: page.getByRole("heading", { name: tr("Experiment comparison", "实验比较"), exact: true }) });
  await expect(comparison).toContainText(sourceID);
  await expect(comparison.locator("dt").filter({ hasText: new RegExp(`^${tr("Source state", "源状态")}$`) }).locator("..")).toContainText(tr("paused", "已暂停"));
  const sourceAttempts: Attempt[] = (await (await page.request.get(root + sourceID + "/attempts?limit=100")).json()).items;
  const attempts: Attempt[] = (await (await page.request.get(root + experimentID + "/attempts?limit=100")).json()).items;
  const paths = [...new Set([...sourceAttempts, ...attempts].map(attempt => attempt.call.instance))].sort();
  const rows = comparison.locator("tbody tr");
  await expect(rows).toHaveCount(paths.length);
  expect(await rows.locator("th").allTextContents()).toEqual(paths);
  const reused = attempts.find(attempt => attempt.reused_from)!;
  expect(reused.reused_from?.run_id).toBe(sourceID);
  const reusedRow = rows.filter({ has: page.getByRole("rowheader", { name: reused.call.instance, exact: true }) });
  await expect(reusedRow).toContainText(tr("unchanged", "未变化"));
  await expect(rows.filter({ has: page.getByRole("rowheader", { name: "root/revise", exact: true }) })).toContainText(tr("added", "新增"));
  const reusedDetail = page.locator("details").filter({ has: page.locator("summary strong", { hasText: reused.call.instance }) });
  await reusedDetail.locator("summary").click();
  await expect(reusedDetail).toContainText(reused.reused_from!.checkpoint_id);
  await reusedDetail.getByRole("link", { name: sourceID, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`${sourceID}/?\\?attempt=${reused.reused_from!.attempt_id}$`));
  await expect(page.locator(`#attempt-${reused.reused_from!.attempt_id}`)).toHaveAttribute("open", "");
  await page.goBack();
  await expect(comparison).toBeVisible();
  await page.screenshot({ path: info.outputPath("native-experiment-comparison.png"), fullPage: true });

  // A real browser network outage clears evidence, then reconstructs it from
  // the actual service. Replayed transport pages use retained native events.
  await context.setOffline(true);
  await expect(page.getByRole("alert").filter({ hasText: chinese ? /运行(证据不可用|详情)/ : /Run (detail|evidence) is unavailable/ })).toBeVisible({ timeout: 10000 });
  await expect(page.getByRole("heading", { name: tr("Execution debugger", "执行调试器"), exact: true })).toHaveCount(0);
  await expect(page.locator("details")).toHaveCount(0);
  await expect(comparison).toHaveCount(0);
  await context.setOffline(false);
  const events = await (await page.request.get(root + experimentID + "/events?after=0&limit=100")).json();
  expect(events.has_more).toBe(false); expect(events.events.length).toBeGreaterThan(0);
  let replayed = false; let resumed = false;
  const eventPattern = `**${root}${experimentID}/events?*`;
  await page.route(eventPattern, async route => {
    const after = Number(new URL(route.request().url()).searchParams.get("after"));
    if (!replayed) {
      replayed = true;
      await route.fulfill({ json: { ...events, events: [...events.events, ...events.events], next_after: 0 } });
    } else {
      resumed = after >= events.next_after;
      await route.continue();
    }
  });
  const refresh = page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true });
  if (await refresh.count()) await refresh.click();
  else await page.getByRole("button", { name: tr("Retry", "重试"), exact: true }).click();
  await expect(comparison).toBeVisible();
  const timeline = page.locator("section.card").filter({ has: page.getByRole("heading", { name: tr("Event timeline", "事件时间线"), exact: true }) }).locator("ol > li");
  await expect(timeline).toHaveCount(events.events.length);
  await expect.poll(() => resumed, { timeout: 10000 }).toBe(true);
  await expect(timeline).toHaveCount(events.events.length);
  await expect(page.locator("details")).toHaveCount(attempts.length);
  await expect(page.getByRole("heading", { name: tr("Run status: completed", "运行状态: 已完成"), exact: true })).toBeVisible();
  await page.unroute(eventPattern);
  const session = await (await page.request.get("/evolution-space/api/v1/session")).json();
  const grantRoot = "/api/admin/groups/standard-users/optimization/grants";
  const grants = await (await page.request.get(`${grantRoot}/${session.subject}`)).json();
  let revision = grants.grants.find((grant: { action: string }) => grant.action === "source.read").revision;
  const setEvidenceAccess = async (effect: string) => {
    const response = await page.request.put(grantRoot, { headers: { Origin: baseURL! }, data: { request_id: crypto.randomUUID(), account_id: session.subject, action: "source.read", effect, expected_revision: revision } });
    expect(response.ok(), await response.text()).toBe(true);
    revision = (await response.json()).revision;
  };
  await setEvidenceAccess("deny");
  try {
    await page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true }).click();
    await expect(page.getByRole("alert").filter({ hasText: tr("Run evidence is unavailable.", "运行证据不可用") })).toBeVisible();
    await expect(page.locator("details")).toHaveCount(0);
    await expect(comparison).toHaveCount(0);
    expect((await page.request.get(root + experimentID + "/attempts?limit=100")).status()).toBe(403);
    expect((await page.request.get(root + experimentID + "/artifacts/" + events.events.find((event: { artifacts?: unknown[] }) => event.artifacts?.length).artifacts[0].id)).status()).toBe(403);
    await page.screenshot({ path: info.outputPath("native-inspector-denied.png"), fullPage: true });
  } finally { await setEvidenceAccess("allow"); }
  await page.getByRole("button", { name: tr("Refresh", "刷新"), exact: true }).click();
  await expect(comparison).toBeVisible();
  expect(await (await page.request.get(root + sourceID)).json()).toEqual(sourceBefore);
  const overflow = await page.evaluate(() => [...document.querySelectorAll("body *")].filter(element => element.getBoundingClientRect().right > innerWidth + 1).map(element => `${element.tagName}.${element.className}`).slice(0, 12));
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), JSON.stringify(overflow)).toBe(true);
  await page.screenshot({ path: info.outputPath("native-inspector-reconnected.png"), fullPage: true });
  expect(errors).toEqual([]);
  console.log(`NATIVE_EXPERIMENT_ID=${experimentID}`);
  console.log(`NATIVE_EXPERIMENT_REQUEST=${Buffer.from(experimentRequest).toString("base64")}`);
});
