import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { SHA256 } from './sha256.mjs';
for (const size of [0,1,55,56,63,64,65,127,128,1024,65536,1048576]) {
  test(`incremental SHA-256 ${size} bytes`, () => {
    const data = randomBytes(size);
    const expected = 'sha256:' + createHash('sha256').update(data).digest('hex');
    for (const chunk of [1,63,64,65,65536]) {
      const hash = new SHA256();
      for (let offset=0;offset<size;offset+=chunk) hash.update(data.subarray(offset,offset+chunk));
      assert.equal(hash.digest(), expected);
      assert.throws(() => hash.digest());
    }
  });
}
