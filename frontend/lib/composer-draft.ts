// Lightweight cross-component signal for "put this text in the composer".
//
// Mirrors message-jump.ts, and for the same reason: the suggested-prompt cards
// render inside the conversation's empty state, while the input value lives in
// useComposerState inside <Composer/>. There is no prop path between the two
// without lifting the draft into HarnessProvider, which would re-render the
// whole workbench on every keystroke.
//
// A monotonic `seq` accompanies the text so that picking the *same* prompt twice
// still registers as a new request — comparing text alone would silently drop
// the second pick.

export type ComposerDraft = { text: string; seq: number };

let current: ComposerDraft | null = null;
let seq = 0;
const listeners = new Set<() => void>();

export function requestComposerDraft(text: string) {
  seq += 1;
  current = { text, seq };
  for (const listener of listeners) listener();
}

export function clearComposerDraft() {
  if (current === null) return;
  current = null;
  for (const listener of listeners) listener();
}

export function getComposerDraft(): ComposerDraft | null {
  return current;
}

export function subscribeComposerDraft(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
