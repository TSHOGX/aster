import { expect, test, type Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";

const summary = (n: number) => ({
  id: `task-${n}`,
  project_id: "battery",
  project_title: "Battery research",
  title: `Task ${n}`,
  timezone: "UTC",
  cron: "",
  enabled: true,
  revision: 1,
});
const hint = async (page: Page, kind: string) =>
  page.evaluate(
    (kind) =>
      window.dispatchEvent(
        new CustomEvent("fixture-resource-event", {
          detail: {
            resource_kind: kind,
            payload: { project_id: "battery", schedule_id: "task-1" },
          },
        }),
      ),
    kind,
  );

function startOf(url: URL) {
  const q = url.searchParams;
  if (q.has("before")) return Math.max(0, Number(q.get("before")) - 50);
  if (q.has("anchor"))
    return Number(q.get("anchor")?.replace(/^(task-|run-)/, ""));
  return Number(q.get("after") || 0);
}

test("tasks load continuously with bounded DOM, one stream and no healthy idle polling", async ({
  page,
}) => {
  await workspaceFixture(page);
  const requests: string[] = [];
  await page.route("**/api/schedules?*", (route) => {
    const url = new URL(route.request().url());
    requests.push(url.search);
    const start = startOf(url);
    return route.fulfill({
      json: {
        schedules: Array.from({ length: 50 }, (_, i) => summary(start + i)),
        next: String(start + 50),
        previous: start ? String(start) : undefined,
      },
    });
  });
  await page.clock.install();
  await page.goto("/?view=schedules");
  await expect(page.locator(".schedule-row")).toHaveCount(50);
  await page.clock.runFor(1000);
  // Virtual time does not wait for HTTP or React commits. The initial read and
  // consumer-attach reconciliation must settle before measuring healthy idle.
  await expect(page.locator(".schedule-sync-live")).toContainText(/^Live/);
  const initial = requests.length;
  await page.clock.runFor(60000);
  expect(requests).toHaveLength(initial);
  await expect(
    page.getByRole("button", { name: "Refresh", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Next page", exact: true }),
  ).toHaveCount(0);
  for (let n = 0; n < 25; n++) {
    const before = requests.length;
    await page.locator(".schedule-list").evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await expect.poll(() => requests.length).toBeGreaterThan(before);
    await page.clock.runFor(100);
    expect(await page.locator(".schedule-row").count()).toBeLessThanOrEqual(50);
  }
  expect(requests.some((url) => url.includes("after=1250"))).toBe(true);
  // Reverse demand uses a retained cursor, never replays from page one.
  await page.locator(".schedule-list").evaluate((el) => {
    el.scrollTop = 0;
  });
  await expect
    .poll(() => requests.some((url) => url.includes("before=")))
    .toBe(true);
});

test("filter changes fence late replies and read full instructions only for the selection", async ({
  page,
}) => {
  await workspaceFixture(page);
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let first = true,
    details = 0,
    delivered = false;
  await page.route("**/api/schedules?*", async (route) => {
    if (first) {
      first = false;
      await pending;
      await route.fulfill({ json: { schedules: [summary(0)] } });
      delivered = true;
      return;
    }
    await route.fulfill({ json: { schedules: [summary(1)] } });
  });
  await page.route("**/api/projects/battery/schedules/task-1", (route) => {
    details++;
    return route.fulfill({
      json: {
        ...summary(1),
        prompt: "The complete private scheduled prompt",
        deleted: false,
      },
    });
  });
  await page.goto("/?view=schedules");
  try {
    await page
      .getByRole("button", { name: "Filter by Project", exact: true })
      .click();
    await page
      .getByRole("option", { name: "Battery research", exact: true })
      .click();
    await expect(page.locator(".schedule-row")).toContainText("Task 1");
    release();
    await expect.poll(() => delivered).toBe(true);
    await expect(page.locator(".schedule-row")).not.toContainText("Task 0");
    expect(details).toBe(0);
    await page.locator(".schedule-description").click();
    await expect(page.locator(".schedule-reading")).toContainText(
      "The complete private scheduled prompt",
    );
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.locator(".schedule-editor textarea")).toHaveValue(
      "The complete private scheduled prompt",
    );
  } finally {
    release();
  }
});

async function historyFixture(page: Page) {
  await workspaceFixture(page);
  let revision = 1,
    state = "queued",
    unavailable = false,
    historyReads = 0,
    detailReads = 0;
  const saves: Array<{ cron: string; prompt: string }> = [];
  await page.route("**/api/schedules?*", (route) =>
    route.fulfill({ json: { schedules: [summary(1)] } }),
  );
  await page.route("**/api/projects/battery/schedules/task-1", (route) => {
    detailReads++;
    if (route.request().method() === "PATCH") {
      const body = route.request().postDataJSON();
      saves.push(body);
      if (body.revision < revision)
        return route.fulfill({ status: 409, json: { error: "conflict" } });
      return route.fulfill({ json: { ...body, revision: revision + 1 } });
    }
    return unavailable
      ? route.fulfill({ status: 503, json: { error: "temporary" } })
      : route.fulfill({
          json: {
            ...summary(1),
            revision,
            title: `Version ${revision}`,
            prompt: "Original prompt",
            cron: revision > 1 ? "0 9 * * *" : "",
            deleted: false,
          },
        });
  });
  await page.route(
    "**/api/projects/battery/schedules/task-1/runs?*",
    (route) => {
      historyReads++;
      const start = startOf(new URL(route.request().url()));
      return route.fulfill({
        json: {
          next: String(start + 50),
          previous: start ? String(start) : undefined,
          runs: Array.from({ length: 50 }, (_, i) => ({
            id: `run-${start + i}`,
            project_id: "battery",
            session_id: "analysis",
            turn_id: `turn-${start + i}`,
            state: start + i === 0 ? state : "completed",
            due_at: "",
          })),
        },
      });
    },
  );
  return {
    change() {
      revision++;
      state = "running";
    },
    fail() {
      unavailable = true;
    },
    recover() {
      unavailable = false;
    },
    reads: () => historyReads,
    detailReads: () => detailReads,
    saves,
  };
}

test("history continuously loads, buffers authoritative updates and retains the editor draft", async ({
  page,
}) => {
  const f = await historyFixture(page);
  await page.goto("/?view=schedules&schedule=task-1&schedule_project=battery");
  const history = page.getByRole("region", {
    name: "Recent runs",
    exact: true,
  });
  await expect(history.locator("article")).toHaveCount(5);
  await expect(
    page.getByRole("button", { name: /Refresh (task|history)/ }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const editor = page.locator(".schedule-editor textarea");
  await editor.fill("Unsaved scheduled draft");
  await history
    .getByRole("button", { name: "View full history", exact: true })
    .click();
  await expect(history.locator("article")).toHaveCount(50);
  for (let n = 0; n < 6; n++) {
    const reads = f.reads();
    await history.locator(".run-history-scroll").evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await expect.poll(f.reads).toBeGreaterThan(reads);
  }
  expect(await history.locator("article").count()).toBeLessThanOrEqual(50);
  const top = await history
    .locator(".run-history-scroll")
    .evaluate((el) => el.scrollTop);
  f.change();
  await hint(page, "run.updated");
  await expect(
    history.getByRole("button", { name: /1 updates · Back to latest/ }),
  ).toBeVisible();
  expect(
    await history.locator(".run-history-scroll").evaluate((el) => el.scrollTop),
  ).toBeCloseTo(top, 0);
  await expect(editor).toHaveValue("Unsaved scheduled draft");
  await history
    .getByRole("button", { name: /1 updates · Back to latest/ })
    .click();
  await expect(history.locator("article").first()).toContainText("Running");
  await expect
    .poll(() =>
      history.locator(".run-history-scroll").evaluate((el) => el.scrollTop),
    )
    .toBe(0);
  await page.keyboard.press("Escape");
  await expect(
    history.getByRole("button", { name: "View full history", exact: true }),
  ).toBeFocused();
  await expect(editor).toHaveValue("Unsaved scheduled draft");
});

test("background task updates retain content on failure and recover without a manual refresh", async ({
  page,
}) => {
  const f = await historyFixture(page);
  await page.goto("/?view=schedules&schedule=task-1&schedule_project=battery");
  await expect(page.locator(".schedule-reading h2")).toHaveText("Version 1");
  f.fail();
  await hint(page, "schedule.updated");
  await expect(
    page.locator(".schedule-detail-pane").getByRole("alert"),
  ).toContainText("Updates may be delayed.");
  await expect(page.locator(".schedule-reading h2")).toHaveText("Version 1");
  f.recover();
  f.change();
  await hint(page, "schedule.updated");
  await expect(page.locator(".schedule-reading h2")).toHaveText("Version 2");
  await expect(
    page.locator(".schedule-detail-pane").getByRole("alert"),
  ).toHaveCount(0);
});

test("mobile history is a contained drawer and Escape returns to the task", async ({
  page,
}) => {
  await historyFixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?view=schedules&schedule=task-1&schedule_project=battery");
  await page
    .getByRole("button", { name: "View full history", exact: true })
    .click();
  const panel = page.locator(".history-expanded");
  await expect(panel).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await expect(page.locator(".schedule-reading h2")).toBeVisible();
});

test("disconnected views fall back, hidden tabs stop reads and return automatically reconciles", async ({
  page,
}) => {
  const f = await historyFixture(page);
  await page.clock.install();
  await page.goto("/?view=schedules&schedule=task-1&schedule_project=battery");
  await expect(page.locator(".schedule-reading h2")).toHaveText("Version 1");
  await page.clock.runFor(1000);
  await page.evaluate(() =>
    window.dispatchEvent(
      new CustomEvent("fixture-file-event", { detail: "connection-error" }),
    ),
  );
  await expect(page.locator(".schedule-sync")).toContainText("Reconnecting…");
  f.change();
  await page.clock.runFor(6000);
  await expect(page.locator(".schedule-reading h2")).toHaveText("Version 2");
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", {
      value: true,
      configurable: true,
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.runFor(1000);
  const reads = f.reads();
  await page.clock.runFor(120000);
  expect(f.reads()).toBe(reads);
  f.change();
  await page.evaluate(() => {
    window.dispatchEvent(
      new CustomEvent("fixture-file-event", { detail: "connection-open" }),
    );
    Object.defineProperty(document, "hidden", {
      value: false,
      configurable: true,
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.runFor(1000);
  await expect(page.locator(".schedule-reading h2")).toHaveText("Version 3");
  await expect(page.locator(".schedule-sync")).toContainText("Live");
});

test("automatic configuration updates cannot silently change an editing baseline", async ({
  page,
}) => {
  const f = await historyFixture(page);
  await page.goto(
    "/?view=schedules&schedule=task-1&schedule_project=battery&mode=edit",
  );
  const editor = page.locator(".schedule-editor");
  await expect(
    editor.getByRole("combobox", { name: "Repeat", exact: true }),
  ).toHaveText("Manual only");
  await editor.locator("textarea").fill("Retained draft configuration");
  const reads = f.detailReads();
  f.change();
  await hint(page, "schedule.updated");
  await expect.poll(f.detailReads).toBeGreaterThan(reads);
  await expect(
    editor.getByRole("combobox", { name: "Repeat", exact: true }),
  ).toHaveText("Manual only");
  await editor
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(editor.getByText("Review latest saved task")).toBeVisible();
  expect(f.saves[0].cron).toBe("");
  expect(f.saves[0].prompt).toBe("Retained draft configuration");
  await expect(editor.locator("textarea")).toHaveValue(
    "Retained draft configuration",
  );
});
