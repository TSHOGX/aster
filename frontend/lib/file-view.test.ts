import { expect, it } from "vitest";
import { decodeTextFile, hasSourceView, isTextFile, parseDelimited, TEXT_FILE_LIMIT, textPage, TEXT_PAGE_CHARS } from "./file-view";
import { classifyArtifact } from "./mime";
it("offers source and editing only for appropriate, bounded UTF-8 text", async () => {
  for (const path of ["a.md","a.csv","a.tsv","a.html","a.htm","a.json"]) {
    expect(hasSourceView(path)).toBe(true);
    expect(await decodeTextFile(path, new Blob(["text"]))).toBe("text");
  }
  expect(classifyArtifact("a.htm").kind).toBe("html");
  expect(isTextFile("a.mjs")).toBe(true);
  for (const path of ["a.png","a.pdf","a.xlsx","a.zip","a.bin"])
    expect(await decodeTextFile(path, new Blob(["text"]))).toBeNull();
  expect(await decodeTextFile("a.txt", new Blob([new Uint8Array([255])]))).toBeNull();
  expect(await decodeTextFile("a.md", new Blob(["abc\0def"]))).toBeNull();
  expect(await decodeTextFile("a.txt", new Blob([new Uint8Array(TEXT_FILE_LIMIT + 1)]))).toBeNull();
  expect(await decodeTextFile("a.txt", new Blob([]))).toBe("");
});
it("parses quoted separators, multiline values, escaped quotes and empty cells", () => {
  expect(parseDelimited('name,value\r\n"A,B","one\n""two"""\r\n,3,', ',')).toEqual({rows:[['name','value'],['A,B','one\n"two"'],['','3','']],truncated:false});
  expect(parseDelimited('a\tb\n1\t2\n', '\t').rows).toEqual([['a','b'],['1','2']]);
  expect(parseDelimited('', ',').rows).toEqual([]);
  expect(() => parseDelimited('"broken', ',')).toThrow();
  expect(() => parseDelimited('"closed"extra', ',')).toThrow();
});
it("bounds decoded rows, columns and cells without claiming a complete table", () => {
  const rows=parseDelimited('a,b\n'.repeat(3000), ',');
  expect(rows.rows).toHaveLength(2000); expect(rows.truncated).toBe(true);
  const columns=parseDelimited('a,'.repeat(300), ',');
  expect(columns.rows[0]).toHaveLength(256); expect(columns.truncated).toBe(true);
  const cells=parseDelimited(('a,'.repeat(99)+'b\n').repeat(300), ',');
  expect(cells.rows.flat()).toHaveLength(20000); expect(cells.truncated).toBe(true);
});

it("decodes UTF-8 text up to 20 MiB, including files above the old 1 MiB limit", async () => {
  const text = "a".repeat(20 * 1024 * 1024);
  expect(await decodeTextFile("large.txt", new Blob([text]))).toBe(text);
  expect(await decodeTextFile("large.txt", new Blob([text, "a"]))).toBeNull();
});
it("pages text without losing characters or splitting emoji across boundaries", () => {
  const text = "a".repeat(TEXT_PAGE_CHARS - 1) + "🔋" + "b".repeat(TEXT_PAGE_CHARS) + "END";
  const pages = Array.from({ length: Math.ceil(text.length / TEXT_PAGE_CHARS) }, (_, page) => textPage(text, page));
  expect(pages.join("")).toBe(text);
  expect(pages[1].startsWith("🔋")).toBe(true);
  expect(pages.every(page => page.length <= TEXT_PAGE_CHARS + 1)).toBe(true);
});
