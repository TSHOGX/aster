import { expect, test } from "@playwright/test";
import { adminFixture } from "./fixture";
import { chooseValue } from "./picker";
const ref = { id: "example", revision: 1, digest: `sha256:${"a".repeat(64)}` };
const budget = { max_tokens: 100, max_cost_micros: 1000, max_attempts: 10, timeout_seconds: 120, max_artifact_bytes: 1048576 };
for (const width of [390, 1440]) test(`output selector explains unavailable libraries and reservation overlap at ${width}px`, async ({ page }) => {
  await adminFixture(page); await page.setViewportSize({ width, height: 1000 });
  const library = { id: "library", name: "Team library", profile: "shared-assets-v1", archived: false, owner_group: "standard-users", can_configure: true };
  let denyDetail = false;
  const configuration = { group_id: "standard-users", revision: 0, auto_enabled: false, budget, sources: [], bindings: [{ id: "skills", enabled: true, template: ref, workflow: ref, service: ref, inputs: {}, parameters: {}, budget, depends_on: [], source_ids: [], evaluation: { mode: "disabled", reference_strategy: "none" }, output: { library_id: "", prefix: "skills", profile: "shared-assets-v1", reservation_generation: 1 }, trigger: "manual" }] };
  await page.route("**/api/admin/groups/standard-users/optimization**", async route => {
    const url = new URL(route.request().url()), path = url.pathname;
    if (path.endsWith("/permissions")) return route.fulfill({ json: { account_id: "admin-id", can_manage: true, can_run: true, sources: [] } });
    if (path.endsWith("/draft")) return route.fulfill({ json: { revision: 1, configuration } });
    if (path.endsWith("/sources")) return route.fulfill({ json: { sources: [], has_more: false } });
    if (path.endsWith("/optimization")) return route.fulfill({ json: { group_id: "standard-users", desired_revision: 0, active_revision: 0, state: "draft", auto_enabled: false, operation_id: "" } });
    if (path.endsWith("/outputs")) return route.fulfill({ json: { items: [library, { ...library, id: "other", name: "Other group", owner_group: "other" }, { ...library, id: "profile", name: "Data library", profile: "authored-v1" }, { ...library, id: "readonly", name: "Read only", can_configure: false }, { ...library, id: "archived", name: "Archived", archived: true }], total: 5, next: "" } });
    if (path.endsWith("/outputs/library") && denyDetail) return route.fulfill({ status: 403, json: { error: "Output permission revoked" } });
    if (path.endsWith("/outputs/library")) return route.fulfill({ json: { library, main: { library_id: "library", profile: "shared-assets-v1", commit_id: "a".repeat(64), tree_digest: ref.digest }, published: { version_id: "release-one" }, reservations: [{ prefix: "skills", group_id: "standard-users", binding_id: "sibling", state: "active", generation: 1 }] } });
    if (path.includes("/catalog/")) {
      const kind = path.split("/catalog/")[1].split("/")[0];
      if (path.endsWith(`/catalog/${kind}`)) return route.fulfill({ json: { revisions: [ref], next_id: ref.id, next_revision: 1, has_more: false } });
      const definition = kind === "template" ? { ref, goal: "Fixture goal", profile: "shared-assets-v1", workflows: [ref], inputs: {}, outputs: {} } : kind === "service" ? { ref, kind: "evolution", enabled: true, capabilities: {} } : { id: "example", inputs: {}, outputs: {}, nodes: [], parameters_schema: {} };
      return route.fulfill({ json: { ref, definition, status: { id: "example", enabled: true, revision: 1 } } });
    }
    return route.fallback();
  });
  await page.goto("/admin/groups/standard-users/optimization");
  const picker = page.getByRole("combobox", { name: "skills: Output library", exact: true });
  await expect(picker).toBeEnabled(); await picker.click();
  await expect(page.getByRole("option", { name: /Other group.*Owned by another group/ })).toHaveAttribute("aria-disabled", "true");
  await expect(page.getByRole("option", { name: /Data library.*Incompatible library profile/ })).toHaveAttribute("aria-disabled", "true");
  await expect(page.getByRole("option", { name: /Read only.*permission required/ })).toHaveAttribute("aria-disabled", "true");
  await expect(page.getByRole("option", { name: /Archived.*Archived library/ })).toHaveAttribute("aria-disabled", "true");
  await page.keyboard.press("Escape"); await chooseValue(picker, "library");
  await expect(page.getByText("Published version: release-one", { exact: true })).toBeVisible();
  await expect(page.getByRole("alert").filter({ hasText: "This directory overlaps an existing reservation." })).toContainText("sibling");
  await page.getByLabel("Owned directory", { exact: true }).fill("available");
  await expect(page.getByRole("alert").filter({ hasText: "This directory overlaps an existing reservation." })).toHaveCount(0);
  denyDetail = true;
  await page.getByRole("button", { name: "Refresh output metadata", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Output permission revoked" })).toBeVisible();
  await expect(page.getByText("Published version: release-one", { exact: true })).toHaveCount(0);
  // A successful list refresh must never erase the independent detail denial.
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(picker).toBeEnabled();
  await expect(page.getByRole("alert").filter({ hasText: "Output permission revoked" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
});
