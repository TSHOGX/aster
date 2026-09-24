// Qualification fixture for the pinned Pi 0.77.0 package. Fake model streams;
// no provider request or application database. This exercises a real Session.
import assert from 'node:assert/strict';
import { AsyncLocalStorage } from 'node:async_hooks';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { createAgentSession } from '../../../sandbox-image/rootfs/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/sdk.js';
import { SessionManager } from '../../../sandbox-image/rootfs/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/session-manager.js';
import { getModel } from '../../../sandbox-image/rootfs/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/index.js';

function answer(n) {
  const message = { role: 'assistant', content: [{ type: 'text', text: `answer-${n}` }], stopReason: 'stop', timestamp: Date.now(), usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2 } };
  return {
    async *[Symbol.asyncIterator]() {
      yield { type: 'start', partial: { ...message, content: [] } };
      yield { type: 'done', message };
    },
    async result() { return message; },
  };
}

test('full Session correlates duplicate expanded Steers by message identity', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'queued-prompt-session-'));
  try {
    const model = getModel('anthropic', 'claude-sonnet-4-5');
    assert(model);
    const modelRegistry = {
      hasConfiguredAuth: () => true,
      getApiKeyAndHeaders: async () => ({ ok: true, apiKey: 'fixture-only' }),
    };
    const { session } = await createAgentSession({
      cwd: dir, agentDir: dir, model, modelRegistry,
      sessionManager: SessionManager.inMemory(dir), noTools: 'all',
    });
    const oldPrompts = session._resourceLoader.getPrompts.bind(session._resourceLoader);
    session._resourceLoader.getPrompts = () => ({ ...oldPrompts(), prompts: [{ name: 'same', content: 'expanded same words' }] });
    const context = new AsyncLocalStorage();
    const identities = new WeakMap();
    const accepted = [], consumed = [], trace = [];
    let calls = 0, queued = false, submissions;
    const oldSteer = session.agent.steer.bind(session.agent);
    const oldPrompt = session.agent.prompt.bind(session.agent);
    session.agent.prompt = (messages, ...rest) => {
      const token = context.getStore();
      if (token) {
        const input = Array.isArray(messages) ? messages.find(m => m.role === 'user') : messages;
        assert.equal(input.role, 'user');
        identities.set(input, token);
      }
      return oldPrompt(messages, ...rest);
    };
    session.agent.steer = message => {
      const token = context.getStore();
      if (token) { identities.set(message, token); accepted.push(token); }
      return oldSteer(message);
    };
    session.agent.streamFn = async () => answer(++calls);
    session.subscribe(event => {
      if (event.type === 'message_start' && event.message.role === 'user') {
        const token = identities.get(event.message);
        if (token) { consumed.push(token); trace.push(`consume:${token}`); }
      }
      if (event.type === 'message_start' && event.message.role === 'assistant' && !queued) {
        queued = true;
        submissions = Promise.all([
          context.run('A', () => session.steer('/same')),
          context.run('B', () => session.prompt('/same', { streamingBehavior: 'steer', source: 'rpc' })),
        ]);
      }
    });
    await context.run('normal', () => session.prompt('initial'));
    await submissions;
    assert.deepEqual(accepted, ['A', 'B']);
    assert.deepEqual(consumed, ['normal', ...accepted]);
    assert.equal(calls, 3);
    assert.equal(session.messages.filter(m => m.role === 'user').length, 3);
    assert.deepEqual(session.messages.filter(m => m.role === 'user').slice(1).map(m => m.content[0].text), ['expanded same words', 'expanded same words']);
    assert(!JSON.stringify(session.messages).includes('fixture-only'));
    assert(!JSON.stringify(session.messages).includes('consume:A'));
    assert.deepEqual(trace, ['consume:normal', 'consume:A', 'consume:B']);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
