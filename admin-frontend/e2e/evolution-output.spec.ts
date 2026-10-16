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

for (const width of [390, 1440]) test(`existing binding adopts a compatible template without losing ownership at ${width}px`, async ({ page }, info) => {
  await adminFixture(page); await page.setViewportSize({ width, height: 1000 });
  const nextRef = { ...ref, revision: 2, digest: `sha256:${"b".repeat(64)}` };
  const incompatibleRef = { ...ref, revision: 3, digest: `sha256:${"c".repeat(64)}` };
  const off = { mode: "disabled", reference_strategy: "none" };
  const policy = { mode: "required", reference_strategy: "generation_base", profile: ref, service: ref };
  const template = (revision: number) => ({ ref: revision === 1 ? ref : revision === 2 ? nextRef : incompatibleRef, goal: `Goal revision ${revision}`, profile: revision === 3 ? "authored-v1" : "shared-assets-v1", workflows: [revision === 1 ? ref : nextRef], inputs: { corpus: ref.digest }, outputs: { patch: ref.digest }, parameters_schema: { type: "object" }, evaluation: revision === 1 ? off : policy });
  const binding = { id: "skills", enabled: true, template: ref, workflow: ref, service: ref, inputs: { corpus: { sources: ["source"] } }, parameters: { limit: 7 }, budget, depends_on: [], source_ids: ["source"], evaluation: off, output: { library_id: "library", prefix: "skills", profile: "shared-assets-v1", reservation_generation: 4 }, trigger: "manual" };
  const configuration = { group_id: "standard-users", revision: 0, auto_enabled: false, budget, sources: [{ id: "source", kind: "project", resource_id: "project", policy_revision: 1, audience_groups: ["standard-users"], derivative_publication: true, exclusions: [] }], bindings: [binding] };
  const settings = { service: ref, bindings: { skills: { service: false, evaluation: false } } };
  let saved: { configuration: typeof configuration; settings: typeof settings } | undefined;
  let reservationState = "active", denyTemplate = false;
  await page.route("**/api/admin/groups/standard-users/optimization**", async route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path.endsWith("/permissions")) return route.fulfill({ json: { account_id: "admin-id", can_manage: true, can_run: true, sources: [] } });
    if (path.endsWith("/draft")) {
      if (request.method() === "PUT") { saved = request.postDataJSON(); return route.fulfill({ json: { revision: 2, ...saved } }); }
      return route.fulfill({ json: { revision: 1, configuration, settings } });
    }
    if (path.endsWith("/sources")) return route.fulfill({ json: { sources: configuration.sources, has_more: false } });
    if (path.includes("/revisions/")) return route.fulfill({ json: { configuration: { ...configuration, revision: 1 } } });
    if (path.endsWith("/optimization")) return route.fulfill({ json: { group_id: "standard-users", desired_revision: 1, active_revision: 1, state: "active", auto_enabled: false, operation_id: "" } });
    const library = { id: "library", name: "Team output", profile: "shared-assets-v1", archived: false, owner_group: "standard-users", can_configure: true };
    if (path.endsWith("/outputs")) return route.fulfill({ json: { items: [library], next: "", total: 1 } });
    if (path.endsWith("/outputs/library")) return route.fulfill({ json: { library, main: { commit_id: "a".repeat(64), tree_digest: ref.digest }, reservations: [{ prefix: "skills", group_id: "standard-users", binding_id: "skills", state: reservationState, generation: 4 }] } });
    if (path.includes("/catalog/")) {
      const [kind, , revision] = path.split("/catalog/")[1].split("/");
      if (!revision) return route.fulfill({ json: { revisions: kind === "template" ? [ref, nextRef, incompatibleRef] : [ref, nextRef], has_more: false, next_id: ref.id, next_revision: 2 } });
      if (kind === "template" && denyTemplate) return route.fulfill({ status: 403, json: { error: "Template access denied" } });
      const selected = Number(revision) === 1 ? ref : Number(revision) === 2 ? nextRef : incompatibleRef;
      const definition = kind === "template" ? template(Number(revision)) : kind === "workflow" ? { id: ref.id, inputs: { corpus: ref.digest }, outputs: { patch: ref.digest }, nodes: [], parameters_schema: {} } : { ref: selected, kind: "evolution", enabled: true, capabilities: {} };
      return route.fulfill({ json: { ref: selected, definition, status: { id: ref.id, enabled: true, revision: 1 } } });
    }
    return route.fallback();
  });
  await page.goto("/admin/groups/standard-users/optimization");
  const card = page.locator("article.evolution-binding");
  const picker = card.getByRole("combobox", { name: "Binding template revision", exact: true });
  await chooseValue(picker, { label: "example · 2" });
  await expect(card.getByText("Goal revision 2", { exact: true })).toBeVisible();
  await expect(card.getByRole("combobox", { name: "Evaluation mode", exact: true })).toContainText("Off");
  await card.locator("summary", { hasText: "Workflow, inputs and execution limits" }).click();
  await expect(card.getByRole("combobox", { name: "Workflow revision", exact: true })).toContainText("example · 2");
  await expect(card.getByLabel("skills: Workflow parameters (JSON object)")).toHaveValue(/"limit": 7/);
  await expect(card.getByRole("group", { name: "corpus", exact: true }).getByRole("checkbox", { name: "source", exact: true })).toBeChecked();
  await card.getByRole("button", { name: "Use next reservation generation", exact: true }).click();
  await expect(card.getByLabel("Reservation generation", { exact: true })).toHaveValue("5");
  reservationState = "released";
  await card.getByRole("button", { name: "Refresh output metadata", exact: true }).click();
  await expect(card.getByText("Existing reservation generation: 4 · released", { exact: false })).toBeVisible();
  await card.getByLabel("Reservation generation", { exact: true }).fill("4");
  await card.getByRole("button", { name: "Use next reservation generation", exact: true }).click();
  await expect(card.getByLabel("Reservation generation", { exact: true })).toHaveValue("5");
  await page.getByRole("button", { name: "Save configuration draft", exact: true }).click();
  await expect.poll(() => saved?.configuration.bindings[0].template).toEqual(nextRef);
  expect(saved?.configuration.bindings[0]).toEqual({ ...binding, template: nextRef, workflow: nextRef, output: { ...binding.output, reservation_generation: 5 } });
  await card.getByRole("button", { name: "Reset evaluation to group default", exact: true }).click();
  await expect(card.getByRole("combobox", { name: "Evaluation mode", exact: true })).toContainText("Required for merge");
  await page.getByRole("button", { name: "Save configuration draft", exact: true }).click();
  // Central resolves inherited policy from exact catalog definitions; this
  // intercepted boundary verifies the submitted inheritance intent.
  await expect.poll(() => saved?.settings.bindings.skills.evaluation).toBe(true);
  await chooseValue(picker, { label: "example · 3" });
  await expect(card.getByRole("alert").filter({ hasText: "different output profile" })).toBeVisible();
  await expect(card.getByRole("combobox", { name: "skills: Output library", exact: true })).toContainText("Team output");
  await chooseValue(picker, { label: "example · 2" });
  await expect(card.getByText("Goal revision 2", { exact: true })).toBeVisible();
  const catalog = picker.locator("xpath=ancestor::div[contains(@class, 'definition-picker')]");
  denyTemplate = true;
  await chooseValue(picker, { label: "example · 1" });
  await expect(catalog.getByRole("alert").filter({ hasText: "Template access denied" })).toBeVisible();
  await expect(picker).toContainText("example · 2");
  await catalog.getByRole("button", { name: "Refresh catalog", exact: true }).click();
  await expect(catalog.getByRole("alert").filter({ hasText: "Template access denied" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("binding-template-upgrade.png"), fullPage: true });
});
