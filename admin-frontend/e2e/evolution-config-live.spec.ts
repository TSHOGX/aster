import { expect, test } from "@playwright/test";
import { chooseValue } from "./picker";
import { ZH } from "../lib/i18n/messages";
import { writeFile } from "node:fs/promises";
test("published catalog drives recoverable draft and two-participant activation", async ({ page, context, baseURL }, info) => {
  const tr = (text: string) => process.env.ADMIN_EVOLUTION_LOCALE?.startsWith("zh") ? ZH[text] || text : text;
  await context.addCookies([{ name: "project_login", value: process.env.ADMIN_EVOLUTION_TOKEN!, url: baseURL!, secure: true, httpOnly: true, sameSite: "Lax" }]);
  let lose = true; const requests: unknown[] = [];
  await page.route("**/optimization/draft", async route => {
    if (route.request().method() !== "PUT") return route.continue();
    requests.push(route.request().postDataJSON());
    if (lose) { lose = false; const response = await route.fetch(); expect(response.status()).toBe(200); await response.dispose(); await route.abort("failed"); return; }
    await route.continue();
  });
  await page.goto("/admin/groups/standard-users/optimization");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(tr("Automatic optimization"));
  await chooseValue(page.getByRole("combobox", { name: tr("Default evolution service"), exact: true }), { label: "native-service · 1" });
  const groupBudget = { "Maximum tokens": "2000", "Maximum cost (micros)": "20000", "Maximum attempts": "20", "Timeout (seconds)": "120", "Artifact size limit (bytes)": "1048576" };
  for (const [label, value] of Object.entries(groupBudget)) await page.getByLabel(`${tr("Group budget ceiling")}: ${tr(label)}`, { exact: true }).fill(value);
  const sourceSection = page.locator("section.panel").filter({ has: page.getByRole("heading", { name: tr("Approved source policies"), exact: true }) });
  await sourceSection.getByRole("button", { name: tr("Edit source policy"), exact: true }).click();
  const sourcePolicy = page.getByRole("region", { name: tr("Source reuse policy"), exact: true });
  const policies = await (await page.request.get("/api/admin/groups/standard-users/optimization/sources?limit=50")).json();
  const resources = await (await page.request.get("/api/admin/groups/standard-users/optimization/source-resources?kind=project&limit=25")).json();
  expect(resources.items.some((item: { id: string }) => item.id === policies.sources[0].resource_id)).toBe(true);
  await chooseValue(sourcePolicy.getByRole("combobox", { name: tr("Source resource"), exact: true }), policies.sources[0].resource_id);
  await expect(sourcePolicy.getByRole("group", { name: tr("Source audience groups"), exact: true }).getByRole("checkbox").first()).toBeChecked();
  await sourcePolicy.getByLabel(tr("Excluded selectors (one per line)"), { exact: true }).fill("executions/unrelated");
  await sourcePolicy.getByRole("button", { name: tr("Save source policy"), exact: true }).click();
  await expect(sourcePolicy).toHaveCount(0);
  await sourceSection.getByRole("checkbox").check();
  for (const id of ["example-skills", "example-corrections"]) {
    await chooseValue(page.getByRole("combobox", { name: tr("Goal template"), exact: true }), { label: `${id} · 1` });
    await page.getByRole("button", { name: tr("Add template binding"), exact: true }).click();
    const binding = page.locator("article.evolution-binding").filter({ has: page.getByRole("heading", { name: id, exact: true }) });
    await chooseValue(binding.getByRole("combobox", { name: `${id}: ${tr("Output library")}`, exact: true }), "aster-shared-assets");
    await expect(binding.getByText(tr("Accepted main"), { exact: false })).toBeVisible();
    await binding.getByRole("group", { name: tr("Source selection"), exact: true }).getByRole("checkbox", { name: "source", exact: true }).check();
    const details = binding.locator("details").filter({ has: page.locator("summary", { hasText: tr("Workflow, inputs and execution limits") }) });
    await details.locator("summary").first().click();
    const input = binding.getByRole("group", { name: "corpus", exact: true });
    await input.getByRole("checkbox", { name: "source", exact: true }).check();
    await binding.getByLabel(`${tr("Binding budget")}: ${tr("Maximum tokens")}`, { exact: true }).fill("1000");
    await binding.getByLabel(`${tr("Binding budget")}: ${tr("Maximum cost (micros)")}`, { exact: true }).fill("10000");
  }
  await page.getByRole("button", { name: tr("Save configuration draft"), exact: true }).click();
  await expect(page.getByRole("button", { name: tr("Check original request"), exact: true })).toBeVisible();
  page.on("dialog", dialog => dialog.accept()); await page.reload();
  await page.getByRole("button", { name: tr("Check original request"), exact: true }).click();
  await expect(page.getByRole("button", { name: tr("Preview activation"), exact: true })).toBeEnabled();
  expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
  await page.getByRole("button", { name: tr("Preview activation"), exact: true }).click();
  await expect(page.getByRole("region", { name: tr("Activation review"), exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: tr("Activate reviewed configuration"), exact: true })).toBeDisabled();
  await page.getByRole("checkbox", { name: tr("I reviewed source scope, output ownership, budgets and evaluation changes."), exact: true }).check();
  let loseActivation = true;
  await page.route("**/optimization/activate", async route => {
    if (loseActivation) { loseActivation = false; const response = await route.fetch(); expect(response.status()).toBe(202); await response.dispose(); await route.abort("failed"); return; }
    await route.continue();
  });
  await page.getByRole("button", { name: tr("Activate reviewed configuration"), exact: true }).click();
  await expect(page.getByRole("button", { name: tr("Check original request"), exact: true })).toBeVisible();
  await page.reload(); await page.getByRole("button", { name: tr("Check original request"), exact: true }).click();

  await expect.poll(async () => (await (await page.request.get("/api/admin/groups/standard-users/optimization")).json()).state).toBe("active");
  await page.getByRole("button", { name: tr("Reload server status"), exact: true }).click();
  const lifecycle = page.locator("section.panel").filter({ has: page.getByRole("heading", { name: tr("Configuration lifecycle"), exact: true }) });
  await expect(lifecycle).toContainText("studio"); await expect(lifecycle).toContainText("evolution-space");
  const state = await (await page.request.get("/api/admin/groups/standard-users/optimization")).json(); expect(state.auto_enabled).toBe(false);
  const revision = await (await page.request.get(`/api/admin/groups/standard-users/optimization/revisions/${state.active_revision}`)).json();
  expect(revision.configuration.bindings).toHaveLength(2); expect(revision.configuration.bindings[0].inputs.corpus.sources).toEqual(["source"]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("configuration-active.png"), fullPage: true });
  // Defaults are durable authoring intent. An explicit Off override survives
  // group changes, and template reset uses each exact published template.
  const defaults = page.locator("section.panel").filter({ has: page.getByRole("heading", { name: tr("Group defaults"), exact: true }) });
  const skills = page.locator("article.evolution-binding").filter({ has: page.getByRole("heading", { name: "example-skills", exact: true }) });
  const corrections = page.locator("article.evolution-binding").filter({ has: page.getByRole("heading", { name: "example-corrections", exact: true }) });
  await chooseValue(defaults.getByRole("combobox", { name: tr("Evaluation mode"), exact: true }), "required");
  await chooseValue(corrections.getByRole("combobox", { name: tr("Evaluation mode"), exact: true }), "disabled");
  await page.getByRole("button", { name: tr("Save configuration draft"), exact: true }).click();
  await expect(page.getByText(tr("Request confirmed."), { exact: true })).toBeVisible();
  await page.evaluate(() => sessionStorage.clear()); await page.reload();
  await expect(defaults.getByRole("combobox", { name: tr("Default evolution service"), exact: true })).toContainText("native-service");
  await expect(skills.getByText(tr("Evaluation inherited from group"), { exact: true })).toBeVisible();
  await expect(corrections.getByText(tr("Explicit evaluation override"), { exact: true })).toBeVisible();
  const inherited = await (await page.request.get("/api/admin/groups/standard-users/optimization/draft")).json();
  expect(inherited.configuration.bindings.map((item: { evaluation: { mode: string } }) => item.evaluation.mode)).toEqual(["required", "disabled"]);
  await defaults.getByRole("checkbox", { name: tr("Use template evaluation defaults"), exact: true }).check();
  await page.getByRole("button", { name: tr("Save configuration draft"), exact: true }).click();
  await expect(page.getByText(tr("Request confirmed."), { exact: true })).toBeVisible();
  const reset = await (await page.request.get("/api/admin/groups/standard-users/optimization/draft")).json();
  expect(reset.settings.evaluation).toBeUndefined();
  expect(reset.configuration.bindings.map((item: { evaluation: { mode: string } }) => item.evaluation.mode)).toEqual(["disabled", "disabled"]);
  const pinned = await (await page.request.get(`/api/admin/groups/standard-users/optimization/revisions/${state.active_revision}`)).json();
  expect(pinned.configuration).toEqual(revision.configuration);
  // A concurrent saved revision must not overwrite local edits or silently win
  // a stale save. The user explicitly chooses the new server draft afterwards.
  const first = page.locator("article.evolution-binding").filter({ has: page.getByRole("heading", { name: "example-skills", exact: true }) });
  await first.getByLabel(tr("Owned directory"), { exact: true }).fill("skills-local");
  const draft = await (await page.request.get("/api/admin/groups/standard-users/optimization/draft")).json();
  draft.configuration.budget.max_tokens = 1900;
  const concurrent = await page.request.put("/api/admin/groups/standard-users/optimization/draft", { data: { request_id: crypto.randomUUID(), expected_revision: draft.revision, configuration: draft.configuration, settings: draft.settings } });
  expect(concurrent.status()).toBe(200);
  await page.getByRole("button", { name: tr("Save configuration draft"), exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: tr("The server revision changed. Local edits are retained; review the latest saved draft before replacing it.") })).toBeVisible();
  await expect(first.getByLabel(tr("Owned directory"), { exact: true })).toHaveValue("skills-local");
  await page.getByRole("button", { name: tr("Reload server status"), exact: true }).click();
  await expect(page.getByText(tr("A newer saved draft is available. Local edits have not been overwritten."), { exact: true })).toBeVisible();
  await page.getByRole("button", { name: tr("Use latest saved draft"), exact: true }).click();
  await expect(first.getByLabel(tr("Owned directory"), { exact: true })).toHaveValue("skills");
  await first.locator("summary").filter({ hasText: tr("Workflow, inputs and execution limits") }).click();
  await first.getByRole("textbox", { name: `example-skills: ${tr("Workflow parameters (JSON object)")}`, exact: true }).fill("{unfinished");
  // Local recovery must preserve incomplete parameter text without silently
  // submitting the last parseable object after reload.
  await expect.poll(async () => page.evaluate(() => Object.values(sessionStorage).some(value => String(value).includes("{unfinished")))).toBe(true);
  await page.reload();
  await first.locator("summary").filter({ hasText: tr("Workflow, inputs and execution limits") }).click();
  await expect(first.getByRole("textbox", { name: `example-skills: ${tr("Workflow parameters (JSON object)")}`, exact: true })).toHaveValue("{unfinished");
  const beforeInvalid = requests.length;
  await page.getByRole("button", { name: tr("Save configuration draft"), exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: tr("Enter a valid JSON object.") })).toBeVisible();
  expect(requests).toHaveLength(beforeInvalid);
  await page.getByRole("button", { name: tr("Use latest saved draft"), exact: true }).click();
  // Platform administrators can inspect and change scoped capabilities without
  // turning the target account into an administrator.
  const permissions = await (await page.request.get("/api/admin/groups/standard-users/optimization/permissions")).json();
  await page.goto("/admin/groups/standard-users/evolution-permissions");
  await chooseValue(page.getByRole("combobox", { name: tr("Account permissions"), exact: true }), permissions.account_id);
  const evidence = page.getByRole("row").filter({ hasText: tr("Read source evidence") });
  await evidence.getByRole("button", { name: tr("Deny"), exact: true }).click();
  await page.getByRole("button", { name: tr("Apply reviewed permission"), exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: tr("Permission change confirmed.") })).toBeVisible();
  await expect.poll(async () => (await (await page.request.get("/api/admin/groups/standard-users/optimization/permissions")).json()).can_read_evidence).toBe(false);
  await evidence.getByRole("button", { name: tr("Allow"), exact: true }).click();
  await page.getByRole("button", { name: tr("Apply reviewed permission"), exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: tr("Permission change confirmed.") })).toBeVisible();
  await expect.poll(async () => (await (await page.request.get("/api/admin/groups/standard-users/optimization/permissions")).json()).can_read_evidence).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await writeFile(process.env.ADMIN_EVOLUTION_RESULT!, JSON.stringify(state));
});

