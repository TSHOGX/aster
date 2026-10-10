/** Retry only observational reads. Every wait belongs to its consumer's signal. */
export const FILE_BUSY_MESSAGE = "Files are taking longer to load. Please try again in a moment.";

function wait(milliseconds: number, signal?: AbortSignal | null) {
  signal?.throwIfAborted();
  return new Promise<void>((resolve, reject) => {
    const abort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", abort);
      resolve();
    }, milliseconds);
    signal?.addEventListener("abort", abort, { once: true });
  });
}

export async function fetchFileRead(url: string, init: RequestInit = {}) {
  if (init.method && !["GET", "HEAD"].includes(init.method))
    throw new Error("File read retries cannot submit changes.");
  for (let attempt = 0; ; attempt++) {
    init.signal?.throwIfAborted();
    const response = await fetch(url, init);
    let retry = [202, 429, 503].includes(response.status);
    if (response.status === 429) {
      const error = await response.clone().json().catch(() => ({}));
      if (error.code === "quota_exceeded") return response;
    }
    if (response.status === 409) {
      const error = await response.clone().json().catch(() => ({}));
      retry = error.code === "changed_during_read";
    }
    if (!retry || attempt === 6) return response;
    const retryAfter = Number(response.headers.get("Retry-After")) * 1000;
    await response.body?.cancel();
    const backoff = Math.min(2000, 250 * 2 ** attempt);
    await wait(Math.min(2000, Math.max(backoff, retryAfter || 0)) * (0.8 + Math.random() * 0.2), init.signal);
  }
}
