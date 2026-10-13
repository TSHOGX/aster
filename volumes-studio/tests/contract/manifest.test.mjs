import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import assert from 'node:assert/strict';

const fixtures = new URL('../../contracts/v1/fixtures/', import.meta.url);
const index = JSON.parse(readFileSync(new URL('index.json', fixtures), 'utf8'));
const hash = bytes => `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
const byteOrder = (a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b));

// Independent JavaScript implementation of the wire encoding. JavaScript's
// native UTF-16 sort is deliberately not used for paths or tree records.
function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value !== null && typeof value === 'object') {
    return `{${Object.keys(value).sort(byteOrder).map(key => `${canonical(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`);
}

for (const vector of index) {
  test(`canonical ${vector.name}`, () => {
    const bytes = readFileSync(new URL(`${vector.name}.json`, fixtures));
    const d = JSON.parse(bytes);
    assert.equal(canonical(d), bytes.toString('utf8'));
    assert.equal(hash(Buffer.concat([Buffer.from('volume-manifest-v1\0'), bytes])), vector.descriptor_digest);
    assert.deepEqual(d.entries.map(e => e.path), d.entries.map(e => e.path).sort(byteOrder));
    const records = d.entries.map(e => `${e.kind === 'directory' ? 'd' : 'f'}\0${e.path}\0${e.size}\0${e.digest}\n`).sort(byteOrder);
    assert.equal(hash(records.join('')), d.tree_digest);
    assert.equal(d.entry_count, d.entries.length);
    assert.equal(d.total_bytes, d.entries.reduce((n, e) => n + BigInt(e.size), 0n).toString());
  });
}
