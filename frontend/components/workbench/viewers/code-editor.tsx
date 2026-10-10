"use client";

import { useEffect, useRef } from "react";
import { EditorState, type Extension } from "@codemirror/state";
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
} from "@codemirror/view";
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from "@codemirror/commands";
import { bracketMatching, indentOnInput } from "@codemirror/language";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { python } from "@codemirror/lang-python";
import { markdown } from "@codemirror/lang-markdown";
import { html } from "@codemirror/lang-html";
import { css } from "@codemirror/lang-css";
import { xml } from "@codemirror/lang-xml";
import { yaml } from "@codemirror/lang-yaml";

import { codeMirrorLang, type CodeMirrorLang } from "@/lib/languages";

// Instantiate the CodeMirror language extension for a loader key. The key→ext
// mapping lives in lib/languages.ts (shared with the hljs highlighter); this
// switch is the only place the heavy @codemirror/lang-* loaders are pulled in.
function extensionForLang(lang: CodeMirrorLang): Extension {
  switch (lang) {
    case "javascript":
      return javascript();
    case "typescript":
      return javascript({ typescript: true });
    case "tsx":
      return javascript({ typescript: true, jsx: true });
    case "json":
      return json();
    case "python":
      return python();
    case "markdown":
      return markdown();
    case "html":
      return html();
    case "css":
      return css();
    case "xml":
      return xml();
    case "yaml":
      return yaml();
  }
}

// Map a file path to a CodeMirror language extension. Returns undefined for
// plain text (the editor still works, just without syntax awareness).
function languageForPath(path: string): Extension | undefined {
  const ext = (path.split(".").pop() ?? "").toLowerCase();
  const lang = codeMirrorLang(ext);
  return lang ? extensionForLang(lang) : undefined;
}

// Light theme aligned with the app's CSS variables. Kept minimal on purpose.
const appTheme = EditorView.theme({
  "&": {
    height: "100%",
    fontSize: "12px",
    backgroundColor: "var(--color-code-bg)",
    color: "var(--color-code-ink)",
  },
  ".cm-scroller": {
    fontFamily:
      "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace",
    lineHeight: "1.6",
    overflow: "auto",
  },
  ".cm-content": { padding: "12px 0" },
  ".cm-gutters": {
    backgroundColor: "var(--color-code-bg)",
    color: "var(--color-foreground-muted)",
    border: "none",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-activeLine": { backgroundColor: "var(--color-surface-muted)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--color-surface-muted)" },
});

type Props = {
  path: string;
  initialText: string;
  onChange: (text: string) => void;
};

export function CodeEditor({ path, initialText, onChange }: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const onChangeRef = useRef(onChange);

  // Keep the ref pointing at the latest onChange without recreating the editor
  // (the view is rebuilt only when `path` changes).
  useEffect(() => {
    onChangeRef.current = onChange;
  }, [onChange]);

  useEffect(() => {
    if (!hostRef.current) return;
    const lang = languageForPath(path);
    const extensions: Extension[] = [
      lineNumbers(),
      EditorView.contentAttributes.of({ "aria-label": `Edit ${path}` }),
      EditorState.transactionFilter.of((transaction) =>
        transaction.newDoc.length <= 1 << 20 ? transaction : [],
      ),
      history(),
      indentOnInput(),
      bracketMatching(),
      highlightActiveLine(),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      EditorView.lineWrapping,
      appTheme,
      EditorView.updateListener.of((update) => {
        if (update.docChanged) {
          onChangeRef.current(update.state.doc.toString());
        }
      }),
    ];
    if (lang) extensions.push(lang);

    const view = new EditorView({
      state: EditorState.create({ doc: initialText, extensions }),
      parent: hostRef.current,
    });
    return () => view.destroy();
    // Rebuild only when switching files; text edits flow through updateListener.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  return <div ref={hostRef} className="h-full min-h-0 overflow-hidden" />;
}
