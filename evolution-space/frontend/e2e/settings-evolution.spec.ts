import { expect, test } from "@playwright/test";
import { settingsFixture, openSettings } from "./fixture";
import type { Backfill, Configuration, Permissions } from "../lib/evolution";
const budget = { max_tokens: 1000, max_cost_micros: 10000, max_attempts: 10, timeout_seconds: 120, max_artifact_bytes: 1048576 };
const ref = { id: "example", revision: 1, digest: `sha256:${"a".repeat(64)}` };
const config: Configuration = { group_id: "standard-users", revision: 1, auto_enabled: false, budget, sources: [{ id: "team-source", kind: "project", resource_id: "project", policy_revision: 1, audience_groups: ["standard-users"], derivative_publication: true, exclusions: ["turns/private"] }], bindings: [{ id: "skills", enabled: true, template: ref, workflow: ref, service: ref, budget, output: { library_id: "library", prefix: "skills", profile: "authored-v1" }, source_ids: ["team-source"], trigger: "manual", depends_on: [], inputs: { corpus: { sources: ["team-source"] } } }] };
async function fixture(page: import("@playwright/test").Page) {
  await settingsFixture(page);
  const permissions: Permissions = { group_name: "Standard users", sources: [{ source_id: "team-source", valid: true }], account_id: "admin-id", can_manage: true, can_run: true, can_read_evidence: false, can_backfill: false, can_manage_grants: true, evidence_grant_revision: 0 };
  const control = { loseReply: true, changed: false, statusDenied: false, revoked: false };
  const requests: Record<string, unknown>[] = [], receipts = new Map<string, Backfill>();
  let preview: Backfill | null = null;
  await page.route("**/api/admin/groups/standard-users/**", async route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path.endsWith("/optimization/permissions")) return route.fulfill({ json: control.revoked ? { ...permissions, can_read_evidence: false, can_backfill: false } : permissions });
    if (path.endsWith("/optimization")) return route.fulfill({ json: { group_id: "standard-users", active_revision: control.changed ? 2 : 1, desired_revision: control.changed ? 2 : 1, state: "active", auto_enabled: false } });
    if (path.includes("/optimization/revisions/")) return route.fulfill({ json: { configuration: { ...config, revision: control.changed ? 2 : 1 } } });
    if (path.endsWith("/optimization/grants")) { expect(request.method()).toBe("PUT"); Object.assign(permissions, { can_read_evidence: true, can_backfill: true, evidence_grant_revision: 1 }); return route.fulfill({ json: { revision: 1 } }); }
    if (path.endsWith("/corpus/collectors")) return route.fulfill({ json: [] });
    if (path.endsWith("/evolution/triggers")) return control.statusDenied ? route.fulfill({ status: 403, json: { error: "Work is unavailable" } }) : route.fulfill({ json: { items: [], next: 0 } });
    if (path.endsWith("/backfills/preview")) {
      const body = request.postDataJSON(); requests.push(body);
      if (receipts.has(body.request_id)) return route.fulfill({ json: receipts.get(body.request_id) });
      preview = { download_inputs: { skills: ["corpus"] }, id: "backfill-one", requested_by: "admin-id", digest: ref.digest, configuration_revision: 1, from: body.from, until: body.until, budgets: body.budgets, maximum_tokens: 1000, maximum_cost_micros: 10000, runs: { skills: "run" }, state: "preview", last_error: "", episodes: [{ source_id: "team-source", execution_id: "execution", started_at: body.from, family_id: "family", family_kind: "local", digest: ref.digest, capture_state: "partial", outcome: "completed", truncated: true }] };
      receipts.set(body.request_id, preview);
      if (control.loseReply) { control.loseReply = false; return route.abort("failed"); }
      return route.fulfill({ json: preview });
    }
    if (path.endsWith("/execute")) { if (control.changed) return route.fulfill({ status: 409, json: { error: "changed" } }); preview = { ...preview!, state: "queued" }; return route.fulfill({ json: preview }); }
    if (path.endsWith("/cancel")) { preview = { ...preview!, state: "cancelled" }; return route.fulfill({ json: preview }); }
    if (path.includes("/inputs/")) return route.fulfill({ contentType: "application/octet-stream", headers: { "Content-Disposition": "attachment; filename=backfill.json" }, body: "frozen evidence" });
    if (path.endsWith("/backfill-one/status")) return route.fulfill({ json: { id: preview!.id, requested_by: preview!.requested_by, digest: preview!.digest, configuration_revision: 1, state: preview!.state } });
    if (path.endsWith("/backfill-one")) return control.revoked ? route.fulfill({ status: 403, json: {error:"denied"} }) : route.fulfill({ json: preview });
    return route.fallback();
  });
  return { control, requests };
}
for (const width of [390, 1440]) test(`backfill retains an uncertain request and fits ${width}px`, async ({ page }, info) => {
  const { requests } = await fixture(page); await page.setViewportSize({ width, height: 1000 });
  await openSettings(page, "settings/sources/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Sources and corpus");
  await expect(page.getByRole("button", { name: "Preview historical backfill", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Grant myself evidence access", exact: true }).click();
  await page.getByRole("checkbox", { name: "skills · skills", exact: true }).check();
  await page.getByRole("button", { name: "Preview historical backfill", exact: true }).click();
  await expect(page.getByRole("button", { name: "Check original request", exact: true })).toBeVisible();
  page.on("dialog", dialog => dialog.accept()); await page.reload();
  await page.getByRole("button", { name: "Check original request", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Frozen selection", exact: true })).toBeVisible();
  expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
  await expect(page.getByText("Partial evidence", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Execute reviewed backfill" })).toBeDisabled();
  await expect(page.getByRole("link", { name: "Download frozen input · corpus" })).toHaveAttribute("href", "/api/admin/groups/standard-users/evolution/backfills/backfill-one/inputs/skills/corpus");
  await page.getByRole("checkbox", { name: "I reviewed the selected evidence and spending ceilings." }).check();
  await page.getByRole("button", { name: "Execute reviewed backfill" }).click();
  await expect(page.getByRole("region", { name: "Backfill review" })).toContainText("queued");
  await page.reload(); await expect(page.getByRole("region", { name: "Backfill review" })).toContainText("queued");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath(`backfill-${width}.png`), fullPage: true });
});
test("stale preview cannot execute and denied work is not an empty success", async ({ page }) => {
  const { control } = await fixture(page); control.loseReply = false;
  await openSettings(page, "settings/sources/"); await page.getByRole("button", { name: "Grant myself evidence access" }).click();
  await page.getByRole("checkbox", { name: "skills · skills", exact: true }).check(); await page.getByRole("button", { name: "Preview historical backfill", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Frozen selection" })).toBeVisible(); control.changed = true;
  await page.getByRole("button", { name: "Reload current data" }).click();
  await page.getByRole("checkbox", { name: "I reviewed the selected evidence and spending ceilings." }).check();
  await expect(page.getByRole("button", { name: "Execute reviewed backfill" })).toBeDisabled();
  control.statusDenied = true; await page.getByRole("button", { name: "Refresh work" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Work is unavailable" })).toBeVisible();
  await expect(page.getByText("No work on this page.", { exact: true })).toHaveCount(0);
});

test("revoked evidence stays hidden after reload while owner cleanup remains available", async ({ page }) => {
  const { control } = await fixture(page); control.loseReply = false;
  await openSettings(page, "settings/sources/");
  await page.getByRole("button", { name: "Grant myself evidence access" }).click();
  await page.getByRole("checkbox", { name: "skills · skills", exact: true }).check();
  await page.getByRole("button", { name: "Preview historical backfill", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Frozen selection" })).toBeVisible();
  control.revoked = true; await page.reload();
  await expect(page.getByRole("button", { name: "Cancel backfill", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Frozen selection" })).toHaveCount(0);
  await expect(page.getByText("execution", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Cancel backfill", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Backfill cancelled.");
});

for (const width of [390,1440]) test(`delegated group configuration saves drafts and clears on denial at ${width}px`,async({page})=>{
 await settingsFixture(page,{can_run:false,can_read_evidence:false});await page.setViewportSize({width,height:1000});
 let denied=false,revision=0;
 const configuration={group_id:"standard-users",revision:0,auto_enabled:false,budget,sources:[],bindings:[]};
 await page.route("**/api/admin/groups/standard-users/optimization**",route=>{
  const path=new URL(route.request().url()).pathname;
  if(denied)return route.fulfill({status:403,json:{error:"Group configuration access denied"}});
  if(path.endsWith("/permissions"))return route.fulfill({json:{group_name:"Delegated team",account_id:"admin-id",can_manage:true,can_run:false,can_read_evidence:false,can_backfill:false,can_manage_grants:false,sources:[]}});
  if(path.endsWith("/sources"))return route.fulfill({json:{sources:[],has_more:false}});
  if(path.endsWith("/draft")){
   if(route.request().method()==="PUT"){revision++;return route.fulfill({json:{revision,configuration:route.request().postDataJSON().configuration}})}
   return revision?route.fulfill({json:{revision,configuration}}):route.fulfill({status:404,json:{error:"not configured"}});
  }
  if(path.includes("/catalog/"))return route.fulfill({json:{revisions:[],has_more:false}});
  return route.fulfill({status:404,json:{error:"not configured"}});
 });
 await openSettings(page,"settings/configuration/");
 await expect(page.getByRole("heading",{name:"Configuration and activation",exact:true,level:1})).toBeVisible();
 // Group settings live in the group's sidebar, not in global administration.
 await expect(page.getByRole("navigation",{name:"Group sections",exact:true}).getByRole("link",{name:"Configuration and activation",exact:true})).toHaveAttribute("aria-current","page");
 await expect(page.getByRole("link",{name:"Users",exact:true})).toHaveCount(0);
 await page.getByRole("button",{name:"Save configuration draft",exact:true}).click();
 await expect(page.getByText("Request confirmed.",{exact:true})).toBeVisible();
 await page.reload();await expect(page.getByRole("button",{name:"Save configuration draft",exact:true})).toBeVisible();
 denied=true;await page.getByRole("button",{name:"Reload server status",exact:true}).click();
 await expect(page.getByRole("alert").filter({hasText:"Group configuration access denied"})).toBeVisible();
 await expect(page.getByRole("button",{name:"Save configuration draft",exact:true})).toHaveCount(0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
});


test("manual run recovery blocks malformed storage and clears denied evidence",async({page})=>{
 await settingsFixture(page);let denied=false,calls=0;
 await page.route("**/api/admin/groups/standard-users/optimization**",route=>{
  if(denied)return route.fulfill({status:403,json:{error:"Group access revoked"}});
  const path=new URL(route.request().url()).pathname;
  if(path.endsWith("/permissions"))return route.fulfill({json:{group_name:"Operator team",account_id:"admin-id",can_run:true,can_read_evidence:true,sources:[]}});
  if(path.includes("/revisions/"))return route.fulfill({json:{configuration:config}});
  return route.fulfill({json:{group_id:"standard-users",state:"active",active_revision:1,auto_enabled:false}});
 });
 await page.route("**/evolution/manual",route=>{calls++;return route.fulfill({status:503,json:{error:"uncertain"}})});
 await page.addInitScript(()=>{sessionStorage.setItem("evolution-manual:admin-id:standard-users","{malformed")});
 await openSettings(page,"runs/new/");
 await expect(page.getByRole("button",{name:"Start manual batch",exact:true})).toBeDisabled();
 await expect(page.getByRole("alert").filter({hasText:"Saved request is invalid."})).toBeVisible();
 page.on("dialog",dialog=>dialog.accept());
 await page.getByRole("button",{name:"Discard recovery",exact:true}).click();
 await page.getByRole("checkbox",{name:/^skills ·/}).check();
 await page.getByRole("checkbox",{name:"I reviewed the selected workflows, input scope and spending ceilings.",exact:true}).check();
 await page.evaluate(()=>{const original=Storage.prototype.setItem;Storage.prototype.setItem=function(key,value){if(key.startsWith("evolution-manual:"))throw new Error("storage unavailable");return original.call(this,key,value)}});
 await page.getByRole("button",{name:"Start manual batch",exact:true}).click();
 await expect(page.getByRole("alert").filter({hasText:"storage unavailable"})).toBeVisible();expect(calls).toBe(0);
 denied=true;await page.evaluate(()=>window.dispatchEvent(new Event("focus")));
 await expect(page.getByRole("alert").filter({hasText:"Group access revoked"})).toBeVisible();
 await expect(page.getByRole("heading",{name:"New run",exact:true,level:1})).toBeVisible();
 await expect(page.getByRole("button",{name:"Start manual batch",exact:true})).toHaveCount(0);
});
