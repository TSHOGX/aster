export function hashFile(file: File, progress: (bytes: number) => void, signal?: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const worker = new Worker(new URL('./hash-file.worker.ts', import.meta.url), { type: 'module' });
    const cleanup = () => { worker.terminate(); signal?.removeEventListener('abort', abort); };
    const abort = () => { cleanup(); reject(new DOMException('Upload canceled', 'AbortError')); };
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) { abort(); return; }
    worker.onerror = () => { cleanup(); reject(new Error('File hashing failed.')); };
    worker.onmessage = (event: MessageEvent<{ digest?: string; progress?: number; error?: string }>) => {
      if (event.data.digest) { cleanup(); resolve(event.data.digest); }
      else if (event.data.error) { cleanup(); reject(new Error(event.data.error)); }
      else if (event.data.progress !== undefined) progress(event.data.progress);
    };
    worker.postMessage(file);
  });
}
export async function hashChunk(data: ArrayBuffer): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', data));
  return 'sha256:' + Array.from(digest, x => x.toString(16).padStart(2, '0')).join('');
}
