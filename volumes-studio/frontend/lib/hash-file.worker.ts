import { SHA256 } from './sha256.mjs';
self.onmessage = async (event: MessageEvent<File>) => {
  try {
    const file = event.data;
    const hash = new SHA256();
    for (let offset = 0; offset < file.size; offset += 1 << 20) {
      hash.update(new Uint8Array(await file.slice(offset, offset + (1 << 20)).arrayBuffer()));
      self.postMessage({ progress: Math.min(offset + (1 << 20), file.size) });
    }
    self.postMessage({ digest: hash.digest() });
  } catch { self.postMessage({ error: 'Unable to read the selected file.' }); }
};
