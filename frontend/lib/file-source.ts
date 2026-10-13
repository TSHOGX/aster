import { encode, fileURL, fileDownloadURL } from "./project-api";
import type { ConversationSource } from "./conversation-source";

export type FileSource = {
  key: string;
  editable: boolean;
  content: (path: string) => string | undefined;
  download: (path: string) => string | undefined;
};
export function sharedFileSource(
  source: ConversationSource,
  paths: readonly string[],
): FileSource {
  const allowed = new Set(paths);
  return {
    key: source.key,
    editable: false,
    content: (path) =>
      allowed.has(path)
        ? `${source.base}/files/content?path=${encode(path)}`
        : undefined,
    download: (path) =>
      allowed.has(path) && source.capabilities.download
        ? `${source.base}/files/download?path=${encode(path)}`
        : undefined,
  };
}
export function ownedFileSource(
  project: string,
  imported: Record<string, string> = {},
): FileSource {
  const resolve = (path: string) =>
    Object.hasOwn(imported, path) ? imported[path] : path;
  return {
    key: project,
    editable: true,
    content: (path) =>
      resolve(path) ? fileURL(project, resolve(path)) : undefined,
    download: (path) =>
      resolve(path) ? fileDownloadURL(project, resolve(path)) : undefined,
  };
}
