import type { Page } from "@playwright/test";

export async function chooseOption(page: Page, label: string, option: string) {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page
    .getByRole("listbox", { name: label, exact: true })
    .getByRole("option", { name: option, exact: true })
    .click();
}

export async function chooseValue(control: import("@playwright/test").Locator, value: string | {label:string}) {
  await control.click();
  const id = await control.getAttribute("aria-controls");
  if (!id) throw new Error("Picker did not open");
  const list = control.page().locator(`[id=${JSON.stringify(id)}]`);
  const option = typeof value === "string" ? list.locator(`[data-value=${JSON.stringify(value)}]`) : list.getByRole("option", {name:value.label,exact:true});
  await option.click();
}
