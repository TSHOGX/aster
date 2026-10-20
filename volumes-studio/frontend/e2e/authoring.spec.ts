import { test, expect, Page } from "@playwright/test";
import { createHash } from "node:crypto";
const origin = "https://127.0.0.1:18443";
test.beforeEach(async ({ page }) => {
  page.on("dialog", (dialog) => {
    if (dialog.type() === "beforeunload") void dialog.accept();
    else throw new Error(`Unexpected browser dialog: ${dialog.type()}`);
  });
});
async function signIn(page: Page) {
  const response = await page.request.post(`${origin}/api/auth/login`, {
    headers: { Origin: origin },
    data: { username: "studio-admin", password: "browser-fixture-password" },
  });
  expect(response.ok()).toBeTruthy();
  await page.goto("/volumes-studio/");
  await page.getByRole("link", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "New library", exact: true }),
  ).toBeVisible();
}
async function createLibrary(page: Page, name: string) {
  await page.getByRole("button", { name: "New library", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByLabel("Description").fill("Reusable research source files.");
  await page.getByRole("button", { name: "Create draft", exact: true }).click();
  await expect(page).toHaveURL(/\/libraries\/[^/?]+/);
  await provisionBranch(page);
}
async function provisionBranch(page: Page) {
  await expect(page).toHaveURL(/\/libraries\/[^/?]+/);
  const id = libraryID(page);
  const identity = await session(page);
  // Creating a library grants its creator every Studio action; no self-grant is needed.
  await page.reload();
  await page.getByLabel("New branch name", { exact: true }).fill("Human edits");
  await page.getByRole("button", { name: "Create branch from main", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Working branch", exact: true })).toHaveValue(/.+/);
}
function branchRoot(page: Page) {
  const branch = new URL(page.url()).searchParams.get("branch");
  expect(branch).toBeTruthy();
  return `/volumes-studio/api/v1/libraries/${libraryID(page)}/branches/${branch}`;
}
async function savedContent(page: Page, path: string) {
  const root = branchRoot(page);
  const branch = await (await page.request.get(root)).json();
  return page.request.get(`${root}/draft/content?${new URLSearchParams({path, revision: String(branch.draft_revision)})}`);
}
async function acceptBranch(page: Page, checkpoint = true) {
 if (checkpoint) {
  await page.getByLabel("Commit message", { exact: true }).fill("Reviewed candidate");
  await page.getByRole("button", { name: "Commit saved draft", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: "Committed saved revision" })).toBeVisible();
 }
  await page.getByRole("button", { name: "Prepare integration", exact: true }).click();
  await page.getByRole("button", { name: "Approve integration", exact: true }).click();
  await page.getByRole("button", { name: "Merge approved integration", exact: true }).click();
  await expect(page.getByText("Acceptance is complete. Publishing creates a version; it does not deploy to Projects.")).toBeVisible();
}
async function publishAccepted(page: Page) {
  const response = page.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith(`/libraries/${libraryID(page)}/versions`));
  await page.getByRole("button", { name: "Publish accepted commit", exact: true }).click();
  const reply = await response;
  expect(reply.status(), await reply.text()).toBe(202);
  const version = await reply.json();
  await expect.poll(async () => (await (await page.request.get(`/volumes-studio/api/v1/libraries/${libraryID(page)}/versions/${version.id}`)).json()).state).toBe("published");
  return version;
}
async function createFile(page: Page, name: string) {
  await page.getByRole("button", { name: "Add files", exact: true }).click();
  await page.getByRole("menuitem", { name: "New file", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: `Contents of ${name}` }),
  ).toBeVisible();
}
async function editorText(page: Page, name: string, text: string) {
  const editor = page.getByRole("textbox", { name: `Contents of ${name}` });
  await editor.fill(text);
  return editor;
}
async function session(page: Page) {
  return (await page.request.get("/volumes-studio/api/v1/session")).json();
}
function libraryID(page: Page) {
  return new URL(page.url()).pathname.split("/").filter(Boolean).at(-1)!;
}

test("central login, multi-tab edit, publish, immutable version and logout", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await signIn(page);
  await createLibrary(page, "Research handbook " + Date.now());
  await createFile(page, "readme.md");
  await editorText(page, "readme.md", "# Reviewed handbook\n\nFirst version.");
  await createFile(page, "config.json");
  await editorText(page, "config.json", '{"enabled":true}');
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await page.getByRole("tab", { name: "readme.md" }).click();
  await expect(
    page.getByRole("textbox", { name: "Contents of readme.md" }),
  ).toContainText("First version.");
  let saved!: () => void;
  let releaseSave!: () => void;
  const saveArrived = new Promise<void>((resolve) => {
    saved = resolve;
  });
  const saveReply = new Promise<void>((resolve) => {
    releaseSave = resolve;
  });
  await page.route("**/draft/text", async (route) => {
    if (route.request().postDataJSON().path === "readme.md") {
      const response = await route.fetch();
      saved();
      await saveReply;
      await route.fulfill({ response });
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await saveArrived;
  await expect(page.getByText("Unsaved editor changes are excluded from this commit.")).toBeVisible();
  let checkpoints = 0;
  page.on("request", request => { if (request.method() === "POST" && request.url().endsWith("/commits")) checkpoints++; });
  await page.getByLabel("Commit message", {exact:true}).fill("Save then checkpoint");
  await page.getByRole("button", {name:"Commit saved draft",exact:true}).click();
  await expect(page.getByRole("button", {name:"Commit saved draft",exact:true})).toBeDisabled();
  expect(checkpoints).toBe(0);
  releaseSave();
  await expect(page.getByRole("status").filter({hasText:"Committed saved revision"})).toBeVisible();
  expect(checkpoints).toBe(1);
  await page.unroute("**/draft/text");
  await acceptBranch(page, false);
  await page.getByLabel("Version label").fill("v1");
  await page.getByLabel("Release note").fill("Reviewed starter handbook.");
  const published = await publishAccepted(page);
  await page.goto(`/volumes-studio/libraries/${libraryID(page)}?branch=${new URL(page.url()).searchParams.get("branch")}&version=${published.id}`);
  await page
    .getByRole("button", { name: "Open readme.md", exact: true })
    .click();
  await expect(page.locator(".cm-content")).toHaveAttribute(
    "contenteditable",
    "false",
  );
  await expect(page.locator(".cm-content")).toContainText("First version.");
  const id = libraryID(page);
  const version = new URL(page.url()).searchParams.get("version");
  await page
    .getByRole("button", { name: "Select version", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Draft Editable working copy" })
    .click();
  await page
    .getByRole("button", { name: "Open readme.md", exact: true })
    .click();
  await editorText(page, "readme.md", "Later draft edit");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save file", exact: true }),
  ).toBeDisabled();
  const frozen = await page.request.get(
    `/volumes-studio/api/v1/libraries/${id}/versions/${version}/content?path=readme.md`,
  );
  expect(await frozen.text()).toBe("# Reviewed handbook\n\nFirst version.");
  expect(frozen.headers()["content-security-policy"]).toContain("sandbox");
  await page.screenshot({
    path: `test-results/studio-${test.info().project.name}-desktop.png`,
  });
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page
    .getByRole("menuitem", { name: "Sign out everywhere in this session" })
    .click();
  await expect(
    page.getByRole("link", { name: "Sign in", exact: true }),
  ).toBeVisible();
  expect((await page.request.get("/api/auth/session")).status()).toBe(401);
  expect(errors).toEqual([]);
});

test("local recovery and external conflict retain text", async ({ page }) => {
  await signIn(page);
  await createLibrary(page, "Recovery " + Date.now());
  await createFile(page, "notes.md");
  await editorText(page, "notes.md", "Unsaved local research");
  await expect
    .poll(async () =>
      page.evaluate(async () => {
        const db = await new Promise<IDBDatabase>((resolve, reject) => {
          const r = indexedDB.open("volume-studio-recovery", 1);
          r.onsuccess = () => resolve(r.result);
          r.onerror = () => reject(r.error);
        });
        return new Promise<number>((resolve) => {
          const r = db.transaction("buffers").objectStore("buffers").count();
          r.onsuccess = () => resolve(r.result);
        });
      }),
    )
    .toBeGreaterThan(0);
  await page.reload();
  await page
    .getByRole("button", { name: "Review recovery", exact: true })
    .click();
  await page.getByRole("button", { name: "Restore", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "Contents of notes.md" }),
  ).toContainText("Unsaved local research");
  const id = libraryID(page),
    s = await session(page);
  const branchID = new URL(page.url()).searchParams.get("branch");
  expect(branchID).toBeTruthy();
  const branch = await (
    await page.request.get(`/volumes-studio/api/v1/libraries/${id}/branches/${branchID}`)
  ).json();
  const update = await page.request.post(
    `/volumes-studio/api/v1/libraries/${id}/branches/${branchID}/draft/text`,
    {
      headers: { Origin: origin, "X-CSRF-Token": s.csrf },
      data: {
        request_id: "external-" + Date.now(),
        expected_revision: branch.draft_revision,
        expected_head: branch.head,
        path: "notes.md",
        text: "External saved version",
      },
    },
  );
  expect(update.ok()).toBeTruthy();
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "Resolve conflict · notes.md" }),
  ).toBeVisible();
  await expect(page.getByRole("dialog")).toContainText(
    "External saved version",
  );
  await expect(page.getByRole("dialog")).toContainText(
    "Unsaved local research",
  );
});

