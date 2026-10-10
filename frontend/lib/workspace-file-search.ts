import { APIError, type FileEntry, type FilePage } from "./project-api";

export type FileMatch = { path: string; entry: FileEntry };
export type SearchPage = { matches: FileMatch[]; done: boolean; limited: boolean; scanned: number };
type Branch = { path: string; after: string; last: string; resumeAfter?: string; page?: FilePage; bytes: number; index: number };
type Listing = (path: string, after: string, signal: AbortSignal) => Promise<FilePage>;

export const searchQuery = (query: string) => query.trim().normalize("NFC").toLowerCase();
// Go's directory snapshots sort UTF-8 names. Compare Unicode scalar values,
// rather than locale collation or UTF-16 surrogate ordering, when resuming them.
function afterName(name: string, previous: string) {
  const a = Array.from(name), b = Array.from(previous);
  for (let n = 0; n < Math.min(a.length, b.length); n++) {
    const difference = a[n].codePointAt(0)! - b[n].codePointAt(0)!;
    if (difference) return difference > 0;
  }
  return a.length > b.length;
}

/** One cancelable traversal per query; never reads file bodies or follows links.
 * Results replace a 100-row window. A depth-bounded stack retains only the
 * current page of each ancestor, independent of the expanded UI tree.
 */
export class WorkspaceFileSearch {
  private stack: Branch[] = [{path:"", after:"", last:"", bytes:0, index:0}];
  private bytes = 0;
  private scanned = 0;
  private limited = false;
  private pending: FileMatch[] = [];
  readonly query: string;
  constructor(query: string, private list: Listing) { this.query = searchQuery(query); }

  dispose() {
    for (const branch of this.stack) branch.page = undefined;
    this.stack = [];
    this.pending.length = 0;
    this.bytes = 0;
  }
  private pop() { this.bytes -= this.stack.pop()?.bytes ?? 0; }

  async next(signal: AbortSignal, pageBudget = 100, onProgress?: (page: SearchPage) => void): Promise<SearchPage> {
    signal.throwIfAborted();
    const matches = this.pending;
    const started = performance.now();
    let requests = 0;
    while (this.stack.length && matches.length < 100) {
      signal.throwIfAborted();
      const branch = this.stack[this.stack.length - 1];
      if (!branch.page) {
        if (requests >= pageBudget || performance.now() - started > 5000) break;
        requests++;
        let page: FilePage;
        try { page = await this.list(branch.path, branch.after, signal); }
        catch (error) {
          signal.throwIfAborted();
          if (error instanceof APIError && error.status === 409 && branch.after) {
            // Snapshots expire while the user is reading results. Rescan just
            // this branch and skip names already traversed; never replay rows.
            branch.after = "";
            branch.resumeAfter = branch.last;
            continue;
          }
          if (error instanceof APIError && error.status === 404) {
            this.pop(); // A directory disappeared during this observation.
            continue;
          }
          throw error;
        }
        signal.throwIfAborted();
        const bytes = JSON.stringify(page).length * 3;
        if (page.entries.length > 100 || bytes > 512 * 1024 || this.bytes + bytes > 8 * 1024 * 1024) {
          this.limited = true;
          for (const item of this.stack) item.page = undefined;
          this.stack = [];
          this.bytes = 0;
          break;
        }
        this.bytes += bytes;
        branch.bytes = bytes;
        branch.page = page;
        branch.index = 0;
      }
      const entry = branch.page.entries[branch.index];
      if (!entry) {
        onProgress?.({ matches: [...matches], done: false, limited: this.limited, scanned: this.scanned });
        const next = branch.page.next;
        if (next && next !== branch.after) { branch.after = next; branch.page = undefined; this.bytes -= branch.bytes; branch.bytes = 0; }
        else {
          if (next) this.limited = true;
          this.pop();
        }
        continue;
      }
      branch.index++;
      if (branch.resumeAfter && !afterName(entry.name, branch.resumeAfter)) continue;
      branch.resumeAfter = undefined;
      branch.last = entry.name;
      this.scanned++;
      if (!entry.name || /[/\\\0]/.test(entry.name) || [".", ".."].includes(entry.name)) {
        this.limited = true;
        continue;
      }
      const path = branch.path ? `${branch.path}/${entry.name}` : entry.name;
      if (entry.kind === "directory") {
        if (this.stack.length >= 64 || new TextEncoder().encode(path).length > 4096) {
          this.limited = true;
          continue;
        }
        this.stack.push({path, after:"", last:"", bytes:0, index:0});
      } else if (entry.kind === "file" && searchQuery(path).includes(this.query)) {
        matches.push({path, entry});
      }
    }
    // Consuming the last matching row can finish a page exactly at its limit.
    // Retire exhausted ancestors without another network request or empty page.
    while (this.stack.length) {
      const branch = this.stack[this.stack.length - 1];
      if (!branch.page || branch.index < branch.page.entries.length || branch.page.next) break;
      this.pop();
    }
    this.pending = [];
    return {matches, done:this.stack.length === 0, limited:this.limited, scanned:this.scanned};
  }
}
