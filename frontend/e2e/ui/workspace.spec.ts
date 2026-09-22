import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";
const evidence = resolve(
  __dirname,
  "../../../private/uiux-refinement/evidence/screenshots",
);
test("navigation, draft isolation, independent docks and edit protection", async ({
  page,
}) => {
  const f = await workspaceFixture(page);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  await expect(page.locator(".workspace-switches button")).toHaveCount(2);
  await expect(
    page.getByLabel("Project Files", { exact: true }),
  ).not.toBeVisible();
  await expect(page.locator(".terminal-dock")).toHaveCount(0);
  await page
    .getByLabel("Message Compare cycle performance", { exact: true })
    .fill("Keep this draft");
  await page
    .getByRole("button", { name: "Prepare the research report", exact: true })
    .click();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  await expect(page.locator(".agent-panel")).toContainText(
    "ready for your review",
  );
  await page.goBack();
  await expect(
    page.getByLabel("Message Compare cycle performance", { exact: true }),
  ).toHaveValue("Keep this draft");
  await page.getByRole("link", { name: "the research report" }).click();
  await expect(
    page.getByRole("region", { name: "File reports/summary.md", exact: true }),
  ).toContainText("Cycle performance report");
  await page.getByLabel("Enter Full Screen", { exact: true }).click();
  await expect(page.locator(".project-shell")).toHaveClass(/file-fullscreen/);
  await page.keyboard.press("Escape");
  await expect(
    page.getByLabel("Enter Full Screen", { exact: true }),
  ).toBeFocused();
  await page.getByLabel("File actions", { exact: true }).click();
  await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
  await page
    .getByLabel("Edit reports/summary.md", { exact: true })
    .fill("# Unsaved research");
  await page.getByLabel("Close file preview", { exact: true }).click();
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  await expect(
    page.getByLabel("Edit reports/summary.md", { exact: true }),
  ).toContainText("Unsaved research");
  await page.getByRole("button", { name: "README.md", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("Unsaved changes");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(
    page.getByLabel("Edit reports/summary.md", { exact: true }),
  ).toContainText("Unsaved research");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect
    .poll(() => f.files["reports/summary.md"])
    .toBe("# Unsaved research");
  await page.getByLabel("Toggle Terminal", { exact: true }).click();
  expect(f.terminals).toHaveLength(0);
  await page.getByLabel("New terminal", { exact: true }).click();
  await expect(page.locator(".xterm")).toBeVisible();
  await page
    .getByRole("button", { name: "Prepare the research report", exact: true })
    .click();
  await expect(page.locator(".xterm")).toBeVisible();
  await page.getByLabel("Hide terminal", { exact: true }).click();
  expect(
    f.calls.filter((c) => c.path.endsWith("/stop") || c.method === "DELETE"),
  ).toHaveLength(0);
  expect(errors).toEqual([]);
});
test("first-send lost reply retains identity and never duplicates a message", async ({
  page,
}) => {
  const f = await workspaceFixture(page, { lostFirstReply: true });
  await page.goto("/?project=battery&draft=1");
  await page
    .getByRole("button", { name: "New Chat", exact: true })
    .first()
    .click();
  expect(
    f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/sessions")),
  ).toHaveLength(0);
  await page
    .getByLabel("New message", { exact: true })
    .fill("A new experiment");
  await page.getByLabel("Send", { exact: true }).click();
  await expect(page.locator(".new-chat-view").getByRole("alert")).toContainText(
    "outcome is unknown",
  );
  await page.reload();
  await page.getByLabel("Retry", { exact: true }).click();
  await expect(page.locator(".agent-panel")).toContainText("A new experiment");
  const creates = f.calls.filter(
    (c) => c.method === "POST" && c.path.endsWith("/sessions"),
  );
  const turns = f.calls.filter(
    (c) => c.method === "POST" && c.path.endsWith("/turns"),
  );
  expect(creates).toHaveLength(1);
  expect(turns).toHaveLength(2);
  expect(turns[0].body!.request_id).toBe(turns[1].body!.request_id);
  expect(f.messages[String(creates[0].body!.id)]).toHaveLength(1);
});
test("appends sessions beyond page one and restores off-page deep links", async ({
  page,
}) => {
  await workspaceFixture(page, { many: true });
  await page.goto("/?project=battery&session=report");
  await expect(
    page.getByRole("button", {
      name: "Prepare the research report",
      exact: true,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Show more", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Study 104", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Study 0", exact: true }),
  ).toBeVisible();
});
for (const locale of ["en-US", "zh-CN"])
  for (const theme of ["light", "dark"] as const)
    for (const [width, height] of [
      [1600, 1000],
      [1280, 800],
      [1024, 768],
      [390, 844],
    ]) {
      test(`visual ${locale} ${theme} ${width}`, async ({ browser }) => {
        const context = await browser.newContext({
          locale,
          colorScheme: theme,
          viewport: { width, height },
        });
        const page = await context.newPage();
        const errors: string[] = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await workspaceFixture(page);
        await page.goto(
          "http://127.0.0.1:3107/?project=battery&session=analysis",
        );
        await expect(page.locator(".chat-message.assistant")).toBeVisible();
        mkdirSync(evidence, { recursive: true });
        await page.screenshot({
          path: resolve(evidence, `${locale}-${theme}-${width}-chat.png`),
        });
        await page.locator(".workspace-switches button").nth(1).click();
        await expect(page.locator(".files-panel")).toBeVisible();
        await page
          .getByRole("button", { name: "README.md", exact: true })
          .click();
        await expect(page.locator(".file-markdown")).toBeVisible();
        await page.locator(".workspace-switches button").first().click();
        await page.screenshot({
          path: resolve(
            evidence,
            `${locale}-${theme}-${width}-files-terminal.png`,
          ),
        });
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true);
        expect(errors).toEqual([]);
        await context.close();
      });
    }

test("global schedules, project selection and keyboard account menu", async ({
  page,
}) => {
  const f = await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "Scheduled", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Scheduled", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".schedule-row")).toContainText(
    "Daily experiment summary",
  );
  expect(f.calls.some((c) => c.path === "/api/schedules")).toBe(true);
  await page
    .getByRole("button", { name: "Create schedule", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Project", exact: true }).click();
  await dialog
    .getByRole("option", { name: "Battery research", exact: true })
    .click();
  await dialog
    .getByRole("combobox", { name: "Target Session", exact: true })
    .selectOption("analysis");
  await dialog.getByRole("button", { name: "Project", exact: true }).click();
  await dialog
    .getByRole("option", { name: "Research notes", exact: true })
    .click();
  await expect(
    dialog.getByRole("combobox", { name: "Target Session", exact: true }),
  ).toHaveValue("");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByLabel("Account", { exact: true }).click();
  await expect(
    page.getByRole("menuitem", { name: "Theme", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(
    page.getByRole("menuitem", { name: "Sign out", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByLabel("Account", { exact: true })).toBeFocused();
});

test("mobile drawer keyboard and fullscreen restore", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  await page.getByLabel("Open navigation", { exact: true }).click();
  await expect(page.locator(".project-sidebar")).not.toHaveAttribute("inert");
  await page.keyboard.press("Escape");
  await expect(
    page.getByLabel("Open navigation", { exact: true }),
  ).toBeFocused();
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  await page.getByRole("button", { name: "README.md", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "File tree", exact: true }),
  ).toHaveCount(0);
  await page.getByLabel("Enter Full Screen", { exact: true }).click();
  await expect(page.locator(".file-workspace-host")).toHaveCSS(
    "position",
    "fixed",
  );
  await page.keyboard.press("Escape");
  await expect(
    page.getByLabel("Enter Full Screen", { exact: true }),
  ).toBeFocused();
});

test("a missing session in an owned project does not navigate to shared conversations", async ({
  page,
}) => {
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=missing");
  await expect(
    page.getByText("This conversation is unavailable.", { exact: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(/project=battery&session=missing/);
});

test("native timeline retains tool/thinking/text positions during streaming, completion and reload", async ({
  page,
}) => {
  await workspaceFixture(page);
  let completed = false;
  const items = [
    {
      id: "u",
      cursor: 1,
      turn_id: "t",
      kind: "user",
      content: "Draw a heart",
      message_id: "u",
    },
    {
      id: "m/0",
      cursor: 2,
      turn_id: "t",
      kind: "thinking",
      content: "Prepare the script",
    },
    {
      id: "tool/write",
      cursor: 3,
      turn_id: "t",
      kind: "tool",
      name: "write",
      arguments: '{"path":"/workspace/README.md"}',
      result: '{"ok":true}',
      state: "completed",
    },
    {
      id: "m2/0",
      cursor: 4,
      turn_id: "t",
      kind: "assistant",
      content: "Created `/workspace/README.md`.",
    },
    {
      id: "tool/bash",
      cursor: 5,
      turn_id: "t",
      kind: "tool",
      name: "bash",
      arguments: '{"command":"python heart.py"}',
      state: "running",
    },
    {
      id: "m3/0",
      cursor: 6,
      turn_id: "t",
      kind: "thinking",
      content: "Check the output",
    },
    {
      id: "m3/1",
      cursor: 6,
      turn_id: "t",
      kind: "assistant",
      content: "Open `README.md` to inspect",
      state: "streaming",
    },
  ];
  await page.route("**/sessions/analysis/snapshot?**", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: completed ? "idle" : "running",
          revision: completed ? 2 : 1,
          control_revision: 1,
          deletion: 0,
        },
        messages: [],
        turns: completed ? [] : [{ id: "t", state: "running" }],
        record_cursor: 6,
        has_earlier: false,
        timeline: items.map((x) =>
          completed ? { ...x, state: "completed" } : x,
        ),
      }),
    }),
  );
  await page.goto("/?project=battery&session=analysis");
  const rows = page.locator("[data-timeline-id]");
  await expect(rows).toHaveCount(7);
  const keys = await rows.evaluateAll((nodes) =>
    nodes.map((n) => n.getAttribute("data-timeline-id")),
  );
  expect(keys).toEqual(items.map((x) => x.id));
  await page.locator('[data-activity-id="m/0"] > .activity-summary').click();
  await page.locator('[data-timeline-id="tool/write"] summary').click();
  completed = true;
  await expect(page.locator(".streaming")).toHaveCount(0);
  expect(
    await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getAttribute("data-timeline-id")),
    ),
  ).toEqual(keys);
  await expect(page.locator('[data-timeline-id="tool/write"]')).toHaveAttribute(
    "open",
    "",
  );
  await expect(page.locator(".file-mention")).toHaveCount(0);
  await page
    .getByRole("link", { name: "/workspace/README.md", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "File README.md", exact: true }),
  ).toContainText("Battery research");
  await page.reload();
  await expect(rows).toHaveCount(7);
  expect(
    await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getAttribute("data-timeline-id")),
    ),
  ).toEqual(keys);
  await expect(
    page.getByRole("link", { name: "README.md", exact: true }),
  ).toBeVisible();
});

