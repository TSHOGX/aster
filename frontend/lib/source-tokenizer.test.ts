import { expect, it } from "vitest";
import { indexSourceLines, scanXml, yamlTokens, type Token } from "./source-tokenizer";

it("restores XML comment, CDATA and quoted attribute state at line boundaries", () => {
  const text = '<root>\n<!-- first\nsecond -->\n<![CDATA[🔋\ncell]]>\n<item name="line\nvalue">ok</item>';
  const states = indexSourceLines(text, "xml");
  expect([...states]).toEqual([0, 0, 4, 0, 5, 0, 2]);
  const start = text.indexOf("cell]]>");
  const tokens: Token[] = [];
  scanXml(text, start, text.indexOf("\n", start), states[4], token => tokens.push(token));
  expect(tokens[0]).toEqual({ from: start, to: start + "cell]]>".length, kind: "cdata" });
});

it("keeps YAML block scalar state until indentation returns to the key", () => {
  const text = "entry: |\n  first\n\n  second\nother: \"value\"\n";
  const states = indexSourceLines(text, "yaml");
  expect([...states]).toEqual([0, 1, 1, 1, 1, 0]);
  const first = text.indexOf("  first");
  expect(yamlTokens(text, first, first + 7, states[1])).toEqual([{ from: first, to: first + 7, kind: "string" }]);
  const other = text.indexOf("other:");
  expect(yamlTokens(text, other, text.indexOf("\n", other), states[4]).map(token => token.kind)).toEqual(["key", "string"]);
});
