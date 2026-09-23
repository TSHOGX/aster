import { startTiming, type ResourceCategory } from "./interaction-metrics";

export type ResourceKey = { project: string; kind: ResourceCategory; identity: string; epoch?: string };
type Entry = { key: ResourceKey; value: unknown; bytes: number; used: number; pins: number };
type Flight = {
  key: ResourceKey; id: string; generation: number; consumers: number;
  controller: AbortController; load: (signal: AbortSignal) => Promise<unknown>;
  promise: Promise<unknown>; resolve: (v: unknown) => void; reject: (e: unknown) => void;
  started: boolean; refreshes: number; queued: number; priority: number; queueDone: () => void;
};
const abortError = () => new DOMException("Read canceled", "AbortError");
const keyID = (key: ResourceKey) => JSON.stringify([key.project, key.kind, key.identity, key.epoch || ""]);

/** One instance per authenticated application mount. Commands never enter it. */
export class ResourceStore {
  private entries = new Map<string, Entry>();
  private flights = new Map<string, Flight>();
  private queue: Flight[] = [];
  private running = 0;
  private directoryReads = new Set<string>();
  private fileReads = new Map<string, number>();
  private bytes = 0;
  private generation = 0;
  private lastProject = "";
  constructor(readonly actor: string, readonly byteLimit = 32 * 1024 * 1024, readonly entryLimit = 256) {}

