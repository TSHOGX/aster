// Resource cache budget is an estimate of retained content, not JS heap size.
export class BoundedCache<T> {
  private entries = new Map<string, { value: T; bytes: number }>();
  private bytes = 0;
  constructor(
    private limit: number,
    private maxEntries = 500,
  ) {}
  get(key: string) {
    const entry = this.entries.get(key);
    if (!entry) return;
    this.entries.delete(key);
    this.entries.set(key, entry);
    return entry.value;
  }
  set(key: string, value: T, bytes: number) {
    this.delete(key);
    if (bytes > this.limit) return;
    this.entries.set(key, { value, bytes });
    this.bytes += bytes;
    while (this.bytes > this.limit || this.entries.size > this.maxEntries)
      this.delete(this.entries.keys().next().value!);
  }
  delete(key: string) {
    const entry = this.entries.get(key);
    if (entry) this.bytes -= entry.bytes;
    this.entries.delete(key);
  }
  clear() {
    this.entries.clear();
    this.bytes = 0;
  }
  get sizeBytes() {
    return this.bytes;
  }
}
