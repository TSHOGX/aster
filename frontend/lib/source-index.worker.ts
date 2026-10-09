import { indexSourceLines, type SourceLexFormat } from "./source-tokenizer";

self.onmessage = (event: MessageEvent<{ text: string; format: SourceLexFormat }>) => {
  const states = indexSourceLines(event.data.text, event.data.format);
  (self as unknown as Worker).postMessage({ states: states.buffer }, [states.buffer]);
};
