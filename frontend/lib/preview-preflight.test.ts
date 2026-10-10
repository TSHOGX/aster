import { expect, it } from "vitest";
import { imagePixels, preflightBytes, preflightPreview } from "./preview-preflight";
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
  expect(() => imagePixels(png(100_000, 100_000))).toThrow("content_limit");
  expect(() => imagePixels(png(0, 0))).toThrow("container_invalid");
});
it("refuses unsupported formats and malformed containers before renderer import", async () => {
  for (const ext of ["pdf", "xls", "svg", "gif", "docx", "xlsx", "pptx"]) {
    await expect(
      preflightPreview(
        new Blob(["malformed"]),
        `test.${ext}`,
        new AbortController().signal,
      ),
    ).rejects.toThrow();
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
      new Blob([new Uint8Array(20 * 1024 * 1024 + 1)]),
      "x.png",
      new AbortController().signal,
    ),
  ).rejects.toThrow("source_too_large");
});

const workbook =
  '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"/>';
const sheet = (content: string) =>
  `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">${content}</worksheet>`;
const check = (entries: Parameters<typeof zipFixture>[0]) =>
  preflightBytes(
    new Uint8Array(zipFixture(entries)),
    "test.xlsx",
    new AbortController().signal,
    true,
  );
it("validates actual cells and checks spreadsheet coordinates separately from populated-cell budgets", async () => {
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
  for (const reference of ["A1:XFE1", "A1:A1048577"]) {
    await expect(
      check([
        base,
        {
          name: "xl/worksheets/sheet1.xml",
          text: sheet(`<dimension ref="${reference}"/>`),
        },
      ]),
    ).rejects.toThrow();
  }
});
it("checks decompressed length and CRC independently of ZIP metadata claims", async () => {
  for (const entry of [
    { text: workbook, declaredSize: 1 },
    { text: workbook, declaredSize: 8 * 1024 * 1024 + 1 },
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
  await expect(check([base, base])).rejects.toThrow("container_invalid");
  await expect(
    check([base, { name: "xl/../file.xml", text: "<a/>" }]),
  ).rejects.toThrow("container_invalid");
  await expect(
    check([
      base,
      {
        name: "xl/_rels/workbook.xml.rels",
        text: '<Relationships><Relationship TargetMode="External" Type="image" Target="https://example.com/image.png"/></Relationships>',
      },
    ]),
  ).rejects.toThrow("container_invalid");
  await expect(
    check([
      base,
      ...Array.from({ length: 4096 }, (_, i) => ({
        name: `f${i}.xml`,
        text: "<a/>",
      })),
    ]),
  ).rejects.toThrow("content_limit");
});

it("accepts PDFs at the 20 MiB boundary and rejects larger PDFs", async () => {
  const bytes = new Uint8Array(20 * 1024 * 1024);
  bytes.set(new TextEncoder().encode("%PDF-1.7"));
  await expect(preflightPreview(new Blob([bytes]), "report.pdf", new AbortController().signal)).resolves.toMatchObject({ nodes: 0 });
  await expect(preflightPreview(new Blob([bytes, "x"]), "report.pdf", new AbortController().signal)).rejects.toThrow("source_too_large");
});
it("admits ordinary 12 megapixel images within the expanded pixel budget", () => {
  expect(imagePixels(png(4000, 3000))).toBe(12_000_000);
});
it("admits bounded static GIFs and rejects animated GIFs explicitly", () => {
  const gif = Buffer.from("R0lGODlhAQABAAD/ACwAAAAAAQABAAACAUwAOw==", "base64");
  expect(imagePixels(gif)).toBe(1);
  const extraFrame = Buffer.concat([gif.subarray(0, gif.length - 1), gif.subarray(13, gif.length)]);
  expect(() => imagePixels(extraFrame)).toThrow("unsupported_feature");
});
it("accepts legal deflate options and UTF-16 embedded workbook XML", async () => {
  const utf16 = Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(workbook, "utf16le")]);
  const archive = zipFixture([
    { name: "xl/workbook.xml", bytes: utf16, flags: 2 },
    { name: "xl/worksheets/sheet1.xml", text: sheet("<sheetData/>"), flags: 4 },
  ]);
  await expect(preflightBytes(new Uint8Array(archive), "test.xlsx", new AbortController().signal, true))
    .resolves.toMatchObject({ cells: 0 });
  await expect(preflightPreview(new Blob([new Uint8Array(archive)]), "test.xlsx", new AbortController().signal)).resolves.toMatchObject({ cells: 0 });
  await expect(preflightPreview(new Blob([new Uint8Array(archive)]), "test.docx", new AbortController().signal)).rejects.toMatchObject({code:"container_invalid"});
});
it("admits larger Office XML entries while retaining structural limits", async () => {
  await expect(check([{ name: "xl/workbook.xml", text: workbook.replace("/>", ">" + " ".repeat(5 * 1024 * 1024) + "</workbook>") }])).resolves.toMatchObject({ cells: 0 });
});

it("distinguishes corrupt containers and unsupported formats while admitting simple 200-slide decks", async () => {
  const signal = new AbortController().signal;
  await expect(preflightPreview(new Blob(["broken"]), "report.pptx", signal))
    .rejects.toMatchObject({ code: "container_invalid", stage: "container" });
  await expect(preflightPreview(new Blob(["broken"]), "report.xls", signal))
    .rejects.toMatchObject({ code: "unsupported_feature", stage: "parse" });
  for (const count of [40, 41, 200]) {
    const entries = [
      { name: "ppt/presentation.xml", text: "<presentation/>" },
      ...Array.from({ length: count }, (_, i) => ({ name: `ppt/slides/slide${i + 1}.xml`, text: "<slide/>" })),
    ];
    await expect(preflightPreview(new Blob([new Uint8Array(zipFixture(entries))]), "report.pptx", signal))
      .resolves.toMatchObject({ slides: count });
  }
});
