import { expect, test } from "@playwright/test";
import { settingsFixture, openSettings } from "./fixture";
import { chooseValue } from "./picker";
for (const width of [390, 1440]) test(`source selectors preserve explicit audiences and distinguish empty from unavailable at ${width}px`, async ({ page }) => {
 await settingsFixture(page); await page.setViewportSize({ width, height: 1000 });
 let saved: { source: { kind: string; resource_id: string; audience_groups: string[] } } | undefined;
 let unavailable = false;
 await page.route("**/api/admin/groups/standard-users/optimization**", async route => {
  const url = new URL(route.request().url()), path = url.pathname;
  if (path.endsWith("/audiences")) return route.fulfill({ json: { items: [{ id: "standard-users", name: "Standard users" }], has_more: false } });
  if (path.endsWith("/permissions")) return route.fulfill({ json: { account_id: "admin-id", can_manage: true, can_run: true, sources: [] } });
  if (path.endsWith("/draft") || path.endsWith("/optimization")) return route.fulfill({ status: 404, json: { error: "not configured" } });
  if (path.endsWith("/sources")) return route.fulfill({ json: { sources: [], has_more: false } });
  if (path.endsWith("/source-resources")) {
   if (unavailable) return route.fulfill({ status: 503, json: { error: "Resource catalog offline" } });
   const kind = url.searchParams.get("kind"), search = url.searchParams.get("search"), more = url.searchParams.get("after");
   if (search) return route.fulfill({ json: { items: [], has_more: false, next_after: "" } });
   return route.fulfill({ json: { items: [{ id: more ? "second" : "first", name: kind === "session" ? "Owned session" : more ? "Another owned Project" : "Owned Project", project_id: "project" }], has_more: kind !== "session" && !more, next_after: more ? "second" : "first" } });
  }
  if (path.endsWith("/sources/new-source")) { saved = route.request().postDataJSON(); return route.fulfill({ json: { ...saved!.source, policy_revision: 1 } }); }
  if (path.includes("/catalog/")) return route.fulfill({ json: { revisions: [], has_more: false, next_id: "", next_revision: 0 } });
  return route.fallback();
 });
 await openSettings(page, "settings/configuration/");
 await page.getByRole("button", { name: "Authorize a source I own", exact: true }).click();
 const form = page.getByRole("region", { name: "Source reuse policy", exact: true });
 await form.getByLabel("Source ID", { exact: true }).fill("new-source");
 await form.getByRole("button", { name: "More source resources", exact: true }).click();
 await chooseValue(form.getByRole("combobox", { name: "Source resource", exact: true }), "second");
 await chooseValue(form.getByRole("combobox", { name: "Source kind", exact: true }), "session");
 await expect(form.getByRole("combobox", { name: "Source resource", exact: true })).toContainText("Choose a source resource");
 await chooseValue(form.getByRole("combobox", { name: "Source resource", exact: true }), "first");
 await form.getByLabel("Search source resources", { exact: true }).fill("missing");
 await form.getByRole("button", { name: "Search resources", exact: true }).click();
 await expect(form.getByText("No eligible source resources match. Ownership is required even for administrators.", { exact: true })).toBeVisible();
 unavailable = true;
 await form.getByRole("button", { name: "Search resources", exact: true }).click();
 await expect(form.getByRole("alert")).toHaveText("Resource catalog offline");
 await expect(form.getByText("No eligible source resources match. Ownership is required even for administrators.", { exact: true })).toHaveCount(0);
 const audience = form.getByRole("group", { name: "Source audience groups", exact: true }).getByRole("checkbox");
 await audience.uncheck(); await audience.check();
 await form.getByRole("button", { name: "Save source policy", exact: true }).click();
 await expect(form).toHaveCount(0);
 expect(saved!.source).toMatchObject({ kind: "session", resource_id: "first", audience_groups: ["standard-users"] });
 expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
});
