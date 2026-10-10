import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createServer } from 'node:http';
import test from 'node:test';
import { DefaultResourceLoader, createAgentSession, SessionManager } from '@earendil-works/pi-coding-agent';
import { getModel } from '@earendil-works/pi-ai';
import { registerScheduleTools } from './platform-schedules.mjs';

const args = { title: 'Result check', prompt: 'Check the named match and report its result with a source.', trigger: { kind: 'at', after_minutes: 15, timezone: 'Asia/Shanghai' } };
const config = { endpoint: 'http://127.0.0.1:1/platform/v1/schedules', credential: 'fixture-credential', session_id: 'logical-session' };
const context = { reference_time: '2026-10-10T10:00:00.000000000Z', project_id: 'project', session_id: config.session_id, run_id: 'original-run' };
const response = data => ({ ok: true, text: async () => JSON.stringify(data) });
function receipt(input) {
  return { receipt: { request_id: input.request_id, reference_time: context.reference_time, schedule: { id: input.id, project_id: 'project', title: input.patch.title, prompt: input.patch.prompt, timezone: input.patch.timezone, state: 'active', next_at: input.patch.run_at } } };
}
function toolsFor(transport) {
  const tools = new Map(); registerScheduleTools({ registerTool: tool => tools.set(tool.name, tool) }, config, transport); return tools;
}

test('parallel invocations capture the assistant entry before awaits and retries keep identical bytes', async () => {
  let leaf = 'assistant-1', clocks = 0;
  const attempts = new Map();
  const tools = toolsFor(async (_url, options) => {
    assert.equal(options.headers.Authorization, `Bearer ${config.credential}`);
    const input = JSON.parse(options.body);
    if (input.action === 'context') { clocks++; await new Promise(resolve => setTimeout(resolve, 5)); return response({ context }); }
    assert.equal(input.expected_run_id, 'original-run'); assert.equal(input.patch.run_at, '2026-10-10T10:15:00.000Z');
    const prior = attempts.get(input.request_id) || []; prior.push(options.body); attempts.set(input.request_id, prior);
    if (prior.length === 1) throw new Error('lost response fixture');
    assert.equal(prior[0], options.body); return response(receipt(input));
  });
  const ctx = { sessionManager: { getLeafId: () => leaf, getSessionId: () => 'native-session' } };
  const first = tools.get('schedules_create').execute('provider-1', args, undefined, undefined, ctx);
  const second = tools.get('schedules_create').execute('provider-2', args, undefined, undefined, ctx);
  leaf = 'tool-result';
  const results = await Promise.all([first, second]);
  assert.equal(clocks, 2); assert.equal(attempts.size, 2);
  assert.notEqual(results[0].details.request_id, results[1].details.request_id);
  assert(!JSON.stringify(results).includes(config.credential));
  assert(!Object.hasOwn(results[0].details.schedule, 'prompt'));
  leaf = 'assistant-2';
  const later = await tools.get('schedules_create').execute('provider-1', args, undefined, undefined, ctx);
  assert.notEqual(later.details.request_id, results[0].details.request_id);
});

test('abort after sending preserves uncertainty and stops automatic retries', async () => {
  const abort = new AbortController(); let calls = 0;
  const tools = toolsFor(async (_url, options) => {
    const input = JSON.parse(options.body); if (input.action === 'context') return response({ context });
    calls++; abort.abort(); throw new Error('secret provider network detail');
  });
  await assert.rejects(tools.get('schedules_create').execute('call', args, abort.signal, undefined, { sessionManager: { getLeafId: () => 'assistant', getSessionId: () => 'native' } }), /outcome_unknown/);
  assert.equal(calls, 1);
});

