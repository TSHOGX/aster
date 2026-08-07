import { describe, expect, it } from "vitest";

import { MENTION_CAP, buildPathIndex, extractMentionedPaths } from "./mentioned-files";
import type { ApiArtifact } from "./types";

function artifacts(...paths: string[]): ApiArtifact[] {
  return paths.map((path) => ({
    session_id: "s1",
    path,
    size: 10,
    mod_time: "2026-08-07T00:00:00Z",
    created_at: "2026-08-07T00:00:00Z",
    updated_at: "2026-08-07T00:00:00Z"
  }));
}

function extract(content: string, ...paths: string[]): string[] {
  return extractMentionedPaths(content, buildPathIndex(artifacts(...paths)));
}

describe("extractMentionedPaths", () => {
  it("matches a bare basename and a full path", () => {
    expect(extract("Wrote report.md and outputs/soh.png.", "report.md", "outputs/soh.png")).toEqual([
      "report.md",
      "outputs/soh.png"
    ]);
  });

  it("returns nothing when no artifact matches", () => {
    expect(extract("I looked at the data and found nothing.", "report.md")).toEqual([]);
  });

  it("ignores paths that are not artifacts", () => {
    expect(extract("Compare report.md against missing.csv.", "report.md")).toEqual(["report.md"]);
  });

  it("orders by first appearance, not by name or list order", () => {
    expect(extract("First b.md, then a.md.", "a.md", "b.md")).toEqual(["b.md", "a.md"]);
  });

  it("dedupes repeated mentions", () => {
    expect(extract("report.md ... see report.md again", "report.md")).toEqual(["report.md"]);
  });

  it("resolves the same file cited by path and by name to one entry", () => {
    expect(extract("outputs/a.png then a.png", "outputs/a.png")).toEqual(["outputs/a.png"]);
  });

  describe("ambiguity", () => {
    it("refuses a basename shared by two files", () => {
      expect(extract("See report.md.", "outputs/report.md", "archive/report.md")).toEqual([]);
    });

    it("still resolves an ambiguous name given its full path", () => {
      expect(extract("See outputs/report.md.", "outputs/report.md", "archive/report.md")).toEqual([
        "outputs/report.md"
      ]);
    });
  });

  describe("path prefixes", () => {
    it("folds the /workspace mount away", () => {
      expect(extract("Saved to /workspace/outputs/a.png", "outputs/a.png")).toEqual(["outputs/a.png"]);
      expect(extract("Saved to workspace/outputs/a.png", "outputs/a.png")).toEqual(["outputs/a.png"]);
    });

    it("folds ./ and ~/ away", () => {
      expect(extract("./a.png and ~/b.png", "a.png", "b.png")).toEqual(["a.png", "b.png"]);
    });

    it("matches a leading-slash path", () => {
      expect(extract("at /outputs/a.png", "outputs/a.png")).toEqual(["outputs/a.png"]);
    });
  });

  describe("punctuation", () => {
    it("strips a trailing sentence dot", () => {
      expect(extract("Written to report.md.", "report.md")).toEqual(["report.md"]);
    });

    it("handles CJK punctuation and enumeration commas", () => {
      expect(extract("已生成 report.md、soh.png,请查看。", "report.md", "soh.png")).toEqual([
        "report.md",
        "soh.png"
      ]);
    });

    it("unwraps markdown code spans, quotes and brackets", () => {
      expect(extract("see `report.md`, \"a.png\", (b.csv)", "report.md", "a.png", "b.csv")).toEqual([
        "report.md",
        "a.png",
        "b.csv"
      ]);
    });

    it("unwraps a bolded path", () => {
      expect(extract("**report.md** is ready", "report.md")).toEqual(["report.md"]);
    });

    it("reads a path out of a markdown link target", () => {
      expect(extract("[the chart](outputs/soh.png)", "outputs/soh.png")).toEqual(["outputs/soh.png"]);
    });

    // A Chinese reply reaches for fullwidth brackets far more readily than the
    // ASCII ones, and a path fused to `（` or `《` resolves to nothing at all.
    it("unwraps fullwidth brackets", () => {
      expect(extract("报告（out/report.html）已生成", "out/report.html")).toEqual(["out/report.html"]);
      expect(extract("《out/report.html》", "out/report.html")).toEqual(["out/report.html"]);
      expect(extract("见〈a.png〉和「b.csv」", "a.png", "b.csv")).toEqual(["a.png", "b.csv"]);
    });

    it("splits on an equals sign, as in a flag or assignment", () => {
      expect(extract("file=out/report.html", "out/report.html")).toEqual(["out/report.html"]);
    });

    it("handles an ideographic space", () => {
      expect(extract("报告　out/report.html", "out/report.html")).toEqual(["out/report.html"]);
    });
  });

  describe("false positives", () => {
    it("does not read version numbers or decimals as files", () => {
      const index = buildPathIndex(artifacts("report.md"));
      expect(extractMentionedPaths("v1.2 dropped 3.5% vs 0.98 baseline", index)).toEqual([]);
    });

    it("requires a dot before matching a bare name", () => {
      // An artifact named `data` must not match every prose use of the word.
      expect(extract("the data looks clean", "data")).toEqual([]);
    });

    it("still matches an extensionless file by its full path", () => {
      expect(extract("see outputs/data for the dump", "outputs/data")).toEqual(["outputs/data"]);
    });

    it("does not match a name embedded in a longer token", () => {
      expect(extract("old_report.md is stale", "report.md")).toEqual([]);
    });
  });

  describe("case", () => {
    it("matches case-insensitively but returns the real path", () => {
      expect(extract("see REPORT.MD", "Report.md")).toEqual(["Report.md"]);
    });
  });

  describe("caps", () => {
    it("stops at the cap", () => {
      const paths = Array.from({ length: MENTION_CAP + 5 }, (_, i) => `f${i}.md`);
      const content = paths.join(" ");
      const result = extractMentionedPaths(content, buildPathIndex(artifacts(...paths)));
      expect(result).toHaveLength(MENTION_CAP);
      expect(result[0]).toBe("f0.md");
    });

    it("honours an explicit cap", () => {
      const index = buildPathIndex(artifacts("a.md", "b.md", "c.md"));
      expect(extractMentionedPaths("a.md b.md c.md", index, 2)).toEqual(["a.md", "b.md"]);
    });
  });

  it("handles empty content and an empty index", () => {
    expect(extract("", "report.md")).toEqual([]);
    expect(extractMentionedPaths("report.md", buildPathIndex([]))).toEqual([]);
  });
});

describe("buildPathIndex", () => {
  it("marks duplicated basenames ambiguous and keeps unique ones", () => {
    const index = buildPathIndex(artifacts("a/r.md", "b/r.md", "solo.md"));
    expect(index.byName.get("r.md")).toBeNull();
    expect(index.byName.get("solo.md")).toBe("solo.md");
    expect(index.byPath.get("a/r.md")).toBe("a/r.md");
  });
});

describe("structural file input", () => {
  // The public-link page passes SharedLinkFile (path + size only), built field
  // by field on the server. Indexing must not require the full ApiArtifact.
  it("indexes any object carrying a path", () => {
    const index = buildPathIndex([{ path: "outputs/a.png" }, { path: "b.md" }]);
    expect(extractMentionedPaths("see outputs/a.png and b.md", index)).toEqual([
      "outputs/a.png",
      "b.md"
    ]);
  });

  it("drops a mentioned file that is not in the given list", () => {
    // On a public link the list is the owner's whitelist, so an unpublished file
    // must not produce a row that would 404.
    const index = buildPathIndex([{ path: "published.md" }]);
    expect(extractMentionedPaths("see published.md and secret.md", index)).toEqual([
      "published.md"
    ]);
  });
});
