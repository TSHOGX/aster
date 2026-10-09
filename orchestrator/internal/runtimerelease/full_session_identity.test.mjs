// Qualification fixture for the pinned Pi 0.77.0 package. Fake model streams;
// no provider request or application database. This exercises a real Session.
import assert from 'node:assert/strict';
import { AsyncLocalStorage } from 'node:async_hooks';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { createAgentSession, SessionManager } from '@earendil-works/pi-coding-agent';
import { getModel } from '@earendil-works/pi-ai';

function answer(n, beforeDone = () => {}) {
  const message = { role: 'assistant', content: [{ type: 'text', text: `answer-${n}` }], stopReason: 'stop', timestamp: Date.now(), usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2 } };
  return {
    async *[Symbol.asyncIterator]() {
      yield { type: 'start', partial: { ...message, content: [] } };
      beforeDone(message);
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
    const oldProcessEvents = session.agent.processEvents.bind(session.agent);
    session.agent.processEvents = async event => {
      if (event.type === 'message_start' && event.message.role === 'user') {
        const token = identities.get(event.message);
        if (token) { consumed.push(token); trace.push(`consume:${token}`); }
      }
      return oldProcessEvents(event);
    };
    session.agent.streamFn = async () => answer(++calls);
    session.subscribe(event => {
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

test('full Session queue clearing cannot retract a drained input', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'queued-prompt-drain-'));
  try {
    const model = getModel('anthropic', 'claude-sonnet-4-5');
    const { session } = await createAgentSession({
      cwd: dir, agentDir: dir, model,
      modelRegistry: {
        hasConfiguredAuth: () => true,
        getApiKeyAndHeaders: async () => ({ ok: true, apiKey: 'fixture-only' }),
      },
      sessionManager: SessionManager.inMemory(dir), noTools: 'all',
    });
    let calls = 0, turns = 0, queued = false;
    let reachedBoundary, releaseBoundary;
    const boundary = new Promise(resolve => { reachedBoundary = resolve; });
    const gate = new Promise(resolve => { releaseBoundary = resolve; });
    const seen = [];
    session.agent.streamFn = async () => answer(++calls);
    const oldProcessEvents = session.agent.processEvents.bind(session.agent);
    session.agent.processEvents = async event => {
      if (event.type === 'turn_start' && ++turns === 2) {
        reachedBoundary();
        await gate;
      }
      if (event.type === 'message_start' && event.message.role === 'user') seen.push(event.message.content[0].text);
      return oldProcessEvents(event);
    };
    session.subscribe(event => {
      if (event.type === 'message_start' && event.message.role === 'assistant' && !queued) {
        queued = true;
        void session.steer('already drained');
      }
    });
    const prompt = session.prompt('initial');
    await boundary;
    assert.equal(session.agent.hasQueuedMessages(), false);
    session.clearQueue();
    releaseBoundary();
    await prompt;
    assert.deepEqual(seen, ['initial', 'already drained']);
    assert.equal(calls, 2);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('full prompt settlement permits definite removal of unconsumed input', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'queued-prompt-settle-'));
  try {
    const model = getModel('anthropic', 'claude-sonnet-4-5');
    const { session } = await createAgentSession({
      cwd: dir, agentDir: dir, model,
      modelRegistry: {
        hasConfiguredAuth: () => true,
        getApiKeyAndHeaders: async () => ({ ok: true, apiKey: 'fixture-only' }),
      },
      sessionManager: SessionManager.inMemory(dir), noTools: 'all',
    });
    const seen = [];
    let calls = 0, queued = false, cleared;
    session.agent.streamFn = async () => answer(++calls, message => {
      if (session.agent.signal.aborted) message.stopReason = 'aborted';
    });
    const oldProcessEvents = session.agent.processEvents.bind(session.agent);
    session.agent.processEvents = async event => {
      if (event.type === 'message_start' && event.message.role === 'user') seen.push(event.message.content[0].text);
      return oldProcessEvents(event);
    };
    session.subscribe(event => {
      if (event.type === 'message_start' && event.message.role === 'assistant' && !queued) {
        queued = true;
        void session.steer('unconsumed');
        session.agent.abort();
        cleared = session.clearQueue();
      }
    });
    await session.prompt('initial');
    assert.deepEqual(seen, ['initial']);
    assert.deepEqual(cleared.steering, ['unconsumed']);
    assert.equal(session.agent.hasQueuedMessages(), false);
    await session.prompt('next');
    assert.deepEqual(seen, ['initial', 'next']);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
