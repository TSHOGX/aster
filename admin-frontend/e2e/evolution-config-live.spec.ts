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
  await sourcePolicy.getByLabel(tr("Excluded selectors (one per line)"), { exact: true }).fill("executions/unrelated");
  await sourcePolicy.getByRole("button", { name: tr("Save source policy"), exact: true }).click();
  await expect(sourcePolicy).toHaveCount(0);
  await sourceSection.getByRole("checkbox").check();
  for (const id of ["example-skills", "example-corrections"]) {
    await chooseValue(page.getByRole("combobox", { name: tr("Goal template"), exact: true }), { label: `${id} · 1` });
    await page.getByRole("button", { name: tr("Add template binding"), exact: true }).click();
    const binding = page.locator("article.evolution-binding").filter({ has: page.getByRole("heading", { name: id, exact: true }) });
    await binding.getByLabel(tr("Output library ID"), { exact: true }).fill("aster-shared-assets");
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
  await expect(lifecycle).toContainText("studio"); await expect(lifecycle).toContainText("evaluation-space");
  const state = await (await page.request.get("/api/admin/groups/standard-users/optimization")).json(); expect(state.auto_enabled).toBe(false);
  const revision = await (await page.request.get(`/api/admin/groups/standard-users/optimization/revisions/${state.active_revision}`)).json();
  expect(revision.configuration.bindings).toHaveLength(2); expect(revision.configuration.bindings[0].inputs.corpus.sources).toEqual(["source"]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("configuration-active.png"), fullPage: true });
  // A concurrent saved revision must not overwrite local edits or silently win
  // a stale save. The user explicitly chooses the new server draft afterwards.
  const first = page.locator("article.evolution-binding").filter({ has: page.getByRole("heading", { name: "example-skills", exact: true }) });
  await first.getByLabel(tr("Owned directory"), { exact: true }).fill("skills-local");
  const draft = await (await page.request.get("/api/admin/groups/standard-users/optimization/draft")).json();
  draft.configuration.budget.max_tokens = 1900;
  const concurrent = await page.request.put("/api/admin/groups/standard-users/optimization/draft", { data: { request_id: crypto.randomUUID(), expected_revision: draft.revision, configuration: draft.configuration } });
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
  await writeFile(process.env.ADMIN_EVOLUTION_RESULT!, JSON.stringify(state));
});
