import { expect, it } from "vitest";
import { imagePixels, preflightPreview } from "./preview-preflight";
import { zipFixture } from "../e2e/zip-fixture";

function png(width: number, height: number) {
  const bytes = new Uint8Array(33);
  const v = new DataView(bytes.buffer);
  v.setUint32(0, 0x89504e47);
  v.setUint32(4, 0x0d0a1a0a);
  v.setUint32(8, 13);
  v.setUint32(12, 0x49484452);
  v.setUint32(16, width);
  v.setUint32(20, height);
  return bytes;
}
it("rejects pixel bombs before image decoding", () => {
  expect(imagePixels(png(1200, 900))).toBe(1_080_000);
  expect(() => imagePixels(png(100_000, 100_000))).toThrow("limits");
  expect(() => imagePixels(png(0, 0))).toThrow("limits");
});
it("refuses unsupported formats and malformed containers before renderer import", async () => {
  for (const ext of ["pdf", "xls", "svg", "gif", "docx", "xlsx", "pptx"]) {
    await expect(
      preflightPreview(
        new Blob(["malformed"]),
        `test.${ext}`,
        new AbortController().signal,
      ),
    ).rejects.toThrow("limits");
  }
});
it("honors cancellation before parsing and bounds download buffers", async () => {
  const abort = new AbortController();
  abort.abort();
  await expect(
    preflightPreview(new Blob([png(1, 1)]), "x.png", abort.signal),
  ).rejects.toThrow();
  await expect(
    preflightPreview(
      new Blob([new Uint8Array(16 * 1024 * 1024 + 1)]),
      "x.png",
      new AbortController().signal,
    ),
  ).rejects.toThrow("limits");
});

const workbook =
  '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>';
const sheet = (content: string) =>
  `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">${content}</worksheet>`;
const check = (entries: Parameters<typeof zipFixture>[0]) =>
  preflightPreview(
    new Blob([new Uint8Array(zipFixture(entries))]),
    "test.xlsx",
    new AbortController().signal,
  );
it("validates actual cells and rejects sparse dimensions that would allocate huge grids", async () => {
  const base = { name: "xl/workbook.xml", text: workbook };
  await expect(
    check([
      base,
      {
        name: "xl/worksheets/sheet1.xml",
        text: sheet(
          '<dimension ref="A1:B2"/><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>',
        ),
      },
    ]),
  ).resolves.toMatchObject({ cells: 1, rows: 1 });
  for (const reference of ["A1:XFD1048576", "A1:IW1", "A1:A2001"]) {
    await expect(
      check([
        base,
        {
          name: "xl/worksheets/sheet1.xml",
          text: sheet(`<dimension ref="${reference}"/>`),
        },
      ]),
    ).rejects.toThrow("limits");
  }
});
it("checks decompressed length and CRC independently of ZIP metadata claims", async () => {
  for (const entry of [
    { text: workbook, declaredSize: 1 },
    { text: workbook, declaredSize: 4 * 1024 * 1024 + 1 },
    { text: workbook, crc: 0 },
    { text: '<!DOCTYPE workbook [<!ENTITY e "x">]><workbook>&e;</workbook>' },
    { text: "<workbook><broken>" },
    { text: "<a>".repeat(65) + "</a>".repeat(65) },
  ])
    await expect(
      check([{ name: "xl/workbook.xml", ...entry }]),
    ).rejects.toThrow();
});
it("rejects ambiguous names, excessive entries and external embedded content", async () => {
  const base = { name: "xl/workbook.xml", text: workbook };
  await expect(check([base, base])).rejects.toThrow("limits");
  await expect(
    check([base, { name: "xl/../file.xml", text: "<a/>" }]),
  ).rejects.toThrow("limits");
  await expect(
    check([
      base,
      {
        name: "xl/_rels/workbook.xml.rels",
        text: '<Relationships><Relationship TargetMode="External" Type="image" Target="https://example.com/image.png"/></Relationships>',
      },
    ]),
  ).rejects.toThrow("limits");
  await expect(
    check([
      base,
      ...Array.from({ length: 1024 }, (_, i) => ({
        name: `f${i}.xml`,
        text: "<a/>",
      })),
    ]),
  ).rejects.toThrow("limits");
});
