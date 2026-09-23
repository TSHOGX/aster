import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("global views clear Project selection and Back restores the conversation", async ({ page }) => {
  await workspaceFixture(page);
  const detailReads: string[] = [];
  await page.route("**/api/conversations?*", route => route.fulfill({ json: { conversations: [] } }));
  await page.route("**/api/conversations/*/**", route => { detailReads.push(route.request().url()); return route.fulfill({ json: {} }); });
  await page.route("**/api/schedules?*", route => route.fulfill({ json: { schedules: [] } }));
  await page.route("**/api/notifications?*", route => route.fulfill({ json: { notifications: [], unread: 0 } }));
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".agent-panel")).toContainText("Compare the cycle performance");
  for (const [label, view] of [["Shared with me", "shared"], ["Scheduled", "schedules"], ["Search", "search"], ["Inbox", "inbox"]]) {
    await page.getByRole("button", { name: label, exact: true }).click();
    await expect(page).toHaveURL(`http://127.0.0.1:3107/?view=${view}`);
    await expect(page.locator(".agent-panel")).toHaveCount(0);
    await expect(page.locator(".session-row.selected")).toHaveCount(0);
  }
  expect(detailReads).toEqual([]);
  for (let n = 0; n < 4; n++) await page.goBack();
  await expect(page.locator(".agent-panel")).toContainText("Compare the cycle performance");
  await page.goForward();
  await expect(page.getByText("No conversations have been shared with you.")).toBeVisible();
});

test("mixed global URLs do not restore remembered Sessions", async ({ page }) => {
  await workspaceFixture(page);
  await page.addInitScript(() => localStorage.setItem("project-view:researcher:battery:views", JSON.stringify({ version: 2, activeSessionId: "analysis" })));
  await page.route("**/api/conversations?*", route => route.fulfill({ json: { conversations: [] } }));
  await page.goto("/?view=shared&project=battery&session=analysis&message=m1&history=1");
  await expect(page).toHaveURL("http://127.0.0.1:3107/?view=shared");
  await expect(page.getByText("No conversations have been shared with you.")).toBeVisible();
  await expect(page.locator(".chat-message")).toHaveCount(0);
  await page.reload();
  await expect(page.locator(".chat-message")).toHaveCount(0);
});

test("shared detail eligibility is independent of list pages and revocation removes content", async ({ page }) => {
  await workspaceFixture(page);
  let revoked = false, reads = 0;
  await page.route("**/api/conversations?*", route => route.fulfill({ json: { conversations: [] } }));
  await page.route("**/api/conversations/received", route => route.fulfill(revoked ? { status: 404, json: { error: "not found" } } : { json: { session_id: "received", project_id: "foreign", title: "Received result", revision: 1 } }));
  await page.route("**/api/conversations/received/messages?*", route => { reads++; return route.fulfill({ json: { messages: [{ id: "m", sequence: 1, role: "assistant", content: "An explicitly received answer" }] } }); });
  await page.goto("/?view=shared&conversation=received");
  await expect(page.getByText("An explicitly received answer")).toBeVisible();
  await expect(page.getByText("No conversations have been shared with you.")).toHaveCount(0);
  revoked = true;
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("fixture-file-event", { detail: "connection-error" })));
  await expect(page.getByText("This shared conversation is unavailable.")).toBeVisible();
  await expect(page.getByText("An explicitly received answer")).toHaveCount(0);
  expect(reads).toBe(1);
});
