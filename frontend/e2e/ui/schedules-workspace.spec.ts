import { expect, test, type Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import type { Schedule } from "../../lib/project-api";
async function setup(
  page: Page,
  options: {
    conflict?: "tick" | "edit";
    lostCreate?: boolean;
    lostRun?: boolean;
    advanced?: boolean;
  } = {},
) {
  const base = await workspaceFixture(page);
  const tasks = new Map<string, Schedule>();
  tasks.set("task", {
    id: "task",
    project_id: "battery",
    title: "Research task",
    prompt: "Inspect research files",
    cron: options.advanced ? "*/15 9-17 1,15 * MON-FRI" : "0 9 * * *",
    timezone: "Asia/Shanghai",
    enabled: true,
    deleted: false,
    revision: 1,
    next_at: "2026-09-25T01:00:00Z",
  });
  const saves: Schedule[] = [],
    runs: string[] = [],
    previews: { cron: string; timezone: string }[] = [];
  let conflicted = false,
    lostCreate = false,
    lostRun = false;
  await page.route("**/api/schedules?*", (route) => {
    const q = new URL(route.request().url()).searchParams;
    const items = [...tasks.values()].filter(
      (t) =>
        !t.deleted &&
        (!q.get("q") ||
          t.title.toLowerCase().includes(q.get("q")!.toLowerCase())) &&
        (!q.get("status") || (q.get("status") === "enabled") === t.enabled),
    );
    return route.fulfill({
      json: {
        schedules: items.map((task) => ({
          id: task.id,
          project_id: task.project_id,
          project_title: "Battery research",
          title: task.title,
          cron: task.cron,
          timezone: task.timezone,
          enabled: task.enabled,
          next_at: task.next_at,
          revision: task.revision,
        })),
      },
    });
  });
  await page.route("**/api/schedules/preview", (route) => {
    const body = route.request().postDataJSON();
    previews.push(body);
    return route.fulfill({
      json: {
        ...body,
        valid: true,
        reference_time: "2026-09-24T00:00:00Z",
        occurrences: body.cron
          ? [
              "2026-09-25T01:00:00Z",
              "2026-09-26T01:00:00Z",
              "2026-09-27T01:00:00Z",
            ]
          : [],
      },
    });
  });
  await page.route(
    /\/api\/projects\/battery\/schedules(?:[/?]|$)/,
    async (route) => {
      const path = new URL(route.request().url()).pathname;
      const parts = path.split("/");
      const id = parts[5];
      if (path.endsWith("/runs")) return route.fulfill({ json: { runs: [] } });
      if (path.endsWith("/run")) {
        const request = route.request().postDataJSON().request_id;
        runs.push(request);
        if (options.lostRun && !lostRun) {
          lostRun = true;
          return route.abort();
        }
        return route.fulfill({
          status: 201,
          json: {
            id: "run",
            project_id: "battery",
            session_id: "analysis",
            turn_id: "turn",
            state: "queued",
            due_at: "",
          },
        });
      }
      if (route.request().method() === "GET")
        return tasks.has(id)
          ? route.fulfill({ json: tasks.get(id) })
          : route.fulfill({ status: 404, json: { error: "not found" } });
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON() as Schedule;
        saves.push(body);
        tasks.set(body.id, { ...body, project_id: "battery", revision: 1 });
        if (options.lostCreate && !lostCreate) {
          lostCreate = true;
          return route.abort();
        }
        return route.fulfill({ status: 201, json: tasks.get(body.id) });
      }
      if (route.request().method() === "PATCH") {
        const body = route.request().postDataJSON() as Schedule;
        saves.push(body);
        if (options.conflict && !conflicted) {
          conflicted = true;
          const current = tasks.get(id)!;
          tasks.set(id, {
            ...current,
            revision: 2,
            ...(options.conflict === "edit"
              ? { prompt: "Someone else’s saved instructions" }
              : { next_at: "2026-09-26T01:00:00Z" }),
          });
          return route.fulfill({ status: 409, json: { error: "conflict" } });
        }
        tasks.set(id, { ...body, revision: body.revision + 1 });
        return route.fulfill({ json: tasks.get(id) });
      }
      if (route.request().method() === "DELETE") {
        tasks.delete(id);
        return route.fulfill({ status: 204 });
      }
      return route.fallback();
    },
  );
  return { ...base, tasks, saves, runs, previews };
}
async function createDraft(page: Page) {
  await page.goto("/?view=schedules");
  await page.getByRole("button", { name: "Create task", exact: true }).click();
  const editor = page.locator(".schedule-editor");
  await editor
    .getByRole("textbox", { name: "Task name", exact: true })
    .fill("Daily checks");
  await editor.locator("textarea").fill("Check the project files");
  await editor.getByRole("button", { name: "Project", exact: true }).click();
  await page
    .getByRole("option", { name: "Battery research", exact: true })
    .click();
  return editor;
}
test("daily creation has an authoritative preview, no conversation fan-out, and stays in details after a lost reply", async ({
  page,
}) => {
  const f = await setup(page, { lostCreate: true });
  const editor = await createDraft(page);
  await expect(
    editor.getByRole("combobox", { name: "Repeat", exact: true }),
  ).toHaveValue("daily");
  await expect(editor.locator('input[type="time"]')).toHaveValue("09:00");
  await expect(
    editor.getByText("Cron expression", { exact: true }),
  ).toHaveCount(0);
  await expect(editor.locator(".schedule-preview li")).toHaveCount(3);
  expect(f.calls.filter((c) => c.path.endsWith("/sessions"))).toHaveLength(0);
  await editor
    .getByRole("button", { name: "Create task", exact: true })
    .click();
  await expect(page.locator(".schedule-reading h2")).toHaveText("Daily checks");
  expect(f.saves).toHaveLength(1);
  expect(f.saves[0].cron).toBe("0 9 * * *");
  expect(f.saves[0].session_id).toBe("");
  expect(new URL(page.url()).searchParams.get("project")).toBeNull();
  expect(new URL(page.url()).searchParams.get("schedule")).toBe(f.saves[0].id);
});
test("weekly and monthly controls retain the draft across resize and guard Escape, global navigation, and browser Back", async ({
  page,
}) => {
  await setup(page);
  const editor = await createDraft(page);
  await editor
    .getByRole("combobox", { name: "Repeat", exact: true })
    .selectOption("weekly");
  await editor.getByRole("button", { name: "Monday", exact: true }).click();
  await editor.getByRole("button", { name: "Friday", exact: true }).click();
  await editor.locator('input[type="time"]').fill("16:00");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(editor.locator("textarea")).toHaveValue(
    "Check the project files",
  );
  await editor.getByRole("textbox", { name: "Task name", exact: true }).focus();
  await page.keyboard.press("Escape");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(editor.locator("textarea")).toHaveValue(
    "Check the project files",
  );
  await page.goBack();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page).toHaveURL(/mode=create/);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page
    .getByRole("button", { name: "Shared with me", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await editor
    .getByRole("combobox", { name: "Repeat", exact: true })
    .selectOption("monthly");
  await editor
    .getByRole("combobox", { name: "Day of month", exact: true })
    .selectOption("31");
  await expect(
    editor.getByText("Months without the selected date are skipped."),
  ).toBeVisible();
  await editor
    .getByRole("button", { name: "Create task", exact: true })
    .click();
  await expect(page.locator(".schedule-reading")).toContainText(
    "Monthly · 31 · 16:00",
  );
});
test("advanced rules survive name edits and tick-only revision conflicts retry once", async ({
  page,
}) => {
  const f = await setup(page, { advanced: true, conflict: "tick" });
  await page.goto(
    "/?view=schedules&schedule=task&schedule_project=battery&mode=edit",
  );
  const editor = page.locator(".schedule-editor");
  await expect(
    editor.getByRole("combobox", { name: "Repeat", exact: true }),
  ).toHaveValue("advanced");
  await editor
    .getByRole("textbox", { name: "Task name", exact: true })
    .fill("Renamed task");
  await editor
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(page.locator(".schedule-reading h2")).toHaveText("Renamed task");
  expect(f.saves).toHaveLength(2);
  expect(f.saves.map((s) => s.revision)).toEqual([1, 2]);
  expect(f.tasks.get("task")?.cron).toBe("*/15 9-17 1,15 * MON-FRI");
});
test("configuration conflicts retain edits and require an explicit decision", async ({
  page,
}) => {
  const f = await setup(page, { conflict: "edit" });
  await page.goto(
    "/?view=schedules&schedule=task&schedule_project=battery&mode=edit",
  );
  const editor = page.locator(".schedule-editor");
  await editor.locator("textarea").fill("My retained instructions");
  await editor
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(editor.locator("textarea")).toHaveValue(
    "My retained instructions",
  );
  await expect(editor.getByText("Review latest saved task")).toBeVisible();
  expect(f.saves).toHaveLength(1);
  await editor.getByText("Review latest saved task").click();
  await expect(
    editor.getByText("Someone else’s saved instructions"),
  ).toBeVisible();
});
test("uncertain run retries keep their identity and pausing does not claim to stop it", async ({
  page,
}) => {
  const f = await setup(page, { lostRun: true });
  await page.goto("/?view=schedules&schedule=task&schedule_project=battery");
  await page.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(
    page.locator(".schedule-reading").getByRole("alert"),
  ).toContainText("Retry Run now");
  await page.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(page.getByText("Run submitted.", { exact: true })).toBeVisible();
  expect(f.runs).toHaveLength(2);
  expect(f.runs[0]).toBe(f.runs[1]);
  await expect(page).toHaveURL(/view=schedules/);
  await page.getByRole("button", { name: "Pause", exact: true }).click();
  await expect(
    page.getByText("Paused. Already submitted runs are unaffected."),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Run now", exact: true }),
  ).toBeDisabled();
});
for (const width of [1440, 1280, 1024, 768, 390])
  test(`schedule panes reflow without horizontal overflow at ${width}`, async ({
    page,
  }, info) => {
    await setup(page);
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/?view=schedules&schedule=task&schedule_project=battery");
    await expect(page.locator(".schedule-reading h2")).toHaveText(
      "Research task",
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    if (width >= 1280)
      await expect(page.locator(".schedule-list-pane")).toBeVisible();
    else await expect(page.locator(".schedule-list-pane")).toBeHidden();
    await page.screenshot({
      path: info.outputPath(`schedule-${width}.png`),
      fullPage: true,
    });
  });

test("manual-only is explicit and a stale preview cannot validate a changed rule", async ({
  page,
}) => {
  const f = await setup(page);
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/schedules/preview", async (route) => {
    const body = route.request().postDataJSON();
    if (body.cron) await pending;
    await route.fulfill({
      json: {
        ...body,
        valid: true,
        reference_time: "2026-09-24T00:00:00Z",
        occurrences: body.cron ? ["2026-09-25T01:00:00Z"] : [],
      },
    });
  });
  try {
    const editor = await createDraft(page);
    await expect(
      editor.getByRole("button", { name: "Create task", exact: true }),
    ).toBeDisabled();
    await editor
      .getByRole("combobox", { name: "Repeat", exact: true })
      .selectOption("manual");
    await expect(
      editor.getByText("Runs only when you choose Run now."),
    ).toBeVisible();
    release();
    await expect(editor.locator(".schedule-preview li")).toHaveCount(0);
    await editor
      .getByRole("button", { name: "Create task", exact: true })
      .click();
    await expect(page.locator(".schedule-reading")).toContainText(
      "Manual only",
    );
    expect(f.saves[0].cron).toBe("");
    expect(f.saves[0].enabled).toBe(true);
  } finally {
    release();
  }
});

