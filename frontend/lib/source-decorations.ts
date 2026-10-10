import { Decoration, ViewPlugin, type DecorationSet, type EditorView, type ViewUpdate } from "@codemirror/view";
import type { Range } from "@codemirror/state";
import { scanXml, yamlTokens, type SourceLexFormat, type Token } from "./source-tokenizer";

function visibleTokens(view: EditorView, text: string, format: SourceLexFormat, states: Uint16Array): DecorationSet {
  const ranges: Range<Decoration>[] = [];
  const add = (token: Token) => {
    if (token.to > token.from) ranges.push(Decoration.mark({ class: `cm-preview-${token.kind}` }).range(token.from, token.to));
  };
  for (const visible of view.visibleRanges) {
    const first = view.state.doc.lineAt(visible.from).number;
    const last = view.state.doc.lineAt(visible.to).number;
    for (let number = first; number <= last; number++) {
      const line = view.state.doc.line(number);
      if (format === "xml") scanXml(text, line.from, line.to, states[number - 1] ?? 0, add);
      else for (const token of yamlTokens(text, line.from, line.to, states[number - 1] ?? 0)) add(token);
    }
  }
  return Decoration.set(ranges, true);
}

export function sourceDecorations(text: string, format: SourceLexFormat, states: Uint16Array) {
  return ViewPlugin.fromClass(class {
    decorations: DecorationSet;
    constructor(view: EditorView) { this.decorations = visibleTokens(view, text, format, states); }
    update(update: ViewUpdate) {
      if (update.viewportChanged) this.decorations = visibleTokens(update.view, text, format, states);
    }
  }, { decorations: plugin => plugin.decorations });
}
