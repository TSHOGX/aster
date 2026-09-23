import { expect, it } from "vitest";
import { ResourceStore, type ResourceKey } from "./resource-store";
import { rememberPreview } from "./preview-cache";

it("retains only small text bytes and drops an obsolete entry when a file grows", () => {
  const store = new ResourceStore("actor");
  const key: ResourceKey = { project: "project", kind: "file", identity: "text" };
  const small = new Blob(["cached text"]);
  rememberPreview(store, key, "notes.txt", small);
  expect(store.peek(key)).toBe(small);
  rememberPreview(store, key, "notes.txt", new Blob([new Uint8Array(256 * 1024 + 1)]));
  expect(store.peek(key)).toBeUndefined();
  rememberPreview(store, key, "notes.xlsx", small);
  expect(store.stats().entries).toBe(0);
});
