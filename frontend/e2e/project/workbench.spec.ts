import { chooseOption } from "../picker";
import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";

async function login(page: Page) {
  await page.goto("/");
  await page.getByLabel("Username", { exact: true }).fill("researcher");
  await page
    .getByLabel("Password", { exact: true })
    .fill("isolated-browser-fixture");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "New Project", exact: true }),
  ).toBeVisible();
}
async function terminalInput(page: Page, text: string) {
  const terminal = page
    .locator(".terminal-dock .xterm-helper-textarea")
    .first();
  await terminal.focus();
  await page.keyboard.insertText(text);
  await page.keyboard.press("Enter");
}
async function terminalText(page: Page) {
  return page.locator(".terminal-dock .xterm-screen").first().innerText();
}

test("Project agents, retained terminals and Files through the Go edge", async ({
  page,
  context,
}) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(error.message));
  await page.addInitScript(() => {
    const drawn = new Set<string>();
    (window as Window & { officeDrawn?: Set<string> }).officeDrawn = drawn;
    const original = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (text, x, y, width) {
      if (["Sample", "Capacity", "100", "95"].includes(text)) drawn.add(text);
      return width === undefined
        ? original.call(this, text, x, y)
        : original.call(this, text, x, y, width);
    };
  });
  await login(page);
  await page.getByRole("button", { name: "New Project", exact: true }).click();
  await page.getByRole("dialog").getByRole("textbox").fill("Browser Project");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create a Project", exact: true })
    .click();
  await expect(page.getByLabel("New message", { exact: true })).toBeVisible();
  const project = new URL(page.url()).searchParams.get("project")!;
  await page
    .getByRole("button", { name: "Toggle Terminal", exact: true })
    .click();
  await page.getByRole("button", { name: "New terminal", exact: true }).click();
  await expect(page.locator(".terminal-dock .xterm")).toBeVisible();
  await expect(page.locator(".terminal-dock")).toContainText("input");
  await terminalInput(
    page,
    "printf browser-terminal > /workspace/browser.txt; printf 'PTY-READY\\n'",
  );
  await expect.poll(() => terminalText(page)).toContain("PTY-READY");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "browser.txt", exact: true }),
  ).toBeVisible();
  await page
    .getByLabel("New message", { exact: true })
    .fill("First independent Agent conversation");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  const firstID = await page
    .locator(".agent-panel")
    .getAttribute("data-session-id");
  await page
    .getByRole("button", { name: "New Chat", exact: true })
    .first()
    .click();
  await page
    .getByLabel("New message", { exact: true })
    .fill("Second independent Agent conversation");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  const secondID = await page
    .locator(".agent-panel")
    .getAttribute("data-session-id");
  const first = page.locator(`[data-session-id="${firstID}"]`),
    second = page.locator(`[data-session-id="${secondID}"]`);
  await expect(second).toContainText("deterministic-local-reply", {
    timeout: 45000,
  });
  await expect(second).not.toContainText(
    "First independent Agent conversation",
  );
  await page.locator(`[data-session-link="${firstID}"]`).click();
  // The local model waits for both requests before answering either one.
  await expect(first).toContainText("deterministic-local-reply", {
    timeout: 45000,
  });
  await expect(first).not.toContainText(
    "Second independent Agent conversation",
  );
  await first.locator("textarea").fill("long-for-interrupt");
  await first.getByRole("button", { name: "Send", exact: true }).click();
  await expect(first.locator(".streaming")).toBeVisible();
  await first.getByRole("button", { name: "Stop current task", exact: true }).click();
  await expect
    .poll(async () => {
      const result = await context.request.get(
        `/api/projects/${project}/sessions/${firstID}/turns`,
      );
      return (await result.json()).turns.find(
        (turn: { content: string }) => turn.content === "long-for-interrupt",
      )?.state;
    })
    .toBe("canceled");
  await page
    .getByRole("button", { name: /browser.txt/ })
    .first()
    .click();
  await expect(
    page.getByRole("region", { name: "File browser.txt" }),
  ).toContainText("browser-terminal");
  await page.getByRole("button", { name: "File actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
  await page
    .getByLabel("Edit browser.txt", { exact: true })
    .fill("Files edit while a terminal stays live\n");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(
    page.getByLabel("Edit browser.txt", { exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("region", { name: "File browser.txt" }),
  ).toContainText("Files edit while a terminal stays live");
  await terminalInput(
    page,
    "cat /workspace/browser.txt; printf 'READ-FILES-DONE\\n'",
  );
  await page.getByRole("button", { name: "File actions", exact: true }).click();
  const downloaded = page.waitForEvent("download");
  await page.getByRole("menuitem", { name: "Download", exact: true }).click();
  const original = await downloaded;
  expect(readFileSync((await original.path())!, "utf8")).toBe(
    "Files edit while a terminal stays live\n",
  );
  await expect
    .poll(() => terminalText(page))
    .toContain("Files edit while a terminal stays live");
  const terminalResponse = await context.request.get(
    `/api/projects/${project}/terminals`,
  );
  const terminalID = (await terminalResponse.json()).terminals[0].id as string;
  // Switching conversations retains the shared preview and attached terminal.
  await page.locator(`[data-session-link="${secondID}"]`).click();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  await expect(
    page.getByRole("region", { name: "File browser.txt" }),
  ).toBeVisible();
  await expect.poll(() => terminalText(page)).toContain("READ-FILES-DONE");
  await page.locator(`[data-session-link="${firstID}"]`).click();
  // A second observer cannot acquire input while this view owns it.
  const observer = await context.newPage();
  await observer.goto(`/?project=${project}`);
  await expect(observer.locator(".terminal-dock")).toContainText("read-only");
  await page
    .getByRole("button", { name: "Hide terminal", exact: true })
    .click();
  await expect(page.locator(".terminal-dock")).toHaveCount(0);
  const blockers = await context.request.get(
    `/api/projects/${project}/blockers`,
  );
  expect(
    (await blockers.json()).blockers.some(
      (entry: { kind: string }) => entry.kind === "terminal_process",
    ),
  ).toBe(true);
  await observer
    .getByRole("button", { name: "Take input", exact: true })
    .click();
  await expect(observer.locator(".terminal-dock")).toContainText("input");
  await terminalInput(observer, "printf '世界\\n'; sleep 30");
  await observer
    .getByRole("button", { name: "Terminal input", exact: true })
    .click();
  await observer.getByRole("menuitem", { name: "Ctrl-C", exact: true }).click();
  await terminalInput(observer, "printf 'AFTER-INTERRUPT\\n'");
  await expect.poll(() => terminalText(observer)).toContain("AFTER-INTERRUPT");
  await observer.setViewportSize({ width: 1350, height: 850 });
  await terminalInput(observer, "stty size; printf 'RESIZED\\n'");
  await expect.poll(() => terminalText(observer)).toContain("RESIZED");
  await observer.close();
  await page.reload();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  await page
    .getByRole("button", { name: "Toggle Terminal", exact: true })
    .click();
  await expect(page.locator(".terminal-dock")).toContainText("input");
  const unchanged = await context.request.get(
    `/api/projects/${project}/terminals/${terminalID}`,
  );
  expect((await unchanged.json()).state).toBe("running");
  const forbidden = await context.request.post(
    `/api/projects/${project}/terminals/${terminalID}/stop`,
    { headers: { Origin: "https://foreign.invalid" } },
  );
  expect(forbidden.status()).toBe(403);
  // A sustained PTY flood must not delay another Agent completion.
  const returningAgent = page.locator(`[data-session-id="${firstID}"]`);
  const beforeReplies = await returningAgent
    .locator(".chat-message.assistant")
    .count();
  await terminalInput(
    page,
    "for i in $(seq 1 80); do head -c 10000 /dev/zero | tr '\\0' x; sleep 0.03; done; printf '\\nFLOOD-DONE\\n'",
  );
  const began = Date.now();
  await returningAgent
    .locator("textarea")
    .fill("Complete while the terminal emits output");
  await returningAgent
    .getByRole("button", { name: "Send", exact: true })
    .click();
  await expect(returningAgent.locator(".chat-message.assistant")).toHaveCount(
    beforeReplies + 1,
    { timeout: 10000 },
  );
  console.log(`Agent completion under PTY output: ${Date.now() - began} ms`);
  await expect.poll(() => terminalText(page)).toMatch(/\nFLOOD-DONE/);
  await page
    .getByRole("button", { name: "Hide terminal", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Toggle Terminal", exact: true })
    .click();
  await expect(page.locator(".terminal-dock")).toContainText(
    "Output is missing",
  );
  await expect(page.locator(`[data-session-id="${firstID}"]`)).toContainText(
    "deterministic-local-reply",
  );
  await page.locator(`[data-session-link="${secondID}"]`).click();
  await expect(page.locator(`[data-session-id="${secondID}"]`)).toContainText(
    "deterministic-local-reply",
  );
  const documents = readFileSync("e2e/project/documents.py").toString("base64");
  await terminalInput(
    page,
    `python3 -c "import base64; exec(base64.b64decode('${documents}'))"`,
  );
  await expect
    .poll(() => terminalText(page), { timeout: 20000 })
    .toContain("DOCUMENTS-READY");
  for (const [file, text] of [
    ["preview.docx", "Reviewed document preview"],
    ["preview.pptx", "Reviewed presentation preview"],
    ["preview.xlsx", ""],
  ]) {
    await page
      .getByRole("button", { name: new RegExp(file) })
      .first()
      .click();
    const preview = page.locator(".office-preview");
    await expect(preview).toBeVisible();
    await expect(preview).toHaveAttribute("data-preview-phase", "ready", {
      timeout: 15000,
    });
    const content = page
      .frameLocator(".office-frame")
      .locator(".office-frame-content");
    if (text) await expect(content).toContainText(text, { timeout: 15000 });
    else {
      await expect
        .poll(
          () =>
            content.evaluate(() => [
              ...((window as Window & { officeDrawn?: Set<string> })
                .officeDrawn ?? []),
            ]),
          { timeout: 15000 },
        )
        .toEqual(expect.arrayContaining(["Sample", "Capacity", "100", "95"]));
    }
    await page
      .getByRole("button", { name: "File actions", exact: true })
      .click();
    await expect(page.getByRole("menu", { name: "File actions", exact: true })).toBeVisible();
    await expect(
      page.getByRole("menuitem", { name: "Edit text", exact: true }),
    ).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "Download", exact: true })).toBeEnabled();
    await page.keyboard.press("Escape");
    await expect(page.locator(".selected-file .notice.error")).toHaveCount(0);
  }
  await page.screenshot({
    path:
      process.env.PROJECT_BROWSER_SCREENSHOT ||
      "test-results/project/workbench.png",
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "large-preview.md", exact: true })
    .click();
  const plain = page.locator(".preview-text-page code");
  await expect(plain).toContainText("# Large Markdown");
  const completeMarkdown =
    "# Large Markdown\n\n" +
    "| Sample | Value |\n| --- | --- |\n| A | 100 |\n\n".repeat(20000) +
    "\nComplete large document.\n";
  let reconstructed = "";
  const sectionSize = 64 * 1024;
  for (let offset = 0; offset < completeMarkdown.length; offset += sectionSize) {
    await expect(plain).toHaveText(completeMarkdown.slice(offset, offset + sectionSize));
    reconstructed += await plain.textContent();
    if (offset + sectionSize < completeMarkdown.length)
      await page.getByRole("button", { name: "Next section", exact: true }).click();
  }
  expect(reconstructed).toBe(completeMarkdown);
  await page.getByRole("button", { name: "File actions", exact: true }).click();
  const largeDownload = page.waitForEvent("download");
  await page.getByRole("menuitem", { name: "Download", exact: true }).click();
  expect(readFileSync((await (await largeDownload).path())!, "utf8")).toBe(
    completeMarkdown,
  );
  await page
    .getByRole("button", { name: /browser.txt/ })
    .first()
    .click();
  await expect(
    page.getByRole("region", { name: "File browser.txt" }),
  ).toContainText("Files edit while a terminal stays live");
  await page.setViewportSize({ width: 1590, height: 990 });
  await terminalInput(page, "printf 'AFTER-PREVIEW-CLOSE\\n'");
  await expect.poll(() => terminalText(page)).toContain("AFTER-PREVIEW-CLOSE");
  expect(failures).toEqual([]);
  await page
    .getByRole("button", { name: "Terminal actions", exact: true })
    .click();
  await page.getByRole("menuitem", { name: "Terminate", exact: true }).click();
  await expect(page.locator(".terminal-panel")).toContainText("exited");
  await page.getByRole("button", { name: "New terminal", exact: true }).click();
  const secondTerminal = page.locator(".terminal-panel");
  await expect(secondTerminal).toContainText("input");
  await secondTerminal
    .getByRole("button", { name: "Terminal input", exact: true })
    .click();
  await page.getByRole("menuitem", { name: "Ctrl-D", exact: true }).click();
  await expect(secondTerminal).toContainText("exited");
  const prior = await context.request.get(
    `/api/projects/${project}/terminals/${terminalID}`,
  );
  const priorIncarnation = (await prior.json()).incarnation;
  await page.getByRole("tab", { name: "Terminal 1", exact: true }).click();
  await page
    .getByRole("button", { name: "Terminal actions", exact: true })
    .click();
  await page
    .getByRole("menuitem", { name: "Restart terminal", exact: true })
    .click();
  await expect(page.locator(".terminal-panel")).toContainText("input");
  await terminalInput(page, "printf 'REOPENED-NATIVE\\n'");
  await expect.poll(() => terminalText(page)).toContain("REOPENED-NATIVE");
  const fresh = await context.request.get(
    `/api/projects/${project}/terminals/${terminalID}`,
  );
  expect((await fresh.json()).incarnation).not.toBe(priorIncarnation);
});

test("held queue prompt Steers into the active native run", async ({ page, context }) => {
  await login(page);
  const origin = new URL(process.env.PROJECT_BROWSER_URL!).origin;
  const project = "browser-queued-steer";
  const headers = { Origin: origin };
  const created = await context.request.post("/api/projects", { headers, data: { id: project, title: "Queued guidance" } });
  expect(created.status()).toBe(201);
  try {
    await page.goto(`/?project=${project}`);
    const newMessage = page.getByLabel("New message", { exact: true });
    await expect(newMessage).toBeVisible();
    await newMessage.fill("long-for-owner-handoff");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    const panel = page.locator(".agent-panel");
    await expect(panel.getByRole("button", { name: "Stop current task", exact: true })).toBeVisible();
    await panel.locator("textarea").fill("second question");
    await panel.getByRole("button", { name: "Conversation actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Hold prompt", exact: true }).click();
    const queue = panel.locator(".prompt-queue").last();
    await expect(queue).toBeVisible();
    await queue.locator("summary").click();
    const row = queue.locator(".prompt-queue-row").filter({ hasText: "second question" });
    await expect(row.getByRole("button", { name: /Steer: second question/ })).toBeEnabled();
    await expect(panel.locator(".chat-message.user").filter({ hasText: "second question" })).toHaveCount(0);
    const sessionID = await panel.getAttribute("data-session-id");
    const admitted = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith(`/sessions/${sessionID}/control`));
    await row.getByRole("button", { name: /Steer: second question/ }).click();
    const receipt = await admitted;
    expect(receipt.status()).toBe(202);
    expect((await receipt.json()).delivery_id).toBeTruthy();
    const anchor = panel.locator(".chat-message.user").filter({ hasText: "second question" });
    await expect(anchor).toHaveCount(1, { timeout: 30000 });
    await expect(row).toHaveCount(0, { timeout: 30000 });
    await expect(panel.locator(".chat-message.assistant").last()).toContainText("deterministic-local-reply");
  } finally {
    const current = await context.request.get(`/api/projects/${project}`);
    const revision = (await current.json()).revision;
    expect((await context.request.delete(`/api/projects/${project}?revision=${revision}`, { headers })).status()).toBe(202);
  }
});

