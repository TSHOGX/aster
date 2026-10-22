import { expect, type Page } from "@playwright/test";
export const origin = "https://127.0.0.1:18543";
export async function settleOverview(page: Page) {
  // Complete automatic catalog reads before full-document fixture navigation.
  // WebKit reports interrupted TLS reads as native page errors during unload.
  const catalogs = page.locator(".catalog-editor select");
  for (let index = 0; index < await catalogs.count(); index++) await expect(catalogs.nth(index)).toBeEnabled();
}

export async function provision(admin: Page) {
  expect((await admin.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "evolution-space-admin", password: "browser-fixture-password" } })).ok()).toBeTruthy();
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
export async function login(page: Page, user: { user: { username: string }; temporary_password: string }) {
  // Editor suites drive the keyboard-accessible ordered node list. The graph
  // canvas has its own coverage and selects its view explicitly.
  await page.context().addInitScript(() => { try { if (!localStorage.getItem("evolution-space:canvas-view")) localStorage.setItem("evolution-space:canvas-view", "list"); } catch { /* storage disabled */ } });
  expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: user.user.username, password: user.temporary_password } })).ok()).toBeTruthy();
  expect((await page.request.post("/api/auth/password/setup", { headers: { Origin: origin }, data: { new_password: "permanent-viewer-fixture-password" } })).ok()).toBeTruthy();
}

export async function setPreferences(page: Page, change: Record<string, string>) {
  // Account preferences are edited in Workbench settings; Space only applies them.
  const current = await (await page.request.get("/api/me/preferences")).json() as { revision: number };
  const response = await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { ...change, expected_revision: current.revision } });
  expect(response.ok(), await response.text()).toBeTruthy();
}
export async function switchGroup(page: Page, name: string) {
  await page.getByRole("button", { name: /^(Switch group|切换团队)$/ }).click();
  await page.getByRole("dialog").getByRole("link", { name: new RegExp(`^${name}\\b`) }).click();
}

type Flags = { can_manage?: boolean; can_run?: boolean; can_edit_workflows?: boolean; can_read_evidence?: boolean };
// Settings pages call central group-configuration APIs, which these specs
// intercept. The Space session itself is real; only the group projection for
// the synthetic "standard-users" group and its central reads are supplied here.
export async function settingsFixture(page: Page, flags: Flags = {}) {
  const group = { id: "standard-users", name: "Standard users", description: "", can_manage: true, can_run: true, can_edit_workflows: true, can_read_evidence: true, desired_revision: 1, active_revision: 1, auto_enabled: false, state: "active", ...flags };
  await page.route("**/evolution-space/api/v1/groups/standard-users", route => route.fulfill({ json: { items: [group], next_after: "", has_more: false } }));
  await page.route("**/api/admin/groups/standard-users/optimization/permissions", route => route.fulfill({ json: { group_name: "Standard users", account_id: "admin-id", can_manage: group.can_manage, can_run: group.can_run, can_read_evidence: group.can_read_evidence, can_backfill: true, can_manage_grants: true, evidence_grant_revision: 0, sources: [] } }));
  await page.route("**/api/admin/users?*", route => route.fulfill({ json: { users: [{ id: "admin-id", username: "administrator", display_name: "administrator" }, { id: "user-id", username: "ordinary", display_name: "ordinary" }] } }));
  expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "evolution-space-admin", password: "browser-fixture-password" } })).ok()).toBeTruthy();
  return group;
}
export async function openSettings(page: Page, section: string) {
  await page.goto(`/evolution-space/auth/login?return=${encodeURIComponent(`/evolution-space/groups/standard-users/${section}`)}`);
}
