"use client";

import { useEffect, useRef, useState } from "react";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap, lineNumbers } from "@codemirror/view";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { highlightSelectionMatches, openSearchPanel, search, searchKeymap } from "@codemirror/search";
import { tags } from "@lezer/highlight";
import { sourceLanguage } from "@/lib/source-language";
import { sourceDecorations } from "@/lib/source-decorations";
import { reservePreview } from "@/lib/preview-budget";
import { useLocale } from "@web/i18n";
import { translateText } from "@web/i18n/text";
import { currentLocale } from "@web/i18n/locale";
import { editorPhrases } from "@web/i18n/editor";
import { useT } from "@/lib/i18n";

const sourceTheme = EditorView.theme({
  "&": { height: "100%", backgroundColor: "var(--color-code-bg)", color: "var(--color-code-ink)", fontSize: "12px" },
  ".cm-scroller": { overflow: "auto", fontFamily: "var(--font-mono), ui-monospace, monospace", lineHeight: "1.6" },
  ".cm-content": { padding: "12px 4px" },
  ".cm-gutters": { backgroundColor: "var(--color-code-bg)", color: "var(--color-foreground-muted)", border: "none" },
  ".cm-activeLine": { backgroundColor: "var(--color-surface-muted)" },
  "&.cm-focused": { outline: "none" },
  ".cm-search": { backgroundColor: "var(--color-surface)", color: "var(--color-foreground)", borderBottom: "1px solid var(--color-border)" },
});
const tokenTheme = HighlightStyle.define([
  { tag: tags.keyword, color: "var(--color-code-keyword, #9a44aa)" },
  { tag: [tags.string, tags.attributeValue], color: "var(--color-code-string, #18805b)" },
  { tag: [tags.comment, tags.meta], color: "var(--color-foreground-muted)" },
  { tag: [tags.number, tags.bool], color: "var(--color-code-number, #a45a1e)" },
  { tag: [tags.tagName, tags.attributeName], color: "var(--color-code-tag, #2b67ad)" },
]);

export function SourcePreview({ path, text, initialPosition, onPosition }: {
  path: string;
  text: string;
  initialPosition?: () => number;
  onPosition?: (offset: number) => void;
}) {
  const tr = useT();
  const locale = useLocale();
  const [localization] = useState(() => new Compartment());
  const host = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const positionRef = useRef(onPosition);
  const [line, setLine] = useState(1);
  const [lineCount, setLineCount] = useState(1);
  const [target, setTarget] = useState("");
  useEffect(() => { positionRef.current = onPosition; }, [onPosition]);
  useEffect(() => {
    if (!host.current) return;
    let disposed = false;
    let worker: Worker | undefined;
    let releaseWorker = () => {};
    const language = new Compartment();
    const extensions: Extension[] = [
      EditorState.readOnly.of(true),
      EditorState.transactionFilter.of(transaction => transaction.docChanged ? [] : transaction),
      localization.of([EditorState.phrases.of(editorPhrases(currentLocale())), EditorView.contentAttributes.of({ "aria-label": translateText(currentLocale(), "Source {path}", { path }) })]),
      lineNumbers(), search({ top: true }), highlightSelectionMatches(),
      keymap.of([...searchKeymap]), sourceTheme, syntaxHighlighting(tokenTheme),
      language.of([]),
      EditorView.updateListener.of(update => {
        if (!update.viewportChanged) return;
        const offset = update.view.visibleRanges[0]?.from ?? 0;
        setLine(update.state.doc.lineAt(offset).number);
        positionRef.current?.(offset);
      }),
    ];
    const view = new EditorView({ state: EditorState.create({ doc: text, extensions }), parent: host.current });
    viewRef.current = view;
    setLineCount(view.state.doc.lines);
    const saved = initialPosition?.() ?? 0;
    if (saved > 0) view.dispatch({ effects: EditorView.scrollIntoView(Math.min(saved, view.state.doc.length), { y: "start" }) });
    const extension = path.split(".").pop()?.toLowerCase();
    const indexedFormat = text.length > (1 << 20) && (extension === "xml" || extension === "yaml" || extension === "yml")
      ? extension === "xml" ? "xml" : "yaml" : null;
    if (indexedFormat) {
      try {
        releaseWorker = reservePreview(0, "workers");
        worker = new Worker(new URL("../../lib/source-index.worker.ts", import.meta.url), { type: "module" });
        worker.onmessage = (event: MessageEvent<{ states: ArrayBuffer }>) => {
          if (disposed) return;
          view.dispatch({ effects: language.reconfigure(sourceDecorations(text, indexedFormat, new Uint16Array(event.data.states))) });
          worker?.terminate();
          worker = undefined;
          releaseWorker();
          releaseWorker = () => {};
        };
        worker.onerror = () => { worker?.terminate(); worker = undefined; releaseWorker(); releaseWorker = () => {}; };
        worker.postMessage({ text, format: indexedFormat });
      } catch {
        worker?.terminate();
        worker = undefined;
        releaseWorker();
        releaseWorker = () => {};
      }
    } else {
      void sourceLanguage(path).then(extension => {
        if (!disposed && extension) view.dispatch({ effects: language.reconfigure(extension) });
      });
    }
    return () => { disposed = true; worker?.terminate(); releaseWorker(); viewRef.current = null; view.destroy(); };
    // The content identity owns the view; mode changes recreate it from its saved position.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, text]);
  useEffect(() => { viewRef.current?.dispatch({ effects: localization.reconfigure([EditorState.phrases.of(editorPhrases(locale)), EditorView.contentAttributes.of({ "aria-label": translateText(locale, "Source {path}", { path }) })]) }); }, [locale, localization, path]);
  const jump = () => {
    const view = viewRef.current;
    const number = Number(target);
    if (!view || !Number.isSafeInteger(number) || number < 1 || number > view.state.doc.lines) return;
    const offset = view.state.doc.line(number).from;
    view.dispatch({ selection: { anchor: offset }, effects: EditorView.scrollIntoView(offset, { y: "start" }) });
    view.focus();
  };
  const find = () => {
    const view = viewRef.current;
    if (view) openSearchPanel(view);
  };
  return <div className="source-preview">
    <div className="source-toolbar">
      <strong>{tr("Source")}</strong>
      <span role="status">{tr("Line {current} of {total}", { current: line, total: lineCount })}</span>
      <button type="button" onClick={find}>{tr("Find in file")}</button>
      <form onSubmit={event => { event.preventDefault(); jump(); }}>
        <label htmlFor="source-line-target">{tr("Go to line")}</label>
        <input id="source-line-target" type="number" min={1} max={lineCount} value={target} onChange={event => setTarget(event.target.value)} />
        <button type="submit">{tr("Go")}</button>
      </form>
    </div>
    <div ref={host} className="source-editor" role="region" aria-label={`${tr("Source")} ${path}`} />
  </div>;
}
