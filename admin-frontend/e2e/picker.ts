export async function chooseValue(control: import("@playwright/test").Locator, value: string | {label:string}) {
  await control.click();
  const id = await control.getAttribute("aria-controls");
  if (!id) throw new Error("Picker did not open");
  const list = control.page().locator(`[id=${JSON.stringify(id)}]`);
  const option = typeof value === "string" ? list.locator(`[data-value=${JSON.stringify(value)}]`) : list.getByRole("option", {name:value.label,exact:true});
  await option.click();
}
