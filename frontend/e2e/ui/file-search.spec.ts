import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";
const file = (name: string) => ({name, kind:"file", size:42, modified:0});
const directory = (name: string) => ({...file(name), kind:"directory"});

for (const state of ["active","paused"]) {
  test(`extension search covers collapsed folders and later pages while ${state}`, async({page})=>{
    const fixture=await workspaceFixture(page,{projectState:state});
    fixture.files["reports/nested/chart.html"]="<h1>Found in an unopened folder</h1>";
    await page.route("**/api/projects/battery/files?**",route=>{
      const url=new URL(route.request().url()), path=url.searchParams.get("path"), after=url.searchParams.get("after");
      const listing=path==="reports/nested" ? {entries:[file("chart.html"),file("result.XLSX")]} : path==="reports" ? {entries:[directory("nested")]} : after ? {entries:[file("later.xlsx")]} : {entries:[directory("reports"),file("README.md")],next:"second"};
      return route.fulfill({json:listing});
    });
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("button",{name:"File preview",exact:true}).click();
    const reports=page.getByRole("button",{name:"reports",exact:true});
    await expect(reports).toHaveAttribute("aria-expanded","false");
    const input=page.getByRole("searchbox",{name:"Search files"});
    await input.fill(" XLSX ");
    const results=page.getByRole("region",{name:"File search results"});
    await expect(results.locator(".file-search-row")).toHaveCount(2);
    await expect(results.locator('[data-file-path="reports/nested/result.XLSX"]')).toBeVisible();
    await expect(results.locator('[data-file-path="later.xlsx"]')).toBeVisible();
    await input.fill("html");
    await expect(results.locator(".file-search-row")).toHaveCount(1);
    await results.locator('[data-file-path="reports/nested/chart.html"] .file-name').click();
    await expect(page.frameLocator(".html-preview").getByRole("heading")).toHaveText("Found in an unopened folder");
    await input.fill("nothing-matches");
    await expect(results).toContainText("No matching files.");
    await input.press("Escape");
    await expect(input).toHaveValue("");
    await expect(page.frameLocator(".html-preview").getByRole("heading")).toBeVisible();
    expect(fixture.calls.filter(c=>c.path.endsWith("/resume") || c.path.endsWith("/start"))).toEqual([]);
  });
}

test("search results stay bounded and do not depend on the tree's current page",async({page})=>{
  await workspaceFixture(page);
  await page.route("**/api/projects/battery/files?**",route=>{
    const url=new URL(route.request().url()), start=Number(url.searchParams.get("after") || 0);
    return route.fulfill({json:{entries:Array.from({length:Math.min(100,230-start)},(_,i)=>file(`result-${String(start+i).padStart(3,"0")}.xlsx`)),next:start+100<230?String(start+100):undefined}});
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button",{name:"File preview",exact:true}).click();
  // Let the initial watch attachment validation finish before moving pages.
  await page.waitForTimeout(300);
  await page.locator(".file-tree").getByRole("button",{name:"Next page",exact:true}).click();
  await expect(page.locator('[data-file-path="result-100.xlsx"]')).toBeVisible();
  const input=page.getByRole("searchbox",{name:"Search files"});
  await input.fill("xlsx");
  const results=page.getByRole("region",{name:"File search results"});
  await expect(results.locator(".file-search-row")).toHaveCount(100);
  await expect(results.locator('[data-file-path="result-000.xlsx"]')).toBeVisible();
  await results.getByRole("button",{name:"Next results",exact:true}).click();
  await expect(results.locator('[data-file-path="result-100.xlsx"]')).toBeVisible();
  await expect(results.locator(".file-search-row")).toHaveCount(100);
  await results.getByRole("button",{name:"Next results",exact:true}).click();
  await expect(results.locator(".file-search-row")).toHaveCount(30);
  await expect(results).toContainText("Search complete.");
  await results.getByRole("button",{name:"First results",exact:true}).click();
  await expect(results.locator('[data-file-path="result-000.xlsx"]')).toBeVisible();
  await input.fill("");
  await expect(page.locator('[data-file-path="result-100.xlsx"]')).toBeVisible();
  await expect(page.locator('[data-file-path="result-000.xlsx"]')).toHaveCount(0);
});

test("changing a query aborts the prior traversal and ignores its late result",async({page})=>{
  await workspaceFixture(page);
  let hold=false, held=false, resume!:()=>void;
  const paused=new Promise<void>(resolve=>{resume=resolve;});
  await page.route("**/api/projects/battery/files?**",async route=>{
    if(hold && !held){held=true;await paused;}
    return route.fulfill({json:{entries:[file("table.xlsx"),file("view.html")]}});
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button",{name:"File preview",exact:true}).click();
  await expect(page.getByRole("button",{name:"table.xlsx",exact:true})).toBeVisible();
  await page.waitForTimeout(300); // Initial watch validation is a separate consumer.
  hold=true;
  const input=page.getByRole("searchbox",{name:"Search files"});
  try {
    await input.fill("xlsx");await expect.poll(()=>held).toBe(true);
    await input.fill("html");
    const results=page.getByRole("region",{name:"File search results"});
    await expect(results.locator('[data-file-path="view.html"]')).toBeVisible();
    resume();
    await expect(results.locator('[data-file-path="table.xlsx"]')).toHaveCount(0);
    await input.fill("");
    await expect(page.getByRole("button",{name:"table.xlsx",exact:true})).toBeVisible();
  } finally {resume();}
});

test("a file change marks results for refresh without dropping the current matches",async({page})=>{
  await workspaceFixture(page);
  const entries=[file("one.html")];
  await page.route("**/api/projects/battery/files?**",route=>route.fulfill({json:{entries}}));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button",{name:"File preview",exact:true}).click();
  await page.waitForTimeout(300);
  await page.getByRole("searchbox",{name:"Search files"}).fill("html");
  const results=page.getByRole("region",{name:"File search results"});
  await expect(results.locator(".file-search-row")).toHaveCount(1);
  entries.push(file("two.html"));
  await page.evaluate(()=>window.dispatchEvent(new CustomEvent("fixture-file-event",{detail:{path:"two.html",operation:"write",entry_type:"file"}})));
  await expect(results).toContainText("Files changed. Refresh search for updated results.");
  await expect(results.locator(".file-search-row")).toHaveCount(1);
  await results.getByRole("button",{name:"Refresh search",exact:true}).click();
  await expect(results.locator(".file-search-row")).toHaveCount(2);
  await expect(results).toContainText("Search complete.");
});
