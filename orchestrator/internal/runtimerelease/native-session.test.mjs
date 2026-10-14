import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { SessionManager } from '@earendil-works/pi-coding-agent';

const message = (role, text) => ({ role, content: [{ type: 'text', text }], timestamp: 0 });
test('v1 creates, resumes, branches and retains compaction without rewriting IDs', () => {
  const dir = mkdtempSync(join(tmpdir(), 'native-v1-'));
  try {
    const manager = SessionManager.create(dir, dir);
    assert.equal(manager.getHeader().version, 1);
    const user = manager.appendMessage(message('user', 'question'));
    const assistant = manager.appendMessage({ ...message('assistant', 'answer'), api: 'openai-completions', provider: 'fixture', model: 'fixture', stopReason: 'stop', usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } } });
    const compact = manager.appendCompaction('summary', assistant, 2);
    const file = manager.getSessionFile();
    const before = readFileSync(file, 'utf8');
    const resumed = SessionManager.open(file, dir, dir);
    assert.equal(readFileSync(file, 'utf8'), before);
    assert.equal(resumed.getHeader().version, 1);
    assert.deepEqual(resumed.getEntries().map(e => e.id), [user, assistant, compact]);
    assert.equal(resumed.buildSessionContext().messages[0].role, 'compactionSummary');
    const branch = resumed.createBranchedSession(compact);
    const branched = SessionManager.open(branch, dir, dir);
    assert.equal(branched.getHeader().version, 1);
    assert.deepEqual(branched.getEntries().map(e => e.id), [user, assistant, compact]);
    const fork = SessionManager.forkFrom(file, join(dir, 'fork-workspace'), dir);
    assert.equal(fork.getHeader().version, 1);
    assert.deepEqual(fork.getEntries().map(e => e.id), [user, assistant, compact]);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('unsupported or missing headers and wrong entry shapes are refused unchanged', () => {
  const dir = mkdtempSync(join(tmpdir(), 'native-invalid-'));
  try {
    const header = { type: 'session', version: 1, id: 'test', timestamp: new Date(0).toISOString(), cwd: dir };
    for (const content of [
      ...[undefined, 0, 2, 3, 99].map(version => JSON.stringify({ ...header, version }) + '\n'),
      '',
      JSON.stringify(header) + '\n' + JSON.stringify({ type: 'message', message: message('user', 'missing tree identity') }) + '\n',
      JSON.stringify(header) + '\n' + JSON.stringify({ type: 'message', id: 'old', parentId: null, message: { role: 'hookMessage' } }) + '\n',
    ]) {
      const file = join(dir, 'invalid.jsonl');
      writeFileSync(file, content);
      assert.throws(() => SessionManager.open(file, dir, dir), /Unsupported native session|Invalid native session/);
      assert.equal(readFileSync(file, 'utf8'), content);
    }
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