test("consecutive activity has one live ripple line and keeps its phase on completion", async ({
  page,
}) => {
  await workspaceFixture(page);
  let phase = 0;
  const base = {
    id: "thought",
    cursor: 2,
    turn_id: "t",
    kind: "thinking",
    content: "Checking the measurements and preparing the next experiment",
  };
  await page.route("**/sessions/analysis/snapshot?**", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: phase === 2 ? "idle" : "running",
          revision: phase + 1,
          control_revision: 1,
          deletion: 0,
        },
        messages: [],
        turns: phase === 2 ? [] : [{ id: "t", state: "running" }],
        record_cursor: phase + 2,
        has_earlier: false,
        timeline: [
          {
            id: "user",
            cursor: 1,
            turn_id: "t",
            kind: "user",
            content: "Run the experiment",
          },
          { ...base, state: phase === 0 ? "streaming" : "completed" },
          ...(phase > 0
            ? [
                {
                  id: "bash",
                  cursor: 3,
                  turn_id: "t",
                  kind: "tool",
                  name: "bash",
                  arguments: JSON.stringify({
                    command:
                      "python /workspace/heart.py --check " +
                      "measurements ".repeat(30),
                  }),
                  state: phase === 1 ? "running" : "completed",
                },
              ]
            : []),
          ...(phase === 2
            ? [
                {
                  id: "edit",
                  cursor: 4,
                  turn_id: "t",
                  kind: "tool",
                  name: "edit",
                  state: "completed",
                },
                {
                  id: "read",
                  cursor: 5,
                  turn_id: "t",
                  kind: "tool",
                  name: "read",
                  state: "completed",
                },
                {
                  id: "answer",
                  cursor: 6,
                  turn_id: "t",
                  kind: "assistant",
                  content: "Finished.",
                },
              ]
            : []),
        ],
      }),
    }),
  );
  await page.goto("/?project=battery&session=analysis");
  const group = page.locator(".activity-group");
  const summary = group.locator(":scope > .activity-summary");
  await expect(group).toHaveCount(1);
  await expect(summary).toContainText("Thinking");
  await expect(summary).toContainText("Checking the measurements");
  await expect(summary.locator(".activity-line")).toHaveCSS(
    "animation-name",
    "activity-ripple",
  );
  await expect(page.locator(".working-indicator")).toHaveCount(0);
  mkdirSync(evidence, { recursive: true });
  await page.screenshot({
    path: resolve(evidence, "activity-thinking-live.png"),
  });
  await summary.focus();
  await page.keyboard.press("Enter");
  await expect(summary).toHaveAttribute("aria-expanded", "true");
  phase = 1;
  await expect(group.locator(".tool-trace > summary")).toContainText(
    "Running bash",
  );
  await expect(group.locator(".tool-trace > summary")).toContainText(
    "python /workspace/heart.py",
  );
  await expect(group).toHaveAttribute("data-activity-id", "thought");
  await expect(summary).toHaveAttribute("aria-expanded", "true");
  await summary.click();
  await page.screenshot({ path: resolve(evidence, "activity-tool-live.png") });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(summary.locator(".activity-line")).toHaveCSS(
    "animation-name",
    "none",
  );
  await expect(summary.locator(".activity-line")).not.toHaveCSS(
    "color",
    "rgba(0, 0, 0, 0)",
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(summary).toBeVisible();
  expect(
    await summary.evaluate((n) => n.getBoundingClientRect().height),
  ).toBeLessThan(45);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  phase = 2;
  await expect(summary).toContainText("bash, edit, read • Tool activity (3)");
  await expect(group).not.toHaveClass(/is-live/);
  await expect(page.locator(".chat-message.assistant")).toHaveText("Finished.");
  await summary.click();
  expect(
    await group
      .locator("[data-timeline-id]")
      .evaluateAll((ns) => ns.map((n) => n.getAttribute("data-timeline-id"))),
  ).toEqual(["thought", "bash", "edit", "read"]);
  await page.reload();
  await expect(summary).toContainText("bash, edit, read • Tool activity (3)");
});