test.describe("Chinese workbench", () => {
  test.use({ locale: "zh-CN" });
  test("localized login and Project navigation", async ({ page }) => {
    await page.goto("/");
    await page.getByLabel("用户名", { exact: true }).fill("researcher");
    await page
      .getByLabel("密码", { exact: true })
      .fill("isolated-browser-fixture");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.getByRole("button", { name: "新建项目", exact: true }).click();
    await page.getByRole("dialog").getByRole("textbox").fill("中文项目");
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "创建项目", exact: true })
      .click();
    await expect(page.getByLabel("新消息", { exact: true })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "新对话", exact: true }).first(),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "切换终端", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "文件预览", exact: true }),
    ).toBeVisible();
  });
});

test("schedule metadata opens complete editable prompts through the Go edge", async ({ page, context }) => {
  await login(page);
  const origin = new URL(process.env.PROJECT_BROWSER_URL!).origin;
  const project = "browser-schedule-index";
  const headers = { Origin: origin };
  const created = await context.request.post("/api/projects", { headers, data: { id: project, title: "Scheduled research" } });
  expect(created.status()).toBe(201);
  const prompt = "Complete scheduled prompt\n".repeat(1000);
  const scheduled = await context.request.post(`/api/projects/${project}/schedules`, { headers, data: { id: "browser-schedule", title: "Editable task", prompt, timezone: "UTC", cron: "", enabled: true } });
  expect(scheduled.status()).toBe(201);
  try {
    await page.getByRole("button", { name: "Scheduled", exact: true }).click();
    await expect(page.locator(".schedule-row")).toContainText("Editable task");
    const overview = await context.request.get("/api/schedules?limit=50");
    expect(await overview.text()).not.toContain("Complete scheduled prompt");
    await page.locator(".schedule-description").filter({ hasText: "Editable task" }).click();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.locator(".schedule-editor textarea")).toHaveValue(prompt);
    const revised = prompt + "Updated without truncation\n";
    await page.locator(".schedule-editor textarea").fill(revised);
    await page.locator(".schedule-editor").getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const saved = await context.request.get(`/api/projects/${project}/schedules/browser-schedule`);
    expect((await saved.json()).prompt).toBe(revised);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await chooseOption(page, "Repeat", "Interval");
    await page.getByRole("spinbutton", { name: "Repeat every (minutes)", exact: true }).fill("90");
    await page.locator(".schedule-editor").getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.locator(".schedule-reading")).toContainText("Every 90 minutes");
    const interval = await (await context.request.get(`/api/projects/${project}/schedules/browser-schedule`)).json();
    expect(interval.cron).toBe("@every 90m");
    expect(Date.parse(interval.next_at) - Date.now()).toBeGreaterThan(89 * 60 * 1000);
    const preview = await (await context.request.post("/api/schedules/preview", { headers, data: { cron: interval.cron, timezone: interval.timezone, next_at: interval.next_at } })).json();
    expect(preview.occurrences[0]).toBe(interval.next_at);
    expect(Date.parse(preview.occurrences[1]) - Date.parse(preview.occurrences[0])).toBe(90 * 60 * 1000);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.locator(".schedule-editor textarea").fill(revised + "Keep interval cadence\n");
    await page.locator(".schedule-editor").getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.locator(".schedule-reading")).toBeVisible();
    const renamed = await (await context.request.get(`/api/projects/${project}/schedules/browser-schedule`)).json();
    expect(renamed.next_at).toBe(interval.next_at);
    await page.getByRole("button", { name: "Pause", exact: true }).click();
    await expect(page.locator(".schedule-row")).toContainText("Paused");
    const disabled = await context.request.get(`/api/projects/${project}/schedules/browser-schedule`);
    const data = await disabled.json();
    expect(data.prompt).toBe(revised + "Keep interval cadence\n");
    expect(data.enabled).toBe(false);
  } finally {
    const current = await context.request.get(`/api/projects/${project}`);
    expect((await context.request.delete(`/api/projects/${project}?revision=${(await current.json()).revision}`, { headers })).status()).toBe(202);
  }
});
