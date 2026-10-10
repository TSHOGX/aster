import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import { zipFixture } from "../zip-fixture";

for (const [name, data] of [
  [
    "dimensions.xlsx",
    zipFixture([
      {
        name: "xl/workbook.xml",
        text: '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>',
      },
      {
        name: "xl/worksheets/sheet1.xml",
        text: '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:XFE1048577"/></worksheet>',
      },
    ]),
  ],
  [
    "expansion.docx",
    zipFixture([
      { name: "word/document.xml", text: "a".repeat(8 * 1024 * 1024 + 1) },
    ]),
  ],
  [
    "malformed.pptx",
    zipFixture([{ name: "ppt/presentation.xml", text: "<a><broken>" }]),
  ],
  ["unknown.xls", Buffer.from("old binary spreadsheet")],
] as const) {
  test(`bounded download fallback for ${name}`, async ({ page }) => {
    const f = await workspaceFixture(page);
    f.messages.analysis[1].content = `[Document](/workspace/${name})`;
    await page.route("**/files/content?*", (route) =>
      route.fulfill({ body: data }),
    );
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", { name: "Document", exact: true }).click();
    const expected = name === "unknown.xls" ? "This format is not supported"
      : name === "malformed.pptx" || name === "expansion.docx" ? "damaged or has an invalid structure"
      : "than this preview can process";
    await expect(page.getByRole("region", { name: `File ${name}`, exact: true }).getByRole("status"))
      .toContainText(expected, { timeout: 5000 });
    await expect(
      page.getByRole("link", { name: `Download ${name}`, exact: true }),
    ).toHaveAttribute("download", "");
    await expect(page.locator(".office-preview")).toHaveCount(0);
    await page
      .locator(".agent-panel textarea")
      .fill("The rest of the workspace still responds");
    expect(errors).toEqual([]);
  });
}
