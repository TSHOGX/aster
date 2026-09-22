import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("folder shortcut opens a draft in that project and sends there", async ({
  page,
}) => {
  const f = await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  const row = page
    .locator(".project-row")
    .filter({ hasText: "Research notes" });
  const add = row.getByRole("button", { name: "New chat in Research notes" });
  await expect(add).toHaveCSS("opacity", "0");
  await row.hover();
  await expect(add).toHaveCSS("opacity", "1");
  await add.click();
  await expect(page).toHaveURL(/project=notes.*draft=1/);
  await expect(
    page.getByRole("button", { name: "Project", exact: true }),
  ).toHaveText("Research notes");
  await expect(page.getByLabel("New message", { exact: true })).toBeFocused();
  expect(f.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  await page
    .getByLabel("New message", { exact: true })
    .fill("Summarize these notes");
  await page.getByLabel("Send", { exact: true }).click();
  await expect(page.locator(".agent-panel")).toContainText(
    "Summarize these notes",
  );
  expect(
    f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/sessions")),
  ).toEqual([
    expect.objectContaining({ path: "/api/projects/notes/sessions" }),
  ]);
});

test("searchable project picker supports keyboard, dismissal and isolated drafts", async ({
  page,
}, testInfo) => {
  await workspaceFixture(page);
  await page.goto("/?project=battery&draft=1");
  const trigger = page.getByRole("button", { name: "Project", exact: true });
  const search = page.getByRole("combobox", { name: "Search projects" });
  await page.getByLabel("New message", { exact: true }).fill("Battery draft");
  await trigger.focus();
  await page.keyboard.press("ArrowDown");
  await expect(search).toBeFocused();
  await expect(
    page.getByRole("option", { name: "Battery research" }),
  ).toHaveAttribute("aria-selected", "true");
  await page.screenshot({
    path: testInfo.outputPath("project-picker-desktop.png"),
  });
  await search.fill("unmatched");
  await expect(page.getByRole("status")).toHaveText("No matching projects");
  await search.press("Enter");
  await expect(page).toHaveURL(/project=battery/);
  await search.fill("notes");
  await expect(page.getByRole("option")).toHaveCount(1);
  await search.press("Enter");
  await expect(trigger).toHaveText("Research notes");
  await expect(page.getByLabel("New message", { exact: true })).toHaveValue("");
  await page.getByLabel("New message", { exact: true }).fill("Notes draft");
  await trigger.click();
  await search.press("Home");
  await search.press("Enter");
  await expect(page.getByLabel("New message", { exact: true })).toHaveValue(
    "Battery draft",
  );
  await trigger.click();
  await search.press("Escape");
  await expect(trigger).toBeFocused();
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await trigger.click();
  await search.press("Tab");
  await expect(page.getByLabel("New message", { exact: true })).toBeFocused();
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await trigger.click();
  await page
    .getByRole("heading", { name: "What would you like to work on?" })
    .click();
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await trigger.click();
  await page.getByRole("option", { name: "Research notes" }).click();
  await expect(page.getByLabel("New message", { exact: true })).toHaveValue(
    "Notes draft",
  );
});

test("welcome project choice opens a new draft instead of a remembered session", async ({
  page,
}) => {
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".agent-panel")).toBeVisible();
  await page.goto("/");
  await page.getByRole("button", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: "Battery research" }).click();
  await expect(page).toHaveURL(/project=battery.*draft=1/);
  await expect(page.getByLabel("New message", { exact: true })).toBeVisible();
});

test("touch shortcuts stay visible and the picker fits a narrow dark viewport", async ({
  browser,
}, testInfo) => {
  const context = await browser.newContext({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
    colorScheme: "dark",
  });
  const page = await context.newPage();
  await workspaceFixture(page);
  await page.goto("/?project=battery&draft=1");
  await page.getByLabel("Open navigation", { exact: true }).tap();
  const add = page.getByRole("button", { name: "New chat in Research notes" });
  await expect(add).toHaveCSS("opacity", "1");
  await add.tap();
  await expect(page.locator(".project-shell")).not.toHaveClass(
    /navigation-open/,
  );
  await page.getByRole("button", { name: "Project", exact: true }).tap();
  const popup = page.locator(".project-select-popover");
  await expect(popup).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("project-picker-mobile-dark.png"),
  });
  const box = (await popup.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(390);
  expect(box.y + box.height).toBeLessThanOrEqual(844);
  await page.getByRole("option", { name: "Battery research" }).tap();
  await expect(page).toHaveURL(/project=battery.*draft=1/);
  await context.close();
});
