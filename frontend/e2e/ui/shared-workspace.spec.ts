import { expect, test, type Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import { ConversationFixture } from "./conversation-fixture";

async function received(page: Page, publicToken?: string) {
  const fixture = await workspaceFixture(page);
  const metadata = {
    session_id: "received",
    title: "Shared experiment",
    source: {
      id: "alice",
      display_name: "Alice",
      username: "alice",
      shared_at: "2026-10-13T00:00:00Z",
    },
    revision: 1,
    allow_download: false,
    paths: ["results/report.txt"],
  };
  const base = publicToken
    ? `/api/shared/${publicToken}`
    : "/api/conversations/received";
  await page.route("**/api/conversations/sources?*", (route) =>
    route.fulfill({ json: { sources: [metadata.source] } }),
  );
  await page.route("**/api/conversations?*", (route) =>
    route.fulfill({ json: { conversations: [metadata] } }),
  );
  await page.route(`**${base}`, (route) =>
    route.fulfill({
      json: publicToken
        ? {
            id: "public-id",
            title: metadata.title,
            files: metadata.paths.map((path) => ({ path, size: 12 })),
            allow_download: false,
            access_revision: 1,
            expires_at: "2026-12-01T00:00:00Z",
          }
        : metadata,
    }),
  );
  const view = new ConversationFixture();
  await page.route(`**${base}/view/**`, (route) =>
    route.fulfill({
      json: view.reply(
        {
          session: {
            id: "received",
            title: metadata.title,
            project_id: "",
            state: "idle",
            revision: 1,
            control_revision: 1,
            deletion: 0,
          },
          messages: [
            {
              id: "question",
              turn_id: "turn",
              sequence: 1,
              role: "user",
              content: "Read /workspace/results/report.txt",
              created_at: "",
            },
            {
              id: "answer",
              turn_id: "turn",
              sequence: 2,
              role: "assistant",
              content: "Capacity is 42 mAh.",
              created_at: "",
            },
          ],
        },
        new URL(route.request().url()),
      ),
    }),
  );
  await page.route(`**${base}/turn-index?*`, (route) =>
    route.fulfill({ json: { turns: [] } }),
  );
  await page.route(`**${base}/files/content?*`, (route) =>
    route.fulfill({
      contentType: "text/plain",
      body: "Shared experiment report",
    }),
  );
  return fixture;
}
test("shared navigation expands sources in place and opens the common read-only workspace", async ({
  page,
}, info) => {
  const fixture = await received(page);
  await page.goto("/?project=battery&session=analysis");
  const toggle = page.getByRole("button", {
    name: "Shared with me",
    exact: true,
  });
  await expect(toggle.locator("svg")).toHaveCount(0);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(
    fixture.calls.some((c) => c.path.includes("/conversations/received/view")),
  ).toBe(false);
  await toggle.click();
  await expect(page).toHaveURL(/project=battery&session=analysis/);
  await page.getByRole("button", { name: /Alice @alice/ }).click();
  await page
    .getByRole("button", { name: "Shared experiment", exact: true })
    .click();
  await expect(page).toHaveURL(/view=shared&conversation=received/);
  await expect(page.getByText("Capacity is 42 mAh.")).toBeVisible();
  await expect(page.locator(".composer")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Toggle Terminal", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("link", { name: "/workspace/results/report.txt" })
    .click();
  await expect(page.getByText("Shared experiment report")).toBeVisible();
  const folder = page
    .getByRole("treeitem")
    .filter({
      has: page.getByRole("button", { name: "results", exact: true }),
    });
  await expect(folder).toHaveAttribute("aria-expanded", "true");
  await folder.getByRole("button").click();
  await expect(folder).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByText("Shared experiment report")).toBeVisible();
  await folder.getByRole("button").click();
  await expect(folder).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("link", { name: /Download/ })).toHaveCount(0);
  await page.screenshot({ path: info.outputPath("received-workspace.png") });
  await page.evaluate(() =>
    window.dispatchEvent(
      new CustomEvent("fixture-shared-event", {
        detail: { type: "closed", url: "/received/view/events" },
      }),
    ),
  );
  await expect(
    page.getByText("This shared conversation is unavailable."),
  ).toBeVisible();
  await expect(page.getByText("Shared experiment report")).toHaveCount(0);
  await expect(page.getByText("Capacity is 42 mAh.")).toHaveCount(0);
});
test("copy dialog creates no project on cancel and excludes non-downloadable files", async ({
  page,
}) => {
  const fixture = await received(page);
  await page.goto("/?view=shared&conversation=received");
  await expect(page.getByText("Capacity is 42 mAh.")).toBeVisible();
  await page
    .getByRole("button", { name: "Copy to my project", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("radio", { name: "New project", exact: true }).check();
  await dialog.getByLabel("Project name").fill("Imported research");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  expect(
    fixture.calls.some(
      (c) =>
        c.method === "POST" &&
        (c.path === "/api/projects" || c.path === "/api/conversation-copies"),
    ),
  ).toBe(false);
  let submitted: Record<string, unknown> | undefined;
  await page.route("**/api/conversation-copies", async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({ json: { copies: [] } });
      return;
    }
    submitted = route.request().postDataJSON();
    await route.fulfill({
      json: {
        id: submitted!.id,
        project_id: submitted!.project_id,
        session_id: "copy",
        state: "preparing",
        committed: false,
        error: "",
      },
    });
  });
  await page.route("**/api/conversation-copies/*", (route) =>
    route.fulfill({
      json: {
        id: submitted?.id,
        project_id: submitted?.project_id,
        session_id: "copy",
        state: "failed",
        committed: false,
        error: "Source access was removed.",
      },
    }),
  );
  await page
    .getByRole("button", { name: "Copy to my project", exact: true })
    .click();
  await expect(dialog.getByRole("checkbox")).toBeDisabled();
  await dialog
    .getByRole("button", { name: "Copy conversation", exact: true })
    .click();
  await expect(dialog.getByText("Copy failed", { exact: true })).toBeVisible();
  expect(submitted?.paths).toEqual([]);
  expect(submitted?.kind).toBe("received");
});
test("public snapshot uses the same reader and sends anonymous copy through login", async ({
  page,
}) => {
  const token = "a".repeat(43);
  await received(page, token);
  await page.route("**/api/auth/session", (route) =>
    route.fulfill({ status: 401, json: { error: "login required" } }),
  );
  await page.goto(`/s/${token}`);
  await expect(page.getByText("Capacity is 42 mAh.")).toBeVisible();
  await expect(page.getByText(/Shared snapshot/)).toBeVisible();
  await page
    .getByRole("button", { name: "Copy to my project", exact: true })
    .click();
  await expect(page).toHaveURL(/return_to=/);
  await expect(
    page.getByRole("button", { name: "Sign in", exact: true }),
  ).toBeVisible();
});

