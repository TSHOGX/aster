import { expect, test } from "@playwright/test";
import { adminFixture } from "./fixture";

for (const variant of [{ width: 1440, locale: "en" }, { width: 390, locale: "zh" }]) {
  for (const kind of ["account", "group"]) test(`Studio ${kind} permissions recover exact requests and current authority ${variant.locale}`, async ({ page }) => {
    await adminFixture(page); await page.setViewportSize({ width: variant.width, height: 1000 });
    const tr = (en: string, zh: string) => variant.locale === "zh" ? zh : en;
    await page.route("**/api/me/preferences", route => route.fulfill({ json: { revision: 1, theme_mode: "system", accent: "neutral", language: variant.locale } }));
    const actions = ["volume.read", "volume.author", "volume.review", "volume.merge", "volume.publish", "volume.configure"];
    let grants = actions.map(action => ({ action, effect: "unset", revision: 0, ...(kind === "account" ? { allowed: false } : {}) }));
    let lose = true, conflict = false, denied = false;
    const requests: Record<string, unknown>[] = [], receipts = new Map<string, number>();
    const subject = kind === "account" ? "user-id" : "standard-users";
    await page.route("**/api/admin/studio-grants**", async route => {
      if (route.request().method() === "GET") {
        if (denied) return route.fulfill({ status: 403, json: { error: "Denied" } });
        const query = new URL(route.request().url()).searchParams;
        expect(query.get("kind")).toBe(kind); expect(query.get("subject_id")).toBe(subject);
        return route.fulfill({ json: { kind, subject_id: subject, library_id: query.get("library_id"), display_name: "Reviewer team", grants } });
      }
      const body = route.request().postDataJSON(); requests.push(body);
      expect(body.kind).toBe(kind); expect(body.subject_id).toBe(subject); expect(body.library_id).toBe("aster-shared-assets");
      if (receipts.has(body.request_id)) return route.fulfill({ json: { revision: receipts.get(body.request_id), replayed: true } });
      if (conflict) { conflict = false; grants = grants.map(grant => grant.action === body.action ? { ...grant, effect: "deny", revision: grant.revision + 1 } : grant); return route.fulfill({ status: 409, json: { error: "grant changed" } }); }
      grants = grants.map(grant => grant.action === body.action ? { ...grant, effect: body.effect, revision: grant.revision + 1, ...(kind === "account" ? { allowed: body.effect === "allow" } : {}) } : grant);
      receipts.set(body.request_id, grants.find(grant => grant.action === body.action)!.revision);
      if (lose) { lose = false; return route.abort("failed"); }
      return route.fulfill({ json: { revision: receipts.get(body.request_id) } });
    });
    await page.goto(`/admin/${kind === "account" ? "users" : "groups"}/${subject}/studio-permissions`);
    await page.getByLabel(tr("Studio Volume ID", "Studio 卷 ID"), { exact: true }).fill("aster-shared-assets");
    await page.getByRole("button", { name: tr("Refresh permissions", "刷新权限"), exact: true }).press("Enter");
    const row = page.getByRole("row").filter({ hasText: tr("Review integrations", "审阅集成") });
    await row.getByRole("button", { name: tr("Allow", "允许"), exact: true }).press("Enter");
    const apply = page.getByRole("button", { name: tr("Apply reviewed permission", "应用已审阅权限"), exact: true });
    await expect(page.getByRole("region", { name: tr("Review permission change", "审阅权限变更"), exact: true })).toContainText("aster-shared-assets");
    expect(requests).toHaveLength(0); await apply.press("Enter");
    const recover = page.getByRole("button", { name: tr("Check original request", "检查原请求结果"), exact: true });
    await expect(recover).toBeVisible(); page.on("dialog", dialog => dialog.accept()); await page.reload();
    await expect(page.getByLabel(tr("Studio Volume ID", "Studio 卷 ID"), { exact: true })).toBeDisabled();
    await recover.click();
    await expect(row.getByRole("button", { name: tr("Allow", "允许"), exact: true })).toBeDisabled();
    expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
    expect(new URL(page.url()).searchParams.get("library")).toBe("aster-shared-assets");
    const publish = page.getByRole("row").filter({ hasText: tr("Publish and manage versions", "发布和管理版本") });
    conflict = true;
    await publish.getByRole("button", { name: tr("Allow", "允许"), exact: true }).click(); await apply.click();
    await expect(page.getByRole("main").getByRole("alert")).toContainText(tr("The grant changed.", "授权已更改。"));
    await expect(page.getByRole("table")).toHaveCount(0);
    await page.getByRole("button", { name: tr("Refresh permissions", "刷新权限"), exact: true }).click();
    await expect(publish).toContainText(tr("Revision 1", "修订版本 1"));
    await publish.getByRole("button", { name: tr("Allow", "允许"), exact: true }).click(); await apply.click();
    await expect(publish.getByRole("button", { name: tr("Allow", "允许"), exact: true })).toBeDisabled();
    expect(requests.at(-1)!.expected_revision).toBe(1); expect(requests.at(-1)!.request_id).not.toBe(requests.at(-2)!.request_id);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    denied = true;
    await page.getByRole("button", { name: tr("Refresh permissions", "刷新权限"), exact: true }).click();
    await expect(page.getByRole("table")).toHaveCount(0);
    await expect(page.getByRole("main").getByRole("alert")).toBeVisible();
    // A request saved for a different subject must never be recovered here.
    const beforeInvalid = requests.length;
    await page.evaluate(({ key, command }) => sessionStorage.setItem(key, JSON.stringify(command)), { key: `studio-grants:admin-id:${kind}:${subject}`, command: { ...requests[0], subject_id: "another-account" } });
    await page.reload();
    await expect(page.getByRole("main").getByRole("alert")).toContainText(tr("Saved permission request is invalid.", "已保存的权限请求无效。"));
    await expect(page.getByRole("button", { name: tr("Check original request", "检查原请求结果"), exact: true })).toHaveCount(0);
    await page.getByRole("button", { name: tr("Clear invalid recovery data", "清除无效恢复数据"), exact: true }).click();
    await expect(page.getByLabel(tr("Studio Volume ID", "Studio 卷 ID"), { exact: true })).toBeEnabled();
    expect(requests).toHaveLength(beforeInvalid);
  });
}
