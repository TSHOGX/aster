import { expect, test, type Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";
const key="project-view:researcher:expanded";
async function savedExpansion(page:Page, ids:string[]) {
  await page.addInitScript(({key,ids})=>{
    if(window!==window.top || localStorage.getItem("restoration-fixture"))return;
    localStorage.setItem("restoration-fixture","1");
    localStorage.setItem(key,JSON.stringify(ids));
  },{key,ids});
}

test("reload repairs a removed expanded Project instead of repeating a global 404 banner",async({page})=>{
  await workspaceFixture(page,{projectState:"paused"});
  await savedExpansion(page,["battery","removed-after-reset"]);
  let missingReads=0;
  await page.route("**/api/projects/removed-after-reset/**",route=>{
    missingReads++;
    return route.fulfill({status:404,json:{error:"not found"}});
  });
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".agent-panel")).toContainText("Compare the cycle performance");
  await expect.poll(()=>missingReads).toBeGreaterThan(0);
  await expect.poll(()=>page.evaluate(key=>JSON.parse(localStorage.getItem(key)||"[]"),key)).toEqual(["battery"]);
  await expect(page.locator(".project-main > .notice.error")).toHaveCount(0);
  const before=missingReads;
  await page.reload();
  await expect(page.locator(".agent-panel")).toContainText("Compare the cycle performance");
  await expect(page.getByRole("button",{name:"Battery research",exact:true})).toHaveAttribute("aria-expanded","true");
  expect(missingReads).toBe(before);
  await expect(page.locator(".project-main > .notice.error")).toHaveCount(0);
});

test("a temporary failure stays visible and does not discard saved expansion",async({page})=>{
  await workspaceFixture(page);
  await savedExpansion(page,["battery","temporarily-offline"]);
  await page.route("**/api/projects/temporarily-offline/**",route=>route.fulfill({status:503,json:{error:"temporarily unavailable"}}));
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".project-main > .notice.error")).toContainText("temporarily unavailable");
  expect(await page.evaluate(key=>JSON.parse(localStorage.getItem(key)||"[]"),key)).toContain("temporarily-offline");
});

test("a valid expanded Project outside the first index page is not forgotten",async({page})=>{
  await workspaceFixture(page);
  await savedExpansion(page,["battery","notes"]);
  await page.route("**/api/projects?*",route=>route.fulfill({json:{projects:[{id:"battery",title:"Battery research",state:"active",revision:1,activity_revision:1},...Array.from({length:99},(_,n)=>({id:`p-${n}`,title:`Project ${n}`,state:"paused",revision:1,activity_revision:1}))]}}));
  let notesReads=0;
  await page.route("**/api/projects/notes/sessions?*",route=>{notesReads++;return route.fulfill({json:{sessions:[]}});});
  await page.goto("/?project=battery&session=analysis");
  await expect.poll(()=>notesReads).toBeGreaterThan(0);
  await expect(page.locator(".agent-panel")).toBeVisible();
  expect(await page.evaluate(key=>JSON.parse(localStorage.getItem(key)||"[]"),key)).toContain("notes");
  await expect(page.locator(".project-main > .notice.error")).toHaveCount(0);
});

test("a missing Project URL returns to the workbench without a global error",async({page})=>{
  await workspaceFixture(page);
  await savedExpansion(page,["missing-target"]);
  await page.route(/\/api\/projects\/missing-target(?:[/?]|$)/,route=>route.fulfill({status:404,json:{error:"not found"}}));
  await page.goto("/?project=missing-target");
  await expect(page).toHaveURL("http://127.0.0.1:3107/");
  await expect(page.getByRole("heading",{name:"What would you like to work on?"})).toBeVisible();
  await expect(page.locator(".project-main > .notice.error")).toHaveCount(0);
  expect(await page.evaluate(key=>JSON.parse(localStorage.getItem(key)||"[]"),key)).not.toContain("missing-target");
});

test("a granted conversation can use the shared fallback without a false global 404",async({page})=>{
  await workspaceFixture(page);
  await page.route(/\/api\/projects\/foreign(?:[/?]|$)/,route=>route.fulfill({status:404,json:{error:"not found"}}));
  await page.route("**/api/conversations",route=>route.fulfill({json:{conversations:[{project_id:"foreign",session_id:"granted",title:"Shared result",revision:1}]}}));
  await page.route("**/api/conversations/granted/messages?*",route=>route.fulfill({json:{messages:[{id:"shared-message",role:"assistant",content:"Shared answer",sequence:1,created_at:"2026-09-24T00:00:00Z"}]}}));
  await page.route("**/api/conversations/granted/head",route=>route.fulfill({json:{}}));
  await page.goto("/?project=foreign&session=granted");
  await expect(page).toHaveURL(/view=shared&session=granted/);
  await expect(page.getByText("Shared answer",{exact:true})).toBeVisible();
  await expect(page.locator(".project-main > .notice.error")).toHaveCount(0);
});
