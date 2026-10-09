import type { Extension } from "@codemirror/state";
import { codeMirrorLang } from "./languages";

/** The reader loads one grammar after first text; the source bundle stays small. */
export async function sourceLanguage(path: string): Promise<Extension | null> {
  const ext = (path.split("/").pop()?.split(".").pop() ?? "").toLowerCase();
  switch (codeMirrorLang(ext)) {
    case "javascript": return (await import("@codemirror/lang-javascript")).javascript();
    case "typescript": return (await import("@codemirror/lang-javascript")).javascript({ typescript: true });
    case "tsx": return (await import("@codemirror/lang-javascript")).javascript({ typescript: true, jsx: true });
    case "json": return (await import("@codemirror/lang-json")).json();
    case "python": return (await import("@codemirror/lang-python")).python();
    case "markdown": return (await import("@codemirror/lang-markdown")).markdown();
    case "html": return (await import("@codemirror/lang-html")).html();
    case "css": return (await import("@codemirror/lang-css")).css();
    case "xml": return (await import("@codemirror/lang-xml")).xml();
    case "yaml": return (await import("@codemirror/lang-yaml")).yaml();
    default: return null;
  }
}
