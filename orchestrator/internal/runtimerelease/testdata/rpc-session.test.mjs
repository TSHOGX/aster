import assert from 'node:assert/strict';
import { AsyncLocalStorage } from 'node:async_hooks';
import { randomUUID } from 'node:crypto';
import { EventEmitter } from 'node:events';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import { createAgentSession } from '../../../../sandbox-image/rootfs/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/sdk.js';
import { SessionManager } from '../../../../sandbox-image/rootfs/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/session-manager.js';
import { getModel } from '../../../../sandbox-image/rootfs/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/index.js';

const code = readFileSync(process.argv[2], 'utf8').replace(/^import .*;\n/gm, '')
  .replace('export async function runRpcMode', 'async function runRpcMode');
const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}
async function until(condition) {
  for (let n = 0; n < 1000; n++) {
    if (condition()) return;
    await tick();
  }
  throw new Error('native RPC fixture did not reach the expected event');
}
function stream(answer, gate) {
  const message = { role: 'assistant', content: [{ type: 'text', text: answer }],
    stopReason: 'stop', timestamp: Date.now(),
    usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2 } };
  return {
    async *[Symbol.asyncIterator]() {
      yield { type: 'start', partial: { ...message, content: [] } };
      if (gate) await gate.promise;
      yield { type: 'done', message };
    },
    async result() { return message; },
  };
}

test('patched RPC preserves exact native input identity and settlement with real AgentSession', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'queued-rpc-session-'));
  try {
    const model = getModel('anthropic', 'claude-sonnet-4-5');
    const { session } = await createAgentSession({ cwd: dir, agentDir: dir, model,
      modelRegistry: { hasConfiguredAuth: () => true,
        getApiKeyAndHeaders: async () => ({ ok: true, apiKey: 'fixture-only' }) },
      sessionManager: SessionManager.inMemory(dir), noTools: 'all' });
    const originalPrompts = session._resourceLoader.getPrompts.bind(session._resourceLoader);
    session._resourceLoader.getPrompts = () => ({ ...originalPrompts(),
      prompts: [{ name: 'same', content: 'expanded same words' }] });
    const firstResponse = deferred();
    let calls = 0;
    session.agent.streamFn = async () => stream(`answer-${++calls}`, calls === 1 ? firstResponse : null);
    const outputs = [];
    let line;
    const fakeProcess = new EventEmitter();
    Object.assign(fakeProcess, { stdin: new EventEmitter(), hrtime: process.hrtime, platform: process.platform });
    const context = vm.createContext({ AsyncLocalStorage, crypto: { randomUUID }, process: fakeProcess,
      queueMicrotask, console, setTimeout, clearTimeout,
      takeOverStdout() {}, serializeJsonLine: JSON.stringify,
      writeRawStdout(raw) { outputs.push(JSON.parse(raw)); },
      waitForRawStdoutBackpressure: async () => {},
      attachJsonlLineReader(_input, receive) { line = receive; return () => {}; } });
    vm.runInContext(code + '\nglobalThis.start = runRpcMode;', context);
    void context.start({ session, setRebindSession() {} });
    await until(() => !!line);
    const send = body => line(JSON.stringify(body));
    send({ type: 'prompt', id: 'run', input_token: 'normal', message: 'initial' });
    await until(() => outputs.some(e => e.type === 'message_start' && e.message?.role === 'assistant'));
    send({ type: 'steer', id: 'steer-a', run_id: 'run', input_token: 'A', message: '/same' });
    send({ type: 'steer', id: 'steer-b', run_id: 'run', input_token: 'B', message: '/same' });
    await until(() => outputs.filter(e => e.type === 'response' && e.command === 'steer').length === 2);
    assert.equal(outputs.some(e => e.type === 'prompt_end'), false);
    firstResponse.resolve();
    await until(() => outputs.some(e => e.type === 'prompt_end'));
    const consumed = outputs.filter(e => e.type === 'input_consumed');
    assert.deepEqual(consumed.map(e => e.input_token), ['normal', 'A', 'B']);
    assert(consumed.every(e => e.run_id === 'run' && e.run_elapsed_ms >= 0));
    assert.deepEqual(outputs.filter(e => e.type === 'response' && e.command === 'steer').map(e => e.success), [true, true]);
    const reconciled = outputs.find(e => e.type === 'inputs_reconciled');
    assert.deepEqual(reconciled.removed_input_tokens, []);
    assert(outputs.indexOf(reconciled) < outputs.findIndex(e => e.type === 'prompt_end'));
    assert.equal(calls, 3);
    assert.deepEqual(session.messages.filter(m => m.role === 'user').slice(1).map(m => m.content[0].text),
      ['expanded same words', 'expanded same words']);
    assert.equal(JSON.stringify(session.messages).includes('input_token'), false);

    const secondResponse = deferred();
    let laterCalls = 0;
    session.agent.streamFn = async () => stream(`later-${++laterCalls}`, laterCalls === 1 ? secondResponse : null);
    send({ type: 'prompt', id: 'run2', input_token: 'normal2', message: 'second' });
    await until(() => outputs.some(e => e.type === 'input_consumed' && e.input_token === 'normal2'));
    send({ type: 'steer', id: 'steer-c', run_id: 'run2', input_token: 'C', message: 'never consume' });
    await until(() => outputs.some(e => e.type === 'response' && e.id === 'steer-c'));
    send({ type: 'abort', id: 'stop', run_id: 'run2' });
    secondResponse.resolve();
    await until(() => outputs.some(e => e.type === 'response' && e.id === 'stop'));
    await until(() => outputs.some(e => e.type === 'prompt_end' && e.id === 'run2'));
    assert(outputs.find(e => e.type === 'inputs_reconciled' && e.run_id === 'run2').removed_input_tokens.includes('C'));
    assert.equal(outputs.some(e => e.type === 'input_consumed' && e.input_token === 'C'), false);
    send({ type: 'prompt', id: 'run3', input_token: 'normal3', message: 'next' });
    await until(() => outputs.some(e => e.type === 'prompt_end' && e.id === 'run3'));
    assert.equal(outputs.some(e => e.type === 'input_consumed' && e.input_token === 'C'), false);
    assert.deepEqual(outputs.filter(e => e.type === 'input_consumed' && e.run_id === 'run3').map(e => e.input_token), ['normal3']);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
