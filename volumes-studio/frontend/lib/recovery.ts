import type { Entry } from "./api";
import type { TextFormat } from "./workbench";
export type Recovery = {
  id: string;
  account: string;
  library: string;
  branch: string;
  path: string;
  session: string;
  text: string;
  saved: string;
  entry: Entry;
  revision: number;
  format: TextFormat;
  updated: number;
  pending?: { body: string; text: string };
};
let database: Promise<IDBDatabase> | undefined;
function db() {
  return (database ??= new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open("volume-studio-recovery", 1);
    request.onupgradeneeded = () =>
      request.result.createObjectStore("buffers", { keyPath: "id" });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => {
      database = undefined;
      reject(request.error);
    };
  }));
}
export async function recoveries(account: string) {
  const d = await db();
  return new Promise<Recovery[]>((resolve, reject) => {
    const r = d.transaction("buffers").objectStore("buffers").getAll();
    r.onsuccess = () =>
      resolve((r.result as Recovery[]).filter((x) => x.account === account));
    r.onerror = () => reject(r.error);
  });
}
export async function putRecovery(value: Recovery) {
  const d = await db();
  return new Promise<void>((resolve, reject) => {
    const tx = d.transaction("buffers", "readwrite");
    const store = tx.objectStore("buffers");
    const r = store.getAll();
    r.onsuccess = () => {
      const all = (r.result as Recovery[]).filter(
        (x) => x.account === value.account && x.id !== value.id,
      );
      const bytes =
        all.reduce(
          (n, x) => n + new TextEncoder().encode(x.text + x.saved).length,
          0,
        ) + new TextEncoder().encode(value.text + value.saved).length;
      if (bytes > 50 * 1024 * 1024) {
        tx.abort();
        return;
      }
      store.put(value);
    };
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
    tx.onabort = () =>
      reject(
        new Error(
          "Local recovery storage is unavailable or its 50 MiB budget is full.",
        ),
      );
  });
}
export async function deleteRecovery(id: string) {
  const d = await db();
  return new Promise<void>((resolve, reject) => {
    const tx = d.transaction("buffers", "readwrite");
    tx.objectStore("buffers").delete(id);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}
export async function clearRecoveries(account: string) {
  for (const r of await recoveries(account)) await deleteRecovery(r.id);
}