test("a completed copy opens its Project conversation without retaining the shared reader", async ({
  page,
}) => {
  await received(page);
  let id = "";
  await page.route("**/api/conversation-copies", async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({ json: { copies: [] } });
      return;
    }
    id = route.request().postDataJSON().id;
    await route.fulfill({
      json: {
        id,
        project_id: "battery",
        session_id: "analysis",
        state: "preparing",
        committed: false,
        error: "",
      },
    });
  });
  await page.route("**/api/conversation-copies/*", (route) =>
    route.fulfill({
      json: {
        id,
        project_id: "battery",
        session_id: "analysis",
        state: "complete",
        committed: true,
        error: "",
      },
    }),
  );
  await page.goto("/?view=shared&conversation=received");
  await page
    .getByRole("button", { name: "Copy to my project", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Copy conversation", exact: true })
    .click();
  await expect(page).toHaveURL(/project=battery&session=analysis/);
  await expect(page.locator(".shared-workspace")).toHaveCount(0);
  await expect(page.locator(".agent-panel")).toContainText(
    "Compare the cycle performance",
  );
});

for (const width of [390, 768])
  test(`shared files and copy dialog fit ${width}px`, async ({
    page,
  }, info) => {
    await received(page);
    await page.setViewportSize({ width, height: 844 });
    await page.goto("/?view=shared&conversation=received");
    await expect(page.getByText("Capacity is 42 mAh.")).toBeVisible();
    await page
      .getByRole("link", { name: "/workspace/results/report.txt" })
      .click();
    await expect(page.getByText("Shared experiment report")).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      )
      .toBe(true);
    await page
      .getByRole("button", { name: "Enter Full Screen", exact: true })
      .click();
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("button", { name: "Enter Full Screen", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Close file", exact: true }).click();
    await page
      .getByRole("button", { name: "Copy to my project", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect
      .poll(() =>
        page
          .getByRole("dialog")
          .evaluate(
            (node) =>
              node.getBoundingClientRect().right <= innerWidth &&
              node.getBoundingClientRect().left >= 0,
          ),
      )
      .toBe(true);
    await page.screenshot({
      path: info.outputPath(`shared-copy-${width}.png`),
    });
  });