test("resume durable upload and fit narrow workbench", async ({ page }) => {
  await signIn(page);
  await createLibrary(page, "Resumable dataset " + Date.now());
  const id = libraryID(page),
    s = await session(page);
  const data = Buffer.from([0, 1, 2, 3, 255, 42]);
  const hash = (bytes: Buffer) =>
    "sha256:" + createHash("sha256").update(bytes).digest("hex");
  const headers = { Origin: origin, "X-CSRF-Token": s.csrf };
  const root = `/volumes-studio/api/v1/libraries/${id}`;
  const admitted = await page.request.post(`${root}/uploads`, {
    headers,
    data: {
      request_id: "resume-admit-" + id,
      branch_id: new URL(page.url()).searchParams.get("branch"),
      expected_revision: 1,
      path: "dataset.bin",
      size: "6",
      digest: hash(data),
    },
  });
  expect(admitted.status()).toBe(201);
  const upload = await admitted.json();
  expect(
    (
      await page.request.put(`${root}/uploads/${upload.id}/chunks/0`, {
        headers: {
          ...headers,
          "X-Chunk-Digest": hash(data.subarray(0, 3)),
          "Content-Type": "application/octet-stream",
        },
        data: data.subarray(0, 3),
      })
    ).ok(),
  ).toBeTruthy();
  await page.reload();
  await page.getByLabel("Commit message", { exact: true }).fill("Upload pending");
  await page.getByRole("button", { name: "Commit saved draft", exact: true }).click();
  await expect(page.getByRole("region", { name: "Commit saved draft", exact: true }).getByRole("alert")).toBeVisible();
  await page.getByLabel("Select files", { exact: true }).setInputFiles({
    name: "dataset.bin",
    mimeType: "application/octet-stream",
    buffer: data,
  });
  await expect
    .poll(
      async () =>
        (await (await page.request.get(`${root}/uploads/${upload.id}`)).json())
          .state,
      { timeout: 20000 },
    )
    .toBe("complete");
  expect(
    await (
      await savedContent(page, "dataset.bin")
    ).body(),
  ).toEqual(data);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("button", { name: "Show files", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Show files", exact: true }).click();
  const files = page.getByRole("dialog", { name: "Files", exact: true });
  await expect(files).toBeVisible();
  await expect(
    files.getByRole("button", { name: "Open dataset.bin" }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(files).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Show files", exact: true }),
  ).toBeFocused();
  await page.screenshot({
    path: `test-results/studio-${test.info().project.name}-mobile.png`,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("same-origin cookies and prefix routing", async ({ page }) => {
  await signIn(page);
  const cookies = await page.context().cookies();
  expect(cookies.find((c) => c.name === "project_login")?.path).toBe("/");
  const studio = cookies.find(
    (c) => c.name === "__Secure-volumes_studio_session",
  );
  expect(studio?.path).toBe("/volumes-studio/");
  expect(studio?.httpOnly).toBe(true);
  expect(studio?.secure).toBe(true);
  expect((await page.request.get("/distribution/v1/exports")).status()).toBe(
    404,
  );
  expect((await page.request.get("/volumes-studio-other")).status()).toBe(404);
});

test("save response preserves subsequent typing and file tabs preserve undo", async ({
  page,
}) => {
  await signIn(page);
  await createLibrary(page, "Save timing " + Date.now());
  await createFile(page, "a.md");
  await page
    .getByRole("textbox", { name: "Contents of a.md" })
    .pressSequentially("first submission");
  let release!: () => void;
  let arrived!: () => void;
  const pending = new Promise<void>((r) => (arrived = r));
  const gate = new Promise<void>((r) => (release = r));
  await page.route("**/draft/text", async (route) => {
    const body = route.request().postDataJSON();
    if (body.text === "first submission") {
      const response = await route.fetch();
      arrived();
      await gate;
      await route.fulfill({ response });
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await pending;
  await page
    .getByRole("textbox", { name: "Contents of a.md" })
    .press("ControlOrMeta+a");
  await page
    .getByRole("textbox", { name: "Contents of a.md" })
    .pressSequentially("typed during save");
  release();
  await expect(
    page.getByRole("button", { name: "Save file", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("textbox", { name: "Contents of a.md" }),
  ).toContainText("typed during save");
  await page.unroute("**/draft/text");
  await createFile(page, "b.md");
  await editorText(page, "b.md", "second file");
  await page.getByRole("tab", { name: "a.md" }).click();
  const editor = page.getByRole("textbox", { name: "Contents of a.md" });
  await expect(editor).toContainText("typed during save");
  await editor.press("ControlOrMeta+z");
  await expect(editor).toContainText("first submission");
  await page.getByRole("tab", { name: "b.md" }).click();
  await expect(
    page.getByRole("textbox", { name: "Contents of b.md" }),
  ).toContainText("second file");
});

test("file menus, changes, dark theme and responsive workspace", async ({
  page,
}) => {
  await signIn(page);
  await createLibrary(page, "Workspace " + Date.now());
  await createFile(page, "before.md");
  await editorText(page, "before.md", "# Workspace\n\nA useful document.");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save file", exact: true }),
  ).toBeDisabled();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "Actions for before.md", exact: true })
    .click();
  await page.getByRole("menuitem", { name: "Rename", exact: true }).click();
  await page.getByLabel("New file name").fill("after.md");
  await page.getByLabel("New file name").press("Enter");
  await expect(
    page.getByRole("button", { name: "Open after.md", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("tab", { name: "after.md" })).toBeVisible();
  await page.getByRole("button", { name: "Changes", exact: false }).click();
  await page.getByRole("button", { name: "after.md added" }).click();
  await expect(page.getByLabel("File differences")).toContainText("Workspace");
  await page.getByRole("button", { name: "Files", exact: true }).click();
  await page.getByRole("button", { name: "preview", exact: true }).click();
  await expect(page.locator(".markdown-preview")).toContainText(
    "A useful document.",
  );
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.screenshot({
    path: `test-results/studio-${test.info().project.name}-dark.png`,
  });
  await page.setViewportSize({ width: 1024, height: 768 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.evaluate(() => {
    document.documentElement.style.zoom = "2";
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.evaluate(() => {
    document.documentElement.style.zoom = "1";
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("button", { name: "Show files", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("unavailable recovery storage leaves editing and saved-file closing usable", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, "indexedDB", {
      value: {
        open() {
          throw new Error("Storage denied for qualification");
        },
      },
    });
  });
  await signIn(page);
  await createLibrary(page, "Storage fallback " + Date.now());
  await createFile(page, "fallback.md");
  await editorText(page, "fallback.md", "Retained in this tab");
  await expect(page.getByText(/Local recovery unavailable/)).toBeVisible();
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save file", exact: true }),
  ).toBeDisabled();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "Close fallback.md", exact: true })
    .click();
  await expect(page.getByRole("tab", { name: "fallback.md" })).toHaveCount(0);
  await page
    .getByRole("button", { name: "Open fallback.md", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Contents of fallback.md" }),
  ).toContainText("Retained in this tab");
});

test("late file response cannot replace a newer selection", async ({
  page,
}) => {
  await signIn(page);
  await createLibrary(page, "Selection ordering " + Date.now());
  await createFile(page, "slow.md");
  await createFile(page, "fast.md");
  await page
    .getByRole("button", { name: "Close slow.md", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Close fast.md", exact: true })
    .click();
  let release!: () => void;
  let arrived!: () => void;
  const gate = new Promise<void>((r) => (release = r)),
    waiting = new Promise<void>((r) => (arrived = r));
  await page.route("**/draft/content?**", async (route) => {
    if (new URL(route.request().url()).searchParams.get("path") === "slow.md") {
      const response = await route.fetch();
      arrived();
      await gate;
      await route.fulfill({ response });
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Open slow.md", exact: true }).click();
  await waiting;
  await page.getByRole("button", { name: "Open fast.md", exact: true }).click();
  await expect(page.getByRole("tab", { name: "fast.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  release();
  await expect(page.getByRole("tab", { name: "slow.md" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "fast.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(
    page.getByRole("textbox", { name: "Contents of fast.md" }),
  ).toBeVisible();
});

test("lost save response replays its receipt and keeps later edits unsaved", async ({
  page,
}) => {
  await signIn(page);
  await createLibrary(page, "Receipt replay " + Date.now());
  await createFile(page, "receipt.md");
  await editorText(page, "receipt.md", "Accepted server text");
  let first = true;
  const requests: string[] = [];
  await page.route("**/draft/text", async (route) => {
    requests.push(route.request().postData() || "");
    if (first) {
      first = false;
      await route.fetch();
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(page.getByText("Save failed", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "Close receipt.md", exact: true })
    .click();
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await expect(
    page
      .getByRole("alert")
      .filter({ hasText: "last save is still unconfirmed" })
      .first(),
  ).toBeVisible();
  await expect(page.getByRole("tab", { name: "receipt.md" })).toBeVisible();
  await editorText(page, "receipt.md", "Newer local text");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(page.getByText("Unsaved", { exact: true })).toBeVisible();
  expect(requests[1]).toBe(requests[0]);
  await expect(
    page.getByRole("textbox", { name: "Contents of receipt.md" }),
  ).toContainText("Newer local text");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save file", exact: true }),
  ).toBeDisabled();
  await expect(
    page.locator(".editor-toolbar").getByText("Saved", { exact: true }),
  ).toBeVisible();
  const response = await savedContent(page, "receipt.md");
  expect(await response.text()).toBe("Newer local text");
});

test("image zoom uses native pixels and Markdown rejects remote images", async ({
  page,
}) => {
  await signIn(page);
  await createLibrary(page, "Image preview " + Date.now());
  const png = await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 80;
    canvas.height = 40;
    const context = canvas.getContext("2d")!;
    context.fillStyle = "#4488cc";
    context.fillRect(0, 0, 80, 40);
    return canvas.toDataURL("image/png").split(",")[1];
  });
  await page.getByLabel("Select files", { exact: true }).setInputFiles({
    name: "sample.png",
    mimeType: "image/png",
    buffer: Buffer.from(png, "base64"),
  });
  await page
    .getByRole("button", { name: "Open sample.png", exact: true })
    .click();
  const image = page.getByRole("img", { name: "sample.png", exact: true });
  await page.getByRole("button", { name: "100%", exact: true }).click();
  await expect
    .poll(() => image.evaluate((node) => node.getBoundingClientRect().width))
    .toBe(80);
  await page.getByRole("button", { name: "Zoom in", exact: true }).click();
  await expect
    .poll(() => image.evaluate((node) => node.getBoundingClientRect().width))
    .toBe(100);
  await createFile(page, "images.md");
  await editorText(page, "images.md", "![Local](sample.png)");
  await page.getByRole("button", { name: "preview", exact: true }).click();
  await expect(
    page.getByRole("img", { name: "Local", exact: true }),
  ).toBeVisible();
  let remoteRequests = 0;
  await page.route("https://example.com/**", async (route) => {
    remoteRequests++;
    await route.abort();
  });
  await page.getByRole("button", { name: "edit", exact: true }).click();
  await editorText(
    page,
    "images.md",
    "![Remote](https://example.com/tracker.png)",
  );
  await page.getByRole("button", { name: "preview", exact: true }).click();
  await expect(
    page.getByText("Remote (image not loaded)", { exact: true }),
  ).toBeVisible();
  await expect(page.locator(".markdown-preview img")).toHaveCount(0);
  expect(remoteRequests).toBe(0);
});

test("expanding an unrelated folder preserves the tree scroll and editor", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await signIn(page);
  await createLibrary(page, "Stable folders " + Date.now());
  await createFile(page, "selected.md");
  await page.route("**/draft/tree?*", async (route) => {
    const url = new URL(route.request().url());
    const parent = url.searchParams.get("parent");
    if (parent) {
      return route.fulfill({
        json: {
          entries: [
            { path: `${parent}/child.md`, kind: "file", size: "0", digest: "" },
          ],
          next: "",
          descendant_count: 1,
          revision: 2,
        },
      });
    }
    const response = await route.fetch();
    const value = await response.json();
    const directories = Array.from({ length: 50 }, (_, index) => ({
      path: `folder${String(index).padStart(2, "0")}`,
      kind: "directory",
      size: "0",
      digest: "",
    }));
    await route.fulfill({
      json: {
        ...value,
        entries: [...directories, ...value.entries],
        descendant_count: 51,
        next: "",
      },
    });
  });
  await page.reload();
  await expect(
    page.getByRole("textbox", { name: "Contents of selected.md" }),
  ).toBeVisible();
  const tree = page.getByRole("tree", { name: "Files", exact: true });
  await expect
    .poll(() => tree.evaluate((node) => node.scrollTop))
    .toBeGreaterThan(0);
  await tree.evaluate((node) => {
    node.scrollTop = 0;
  });
  // The first row fits even when review controls leave a short tree viewport.
  // Clicking a lower offscreen row would itself scroll before expansion.
  const folder = page.getByRole("button", {
    name: "Open folder00",
    exact: true,
  });
  await folder.click();
  await expect(
    page.getByRole("button", { name: "Open folder00/child.md", exact: true }),
  ).toBeVisible();
  expect(await tree.evaluate((node) => node.scrollTop)).toBe(0);
  await folder.press("ArrowLeft");
  await expect(
    page.getByRole("button", { name: "Open folder00/child.md", exact: true }),
  ).toHaveCount(0);
  expect(await tree.evaluate((node) => node.scrollTop)).toBe(0);
  await expect(
    page.getByRole("textbox", { name: "Contents of selected.md" }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("filled actions retain contrast in toolbars and dialogs across appearances", async ({
  page,
}) => {
  await signIn(page);
  const check = async (control: import("@playwright/test").Locator) => {
    for (const theme of ["light", "dark"])
      for (const accent of ["neutral", "blue", "green", "violet"]) {
        await page.evaluate(
          ({ theme, accent }) => {
            document.documentElement.dataset.theme = theme;
            document.documentElement.dataset.accent = accent;
          },
          { theme, accent },
        );
        await control.hover();
        const contrast = await control.evaluate((node) => {
          const style = getComputedStyle(node);
          const luminance = (color: string) => {
            const rgb = color
              .match(/[\d.]+/g)!
              .slice(0, 3)
              .map(Number)
              .map((v) => {
                const n = v / 255;
                return n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4;
              });
            return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
          };
          const a = luminance(style.color),
            b = luminance(style.backgroundColor);
          return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
        });
        expect(contrast, `${theme}/${accent} hover`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
  };
  const create = page.getByRole("button", { name: "New library", exact: true });
  await check(create);
  await create.click();
  await page.getByLabel("Name", { exact: true }).fill("Contrast fixture");
  await check(page.getByRole("button", { name: "Create draft", exact: true }));
});

test("language changes keep Studio unsaved content, selection, and confirmation choices", async ({ page }, info) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await signIn(page);
  await createLibrary(page, `Bilingual library ${Date.now()}`);
  await createFile(page, "English Name.md");
  const editor = await editorText(page, "English Name.md", "# Unsaved bilingual content");
  await editor.press("ControlOrMeta+End");
  const current = await page.request.get("/api/me/preferences").then(response => response.json());
  const changed = await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: current.revision, language: "zh" } });
  expect(changed.ok()).toBeTruthy();
  const account = await page.request.get("/api/auth/session").then(response => response.json());
  await page.evaluate(id => window.dispatchEvent(new StorageEvent("storage", { key: `account-appearance:${id}` })), account.id);
  await expect(page.getByRole("textbox", { name: "English Name.md 的内容", exact: true })).toContainText("# Unsaved bilingual content");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
  await page.screenshot({ path: info.outputPath("studio-zh-dirty.png") });
  await page.getByRole("button", { name: "关闭 English Name.md", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "未保存的更改", exact: true });
  await expect(dialog).toContainText("保存对 English Name.md 的更改？");
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "English Name.md 的内容", exact: true })).toContainText("# Unsaved bilingual content");
  await page.getByRole("button", { name: "保存文件", exact: true }).click();
  await expect(page.getByText("已保存", { exact: true })).toBeVisible();
  await page.getByLabel("提交说明", {exact:true}).fill("Active");
  await expect(page.getByLabel("提交说明", {exact:true})).toHaveValue("Active");
  await page.getByRole("button", {name:"提交已保存的草稿",exact:true}).click();
  await expect(page.getByRole("status").filter({hasText:"已提交保存的修订"})).toBeVisible();
  await page.reload();
  await expect(page.getByRole("link", { name: "库", exact: true })).toBeVisible();
  const saved = await page.request.get("/api/me/preferences").then(response => response.json());
  const reset = await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: saved.revision, language: "en" } });
  expect(reset.ok()).toBeTruthy();
  await page.evaluate(id => window.dispatchEvent(new StorageEvent("storage", { key: `account-appearance:${id}` })), account.id);
  await expect(page.getByRole("link", { name: "Libraries", exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("shared assets show actionable validation and publish immutable system context",async({page})=>{
 await signIn(page);
 await page.getByRole("button",{name:"New library",exact:true}).click();
 await page.getByLabel("Name",{exact:true}).fill(`Shared context ${Date.now()}`);
 await page.getByRole("dialog").getByRole("radio",{name:"Shared assets",exact:true}).check();
 await page.getByRole("button",{name:"Create draft",exact:true}).click();
 await provisionBranch(page);
 await page.getByRole("button",{name:"Open AGENTS.md",exact:true}).click();
 await editorText(page,"AGENTS.md","# Aster context\nUse the available runtime tools.");
 await page.getByRole("button",{name:"Save file",exact:true}).click();
 await expect(page.locator(".shared-assets-validation")).toContainText("Content checks passed");
 await acceptBranch(page);
 await page.getByLabel("Version label").fill("v1");
 const version = await publishAccepted(page);
 await page.goto(`/volumes-studio/libraries/${libraryID(page)}?version=${version.id}`);
 await page.getByRole("button",{name:"Open AGENTS.md",exact:true}).click();
 await expect(page.locator(".cm-content")).toHaveAttribute("contenteditable","false");
 await expect(page.locator(".cm-content")).toContainText("Use the available runtime tools.");
});


test("rename waits for an in-flight save without an unsaved confirmation",async({page})=>{
 await signIn(page);await createLibrary(page,`Queued rename ${Date.now()}`);await createFile(page,"before.md");
 await editorText(page,"before.md","Saved before rename");
 let started!:()=>void,release!:()=>void;
 const saving=new Promise<void>(resolve=>{started=resolve;});const gate=new Promise<void>(resolve=>{release=resolve;});
 await page.route("**/draft/text",async route=>{
  if(route.request().postDataJSON().path==="before.md") {const response=await route.fetch();started();await gate;await route.fulfill({response});} else await route.continue();
 });
 await page.getByRole("button",{name:"Save file",exact:true}).click();await saving;
 await page.getByRole("button",{name:"Actions for before.md",exact:true}).click();await page.getByRole("menuitem",{name:"Rename",exact:true}).click();
 await page.getByLabel("New file name").fill("after.md");await page.getByLabel("New file name").press("Enter");
 await expect(page.getByRole("dialog",{name:"Unsaved files",exact:true})).toHaveCount(0);release();
 await expect(page.getByRole("button",{name:"Open after.md",exact:true})).toBeVisible();
 await expect(page.getByRole("tab",{name:"after.md"})).toBeVisible();
});

for (const width of [390, 1440]) test(`bilingual shared-assets controls and validation preserve publication edits at ${width}px`, async ({ page }, info) => {
  await page.setViewportSize({ width, height: 900 });
  await signIn(page);
  const account = await page.request.get("/api/auth/session").then(response => response.json());
  const language = async (value: "en" | "zh") => {
    const saved = await page.request.get("/api/me/preferences").then(response => response.json());
    const reply = await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: saved.revision, language: value } });
    expect(reply.ok()).toBeTruthy();
    await page.evaluate(id => window.dispatchEvent(new StorageEvent("storage", { key: `account-appearance:${id}` })), account.id);
    await expect(page.locator("html")).toHaveAttribute("lang", value === "zh" ? "zh-Hans" : "en");
  };
  try {
    await page.getByRole("button", { name: "New library", exact: true }).click();
    const name = `English context ${Date.now()}`;
    await page.getByLabel("Name", { exact: true }).fill(name);
    await page.getByRole("radio", { name: "Shared assets", exact: true }).check();
    await language("zh");
    const dialog = page.getByRole("dialog", { name: "新建库", exact: true });
    await expect(dialog.getByLabel("名称", { exact: true })).toHaveValue(name);
    const radio = dialog.getByRole("radio", { name: "共享资源", exact: true });
    await expect(radio).toBeChecked();
    const frame = await radio.boundingBox();
    expect(frame!.width).toBeLessThanOrEqual(20);
    expect(frame!.height).toBeLessThanOrEqual(20);
    expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
    await page.screenshot({ path: info.outputPath(`profile-zh-${width}.png`) });
    let unavailable = true;
    await page.route("**/draft/validation?*", route => {
      if (unavailable) { unavailable = false; return route.fulfill({ status: 503, json: { error: "request_failed" } }); }
      return route.continue();
    });
    await dialog.getByRole("button", { name: "创建草稿", exact: true }).click();
    await language("en");
    await provisionBranch(page);
    const validation = page.locator(".shared-assets-validation");
    await expect(validation).toContainText("Validation unavailable");
    await validation.getByRole("button", {name:"Retry validation",exact:true}).click();
    await expect(validation).toContainText("Content checks passed");
    if (width < 768) await page.getByRole("button", {name:"Show files",exact:true}).click();
    await page.getByRole("button", {name:"Open AGENTS.md",exact:true}).click();
    await editorText(page,"AGENTS.md","# English Name\nKeep user content unchanged. 中文内容。");
    await page.getByRole("button", {name:"Save file",exact:true}).click();
    await expect(page.getByRole("button", {name:"Save file",exact:true})).toBeDisabled();
    await acceptBranch(page);
    await language("zh");
    await expect(page.getByRole("textbox", {name:"AGENTS.md 的内容",exact:true})).toContainText("Keep user content unchanged. 中文内容。");
    const review = page.getByRole("region", {name:"发布已接受的提交",exact:true});
    await review.getByLabel("版本标签", {exact:true}).fill("English version name");
    await review.getByLabel("发布说明", {exact:true}).fill("English and 中文 release note");
    expect(await review.evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
    await page.screenshot({path:info.outputPath(`validation-zh-${width}.png`)});
    await language("en");
    const englishReview = page.getByRole("region", {name:"Publish accepted commit",exact:true});
    await expect(englishReview.getByLabel("Version label", {exact:true})).toHaveValue("English version name");
    await expect(englishReview.getByRole("textbox", {name:"Release note",exact:true})).toHaveValue("English and 中文 release note");
    await publishAccepted(page);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  } finally { await language("en"); }
});

test("account menu offers Workbench without duplicating settings and retains unsaved work through central logout", async ({ page, context }) => {
  await page.setViewportSize({ width: 390, height: 900 });
  await signIn(page);
  await createLibrary(page, "Shared session " + Date.now());
  await page.getByRole("button", { name: "Show files", exact: true }).click();
  await createFile(page, "retained.md");
  await editorText(page, "retained.md", "unsaved account-local content");
  await expect(page.getByRole("navigation", { name: "Applications", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await expect(page.getByRole("menuitem", { name: "Back to Workbench", exact: true })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Settings", exact: true })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("combobox", { name: "Theme mode", exact: true })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Contents of retained.md", exact: true })).toContainText("unsaved account-local content");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const other = await context.newPage();
  expect((await other.request.post("/api/auth/logout", { headers: { Origin: origin } })).ok()).toBeTruthy();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("heading", { name: "Sign in again", exact: true })).toBeVisible();
  await expect(page.getByText("Your unsaved edits are retained for this account.", { exact: true })).toBeVisible();
  await other.close();
});
