import { expect, test, type Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import { chooseOption } from "../picker";

async function form(page: Page) {
  await workspaceFixture(page);
  const previews: { cron: string; timezone: string }[] = [];
  const saved: Record<string, unknown>[] = [];
  await page.route("**/api/schedules/preview", (route) => {
    const body = route.request().postDataJSON();
    previews.push(body);
    return route.fulfill({
      json: {
        ...body,
        valid: true,
        reference_time: "2026-09-24T00:00:00Z",
        occurrences: ["2026-09-25T01:00:00Z"],
      },
    });
  });
  await page.route(/\/api\/projects\/battery\/schedules(?:[/?]|$)/, (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/runs")) return route.fulfill({ json: { runs: [] } });
    if (route.request().method() === "POST") {
      const value = {
        ...route.request().postDataJSON(),
        project_id: "battery",
        revision: 1,
      };
      saved.push(value);
      return route.fulfill({ status: 201, json: value });
    }
    return route.fulfill({ json: saved.at(-1) });
  });
  await page.goto("/?view=schedules&mode=create");
  await expect(
    page.getByRole("combobox", { name: "Repeat", exact: true }),
  ).toBeVisible();
  return { previews, saved };
}

test("Repeat lists Interval first and supports keyboard selection, typeahead and Escape", async ({
  page,
}) => {
  await form(page);
  const repeat = page.getByRole("combobox", { name: "Repeat", exact: true });
  await repeat.click();
  const list = page.getByRole("listbox", { name: "Repeat", exact: true });
  await expect(list.getByRole("option").first()).toHaveAccessibleName(
    "Interval",
  );
  await expect(list.getByRole("option").nth(1)).toHaveAccessibleName("Daily");
  await expect(
    list.getByRole("option", { name: "Daily", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Home");
  await page.keyboard.press("Enter");
  await expect(repeat).toHaveText("Interval");
  await expect(repeat).toBeFocused();
  await expect(
    page.getByRole("spinbutton", {
      name: "Repeat every (minutes)",
      exact: true,
    }),
  ).toBeVisible();
  await repeat.press("ArrowDown");
  await page.keyboard.press("w");
  await page.keyboard.press("Enter");
  await expect(repeat).toHaveText("Weekly");
  await repeat.click();
  await page.keyboard.press("Escape");
  await expect(repeat).toBeFocused();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page).toHaveURL(/mode=create/);
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("dialog", { name: "Unsaved changes", exact: true }),
  ).toBeVisible();
});

test("timezone arrow opens a searchable list and committing a zone preserves the local time", async ({
  page,
}) => {
  const fixture = await form(page);
  const timezone = page.getByRole("combobox", {
    name: "Timezone",
    exact: true,
  });
  const original = await timezone.innerText();
  const box = (await timezone.boundingBox())!;
  await timezone.click({ position: { x: box.width - 15, y: box.height / 2 } });
  const search = page.getByRole("combobox", {
    name: "Search timezones",
    exact: true,
  });
  await expect(search).toBeFocused();
  await search.fill("no-such-timezone");
  await expect(
    page.getByText("No matching options", { exact: true }),
  ).toBeVisible();
  await expect(timezone).toHaveText(original);
  await search.fill("New York");
  const option = page.getByRole("option", {
    name: "America/New_York",
    exact: true,
  });
  await expect(option).toContainText("GMT");
  await search.press("Enter");
  await expect(timezone).toHaveText("America/New_York");
  await expect(timezone).toBeFocused();
  await expect(
    page.getByRole("textbox", { name: "Time", exact: true }),
  ).toHaveValue("09:00");
  await expect
    .poll(() => fixture.previews.at(-1)?.timezone)
    .toBe("America/New_York");
  await timezone.click();
  await search.fill("London");
  await search.press("Escape");
  await expect(timezone).toHaveText("America/New_York");
  await timezone.click();
  await search.fill("UTC");
  await page.getByRole("option", { name: "UTC", exact: true }).click();
  await expect(timezone).toHaveText("UTC");
  await expect(
    page.locator(
      ".schedule-editor select, .schedule-editor datalist, .schedule-editor input[type=time]",
    ),
  ).toHaveCount(0);
});

test("custom time columns and typed time commit minute precision through save", async ({
  page,
}) => {
  const fixture = await form(page);
  const time = page.getByRole("textbox", { name: "Time", exact: true });
  await page.getByRole("button", { name: "Choose time", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Choose time", exact: true });
  await expect(
    dialog.getByRole("listbox", { name: "Hour", exact: true }),
  ).toBeFocused();
  await dialog
    .getByRole("listbox", { name: "Hour", exact: true })
    .getByRole("option", { name: "16", exact: true })
    .click();
  await dialog
    .getByRole("listbox", { name: "Minute", exact: true })
    .getByRole("option", { name: "35", exact: true })
    .click();
  await expect(time).toHaveValue("09:00");
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await expect(time).toHaveValue("16:35");
  await expect(
    page.getByRole("button", { name: "Choose time", exact: true }),
  ).toBeFocused();
  await expect.poll(() => fixture.previews.at(-1)?.cron).toBe("35 16 * * *");
  await page.getByRole("button", { name: "Choose time", exact: true }).click();
  await page.keyboard.press("Home");
  await page.keyboard.press("Escape");
  await expect(time).toHaveValue("16:35");
  await expect(dialog).toHaveCount(0);
  await time.fill("935");
  await time.press("Tab");
  await expect(time).toHaveValue("09:35");
  await chooseOption(page, "Repeat", "Monthly");
  await chooseOption(page, "Day of month", "31");
  await page
    .getByRole("textbox", { name: "Task name", exact: true })
    .fill("Custom picker task");
  await page
    .getByRole("textbox", { name: "Instructions", exact: true })
    .fill("Check files");
  await page.getByRole("button", { name: "Project", exact: true }).click();
  await page
    .getByRole("option", { name: "Battery research", exact: true })
    .click();
  await page.getByRole("combobox", { name: "Timezone", exact: true }).click();
  await page
    .getByRole("combobox", { name: "Search timezones", exact: true })
    .fill("Shanghai");
  await page
    .getByRole("option", { name: "Asia/Shanghai", exact: true })
    .click();
  await page
    .locator(".schedule-editor")
    .getByRole("button", { name: "Create task", exact: true })
    .click();
  await expect(page.locator(".schedule-reading h2")).toHaveText(
    "Custom picker task",
  );
  expect(fixture.saved[0]).toMatchObject({
    cron: "35 9 31 * *",
    timezone: "Asia/Shanghai",
  });
});

test("Tab leaves the custom picker in form order and clicking outside dismisses it", async ({
  page,
}) => {
  await form(page);
  const repeat = page.getByRole("combobox", { name: "Repeat", exact: true });
  await repeat.click();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Time", exact: true }),
  ).toBeFocused();
  await repeat.click();
  await page.getByRole("textbox", { name: "Task name", exact: true }).click();
  await expect(repeat).toHaveAttribute("aria-expanded", "false");
  await expect(
    page.getByRole("textbox", { name: "Task name", exact: true }),
  ).toBeFocused();
});

for (const variant of [
  { locale: "en-US", theme: "light", width: 1440 },
  { locale: "en-US", theme: "dark", width: 390 },
  { locale: "zh-CN", theme: "dark", width: 390 },
] as const) {
  test.describe(`${variant.locale} ${variant.theme} ${variant.width} picker layout`, () => {
    test.use({
      locale: variant.locale,
      viewport: { width: variant.width, height: 844 },
      colorScheme: variant.theme,
    });
    test("popovers fit the viewport and remain selectable", async ({
      page,
    }, info) => {
      await workspaceFixture(page);
      await page.addInitScript(
        (theme) => localStorage.setItem("project-theme", theme),
        variant.theme,
      );
      await page.goto("/?view=schedules&mode=create");
      const zh = variant.locale === "zh-CN";
      await page
        .getByRole("combobox", { name: zh ? "重复" : "Repeat", exact: true })
        .click();
      await expect(page.getByRole("option").first()).toHaveAccessibleName(
        zh ? "按间隔" : "Interval",
      );
      await page.screenshot({ path: info.outputPath("repeat-picker.png") });
      await page.keyboard.press("Escape");
      await page
        .getByRole("button", {
          name: zh ? "选择时间" : "Choose time",
          exact: true,
        })
        .click();
      const timePopup = page.getByRole("dialog", {
        name: zh ? "选择时间" : "Choose time",
        exact: true,
      });
      await expect(timePopup).toBeVisible();
      await page.screenshot({ path: info.outputPath("time-picker.png") });
      await page.keyboard.press("Escape");
      await page
        .getByRole("combobox", { name: zh ? "时区" : "Timezone", exact: true })
        .click();
      const popup = page.locator(".selection-picker-popover");
      await expect(popup).toBeVisible();
      const bounds = (await popup.boundingBox())!;
      expect(bounds.x).toBeGreaterThanOrEqual(0);
      expect(bounds.x + bounds.width).toBeLessThanOrEqual(variant.width);
      expect(bounds.y).toBeGreaterThanOrEqual(0);
      expect(bounds.y + bounds.height).toBeLessThanOrEqual(844);
      await page
        .getByRole("combobox", {
          name: zh ? "搜索时区" : "Search timezones",
          exact: true,
        })
        .fill(zh ? "中国" : "Shanghai");
      await page
        .getByRole("option", { name: "Asia/Shanghai", exact: true })
        .click();
      await page
        .getByRole("combobox", { name: zh ? "时区" : "Timezone", exact: true })
        .click();
      await page.screenshot({ path: info.outputPath("timezone-picker.png") });
    });
  });
}

test("timezone search remains usable when the visual viewport shrinks over its trigger", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await form(page);
  const timezone = page.getByRole("combobox", { name: "Timezone", exact: true });
  await timezone.click();
  const search = page.getByRole("combobox", { name: "Search timezones", exact: true });
  await expect(search).toBeFocused();
  await page.evaluate(() => {
    Object.defineProperty(window.visualViewport!, "height", { configurable: true, get: () => 300 });
    window.visualViewport!.dispatchEvent(new Event("resize"));
  });
  await expect.poll(async () => {
    const rect = await page.locator(".selection-picker-popover").boundingBox();
    return rect ? rect.y + rect.height : 10000;
  }).toBeLessThanOrEqual(300);
  await search.fill("London");
  await page.getByRole("option", { name: "Europe/London", exact: true }).click();
  await expect(timezone).toHaveText("Europe/London");
});
