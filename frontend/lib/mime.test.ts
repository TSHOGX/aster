import { describe, expect, it } from "vitest";

import { classifyArtifact } from "./mime";

describe("classifyArtifact", () => {
  it("classifies Office files as rich viewer kinds", () => {
    expect(classifyArtifact("report.xlsx").kind).toBe("spreadsheet");
    expect(classifyArtifact("data.xls").kind).toBe("spreadsheet");
    expect(classifyArtifact("notes.docx").kind).toBe("document");
    expect(classifyArtifact("deck.pptx").kind).toBe("presentation");
  });

  it("leaves legacy binary Office formats as binary", () => {
    expect(classifyArtifact("old.doc").kind).toBe("binary");
  });

  it("still classifies existing formats", () => {
    expect(classifyArtifact("a.md").kind).toBe("markdown");
    expect(classifyArtifact("a.png").kind).toBe("image");
    expect(classifyArtifact("a.csv").kind).toBe("table");
  });
});