test("delegated group manager configures without global administrator access",async({page:admin,browser,baseURL},info)=>{
 const tr=(text:string)=>process.env.ADMIN_EVOLUTION_LOCALE?.startsWith("zh")?ZH[text]||text:text;
 await admin.context().addCookies([{name:"project_login",value:process.env.ADMIN_EVOLUTION_TOKEN!,url:baseURL!,secure:true,httpOnly:true,sameSite:"Lax"}]);
 const created=await admin.request.post("/api/admin/users",{headers:{Origin:baseURL!},data:{request_id:crypto.randomUUID(),username:`manager-${crypto.randomUUID()}`,display_name:"Delegated manager",role:"user",group_ids:["standard-users"]}});
 expect(created.status(),await created.text()).toBe(201);const user=await created.json();
 for(const action of ["config.read","config.manage"]){const response=await admin.request.put("/api/admin/groups/standard-users/optimization/grants",{headers:{Origin:baseURL!},data:{request_id:crypto.randomUUID(),account_id:user.user.id,action,effect:"allow",expected_revision:0}});expect(response.ok(),await response.text()).toBe(true)}
 const context=await browser.newContext({baseURL,ignoreHTTPSErrors:true,viewport:{width:info.project.use.viewport?.width??1440,height:1000},locale:process.env.ADMIN_EVOLUTION_LOCALE||"en-US"});
 try{
  const page=await context.newPage();
  expect((await page.request.post("/api/auth/login",{headers:{Origin:baseURL!},data:{username:user.user.username,password:user.temporary_password}})).ok()).toBe(true);
  expect((await page.request.post("/api/auth/password/setup",{headers:{Origin:baseURL!},data:{new_password:"permanent-manager-password"}})).ok()).toBe(true);
  expect((await page.request.get("/api/admin/groups/standard-users")).status()).toBe(403);
  expect((await page.request.get("/api/admin/users")).status()).toBe(403);
  await page.goto("/admin/groups/standard-users/optimization");
  const attempts=page.getByLabel(`${tr("Group budget ceiling")}: ${tr("Maximum attempts")}`,{exact:true});
  await expect(attempts).toBeEnabled();await attempts.fill("21");
  await expect(page.getByRole("button",{name:tr("Save configuration draft"),exact:true})).toBeEnabled();
  await expect(page.getByRole("link",{name:tr("Users"),exact:true})).toHaveCount(0);
  await page.getByRole("button",{name:tr("Save configuration draft"),exact:true}).click();
  await expect(page.getByText(tr("Request confirmed."),{exact:true})).toBeVisible();
  const audiences=await(await page.request.get("/api/admin/groups/standard-users/optimization/audiences?limit=1")).json();
  expect(audiences.items.map((item:{id:string})=>item.id)).toEqual(["standard-users"]);expect(audiences.has_more).toBe(false);
  await page.getByRole("link",{name:tr("Corpus and historical backfill"),exact:true}).click();
  await expect(page.getByRole("heading",{name:tr("Corpus and historical backfill"),exact:true})).toBeVisible();
  await expect(page.getByRole("button",{name:tr("Grant myself evidence access"),exact:true})).toHaveCount(0);
  await page.getByRole("link",{name:tr("Automatic optimization"),exact:true}).click();
  await expect(attempts).toBeEnabled();
  await page.screenshot({path:info.outputPath("delegated-manager.png"),fullPage:true});
  expect((await admin.request.put("/api/admin/groups/standard-users/optimization/grants",{headers:{Origin:baseURL!},data:{request_id:crypto.randomUUID(),account_id:user.user.id,action:"config.read",effect:"deny",expected_revision:1}})).ok()).toBe(true);
  await page.getByRole("button",{name:tr("Reload server status"),exact:true}).click();
  await expect(page.getByRole("button",{name:tr("Save configuration draft"),exact:true})).toHaveCount(0);
  expect((await page.request.get("/api/admin/groups/standard-users/optimization/audiences")).status()).toBe(403);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
 }finally{await context.close()}
});