test("turn timer begins before creation responds", async ({ page }) => {
  await workspaceFixture(page);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/projects/battery/sessions", async (route) => {
    if (route.request().method() === "POST") await gate;
    await route.fallback();
  });
  await page.goto("/?project=battery&draft=1");
  await page
    .getByLabel("New message", { exact: true })
    .fill("Start timing immediately");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(
    page.locator(".pending-first-turn .chat-message.user"),
  ).toHaveText("Start timing immediately");
  await expect(page.locator(".pending-first-turn .turn-work")).toContainText(
    "Worked for",
  );
  const initial = await page
    .locator(".pending-first-turn .turn-work")
    .innerText();
  await expect
    .poll(() => page.locator(".pending-first-turn .turn-work").innerText())
    .not.toBe(initial);
  release();
  await expect(page.locator(".agent-panel")).toContainText(
    "Start timing immediately",
  );
});

test("completed turn folds intermediate output, freezes elapsed time and provides answer actions", async ({
  page,
  context,
}) => {
  await workspaceFixture(page);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  let phase = "running";
  const start = new Date(Date.now() - 2000).toISOString(),
    finished = new Date(Date.parse(start) + 54000).toISOString();
  const timeline = [
    {
      id: "user1",
      cursor: 1,
      turn_id: "t",
      kind: "user",
      content: "Analyze the experiment",
      message_id: "user1",
    },
    {
      id: "thought1",
      cursor: 2,
      turn_id: "t",
      kind: "thinking",
      content: "Consider the samples",
      state: "completed",
    },
    {
      id: "mid",
      cursor: 3,
      turn_id: "t",
      kind: "assistant",
      content: "Checking the data first.",
      message_id: "middle",
      state: "completed",
    },
    {
      id: "read",
      cursor: 4,
      turn_id: "t",
      kind: "tool",
      name: "read",
      arguments: '{"path":"/workspace/README.md"}',
      result: "{}",
      state: "completed",
    },
    {
      id: "final",
      cursor: 5,
      turn_id: "t",
      kind: "assistant",
      content: "Final answer with **clear results**.",
      message_id: "answer",
      state: "completed",
    },
  ];
  await page.route("**/sessions/analysis/snapshot?**", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: phase === "running" ? "running" : "idle",
          revision: phase === "running" ? 1 : 2,
          control_revision: 1,
          deletion: 0,
        },
        messages: [
          {
            id: "answer",
            role: "assistant",
            content: "Final answer with **clear results**.",
            sequence: 3,
            turn_id: "t",
            created_at: finished,
          },
        ],
        turns: [
          {
            id: "t",
            state: phase,
            created_at: start,
            updated_at: finished,
            content: "Analyze the experiment",
            sequence: 1,
          },
        ],
        turn_summaries: [
          {
            id: "t",
            state: phase,
            created_at: start,
            ...(phase === "completed"
              ? { finished_at: finished, final_message_id: "answer" }
              : {}),
          },
        ],
        timeline,
        record_cursor: 5,
        has_earlier: false,
      }),
    }),
  );
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".turn-work")).toContainText("Worked for");
  await expect(page.locator('[data-message-id="middle"]')).toBeVisible();
  await expect(page.locator(".answer-actions")).toHaveCount(0);
  await expect(page.locator(".working-indicator")).toHaveCount(0);
  phase = "completed";
  await expect(page.locator(".turn-work-toggle")).toHaveText("Worked for 54s");
  await expect(page.locator(".turn-process")).not.toBeVisible();
  await expect(page.locator(".turn-final")).toContainText("Final answer");
  await expect(page.locator(".answer-actions button")).toHaveCount(3);
  await expect(page.locator(".answer-actions time")).toHaveAttribute(
    "datetime",
    finished,
  );
  await page.getByRole("button", { name: "Copy answer", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    "Final answer with **clear results**.",
  );
  await page.getByRole("button", { name: "Like", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Like", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Dislike", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Like", exact: true }),
  ).toHaveAttribute("aria-pressed", "false");
  await page.locator(".turn-work-toggle").click();
  await expect(page.locator('[data-message-id="middle"]')).toBeVisible();
  await page.locator(".turn-work-toggle").click();
  await expect(page.locator('[data-message-id="middle"]')).not.toBeVisible();
  await page.screenshot({
    path: resolve(evidence, "turn-completed-collapsed.png"),
  });
  await page.reload();
  await expect(page.locator(".turn-work-toggle")).toHaveText("Worked for 54s");
  await expect(page.locator(".turn-process")).not.toBeVisible();
  await expect(
    page.getByRole("button", { name: "Dislike", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.goto("/?project=battery&session=analysis&message=middle");
  await expect(page.locator('[data-message-id="middle"]')).toBeVisible();
  await page.screenshot({
    path: resolve(evidence, "turn-completed-expanded.png"),
  });
  phase = "failed";
  await page.reload();
  await expect(page.locator(".turn-process")).toBeVisible();
  await expect(page.locator(".answer-actions")).toHaveCount(0);
  await expect(page.locator(".turn-outcome")).toContainText("Failed");
});

test("composer grows, caps, shades overflow and uses Enter/Shift+Enter with IME protection", async ({
  page,
}) => {
  const f = await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  const input = page.getByLabel("Message Compare cycle performance", {
    exact: true,
  });
  await input.waitFor();
  const small = (await input.boundingBox())!.height;
  await input.fill("Line one\nLine two\nLine three\nLine four");
  expect((await input.boundingBox())!.height).toBeGreaterThan(small);
  await expect(input).toHaveCSS("resize", "none");
  await input.fill(
    Array.from({ length: 40 }, (_, i) => `Research line ${i}`).join("\n"),
  );
  const height = (await input.boundingBox())!.height;
  expect(height).toBeLessThanOrEqual(280);
  expect(await input.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(
    true,
  );
  const wrapper = page.locator(".composer-input-scroll");
  await input.evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(wrapper).toHaveAttribute("data-scroll-top", "false");
  await expect(wrapper).toHaveAttribute("data-scroll-bottom", "true");
  await input.evaluate((el) => {
    el.scrollTop = 100;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(wrapper).toHaveAttribute("data-scroll-top", "true");
  await expect(wrapper).toHaveAttribute("data-scroll-bottom", "true");
  await input.evaluate((el) => {
    el.scrollTop = el.scrollHeight;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(wrapper).toHaveAttribute("data-scroll-bottom", "false");
  await input.fill("hello");
  expect((await input.boundingBox())!.height).toBe(small);
  await input.press("End");
  await input.press("Shift+Enter");
  await input.press("x");
  await expect(input).toHaveValue("hello\nx");
  expect(
    f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/turns")),
  ).toHaveLength(0);
  await input.dispatchEvent("compositionstart");
  await input.press("Enter");
  expect(
    f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/turns")),
  ).toHaveLength(0);
  await input.dispatchEvent("compositionend");
  await input.dispatchEvent("keydown", {
    key: "Enter",
    code: "Enter",
    isComposing: true,
    keyCode: 229,
  });
  expect(
    f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/turns")),
  ).toHaveLength(0);
  await input.fill("Send with Enter");
  await input.press("Enter");
  await expect
    .poll(
      () =>
        f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/turns"))
          .length,
    )
    .toBe(1);
  await expect(input).toHaveValue("");
  await page
    .getByRole("button", { name: "New Chat", exact: true })
    .first()
    .click();
  const draft = page.getByLabel("New message", { exact: true });
  await draft.fill("first\nsecond\nthird\nfourth");
  expect((await draft.boundingBox())!.height).toBeGreaterThan(small);
  await draft.fill("New chat Enter");
  await draft.press("Enter");
  await expect
    .poll(
      () =>
        f.calls.filter((c) => c.method === "POST" && c.path.endsWith("/turns"))
          .length,
    )
    .toBe(2);
});

test("project row toggles folder icon, whole-row hover and keeps menu independent", async ({
  page,
}) => {
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  const project = page.getByRole("button", {
    name: "Battery research",
    exact: true,
  });
  await expect(project).toHaveAttribute("aria-expanded", "true");
  await expect(project.locator(".lucide-folder-open")).toHaveCount(1);
  await project.click();
  await expect(project).toHaveAttribute("aria-expanded", "false");
  await expect(project.locator(".lucide-folder")).toHaveCount(1);
  await expect(page.locator(".agent-panel")).toBeVisible();
  await project.click();
  await expect(project).toHaveAttribute("aria-expanded", "true");
  const session = page.getByRole("button", {
    name: "Prepare the research report",
    exact: true,
  });
  await session.hover();
  const row = session.locator("..");
  expect(
    await row.evaluate((el) => getComputedStyle(el).backgroundColor),
  ).not.toBe("rgba(0, 0, 0, 0)");
  await expect(session).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
  await expect(row).toHaveCSS(
    "background-color",
    await page
      .locator(".session-row.selected")
      .evaluate((el) => getComputedStyle(el).backgroundColor),
  );
  await page
    .getByLabel("Project actions Battery research", { exact: true })
    .click();
  await expect(project).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("menu")).toBeVisible();
  await page.keyboard.press("Escape");
  await project.hover();
  expect(
    await project
      .locator("..")
      .evaluate((el) => getComputedStyle(el).backgroundColor),
  ).not.toBe("rgba(0, 0, 0, 0)");
});

test("failed tool stays inside details without a summary badge", async ({
  page,
}) => {
  await workspaceFixture(page);
  await page.route("**/sessions/analysis/snapshot?**", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: "idle",
          revision: 1,
        },
        messages: [],
        turns: [],
        record_cursor: 1,
        has_earlier: false,
        timeline: [
          {
            id: "bad-tool",
            cursor: 1,
            turn_id: "t",
            kind: "tool",
            name: "bash",
            state: "failed",
            result: "command failed",
          },
        ],
      }),
    }),
  );
  await page.goto("/?project=battery&session=analysis");
  const group = page.locator(".activity-group");
  await expect(group.locator(":scope > .activity-summary")).toHaveText(
    "bash • Tool activity (1)",
  );
  await expect(group.locator(".activity-failure")).toHaveCount(0);
  await group.locator(":scope > .activity-summary").click();
  await expect(group.locator(".tool-trace > summary")).toContainText("Failed");
});

test("turn rail previews with a hover wave, tracks reading and jumps across history pages", async ({
  page,
}) => {
  await workspaceFixture(page);
  const all = Array.from({ length: 130 }, (_, i) => ({
    id: `turn-${i + 1}`,
    sequence: i + 1,
    state: "completed",
    created_at: "2026-09-22T00:00:00Z",
    user_preview: `Question ${i + 1}`,
    assistant_preview: `Final result ${i + 1}`,
  }));
  let indexPages = 0;
  const requestedAnchors: string[] = [];
  await page.route("**/sessions/analysis/turn-index?**", (route) => {
    indexPages++;
    const before = Number(
      new URL(route.request().url()).searchParams.get("before"),
    );
    const available = all.filter((t) => !before || t.sequence < before),
      turns = available.slice(-100);
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        turns,
        ...(available.length > 100 ? { next_before: turns[0].sequence } : {}),
      }),
    });
  });
  await page.route("**/sessions/analysis/snapshot?**", (route) => {
    const params = new URL(route.request().url()).searchParams;
    const target = params.get("turn"),
      before = Number(params.get("before")),
      after = Number(params.get("after"));
    if (target) requestedAnchors.push(target);
    const n = Number(target?.split("-").at(-1));
    const entries = target
      ? all.filter(
          (t) => t.sequence >= Math.max(1, n - 2) && t.sequence <= n + 2,
        )
      : before
        ? all.filter((t) => t.sequence * 2 < before).slice(-3)
        : after
          ? all.filter((t) => t.sequence * 2 > after).slice(0, 3)
          : all.slice(-3);
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        session: {
          id: "analysis",
          project_id: "battery",
          title: "Compare cycle performance",
          state: "idle",
          revision: 1,
          control_revision: 1,
          deletion: 0,
        },
        messages: entries.flatMap((t) => [
          {
            id: `u${t.sequence}`,
            role: "user",
            content: t.user_preview,
            sequence: t.sequence * 2,
            turn_id: t.id,
            created_at: t.created_at,
          },
          {
            id: `a${t.sequence}`,
            role: "assistant",
            content:
              t.assistant_preview +
              "\n\n" +
              "A detailed explanation of the result.\n\n".repeat(10),
            sequence: t.sequence * 2 + 1,
            turn_id: t.id,
            created_at: t.created_at,
          },
        ]),
        turns: [],
        turn_summaries: entries.map((t) => ({
          id: t.id,
          sequence: t.sequence,
          state: "completed",
          created_at: t.created_at,
          finished_at: "2026-09-22T00:00:05Z",
          final_message_id: `a${t.sequence}`,
        })),
        timeline: entries.flatMap((t) => [
          {
            id: `u${t.sequence}`,
            cursor: t.sequence * 2,
            turn_id: t.id,
            kind: "user",
            content: t.user_preview,
            message_id: `u${t.sequence}`,
          },
          {
            id: `a${t.sequence}`,
            cursor: t.sequence * 2 + 1,
            turn_id: t.id,
            kind: "assistant",
            content:
              t.assistant_preview +
              "\n\n" +
              "A detailed explanation of the result.\n\n".repeat(10),
            message_id: `a${t.sequence}`,
          },
        ]),
        record_cursor: 1,
        has_earlier: (entries[0]?.sequence || 1) > 1,
        has_later: (entries.at(-1)?.sequence || 130) < 130,
      }),
    });
  });
  await page.goto("/?project=battery&session=analysis");
  const nav = page.getByRole("navigation", {
    name: "Turn navigation",
    exact: true,
  });
  await expect(nav.locator(".turn-tick")).toHaveCount(100);
  const last = page.getByRole("button", {
    name: "Jump to turn 130",
    exact: true,
  });
  await expect(last).toHaveAttribute("aria-current", "step");
  await last.hover();
  await expect(page.getByRole("tooltip")).toContainText("Question 130");
  await expect(page.getByRole("tooltip")).toContainText("Final result 130");
  await expect(last.locator("span")).toHaveCSS("width", "30px");
  expect(
    await nav
      .locator('[data-nav-turn="turn-129"] span')
      .evaluate((el) => el.getBoundingClientRect().width),
  ).toBeGreaterThan(
    await nav
      .locator('[data-nav-turn="turn-128"] span')
      .evaluate((el) => el.getBoundingClientRect().width),
  );
  await page.screenshot({
    path: resolve(evidence, "turn-navigation-hover.png"),
  });
  await last.focus();
  await page.keyboard.press("ArrowUp");
  await expect(
    page.getByRole("button", { name: "Jump to turn 129", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await page.locator(".conversation-scroll").evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(
    page.getByRole("button", { name: "Jump to turn 128", exact: true }),
  ).toHaveAttribute("aria-current", "step");
  await page.locator(".turn-rail").evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(nav.locator(".turn-tick")).toHaveCount(130);
  expect(indexPages).toBeGreaterThan(1);
  const old = page.getByRole("button", { name: "Jump to turn 5", exact: true });
  await old.click();
  await expect(page.locator('[data-turn-id="turn-5"]')).toBeVisible();
  await expect(
    page.locator('[data-turn-id="turn-5"] .turn-final'),
  ).toContainText("Final result 5");
  await expect(page.locator('[data-turn-id="turn-4"]')).toBeAttached();
  await expect(page.locator('[data-turn-id="turn-6"]')).toBeAttached();
  await expect(page.locator(".conversation-scroll > button")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Return to latest", exact: true }),
  ).toHaveCount(1);
  await expect(
    page.getByRole("button", { name: "Earlier messages", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Later messages", exact: true }),
  ).toHaveCount(0);

  const alignment = await page
    .locator('[data-turn-id="turn-5"] .chat-message.user')
    .evaluate(
      (el) =>
        el.getBoundingClientRect().top -
        document.querySelector(".conversation-scroll")!.getBoundingClientRect()
          .top,
    );
  expect(Math.abs(alignment - 16)).toBeLessThan(3);
  await expect(old).toHaveAttribute("aria-current", "step");
  const sibling = page.getByRole("button", {
    name: "Jump to turn 6",
    exact: true,
  });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await sibling.click();
  expect(requestedAnchors).not.toContain("turn-6");
  await expect(page.locator('[data-turn-id="turn-4"]')).toBeAttached();
  await expect(page.locator('[data-turn-id="turn-7"]')).toBeAttached();
  await page
    .locator(".conversation-scroll")
    .dispatchEvent("wheel", { deltaY: 1000 });
  await page.locator(".conversation-scroll").evaluate((el) => {
    el.scrollTop = el.scrollHeight;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(page.locator('[data-turn-id="turn-8"]')).toBeAttached();
  await expect(page.locator('[data-turn-id="turn-3"]')).toBeAttached();
  await page
    .locator(".conversation-scroll")
    .dispatchEvent("wheel", { deltaY: -1000 });
  await page.locator(".conversation-scroll").evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });
  await expect(page.locator('[data-turn-id="turn-2"]')).toBeAttached();
  await expect(page.locator('[data-turn-id="turn-8"]')).toBeAttached();
  await old.click();
  await expect(old).toHaveAttribute("aria-current", "step");
  await page.reload();
  await expect(page.locator('[data-turn-id="turn-5"]')).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Jump to turn 5", exact: true }),
  ).toHaveAttribute("aria-current", "step");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  const focused = page.locator('.turn-tick[aria-current="step"]');
  await focused.hover();
  await expect(focused.locator("span")).toHaveCSS("transition-duration", "0s");
  const card = await page.getByRole("tooltip").boundingBox();
  expect(card!.x + card!.width).toBeLessThanOrEqual(390);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page
    .getByRole("button", { name: "Return to latest", exact: true })
    .click();
  await expect(page.locator('[data-turn-id="turn-130"]')).toBeAttached();
  await expect(page.locator('[data-turn-id="turn-128"]')).toBeAttached();
  await expect(
    page.getByRole("button", { name: "Jump to turn 130", exact: true }),
  ).toHaveAttribute("aria-current", "step");
});

test("slow transcript loading is not starved by periodic refresh", async ({
  page,
}) => {
  await workspaceFixture(page);
  let calls = 0;
  await page.route("**/sessions/analysis/snapshot?**", async (route) => {
    calls++;
    await new Promise((resolve) => setTimeout(resolve, 3200));
    await route.fallback();
  });
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".chat-message.assistant")).toContainText("95%");
  expect(calls).toBe(1);
  await expect(page.locator(".turn-navigator")).toBeVisible();
});

