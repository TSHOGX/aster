const clearers = new Set<() => void>();
let controller = new AbortController();
export function registerCache(clear: () => void) {
  clearers.add(clear);
  return () => {
    clearers.delete(clear);
  };
}
export function clientRequestSignal(signal?: AbortSignal | null) {
  return signal
    ? AbortSignal.any([controller.signal, signal])
    : controller.signal;
}
export function clearClientResources() {
  controller.abort();
  controller = new AbortController();
  clearers.forEach((clear) => clear());
}
