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
  await expect(page.getByRole("button", { name, exact: true })).toBeVisible();
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
  await page.route("**/draft/mutations", async (route) => {
    if (route.request().postDataJSON().path === "readme.md") {
      const response = await route.fetch();
      saved();
      await saveReply;
      await route.fulfill({ response });
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await saveArrived;
  await page
    .getByRole("button", { name: "Review & publish", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Preparing review…", exact: true }),
  ).toBeDisabled();
  releaseSave();
  await page.getByLabel("Version label").fill("v1");
  await page.unroute("**/draft/mutations");
  await page.getByLabel("Release note").fill("Reviewed starter handbook.");
  await page
    .getByRole("button", { name: "Publish version", exact: true })
    .click();
  await expect(page.getByText("published", { exact: true })).toBeVisible({
    timeout: 20000,
  });
  await page
    .getByRole("button", { name: "Inspect files", exact: true })
    .click();
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
  const l = await (
    await page.request.get(`/volumes-studio/api/v1/libraries/${id}`)
  ).json();
  const update = await page.request.post(
    `/volumes-studio/api/v1/libraries/${id}/draft/mutations`,
    {
      headers: { Origin: origin, "X-CSRF-Token": s.csrf },
      data: {
        request_id: "external-" + Date.now(),
        expected_revision: l.draft_revision,
        action: "text",
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
  await expect(
    page.getByRole("button", { name: "Review & publish", exact: true }),
  ).toBeDisabled();
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
      await page.request.get(`${root}/draft/content?path=dataset.bin`)
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
  await page.route("**/draft/mutations", async (route) => {
    const body = route.request().postDataJSON();
    if (body.action === "text" && body.text === "first submission") {
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
  await page.unroute("**/draft/mutations");
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
  await page.route("**/draft/mutations", async (route) => {
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
  const response = await page.request.get(
    `/volumes-studio/api/v1/libraries/${libraryID(page)}/draft/content?path=receipt.md`,
  );
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
  await signIn(page);
  await createLibrary(page, "Stable folders " + Date.now());
  await createFile(page, "selected.md");
  await page.route("**/draft/tree?*", async (route) => {
    const url = new URL(route.request().url());
    const parent = url.searchParams.get("parent");
    if (parent) {
      return route.fulfill({
        json: {
          items: [
            { path: `${parent}/child.md`, kind: "file", size: "0", digest: "" },
          ],
          next: "",
          total: 1,
          revision: 2,
          base_version: "",
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
        items: [...directories, ...value.items],
        total: 51,
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
  const folder = page.getByRole("button", {
    name: "Open folder05",
    exact: true,
  });
  await folder.click();
  await expect(
    page.getByRole("button", { name: "Open folder05/child.md", exact: true }),
  ).toBeVisible();
  expect(await tree.evaluate((node) => node.scrollTop)).toBe(0);
  await folder.click();
  await expect(
    page.getByRole("button", { name: "Open folder05/child.md", exact: true }),
  ).toHaveCount(0);
  expect(await tree.evaluate((node) => node.scrollTop)).toBe(0);
  await expect(
    page.getByRole("textbox", { name: "Contents of selected.md" }),
  ).toBeVisible();
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
  await page.getByRole("button", { name: "审阅并发布", exact: true }).click();
  const versionLabel = page.getByRole("textbox", { name: "版本标签", exact: true });
  await versionLabel.fill("Active");
  await expect(versionLabel).toHaveValue("Active");
  await page.getByRole("dialog", { name: "审阅并发布", exact: true }).getByRole("button", { name: "取消", exact: true }).click();
  await page.reload();
  await expect(page.getByRole("link", { name: "库", exact: true })).toBeVisible();
  const saved = await page.request.get("/api/me/preferences").then(response => response.json());
  const reset = await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: saved.revision, language: "en" } });
  expect(reset.ok()).toBeTruthy();
  await page.evaluate(id => window.dispatchEvent(new StorageEvent("storage", { key: `account-appearance:${id}` })), account.id);
  await expect(page.getByRole("link", { name: "Libraries", exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});
