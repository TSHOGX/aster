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
    .locator(".terminal-grid .xterm-helper-textarea")
    .first();
  await terminal.focus();
  await page.keyboard.insertText(text);
  await page.keyboard.press("Enter");
}
async function terminalText(page: Page) {
  return page.locator(".terminal-grid .xterm-screen").first().innerText();
}

test("Project agents, retained terminals and Files through the Go edge", async ({
  page,
  context,
}) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(error.message));
  await login(page);
  page.once("dialog", (dialog) => dialog.accept("Browser Project"));
  await page.getByRole("button", { name: "New Project", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: /Browser Project/ }),
  ).toBeVisible();
  const project = new URL(page.url()).searchParams.get("project")!;
  await page.getByRole("button", { name: "New terminal", exact: true }).click();
  await expect(page.locator(".terminal-grid .xterm")).toBeVisible();
  await expect(page.locator(".terminal-grid")).toContainText("input");
  await terminalInput(
    page,
    "printf browser-terminal > /workspace/browser.txt; printf 'PTY-READY\\n'",
  );
  await expect.poll(() => terminalText(page)).toContain("PTY-READY");
  await expect(
    page.getByRole("button", { name: /browser.txt/ }).first(),
  ).toBeVisible();
  await page.getByRole("button", { name: "New agent", exact: true }).click();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  await page.getByRole("button", { name: "New agent", exact: true }).click();
  await expect(page.locator(".agent-panel")).toHaveCount(2);
  const first = page.locator(".agent-panel").nth(0);
  const second = page.locator(".agent-panel").nth(1);
  const firstID = await first.getAttribute("data-session-id");
  const secondID = await second.getAttribute("data-session-id");
  await first.locator("textarea").fill("First independent Agent conversation");
  await second
    .locator("textarea")
    .fill("Second independent Agent conversation");
  await first.getByRole("button", { name: "Send", exact: true }).click();
  await second.getByRole("button", { name: "Send", exact: true }).click();
  // The local model waits for both requests before answering either one.
  await expect(first).toContainText("deterministic-local-reply", {
    timeout: 45000,
  });
  await expect(second).toContainText("deterministic-local-reply", {
    timeout: 45000,
  });
  await expect(first).not.toContainText(
    "Second independent Agent conversation",
  );
  await expect(second).not.toContainText(
    "First independent Agent conversation",
  );
  await page
    .getByRole("button", { name: /browser.txt/ })
    .first()
    .click();
  await expect(
    page.getByRole("region", { name: "File browser.txt" }),
  ).toContainText("browser-terminal");
  await page.getByRole("button", { name: "Edit text", exact: true }).click();
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
  // Closing one Agent view does not remount Project Files or the terminal.
  await first.getByRole("button", { name: "Close view", exact: true }).click();
  await expect(page.locator(".agent-panel")).toHaveCount(1);
  await expect(
    page.getByRole("region", { name: "File browser.txt" }),
  ).toBeVisible();
  await expect.poll(() => terminalText(page)).toContain("READ-FILES-DONE");
  await page
    .locator(".child-strip")
    .getByRole("button", { name: /First independent/ })
    .click();
  await expect(page.locator(".agent-panel")).toHaveCount(2);
  // A second observer cannot acquire input while this view owns it.
  const observer = await context.newPage();
  await observer.goto(`/?project=${project}`);
  await observer
    .locator(".terminal-chip")
    .getByRole("button", { name: /^Terminal 1 ·/ })
    .click();
  await expect(observer.locator(".terminal-grid")).toContainText("read-only");
  await page
    .locator(".terminal-grid")
    .getByRole("button", { name: "Close view", exact: true })
    .click();
  await expect(page.locator(".terminal-grid")).toHaveCount(0);
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
  await expect(observer.locator(".terminal-grid")).toContainText("input");
  await terminalInput(observer, "printf '世界\\n'; sleep 30");
  await observer
    .locator(".terminal-grid")
    .getByRole("button", { name: "Ctrl-C", exact: true })
    .click();
  await terminalInput(observer, "printf 'AFTER-INTERRUPT\\n'");
  await expect.poll(() => terminalText(observer)).toContain("AFTER-INTERRUPT");
  await observer.setViewportSize({ width: 1350, height: 850 });
  await terminalInput(observer, "stty size; printf 'RESIZED\\n'");
  await expect.poll(() => terminalText(observer)).toContain("RESIZED");
  await observer.close();
  await page.reload();
  await expect(page.locator(".agent-panel")).toHaveCount(2);
  await page
    .locator(".terminal-chip")
    .getByRole("button", { name: /^Terminal 1 ·/ })
    .click();
  await expect(page.locator(".terminal-grid")).toContainText("input");
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
    .locator(".terminal-grid")
    .getByRole("button", { name: "Close view", exact: true })
    .click();
  await page
    .locator(".terminal-chip")
    .getByRole("button", { name: /^Terminal 1 ·/ })
    .click();
  await expect(page.locator(".terminal-grid")).toContainText(
    "Output is missing",
  );
  await expect(page.locator(`[data-session-id="${firstID}"]`)).toContainText(
    "deterministic-local-reply",
  );
  await expect(page.locator(`[data-session-id="${secondID}"]`)).toContainText(
    "deterministic-local-reply",
  );
  await page.screenshot({
    path:
      process.env.PROJECT_BROWSER_SCREENSHOT ||
      "test-results/project/workbench.png",
    fullPage: true,
  });
  expect(failures).toEqual([]);
  await page
    .locator(".terminal-chip")
    .getByRole("button", { name: "Terminate", exact: true })
    .click();
  await expect(page.locator(".terminal-chip")).toContainText("exited");
  await page.getByRole("button", { name: "New terminal", exact: true }).click();
  const secondTerminal = page.locator(".terminal-grid > section").last();
  await expect(secondTerminal).toContainText("input");
  await secondTerminal
    .getByRole("button", { name: "Ctrl-D", exact: true })
    .click();
  await expect(secondTerminal).toContainText("exited");
});