test("replacing an advanced rule requires an explicit choice", async ({
  page,
}) => {
  await setup(page, { advanced: true });
  await page.goto(
    "/?view=schedules&schedule=task&schedule_project=battery&mode=edit",
  );
  const repeat = page.getByRole("combobox", { name: "Repeat", exact: true });
  await repeat.selectOption("daily");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(repeat).toHaveValue("advanced");
  await repeat.selectOption("daily");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Replace time rule", exact: true })
    .click();
  await expect(repeat).toHaveValue("daily");
});

for (const locale of ["en-US", "zh-CN"])
  for (const theme of ["light", "dark"])
    test.describe(`${locale} ${theme} schedule form`, () => {
      test.use({ locale });
      test("retains all fields through content-width breakpoints", async ({
        page,
      }, info) => {
        await setup(page);
        await page.addInitScript(
          (theme) => localStorage.setItem("project-theme", theme),
          theme,
        );
        await page.goto("/?view=schedules&mode=create");
        await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
        const editor = page.locator(".schedule-editor");
        await editor
          .locator('input[maxlength="256"]')
          .fill("Long task name / 定时研究任务 ".repeat(6));
        await editor
          .locator("textarea")
          .fill("Research instructions / 研究任务内容\n".repeat(20));
        await editor.locator("select").last().selectOption("weekly");
        for (const width of [1440, 1280, 1024, 768, 390]) {
          await page.setViewportSize({ width, height: 1000 });
          await expect(editor.locator("textarea")).toHaveValue(
            "Research instructions / 研究任务内容\n".repeat(20),
          );
          expect(
            await page.evaluate(
              () => document.documentElement.scrollWidth <= innerWidth,
            ),
          ).toBe(true);
          expect(
            await editor.evaluate((el) => el.scrollWidth <= el.clientWidth),
          ).toBe(true);
          await page.screenshot({
            path: info.outputPath(`form-${width}.png`),
            fullPage: true,
          });
        }
      });
    });

