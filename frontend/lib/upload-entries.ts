import type { UploadEntry } from "./api";
import type { SessionStatus } from "./types";

// A session accepts uploads/edits only when no turn is mutating the workspace.
// running_active/checkpointing mean a turn is busy; failed/destroyed are gone.
// The backend enforces this too (409); this gate is UX-only.
const WRITABLE_STATUSES = new Set<SessionStatus>(["created", "running_idle", "checkpointed"]);

export function isSessionWritable(status: SessionStatus | undefined | null): boolean {
  return status ? WRITABLE_STATUSES.has(status) : false;
}

// Convert a FileList (from <input type=file> or webkitdirectory) into upload
// entries. webkitdirectory populates webkitRelativePath; plain multi-file
// selection has an empty webkitRelativePath so we fall back to the file name.
export function entriesFromFileList(files: FileList | File[]): UploadEntry[] {
  const entries: UploadEntry[] = [];
  for (const file of Array.from(files)) {
    const relPath = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
    entries.push({ relPath, file });
  }
  return entries;
}

// Minimal shape of the non-standard FileSystem entry API exposed via
// DataTransferItem.webkitGetAsEntry(). Typed locally since lib.dom omits it.
type FsEntry = {
  isFile: boolean;
  isDirectory: boolean;
  fullPath: string;
  name: string;
};
type FsFileEntry = FsEntry & { file: (cb: (file: File) => void, err: (e: unknown) => void) => void };
type FsDirEntry = FsEntry & { createReader: () => FsDirReader };
type FsDirReader = {
  readEntries: (cb: (entries: FsEntry[]) => void, err: (e: unknown) => void) => void;
};

function readFile(entry: FsFileEntry): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}

function readDirBatch(reader: FsDirReader): Promise<FsEntry[]> {
  return new Promise((resolve, reject) => reader.readEntries(resolve, reject));
}

async function walkEntry(entry: FsEntry, out: UploadEntry[]): Promise<void> {
  if (entry.isFile) {
    const file = await readFile(entry as FsFileEntry);
    // Strip the leading "/" the FileSystem API prepends to fullPath.
    const relPath = entry.fullPath.replace(/^\/+/, "") || file.name;
    out.push({ relPath, file });
    return;
  }
  if (entry.isDirectory) {
    const reader = (entry as FsDirEntry).createReader();
    // readEntries returns results in batches; keep reading until it drains.
    for (;;) {
      const batch = await readDirBatch(reader);
      if (batch.length === 0) break;
      for (const child of batch) {
        await walkEntry(child, out);
      }
    }
  }
}

// Turn a drop's DataTransferItemList into a flat entry list, recursing into
// directories via webkitGetAsEntry when available. Falls back to the plain
// file list when the FileSystem entry API is unsupported.
export async function entriesFromDataTransfer(dt: DataTransfer): Promise<UploadEntry[]> {
  const items = dt.items;
  const getAsEntry = (item: DataTransferItem): FsEntry | null => {
    const fn = (item as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null }).webkitGetAsEntry;
    return typeof fn === "function" ? fn.call(item) : null;
  };

  const roots: FsEntry[] = [];
  if (items && items.length > 0) {
    for (const item of Array.from(items)) {
      if (item.kind !== "file") continue;
      const entry = getAsEntry(item);
      if (entry) roots.push(entry);
    }
  }

  if (roots.length === 0) {
    // No FileSystem entry support: use the flat file list.
    return entriesFromFileList(dt.files);
  }

  const out: UploadEntry[] = [];
  for (const root of roots) {
    await walkEntry(root, out);
  }
  return out;
}
