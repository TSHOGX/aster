import { expect, it } from "vitest";
import { analyzeHTML } from "./html-preview-model";

it("extracts bounded semantic HTML without exposing script text or markup", () => {
  const model = analyzeHTML('<h1>Battery &amp; capacity</h1><script>secret()</script><p>Cell <b>health</b></p><table><tr><td>A</td><td>95</td></tr></table>');
  expect(model).toMatchObject({ scripts: 1, simplified: false, blocks: [
    { kind: "heading", level: 1, text: "Battery & capacity" },
    { kind: "paragraph", text: "Cell health" },
    { kind: "row", text: "A · 95 ·" },
  ] });
  expect(JSON.stringify(model)).not.toContain("secret");
});

it("reports partial coverage for node-heavy pages with bounded returned blocks", () => {
  const model = analyzeHTML("<main>" + "<p>measurement</p>".repeat(31_000) + "</main>");
  expect(model.simplified).toBe(true);
  expect(model.nodes).toBe(31_001);
  expect(model.blocks).toHaveLength(2_000);
  expect(model.omittedBlocks).toBe(29_000);
});
