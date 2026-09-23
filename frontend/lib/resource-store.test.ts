import { expect, it } from "vitest";
import { ResourceStore, type ResourceKey } from "./resource-store";
const key: ResourceKey = {project:"p",kind:"conversation",identity:"a"};
const deferred = <T>() => {
  let resolve!: (value:T)=>void;
  const promise=new Promise<T>(yes=>{resolve=yes;});
  return {promise,resolve};
};
it("shares exact-key reads, preserves another consumer on cancellation, and coalesces a dirty burst",async()=>{
  const store=new ResourceStore("actor"), first=deferred<string>(), second=deferred<string>();
  let calls=0;
  const load=()=>++calls===1?first.promise:second.promise;
  const a=new AbortController();
  const one=store.read(key,load,a.signal).catch(e=>e.name);
  const two=store.read(key,load);
  a.abort(); expect(await one).toBe("AbortError");
  for(let n=0;n<10;n++)store.invalidate(()=>true);
  first.resolve("old"); await Promise.resolve();
  expect(calls).toBe(2);
  expect(store.peek(key)).toBeUndefined();
  second.resolve("latest"); expect(await two).toBe("latest");
  expect(store.peek(key)).toBeUndefined();
  expect(store.stats().consumers).toBe(0);
});
it("fences late callbacks on last release and auth/project clearing",async()=>{
  const store=new ResourceStore("actor"), pending=deferred<string>(), abort=new AbortController();
  const read=store.read(key,()=>pending.promise,abort.signal).catch(e=>e.name);
  abort.abort(); expect(await read).toBe("AbortError");
  pending.resolve("secret"); await Promise.resolve();
  expect(store.peek(key)).toBeUndefined();
  const next=deferred<string>();
  const result=store.read(key,()=>next.promise).catch(e=>e.name);
  store.clear("p"); expect(await result).toBe("AbortError");
  next.resolve("revoked"); await Promise.resolve();
  expect(store.stats().bytes).toBe(0);
});
it("bounds concurrency, file concurrency, queued demand and retains distinct page demands",async()=>{
  const store=new ResourceStore("actor"), release=deferred<string>();
  let started=0;
  const reads=Array.from({length:134},(_,n)=>store.read({...key,kind:"directory",identity:`page:${n}`},()=>{started++;return release.promise;}).catch(e=>e.message));
  expect(started).toBe(1); expect(store.stats().queued).toBe(128);
  const selected=store.read({...key,project:"other",identity:"selected"},async()=>"selected");
  // Full queues explicitly reject rather than silently dropping a page.
  await expect(selected).rejects.toThrow("capacity");
  release.resolve("page");
  const values=await Promise.all(reads);
  expect(values.filter(v=>v==="page")).toHaveLength(129);
  expect(store.stats().running).toBe(0);
  expect(store.stats().queued).toBe(0);
});
it("evicts unpinned content and retains the active window independently of other sessions",()=>{
  const store=new ResourceStore("actor",2000,2);
  store.put(key,"A"); const unpin=store.pin(key);
  store.put({...key,identity:"b"},"B");
  store.put({...key,identity:"c"},"C");
  expect(store.peek(key)).toBe("A");
  expect(store.peek({...key,identity:"b"})).toBeUndefined();
  expect(store.put({...key,identity:"oversize"},"x".repeat(3000))).toBe(false);
  unpin(); store.clear(); expect(store.stats().entries).toBe(0);
});

it("transient pages do not duplicate or evict the retained reading window",async()=>{
  const store=new ResourceStore("actor",2000,2);
  store.put(key,"retained window");
  for(let n=0;n<50;n++) await store.read({...key,identity:`page:${n}`},async()=>"transient page");
  expect(store.peek(key)).toBe("retained window");
  expect(store.stats().entries).toBe(1);
  const unpin=store.pin(key);
  store.invalidate(k=>k.identity===key.identity);
  expect(store.peek(key)).toBeUndefined();
  expect(store.stats().bytes).toBe(0);
  store.put(key,"new window");
  expect(store.stats().pinnedBytes).toBeGreaterThan(0);
  unpin();
  expect(store.stats().pinnedBytes).toBe(0);
});

it("serializes directory scans but lets the selected content read alongside them", async () => {
  const store = new ResourceStore("actor"), first = deferred<string>();
  const calls: string[] = [];
  const a = store.read({...key, kind:"directory", identity:"root"}, () => { calls.push("root"); return first.promise; });
  const b = store.read({...key, kind:"directory", identity:"child"}, async () => { calls.push("child"); return "child"; });
  const c = store.read({...key, kind:"file", identity:"selected"}, async () => { calls.push("selected"); return "content"; });
  expect(await c).toBe("content");
  expect(calls).toEqual(["root", "selected"]);
  first.resolve("root");
  await Promise.all([a,b]);
  expect(calls).toEqual(["root", "selected", "child"]);
});
