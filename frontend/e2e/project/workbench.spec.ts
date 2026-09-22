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
  await first.getByRole("button", { name: "Stop turn", exact: true }).click();
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
    if (file === "preview.xlsx")
      await page.evaluate(() =>
        (window as Window & { officeDrawn?: Set<string> }).officeDrawn?.clear(),
      );
    await page
      .getByRole("button", { name: new RegExp(file) })
      .first()
      .click();
    const preview = page.locator(".office-preview");
    await expect(preview).toBeVisible();
    if (text) await expect(preview).toContainText(text, { timeout: 15000 });
    else {
      await expect
        .poll(
          () =>
            page.evaluate(() => [
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
    await expect(
      page.getByRole("menuitem", { name: "Edit text", exact: true }),
    ).toBeDisabled();
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
