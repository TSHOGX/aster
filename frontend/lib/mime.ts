import { hljsLanguage } from "./languages";

export type ViewerKind = "markdown" | "code" | "image" | "json" | "table" | "pdf" | "spreadsheet" | "document" | "presentation" | "text" | "binary";
export type ArtifactClassification = { kind: ViewerKind; language?: string; delimiter?: "," | "\t" };

const TEXT_EXT = new Set(["txt", "log", "env", "diff", "patch"]);
const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico"]);

export function classifyArtifact(path: string): ArtifactClassification {
  const ext = (path.split(".").pop() ?? "").toLowerCase();
  const base = (path.split("/").pop() ?? "").toLowerCase();
  if (!ext) {
    if (["dockerfile", "makefile", "rakefile", "readme", "license"].includes(base)) {
      return { kind: "text" };
    }
    return { kind: "binary" };
  }
  if (ext === "md" || ext === "markdown" || ext === "mdx") return { kind: "markdown" };
  if (IMAGE_EXT.has(ext)) return { kind: "image" };
  if (ext === "json") return { kind: "json", language: "json" };
  if (ext === "csv") return { kind: "table", delimiter: "," };
  if (ext === "tsv") return { kind: "table", delimiter: "\t" };
  if (ext === "pdf") return { kind: "pdf" };
  if (ext === "xlsx" || ext === "xls") return { kind: "spreadsheet" };
  if (ext === "docx") return { kind: "document" };
  if (ext === "pptx") return { kind: "presentation" };
  const hljs = hljsLanguage(ext);
  if (hljs) return { kind: "code", language: hljs };
  if (TEXT_EXT.has(ext)) return { kind: "text" };
  return { kind: "binary" };
}