test('real pinned Pi loads only the explicit bundle and emits normal native tool results', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'platform-native-')); let calls = 0;
  const receipts = new Map();
  const server = createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk;
    assert.equal(req.url, '/platform/v1/schedules'); assert.equal(req.headers.authorization, `Bearer ${config.credential}`);
    const input = JSON.parse(raw); res.setHeader('Content-Type', 'application/json');
    if (input.action === 'context') return res.end(JSON.stringify({ context }));
    calls++; if (!receipts.has(input.request_id)) receipts.set(input.request_id, receipt(input));
    if (calls === 1) return req.socket.destroy();
    res.end(JSON.stringify(receipts.get(input.request_id)));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const previous = process.env.PROJECT_PLATFORM_CONFIG;
  let session;
  try {
    mkdirSync(join(dir, '.pi/extensions'), { recursive: true }); writeFileSync(join(dir, '.pi/extensions/rogue.js'), 'throw new Error("Discovery must be disabled");');
    const filename = join(dir, 'platform.json'); writeFileSync(filename, JSON.stringify({ ...config, endpoint: `http://127.0.0.1:${server.address().port}/platform/v1/schedules` }));
    process.env.PROJECT_PLATFORM_CONFIG = filename;
    const loader = new DefaultResourceLoader({ cwd: dir, agentDir: dir, noExtensions: true, noSkills: true, noPromptTemplates: true, noThemes: true, additionalExtensionPaths: [resolve(import.meta.dirname, 'platform-schedules.mjs')] });
    await loader.reload(); assert.deepEqual(loader.getExtensions().errors, []); assert.equal(loader.getExtensions().extensions.length, 1);
    ({ session } = await createAgentSession({ cwd: dir, agentDir: dir, model: getModel('anthropic', 'claude-sonnet-4-5'), resourceLoader: loader, modelRegistry: { hasConfiguredAuth: () => true, getApiKeyAndHeaders: async () => ({ ok: true, apiKey: 'fixture' }) }, sessionManager: SessionManager.inMemory(dir), tools: ['schedules_create'] }));
    let modelCalls = 0;
    session.agent.streamFn = async () => {
      const first = ++modelCalls === 1;
      const message = { role: 'assistant', content: first ? [{ type: 'toolCall', id: 'reused-provider-id', name: 'schedules_create', arguments: args }] : [{ type: 'text', text: 'Saved' }], stopReason: first ? 'toolUse' : 'stop', timestamp: Date.now(), usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2 } };
      return { async *[Symbol.asyncIterator]() { yield { type: 'start', partial: { ...message, content: [] } }; yield { type: 'done', message }; }, async result() { return message; } };
    };
    const events = []; session.subscribe(event => events.push(event));
    await session.prompt('Check the result in 15 minutes');
    const result = session.messages.find(message => message.role === 'toolResult');
    assert(result && !result.isError); assert.equal(result.details.kind, 'schedule_receipt'); assert.equal(calls, 2); assert.equal(receipts.size, 1);
    assert(events.some(event => event.type === 'tool_execution_end' && event.result.details.kind === 'schedule_receipt'));
    modelCalls = 0; await session.prompt('Create a distinct second task'); assert.equal(receipts.size, 2);
  } finally {
    session?.dispose(); if (previous === undefined) delete process.env.PROJECT_PLATFORM_CONFIG; else process.env.PROJECT_PLATFORM_CONFIG = previous;
    server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); rmSync(dir, { recursive: true, force: true });
  }
});

test('a retry denial cannot turn a lost mutation reply into definite failure', async () => {
  let attempts = 0;
  const tools = toolsFor(async (_url, options) => {
    if (JSON.parse(options.body).action === 'context') return response({ context });
    if (++attempts === 1) throw new Error('reply lost after commit');
    return { ok: false, status: 429, text: async () => JSON.stringify({ error: 'capacity' }) };
  });
  await assert.rejects(tools.get('schedules_create').execute('call', args, undefined, undefined, { sessionManager: { getLeafId: () => 'assistant', getSessionId: () => 'native' } }), /outcome_unknown/);
});

test('bounded checks use finite defaults and finish identity is supplied by the host', async () => {
  let execution;
  const bound = { ...context, schedule_id: 'own-task', schedule_revision: 7, operations: ['get', 'finish'] };
  const tools = toolsFor(async (_url, options) => {
    const input = JSON.parse(options.body);
    if (input.action === 'context') return response({ context: bound });
    execution = input;
    return response({ receipt: { request_id: input.request_id, schedule: { id: input.id, project_id: 'project', title: 'Finished check', state: 'exhausted', timezone: 'UTC' } } });
  });
  const ctx = { sessionManager: { getLeafId: () => 'assistant', getSessionId: () => 'native' } };
  const finished = await tools.get('schedules_finish').execute('finish-call', {}, undefined, undefined, ctx);
  assert.equal(execution.id, 'own-task'); assert.equal(execution.revision, 7); assert.equal(execution.expected_run_id, 'original-run');
  assert.equal(finished.details.kind, 'schedule_receipt');
  await assert.rejects(tools.get('schedules_create').execute('recursive', args, undefined, undefined, ctx), /conflict/);
  const foreground = toolsFor(async (_url, options) => {
    const input = JSON.parse(options.body); if (input.action === 'context') return response({ context }); execution = input; return response(receipt(input));
  });
  await foreground.get('schedules_create').execute('create-check', { ...args, trigger: {kind: 'interval', minutes: 5, timezone: 'UTC'}, check: {expires_at:'2026-10-11T10:00:00Z',starts_at:'2026-10-10T12:00:00Z'} }, undefined, undefined, ctx);
  assert.equal(execution.patch.max_occurrences, 12);assert.equal(execution.patch.notification_policy, 'on_finish');assert.equal(execution.patch.starts_at,'2026-10-10T12:00:00.000Z');
});