  peek<T>(key: ResourceKey): T | undefined {
    const e = this.entries.get(keyID(key));
    if (e) e.used = performance.now();
    return e?.value as T | undefined;
  }
  put<T>(key: ResourceKey, value: T): boolean {
    const id = keyID(key), previous = this.entries.get(id);
    // Estimate UTF-16 strings plus decoded/object overhead, not wire bytes alone.
    const bytes = value instanceof Blob ? value.size + 256 : JSON.stringify(value).length * 3 + 256;
    const otherPinned = [...this.entries.values()].reduce((n,e)=>n+(e !== previous && e.pins ? e.bytes : 0),0);
    if (bytes + otherPinned > this.byteLimit) return false;
    this.bytes -= previous?.bytes || 0;
    this.entries.set(id, { key, value, bytes, used: performance.now(), pins: previous?.pins || 0 });
    this.bytes += bytes;
    this.evict();
    return this.entries.has(id);
  }
  pin(key: ResourceKey): () => void {
    const id = keyID(key);
    if (!this.entries.has(id)) this.entries.set(id, {key, value: undefined, bytes: 0, used: performance.now(), pins: 0});
    this.entries.get(id)!.pins++;
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const entry = this.entries.get(id);
      if (entry) entry.pins = Math.max(0, entry.pins - 1);
      this.evict();
    };
  }
  private evict() {
    const candidates = [...this.entries].filter(([, e]) => !e.pins).sort((a,b) => a[1].used-b[1].used);
    for (const [id,e] of candidates) {
      if (this.bytes <= this.byteLimit && this.entries.size <= this.entryLimit) break;
      this.entries.delete(id); this.bytes -= e.bytes;
    }
  }
  stats() {
    return {bytes: this.bytes, pinnedBytes: [...this.entries.values()].reduce((n,e)=>n+(e.pins?e.bytes:0),0), entries:this.entries.size, running:this.running, queued:this.queue.length, consumers:[...this.flights.values()].reduce((n,f)=>n+f.consumers,0)};
  }
  clear(project?: string) {
    this.generation++;
    for (const [id,e] of this.entries) if (!project || e.key.project === project) { this.entries.delete(id); this.bytes -= e.bytes; }
    for (const f of [...this.flights.values()]) if (!project || f.key.project === project) this.cancel(f);
  }
  forget(key: ResourceKey) {
    const id = keyID(key), entry = this.entries.get(id);
    if (!entry) return;
    this.bytes -= entry.bytes;
    if (entry.pins) { entry.value = undefined; entry.bytes = 0; }
    else this.entries.delete(id);
  }
  invalidate(matches: (key: ResourceKey) => boolean) {
    for (const entry of this.entries.values()) if (matches(entry.key)) this.forget(entry.key);
    for (const f of this.flights.values()) if (matches(f.key)) f.generation++;
  }
  private cancel(f: Flight) {
    f.controller.abort();
    if (this.flights.get(f.id) === f) this.flights.delete(f.id);
    this.queue = this.queue.filter(q => q !== f);
    f.reject(abortError());
  }
  /** Concurrent exact-key consumers share one read and one finite-burst successor.
   * Each caller owns only its reference; only the last release aborts the fetch.
   */
  read<T>(key: ResourceKey, load: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal, priority = 1): Promise<T> {
    if (signal?.aborted) return Promise.reject(abortError());
    const id = keyID(key);
    let flight = this.flights.get(id);
    if (!flight) {
      if (this.queue.length >= 128) return Promise.reject(new Error("Read capacity exhausted. Try again shortly."));
      let resolve!: (v: unknown) => void, reject!: (e: unknown) => void;
      const promise = new Promise<unknown>((yes,no)=>{resolve=yes;reject=no;});
      flight = {key,id,generation:0,consumers:0,controller:new AbortController(),load,promise,resolve,reject,started:false,refreshes:0,queued:performance.now(),priority,queueDone:startTiming(key.kind,"queue")};
      this.flights.set(id,flight); this.queue.push(flight);
    }
    const f = flight;
    f.consumers++;
    const result = new Promise<T>((resolve,reject)=> {
      let done = false;
      const release = () => {
        if (done) return false;
        done = true; signal?.removeEventListener("abort",cancel);
        f.consumers--;
        if (!f.consumers && this.flights.get(id) === f) this.cancel(f);
        return true;
      };
      const cancel = () => { if (release()) reject(abortError()); };
      signal?.addEventListener("abort",cancel,{once:true});
      f.promise.then(v=>{if(release()) resolve(v as T);},e=>{if(release()) reject(e);});
    });
    this.pump();
    return result;
  }
  private pump() {
    const now = performance.now();
    this.queue.sort((a,b)=>(a.priority-(now-a.queued)/1000)-(b.priority-(now-b.queued)/1000) || Number(a.key.project===this.lastProject)-Number(b.key.project===this.lastProject));
    while (this.running < 4) {
      const at = this.queue.findIndex(f =>
        (!this.isFile(f) || (this.fileReads.get(f.key.project) || 0) < 2) &&
        (f.key.kind !== "directory" || !this.directoryReads.has(f.key.project)));
      if (at<0) break;
      const f = this.queue.splice(at,1)[0];
      this.running++; f.started=true; f.queueDone(); this.lastProject=f.key.project;
      if (this.isFile(f)) this.fileReads.set(f.key.project,(this.fileReads.get(f.key.project)||0)+1);
      if (f.key.kind === "directory") this.directoryReads.add(f.key.project);
      void this.run(f);
    }
  }
  private isFile(f: Flight) { return f.key.kind==="file"||f.key.kind==="directory"; }
  private async run(f: Flight) {
    const dirty = f.generation, generation = this.generation;
    let again = false;
    try {
      const value = await f.load(f.controller.signal);
      if (f.controller.signal.aborted || this.flights.get(f.id)!==f) return;
      // Project-scoped clears fence their flights directly, while auth clears
      // fence all callbacks, including loaders that ignore AbortSignal.
      if (generation !== this.generation && !this.flights.has(f.id)) return;
      // A continuous writer must not starve browsing. Validate one successor,
      // then show that bounded snapshot; later hints schedule the next refresh.
      if (dirty!==f.generation && (f.key.kind !== "directory" || f.refreshes++ < 1)) { again=true; return; }
      // Domain owners retain their useful view, tree or small preview explicitly.
      // Keeping each transport reply duplicates paged state and pins unused blobs.
      this.flights.delete(f.id); f.resolve(value);
    } catch (e) {
      if (this.flights.get(f.id)===f) this.flights.delete(f.id);
      f.reject(e);
    } finally {
      this.running--;
      if (f.key.kind === "directory") this.directoryReads.delete(f.key.project);
      if(this.isFile(f)) this.fileReads.set(f.key.project,Math.max(0,(this.fileReads.get(f.key.project)||1)-1));
      if (again && !f.controller.signal.aborted && f.consumers) { f.queued=performance.now(); f.queueDone=startTiming(f.key.kind,"queue"); this.queue.push(f); }
      this.pump();
    }
  }
}