for (const theme of ["light", "dark"] as const)
  test(`composer fades text only at overflowing edges (${theme})`, async ({
    page,
  }) => {
    await workspaceFixture(page);
    await page.addInitScript(
      (theme) => localStorage.setItem("project-theme", theme),
      theme,
    );
    await page.goto("/?project=battery&session=analysis");
    const input = page.getByLabel("Message Compare cycle performance", {
        exact: true,
      }),
      wrapper = page.locator(".composer-input-scroll");
    await input.waitFor();
    await expect(input).toHaveCSS("mask-image", "none");
    await expect(wrapper).toHaveAttribute("data-scroll-top", "false");
    await expect(wrapper).toHaveAttribute("data-scroll-bottom", "false");
    expect(
      await wrapper.evaluate((el) => getComputedStyle(el, "::before").content),
    ).toBe("none");
    expect(
      await wrapper.evaluate((el) => getComputedStyle(el, "::after").content),
    ).toBe("none");
    const text = Array.from(
      { length: 32 },
      (_, i) =>
        `${i + 1}. Review the battery measurements and compare the results.`,
    ).join("\n");
    await input.fill(text);
    await input.evaluate((el) => {
      el.scrollTop = 0;
      el.dispatchEvent(new Event("scroll"));
    });
    await expect(input).not.toHaveCSS("mask-image", "none");
    await expect(input).toHaveCSS("--text-fade-top", "0px");
    await expect(input).toHaveCSS("--text-fade-bottom", "20px");
    await input.evaluate((el) => {
      el.scrollTop = 113;
      el.dispatchEvent(new Event("scroll"));
    });
    await expect(wrapper).toHaveAttribute("data-scroll-top", "true");
    await expect(wrapper).toHaveAttribute("data-scroll-bottom", "true");
    await page.screenshot({
      path: resolve(evidence, `composer-text-fade-${theme}.png`),
    });
    await input.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
      el.dispatchEvent(new Event("scroll"));
    });
    await expect(input).toHaveCSS("--text-fade-bottom", "0px");
    await input.fill("Short note");
    await expect(input).toHaveCSS("mask-image", "none");
    await input.fill(text);
    await input.fill("");
    await expect(input).toHaveCSS("mask-image", "none");
    await expect(wrapper).toHaveAttribute("data-scroll-top", "false");
    await expect(wrapper).toHaveAttribute("data-scroll-bottom", "false");
    expect(await input.evaluate((el) => el.scrollTop)).toBe(0);
    await page.screenshot({
      path: resolve(evidence, `composer-empty-${theme}.png`),
    });
    await input.fill(" \n".repeat(40));
    await expect(input).toHaveCSS("mask-image", "none");
    await input.fill("");
    await page
      .getByRole("button", { name: "New Chat", exact: true })
      .first()
      .click();
    const draft = page.getByLabel("New message", { exact: true });
    await draft.fill(text);
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(draft).not.toHaveCSS("mask-image", "none");
    await draft.fill("");
    await expect(draft).toHaveCSS("mask-image", "none");
    await expect(wrapper).toHaveAttribute("data-scroll-bottom", "false");
  });