test("a late pause response cannot replace another selected task", async ({
  page,
}) => {
  const fixture = await setup(page);
  fixture.tasks.set("other", {
    ...fixture.tasks.get("task")!,
    id: "other",
    title: "Another selected task",
  });
  let release!: () => void,
    issued = false;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/projects/battery/schedules/task", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    issued = true;
    const body = route.request().postDataJSON();
    await pending;
    return route.fulfill({ json: { ...body, revision: body.revision + 1 } });
  });
  try {
    await page.goto("/?view=schedules&schedule=task&schedule_project=battery");
    await page.getByRole("button", { name: "Pause", exact: true }).click();
    await expect.poll(() => issued).toBe(true);
    await page
      .locator(".schedule-description")
      .filter({ hasText: "Another selected task" })
      .click();
    await expect(page.locator(".schedule-reading h2")).toHaveText(
      "Another selected task",
    );
    release();
    await expect(page.locator(".schedule-reading h2")).toHaveText(
      "Another selected task",
    );
    await expect(page).toHaveURL(/schedule=other/);
  } finally {
    release();
  }
});

test("upcoming occurrences load on demand and use the schedule timezone", async ({
  page,
}) => {
  const fixture = await setup(page);
  await page.goto("/?view=schedules&schedule=task&schedule_project=battery");
  await expect(page.locator(".schedule-reading h2")).toHaveText(
    "Research task",
  );
  expect(fixture.previews).toHaveLength(0);
  await page
    .getByRole("button", { name: "Upcoming occurrences", exact: true })
    .click();
  await expect(page.locator(".schedule-reading li")).toHaveCount(3);
  await expect(page.locator(".schedule-reading li").first()).toContainText(
    "09:00 GMT+8",
  );
  expect(fixture.previews).toHaveLength(1);
});
