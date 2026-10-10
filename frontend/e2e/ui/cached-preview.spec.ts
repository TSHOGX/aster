import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

for (const result of ["same", "changed", "denied", "missing"] as const) {
  test(`cached text appears during revalidation and handles ${result} bytes or authority`, async ({ page }) => {
    await workspaceFixture(page);
    await page.goto("/?project=battery&session=analysis");
    const report = page.getByRole("link", { name: "the research report" });
    await report.click();
    await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
    await page.getByRole("button", { name: "README.md", exact: true }).click();
    await expect(page.locator(".file-markdown")).toContainText("Battery research");
    let release!: () => void;
    const held = new Promise<void>(resolve => { release = resolve; });
    let waiting = 0;
    await page.route("**/files/content?*", async route => {
      if (new URL(route.request().url()).searchParams.get("path") !== "reports/summary.md") return route.fallback();
      waiting++;
      await held;
      if (result === "denied" || result === "missing") return route.fulfill({ status: result === "denied" ? 403 : 404, json: { error: "This file is unavailable." } });
      if (result === "changed") return route.fulfill({ contentType: "application/octet-stream", body: "# Authoritative replacement\n" });
      return route.fallback();
    });
    try {
      await report.click();
      await expect.poll(() => waiting).toBeGreaterThan(0);
      // The server is deliberately held, so only retained permitted bytes can
      // satisfy this assertion. Keep their validation state visible.
      await expect(page.locator(".file-markdown")).toContainText("Cycle performance report", { timeout: 1000 });
      await expect(page.getByRole("status").filter({ hasText: "Checking for file changes…" })).toBeVisible();
      release();
      if (result === "denied" || result === "missing") {
        await expect(page.locator(".file-markdown")).toHaveCount(0);
        await expect(page.getByRole("region", { name: "File reports/summary.md" })).toContainText("This file is unavailable.");
      } else {
        await expect(page.locator(".file-markdown")).toContainText(result === "same" ? "Cycle performance report" : "Authoritative replacement");
        await expect(page.getByText("Checking for file changes…", { exact: true })).toHaveCount(0);
      }
    } finally {
      release();
    }
  });
}

test("a changed unselected file invalidates cached bytes before reopening", async ({ page }) => {
  const fixture = await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  const report = page.getByRole("link", { name: "the research report" });
  await report.click();
  await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
  await page.getByRole("button", { name: "README.md", exact: true }).click();
  await expect(page.locator(".file-markdown")).toContainText("Battery research");
  await expect.poll(() => page.evaluate(() => Reflect.get(window, "fixtureReadyResources"))).toBeGreaterThan(0);
  fixture.files["reports/summary.md"] = "# Changed while unselected\n";
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("fixture-file-event", { detail: { path: "reports/summary.md", operation: "write", entry_type: "file" } })));
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  let waiting = false;
  await page.route("**/files/content?*", async route => {
    if (new URL(route.request().url()).searchParams.get("path") !== "reports/summary.md") return route.fallback();
    waiting = true;
    await held;
    return route.fallback();
  });
  try {
    await report.click();
    await expect.poll(() => waiting).toBe(true);
    await expect(page.locator(".file-markdown")).toHaveCount(0);
    release();
    await expect(page.locator(".file-markdown")).toContainText("Changed while unselected");
  } finally { release(); }
});

test("a late validation cannot replace another selected cached file", async ({ page }) => {
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  const report = page.getByRole("link", { name: "the research report" });
  await report.click();
  await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
  const readme = page.getByRole("button", { name: "README.md", exact: true });
  await readme.click();
  await expect(page.locator(".file-markdown")).toContainText("Battery research");
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  let delivered = false;
  await page.route("**/files/content?*", async route => {
    if (new URL(route.request().url()).searchParams.get("path") !== "reports/summary.md") return route.fallback();
    await held;
    await route.fulfill({ body: "# Late response must not replace README\n" });
    delivered = true;
  });
  try {
    await report.click();
    await expect(page.getByText("Checking for file changes…", { exact: true })).toBeVisible();
    await readme.click();
    release();
    await expect.poll(() => delivered).toBe(true);
    await expect(page.locator(".file-markdown")).toContainText("Battery research");
    await expect(page.locator(".file-markdown")).not.toContainText("Late response");
  } finally { release(); }
});

test("directory refresh preserves a sibling file's pointer target",async({page})=>{
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link",{name:"the research report"}).click();
  await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
  const tree=page.getByRole("tree",{name:"File tree",exact:true});
  await expect(tree).toHaveAttribute("aria-busy","false");
  const readme=page.getByRole("button",{name:"README.md",exact:true});
  const before=await readme.boundingBox();expect(before).not.toBeNull();
  let release!:()=>void;const held=new Promise<void>(resolve=>{release=resolve;});let waiting=false;
  await page.route("**/api/projects/battery/files?*",async route=>{
    if(new URL(route.request().url()).searchParams.get("path")!=="reports")return route.fallback();
    waiting=true;await held;await route.fallback();
  });
  try{
    await expect.poll(()=>page.evaluate(()=>Reflect.get(window,"fixtureReadyResources"))).toBeGreaterThan(0);
    await page.evaluate(()=>window.dispatchEvent(new CustomEvent("fixture-file-event",{detail:{path:"reports/another.md",operation:"write",entry_type:"file"}})));
    await expect.poll(()=>waiting).toBe(true);
    await expect(tree).toHaveAttribute("aria-busy","true");
    expect((await readme.boundingBox())!.y).toBe(before!.y);
    await page.mouse.move(before!.x+before!.width/2,before!.y+before!.height/2);
    await page.mouse.down();release();
    await expect(tree).toHaveAttribute("aria-busy","false");
    expect((await readme.boundingBox())!.y).toBe(before!.y);
    await page.mouse.up();
    await expect(page.locator(".file-markdown")).toContainText("Battery research");
  }finally{release();}
});
