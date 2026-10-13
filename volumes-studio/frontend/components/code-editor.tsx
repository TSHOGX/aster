/* eslint-disable react-hooks/immutability -- Workspace buffers are imperative controller objects; mutations publish through the Studio provider. */
"use client";
import { useLocale } from "../../../web/i18n";
import { currentLocale } from "../../../web/i18n/locale";
import { editorPhrases } from "../../../web/i18n/editor";
import { translateText } from "../../../web/i18n/text";


import { useEffect, useRef, useState } from "react";
import { EditorState, StateEffect, Compartment } from "@codemirror/state";
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
  isolateHistory,
} from "@codemirror/commands";
import {
  bracketMatching,
  HighlightStyle,
  syntaxHighlighting,
} from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { search, searchKeymap } from "@codemirror/search";
import type { BufferFile } from "./studio-provider";
const theme = EditorView.theme({
  "&": {
    height: "100%",
    fontSize: "13px",
    backgroundColor: "var(--surface)",
    color: "var(--foreground)",
  },
  ".cm-scroller": {
    overflow: "auto",
    fontFamily: "var(--font-mono)",
    lineHeight: "1.65",
  },
  ".cm-content": { padding: "16px 0" },
  ".cm-gutters": {
    backgroundColor: "var(--surface)",
    border: "none",
    color: "var(--foreground-muted)",
  },
  ".cm-activeLine,.cm-activeLineGutter": {
    backgroundColor: "var(--surface-muted)",
  },
  ".cm-panels": {
    backgroundColor: "var(--surface)",
    color: "var(--foreground)",
  },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground": {
    backgroundColor: "var(--selection-bg)",
  },
  ".cm-cursor": { borderLeftColor: "var(--foreground)" },
});
const highlights = HighlightStyle.define([
  { tag: tags.keyword, color: "var(--hl-keyword)" },
  { tag: tags.string, color: "var(--hl-string)" },
  { tag: tags.comment, color: "var(--foreground-muted)" },
  { tag: tags.number, color: "var(--hl-constant)" },
  { tag: tags.heading, fontWeight: "bold" },
]);
async function language(path: string) {
  const ext = path.split(".").at(-1)?.toLowerCase();
  switch (ext) {
    case "md":
    case "markdown":
      return (await import("@codemirror/lang-markdown")).markdown();
    case "json":
      return (await import("@codemirror/lang-json")).json();
    case "js":
    case "jsx":
    case "ts":
    case "tsx":
      return (await import("@codemirror/lang-javascript")).javascript({
        typescript: ext === "ts" || ext === "tsx",
        jsx: ext === "jsx" || ext === "tsx",
      });
    case "py":
      return (await import("@codemirror/lang-python")).python();
    case "html":
      return (await import("@codemirror/lang-html")).html();
    case "css":
      return (await import("@codemirror/lang-css")).css();
    case "xml":
    case "svg":
      return (await import("@codemirror/lang-xml")).xml();
    case "yaml":
    case "yml":
      return (await import("@codemirror/lang-yaml")).yaml();
    default:
      return [];
  }
}
export function CodeEditor({
  buffer,
  wrap,
  onChange,
  onSave,
  onPosition,
}: {
  buffer: BufferFile;
  wrap: boolean;
  onChange: (text: string) => void;
  onSave: () => void;
  onPosition: (line: number, column: number) => void;
}) {

  const locale = useLocale();
  const [localization] = useState(() => new Compartment());
  const host = useRef<HTMLDivElement>(null),
    view = useRef<EditorView | null>(null);
  const [callbacks] = useState(() => ({
    current: { onChange, onSave, onPosition },
  }));
  callbacks.current = { onChange, onSave, onPosition };
  const [wrapping] = useState(() => ({ current: new Compartment() }));
  useEffect(() => {
    if (!host.current) return;
    let disposed = false;
    const lang = new Compartment();
    // Reconfigure retained state with this mount's callbacks and compartments.
    // The same extension set serves new buffers and preserves existing history.
    const extensions = [
      lineNumbers(),
      history(),
      highlightActiveLine(),
      bracketMatching(),
      search({ top: true }),
      theme,
      syntaxHighlighting(highlights),
      EditorState.readOnly.of(!!buffer.version),
      EditorView.editable.of(!buffer.version),
      localization.of([EditorState.phrases.of(editorPhrases(currentLocale())), EditorView.contentAttributes.of({ "aria-label": translateText(currentLocale(), "Contents of {path}", { path: buffer.entry.path }) })]),
      wrapping.current.of(wrap ? EditorView.lineWrapping : []),
      lang.of([]),
      keymap.of([
        {
          key: "Mod-s",
          run: () => {
            callbacks.current.onSave();
            return true;
          },
        },
        ...defaultKeymap,
        ...historyKeymap,
        ...searchKeymap,
        indentWithTab,
      ]),
      EditorView.updateListener.of((update) => {
        if (update.docChanged)
          callbacks.current.onChange(update.state.doc.toString());
        if (update.selectionSet || update.docChanged) {
          const head = update.state.selection.main.head;
          const line = update.state.doc.lineAt(head);
          callbacks.current.onPosition(line.number, head - line.from + 1);
        }
      }),
    ];
    const retained = buffer.editorState;
    const state =
      retained && retained.doc.toString() === buffer.text
        ? retained.update({ effects: StateEffect.reconfigure.of(extensions) })
            .state
        : EditorState.create({ doc: buffer.text, extensions });
    const editor = new EditorView({ state, parent: host.current });
    const head = state.selection.main.head;
    const line = state.doc.lineAt(head);
    callbacks.current.onPosition(line.number, head - line.from + 1);
    view.current = editor;
    buffer.isolateHistory = () =>
      editor.dispatch({ annotations: isolateHistory.of("full") });
    if (buffer.scroll) editor.scrollDOM.scrollTop = buffer.scroll;
    void language(buffer.entry.path)
      .then((extension) => {
        if (!disposed)
          editor.dispatch({ effects: lang.reconfigure(extension) });
      })
      .catch(() => {
        /* Plain text editing remains available if a language chunk fails. */
      });
    return () => {
      disposed = true;
      buffer.isolateHistory = undefined;
      buffer.editorState = editor.state;
      buffer.scroll = editor.scrollDOM.scrollTop;
      editor.destroy();
      view.current = null;
    };
    // Each file retains its own complete editor state, including history.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [buffer.key, buffer.editorState === undefined]);
  useEffect(() => {
    if (view.current) {
      try {
        view.current.dispatch({
          effects: wrapping.current.reconfigure(
            wrap ? EditorView.lineWrapping : [],
          ),
        });
      } catch {
        /* Restored state belongs to another mount; its wrapping is already retained. */
      }
    }
  }, [wrap, wrapping]);
  useEffect(() => {
    view.current?.dispatch({ effects: localization.reconfigure([EditorState.phrases.of(editorPhrases(locale)), EditorView.contentAttributes.of({ "aria-label": translateText(locale, "Contents of {path}", { path: buffer.entry.path }) })]) });
  }, [locale, buffer.entry.path, localization]);
  useEffect(() => {
    if (buffer.status === "Saving")
      view.current?.dispatch({ annotations: isolateHistory.of("full") });
  }, [buffer.status]);
  return <div className="code-host" ref={host} />;
}
