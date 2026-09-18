const listeners = new Map<string, Set<() => void>>();
const pending = new Map<string, ReturnType<typeof setTimeout>>();
export function invalidateArtifacts(id: string) {
  if (pending.has(id)) return;
  pending.set(
    id,
    setTimeout(() => {
      pending.delete(id);
      listeners.get(id)?.forEach((fn) => fn());
    }, 100),
  );
}
export function subscribeArtifacts(id: string, fn: () => void) {
  const set = listeners.get(id) ?? new Set();
  set.add(fn);
  listeners.set(id, set);
  return () => {
    set.delete(fn);
    if (!set.size) listeners.delete(id);
  };
}
export function clearArtifactInvalidations() {
  pending.forEach(clearTimeout);
  pending.clear();
}