test("slow session lists complete while refresh ticks are coalesced", async ({
  page,
}) => {
  await workspaceFixture(page);
  let requests = 0;
  await page.route("**/api/projects/battery/sessions?*", async (route) => {
    requests++;
    await new Promise((resolve) => setTimeout(resolve, 3500));
    await route.fallback();
  });
  await page.goto("/?project=battery&session=analysis");
  await expect(
    page.getByRole("button", {
      name: "Prepare the research report",
      exact: true,
    }),
  ).toBeVisible();
  expect(requests).toBeLessThanOrEqual(2);
});

test("saving a file preserves edits typed while the upload is pending", async ({
  page,
}) => {
  const f = await workspaceFixture(page);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let uploading = false;
  await page.route("**/files/content?*", async (route) => {
    if (route.request().method() === "PUT") {
      uploading = true;
      await gate;
    }
    await route.fallback();
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "the research report" }).click();
  await page.getByLabel("File actions", { exact: true }).click();
  await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
  const editor = page.getByLabel("Edit reports/summary.md", { exact: true });
  await editor.fill("saved text");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect.poll(() => uploading).toBe(true);
  await editor.fill("saved text plus newer edits");
  release();
  await expect.poll(() => f.files["reports/summary.md"]).toBe("saved text");
  await expect(editor).toContainText("saved text plus newer edits");
  await expect(page.locator(".files-panel")).toContainText("Unsaved changes");
});
