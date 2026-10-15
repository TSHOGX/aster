import { expect, test } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { ZH } from "../lib/i18n/messages";
import { chooseValue } from "./picker";
import type { Backfill } from "../lib/evolution";
test("real corpus backfill reviews frozen bytes and reconciles a lost preview across reload", async ({ page, context, baseURL }, info) => {
  const tr = (text: string) => process.env.ADMIN_EVOLUTION_LOCALE?.startsWith("zh") ? ZH[text] || text : text;
  await context.addCookies([{ name: "project_login", value: process.env.ADMIN_EVOLUTION_TOKEN!, url: baseURL!, httpOnly: true, secure: true, sameSite: "Lax" }]);
  let lose = true; const requests: unknown[] = [];
  await page.route("**/evolution/backfills/preview", async route => {
    requests.push(route.request().postDataJSON());
    if (lose) { lose = false; const response = await route.fetch(); expect(response.status()).toBe(200); await response.dispose(); await route.abort("failed"); return; }
    await route.continue();
  });
  await page.goto("/admin/groups/standard-users/corpus");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(tr("Corpus and historical backfill"));
  await page.getByRole("button", { name: tr("Grant myself evidence access"), exact: true }).click();
  await page.getByText(tr("Capture an execution or seal a snapshot"), { exact: true }).click();
  await chooseValue(page.getByRole("combobox", { name: tr("Source"), exact: true }), "source");
  await page.getByLabel(tr("Execution ID"), { exact: true }).fill("episode");
  await page.getByRole("button", { name: tr("Capture execution"), exact: true }).click();
  await expect(page.getByRole("status")).toContainText(tr("Capture state: complete"));
  await page.getByRole("button", { name: tr("Seal snapshot"), exact: true }).click();
  await expect(page.getByRole("link", { name: tr("Download snapshot"), exact: true })).toBeVisible();
  await chooseValue(page.getByRole("combobox", { name: tr("Binding frontier"), exact: true }), "example-skills");
  await page.getByRole("button", { name: tr("Inspect consumption frontier"), exact: true }).click();
  await expect(page.getByText(tr("No successful consumption"), { exact: false })).toBeVisible();
  await page.getByText(tr("Capture an execution or seal a snapshot"), { exact: true }).click();
  for (const id of ["example-skills", "example-corrections"]) {
    await page.getByRole("checkbox", { name: `${id} · ${id === "example-skills" ? "skills" : "lessons"}`, exact: true }).check();
    await page.getByLabel(`${id}: ${tr("Maximum tokens")}`, { exact: true }).fill("200");
    await page.getByLabel(`${id}: ${tr("Maximum cost (micros)")}`, { exact: true }).fill("2000");
  }
  await page.getByLabel(tr("Maximum episodes"), { exact: true }).fill("1");
  await page.getByRole("button", { name: tr("Preview historical backfill"), exact: true }).click();
  await expect(page.getByRole("button", { name: tr("Check original request"), exact: true })).toBeVisible();
  page.on("dialog", dialog => dialog.accept()); await page.reload();
  await page.getByRole("button", { name: tr("Check original request"), exact: true }).click();
  const review = page.getByRole("region", { name: tr("Backfill review"), exact: true });
  await expect(review).toBeVisible(); expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
  await expect(review).toContainText("episode");
  await expect(page.getByRole("button", { name: tr("Execute reviewed backfill"), exact: true })).toBeDisabled();
  const link = page.getByRole("link", { name: `${tr("Download frozen input")} · corpus`, exact: true }).first();
  const response = await page.request.get((await link.getAttribute("href"))!); expect(response.status()).toBe(200);
  const corpus = await response.json(); expect(corpus.episodes).toHaveLength(1); expect(corpus.episodes[0]).toMatchObject({ id: "episode", family_id: "family", family_kind: "local", truncated: false });
  expect(corpus.episodes[0].text).toContain("verified output");
  const download = page.waitForEvent("download"); await link.click(); expect((await download).suggestedFilename()).toBe("backfill.json");
  await page.getByRole("checkbox", { name: tr("I reviewed the selected evidence and spending ceilings."), exact: true }).check();
  await page.getByRole("button", { name: tr("Execute reviewed backfill"), exact: true }).click();
  await expect(review).toContainText(tr("queued"));
  const id = new URL(page.url()).searchParams.get("backfill")!;
  const status = await page.request.get(`/api/admin/groups/standard-users/evolution/backfills/${id}`);
  const preview = await status.json() as Backfill; expect(preview.state).toBe("queued"); expect(preview.maximum_tokens).toBe(400);
  await page.reload(); await expect(page.getByRole("region", { name: tr("Backfill review"), exact: true })).toContainText(tr("queued"));
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("backfill-review.png"), fullPage: true });
  await writeFile(process.env.ADMIN_EVOLUTION_RESULT!, JSON.stringify(preview));
});
