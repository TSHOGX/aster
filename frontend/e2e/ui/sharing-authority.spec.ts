import { chooseValue } from "../picker";
import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("sharing selects an immutable recipient and retains revoke", async ({page}) => {
  const fixture = await workspaceFixture(page, {deniedActions:["shares.public.create"]});
  await page.goto("/?view=share&project=battery&session=analysis");
  await expect(page.getByRole("heading", {name:"Share a conversation"})).toBeVisible();
  await chooseValue(page.getByRole("combobox", {name:"Reader account",exact:true}), {label:"Bob Reader (@bob)"});
  await page.getByRole("button", {name:"Grant conversation access"}).click();
  await expect(page.getByRole("button", {name:"Revoke reader"})).toBeVisible();
  const grant = fixture.calls.find(call=>call.method==="POST" && call.path.endsWith("/readers"));
  expect(grant?.body).toEqual({reader_id:"account-bob"});
  await expect(page.getByRole("button", {name:"Create public snapshot · 7 days"})).toBeDisabled();
  await page.getByRole("button", {name:"Revoke reader"}).click();
  await expect(page.getByRole("button", {name:"Revoke reader"})).toHaveCount(0);
  expect(fixture.calls.some(call=>call.method==="DELETE" && call.path.endsWith("/readers/account-bob"))).toBe(true);
});
