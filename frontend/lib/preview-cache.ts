import { classifyArtifact } from "./mime";
import type { ResourceKey, ResourceStore } from "./resource-store";

// Cache decoded-input bytes only where restoring them is cheap. Rich documents
// keep their separate parse/renderer admission and never retain unused body copies.
export function cacheablePreview(path: string, blob: Blob) {
  return blob.size <= 256 * 1024 &&
    ["code", "text", "markdown", "json", "table"].includes(classifyArtifact(path).kind);
}

export function rememberPreview(store: ResourceStore, key: ResourceKey, path: string, blob: Blob) {
  if (cacheablePreview(path, blob)) store.put(key, blob);
  else store.forget(key);
}
