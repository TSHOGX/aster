import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

for (const state of ["paused", "created"]) {
  test(`${state} compute keeps history, file browsing and text editing available`, async ({page}) => {
    const fixture = await workspaceFixture(page, {projectState:state});
    await page.goto("/?project=battery&session=analysis");
    await expect(page.getByText("Compare the cycle performance and summarize the results in a report.", {exact:true})).toBeVisible();
    await page.getByRole("link", {name:"the research report"}).click();
    await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
    await expect(page.locator('[data-file-path="reports/summary.md"]')).toBeVisible();
    await expect(page.getByRole("button",{name:"Resume Project",exact:true})).toHaveCount(0);
    await page.getByRole("group",{name:"File view"}).getByRole("button",{name:"Source",exact:true}).click();
    await expect(page.locator(".selected-file pre")).toContainText("# Cycle performance report");
    await page.getByLabel("File actions",{exact:true}).click();
    await page.getByRole("menuitem",{name:"Edit text",exact:true}).click();
    await page.getByLabel("Edit reports/summary.md",{exact:true}).fill("# Edited while compute is paused");
    await page.getByRole("button",{name:"Save file",exact:true}).click();
    await expect(page.locator(".selected-file")).toContainText("Edited while compute is paused");
    expect(fixture.files["reports/summary.md"]).toBe("# Edited while compute is paused");
    expect(fixture.calls.filter(c=>c.path.endsWith("/resume") || c.path.endsWith("/start"))).toEqual([]);
  });
}
