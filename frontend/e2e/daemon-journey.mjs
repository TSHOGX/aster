// No request interception: browser -> Next proxy -> real daemon -> real Khora.
import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:net";
import { fileURLToPath } from "node:url";
import path from "node:path";
import assert from "node:assert/strict";

const backend = process.argv[2];
assert(backend, "daemon origin required");
const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const portSocket = createServer().listen(0, "127.0.0.1");
await once(portSocket, "listening");
const port = portSocket.address().port;
await new Promise((resolve) => portSocket.close(resolve));
const origin = `http://127.0.0.1:${port}`;
const next = spawn(process.execPath, ["node_modules/next/dist/bin/next", "dev", "--webpack", "--hostname", "127.0.0.1", "--port", String(port)], {
  cwd: frontend,
  env: { ...process.env, HARNESS_API_BASE_URL: backend, HARNESS_DEV_ORIGINS: "127.0.0.1", NEXT_TELEMETRY_DISABLED: "1" },
  stdio: ["ignore", "pipe", "pipe"],
});
let serverLog = "";
for (const stream of [next.stdout, next.stderr]) stream.on("data", (data) => { serverLog = (serverLog + data).slice(-16000); });
let browser;
let page;
let session;
const browserLog = [];
try {
  const deadline = Date.now() + 90000;
  for (;;) {
    if (next.exitCode !== null) throw new Error(`Next exited: ${serverLog}`);
    try { const response = await fetch(origin, { signal: AbortSignal.timeout(3000) }); if (response.ok) break; } catch {}
    if (Date.now() > deadline) throw new Error(`Next readiness timed out: ${serverLog}`);
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--no-sandbox"] });
  const context = await browser.newContext({ locale: "en-US", viewport: { width: 1440, height: 900 } });
  page = await context.newPage();
  page.on("requestfailed", (request) => browserLog.push(`${request.url()}: ${request.failure()?.errorText}`));
  page.setDefaultTimeout(30000);
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  let streamConnections = 0;
  page.on("response", (response) => { if (response.url().includes("/api/events/stream") && response.status() === 200) streamConnections++; });
  await page.goto(origin);
  await page.getByLabel("Username", { exact: true }).fill("alice");
  await page.getByLabel("Password", { exact: true }).fill("synthetic-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  const created = page.waitForResponse((r) => r.url().endsWith("/api/sessions") && r.request().method() === "POST");
  await page.getByRole("button", { name: "New conversation", exact: true }).click();
  const creation = await created;
  assert.equal(creation.status(), 201, await creation.text());
  session = await creation.json();
  assert.equal(session.mode, "shell");
  await page.locator("textarea").fill("printf 'browser-streamed-ok\\n'; printf 'browser-file-ok' > /workspace/browser.txt");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await page.getByText("browser-streamed-ok", { exact: true }).waitFor();
  await page.getByRole("button", { name: "Run", exact: true }).waitFor();
  await page.reload();
  await page.getByText("browser-streamed-ok", { exact: true }).waitFor();
  const file = await context.request.get(`${origin}/artifacts/${session.id}/browser.txt`);
  assert.equal(file.status(), 200);
  assert.equal(await file.text(), "browser-file-ok");
  await page.locator("textarea").fill("printf 'browser-interrupt-start\\n'; sleep 30");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
  await page.getByRole("button", { name: "Stop", exact: true }).click();
  // Stop is durable, but the interrupted projection can arrive after the
  // current page has rendered. Reload once to exercise the reconnect path and
  // wait for the authoritative idle runtime state.
  await page.reload();
  await page.getByRole("button", { name: "Run", exact: true }).waitFor();
  assert(streamConnections >= 2, `SSE did not reconnect: ${streamConnections}`);
  assert.deepEqual(pageErrors, []);
  const removed = await context.request.delete(`${origin}/api/sessions/${session.id}`, { headers: { Origin: origin } });
  assert([200, 503].includes(removed.status()));
  console.log(JSON.stringify({ browser: "chromium", real_backend: true, mode: session.mode, sse_connections: streamConnections, checks: ["login", "create", "turn", "reload", "workspace", "interrupt"] }));
} catch (error) {
  console.error(serverLog);
  console.error(browserLog);
  if (page) {
    console.error(await page.locator("body").innerText());
    if (session) console.error(await (await page.request.get(`${origin}/api/sessions/${session.id}/messages/page`)).text());
  }
  throw error;
} finally {
  if (browser) await browser.close();
  next.kill("SIGTERM");
  const timeout = setTimeout(() => next.kill("SIGKILL"), 5000);
  if (next.exitCode === null) await once(next, "exit");
  clearTimeout(timeout);
}
