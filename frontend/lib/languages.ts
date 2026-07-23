// Single source of truth mapping a file extension to the two independent
// language targets the app renders with:
//   - hljs: the highlight.js language id used by rehype-highlight in read-only
//     CodeView previews.
//   - cm: a CodeMirror loader key used by the editable CodeEditor. The key is a
//     string (not the loader itself) so this module stays free of the heavy
//     @codemirror/lang-* imports — mime.ts pulls in this table for hljs ids and
//     must not drag CodeMirror into every bundle that classifies an artifact.
//
// The two targets legitimately differ for some extensions (e.g. html → "xml"
// for hljs vs the dedicated html() loader for CodeMirror); both are kept.

export type CodeMirrorLang =
  | "javascript"
  | "typescript"
  | "tsx"
  | "json"
  | "python"
  | "markdown"
  | "html"
  | "css";

export type LanguageEntry = {
  hljs?: string;
  cm?: CodeMirrorLang;
};

// Union of the former lib/mime.ts CODE_EXT map (hljs ids) and the former
// code-editor.tsx languageForPath switch (CodeMirror loaders). An entry only
// carries hljs when the previous CODE_EXT listed it, so classifyArtifact's
// code/binary split is unchanged.
export const LANGUAGES: Record<string, LanguageEntry> = {
  js: { hljs: "javascript", cm: "javascript" },
  jsx: { hljs: "javascript", cm: "javascript" },
  mjs: { cm: "javascript" },
  cjs: { cm: "javascript" },
  ts: { hljs: "typescript", cm: "typescript" },
  tsx: { hljs: "typescript", cm: "tsx" },
  json: { cm: "json" },
  py: { hljs: "python", cm: "python" },
  go: { hljs: "go" },
  rs: { hljs: "rust" },
  java: { hljs: "java" },
  rb: { hljs: "ruby" },
  php: { hljs: "php" },
  sh: { hljs: "bash" },
  bash: { hljs: "bash" },
  zsh: { hljs: "bash" },
  c: { hljs: "c" },
  h: { hljs: "c" },
  cpp: { hljs: "cpp" },
  hpp: { hljs: "cpp" },
  cs: { hljs: "csharp" },
  swift: { hljs: "swift" },
  kt: { hljs: "kotlin" },
  css: { hljs: "css", cm: "css" },
  scss: { hljs: "scss", cm: "css" },
  html: { hljs: "xml", cm: "html" },
  htm: { cm: "html" },
  xml: { hljs: "xml" },
  sql: { hljs: "sql" },
  yaml: { hljs: "yaml" },
  yml: { hljs: "yaml" },
  toml: { hljs: "ini" },
  ini: { hljs: "ini" },
  md: { cm: "markdown" },
  markdown: { cm: "markdown" },
  mdx: { cm: "markdown" }
};

// The highlight.js language id for a bare (already-lowercased) extension, or
// undefined for extensions that were never treated as highlightable code.
export function hljsLanguage(ext: string): string | undefined {
  return LANGUAGES[ext]?.hljs;
}

// The CodeMirror loader key for a bare (already-lowercased) extension, or
// undefined for plain text (the editor still works, just without syntax
// awareness).
export function codeMirrorLang(ext: string): CodeMirrorLang | undefined {
  return LANGUAGES[ext]?.cm;
}
