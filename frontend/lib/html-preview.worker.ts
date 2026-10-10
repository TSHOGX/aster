import { analyzeHTML } from "./html-preview-model";

self.onmessage = (event: MessageEvent<{ source: string }>) => {
  const model = analyzeHTML(event.data.source);
  self.postMessage(model);
};
