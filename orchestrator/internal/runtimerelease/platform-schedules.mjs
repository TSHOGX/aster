import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';

const string = (maxLength = 128) => ({ type: 'string', minLength: 1, maxLength });
const object = (properties, required = []) => ({ type: 'object', properties, required, additionalProperties: false });
const trigger = object({
  kind: { enum: ['at', 'cron', 'interval', 'manual'] },
  at: string(), after_minutes: { type: 'integer', minimum: 1, maximum: 525600 },
  cron: string(), minutes: { type: 'integer', minimum: 1, maximum: 10080 }, timezone: string(),
}, ['kind', 'timezone']);
const target = object({ kind: { enum: ['current', 'new', 'session'] }, session_id: string() }, ['kind']);
const editable = {
  title: string(256), prompt: string(1048576), trigger, target,
  notify: { type: 'boolean' }, expires_at: string(), state: { enum: ['active', 'paused'] },
};
const definitions = {
  create: { description: 'Persist future work. Default target is this conversation; a new conversation needs self-contained instructions. Use an explicit timezone and offset for absolute times, or after_minutes for a delay anchored to server time. Success confirms saved intent, not execution. Inspect existing tasks before recreating an uncertain request. No sleep, cron process, or external event subscription.', parameters: object(editable, ['title', 'prompt', 'trigger']) },
  list: { description: 'List this Project’s saved task metadata to inspect existing intent, including canceled and exhausted tasks. Does not fetch prompts.', parameters: object({ after: string(), limit: { type: 'integer', minimum: 1, maximum: 50 } }) },
  get: { description: 'Inspect one task and up to 20 recent occurrences. Exhausted means its trigger was consumed, not that execution succeeded.', parameters: object({ id: string(), history_limit: { type: 'integer', minimum: 0, maximum: 20 } }, ['id']) },
  update: { description: 'Edit future task instructions, trigger, target, notification or pause state using its current definition revision. Already admitted work is unchanged. Canceled or exhausted tasks cannot be revived.', parameters: object({ id: string(), revision: { type: 'integer', minimum: 1 }, ...editable }, ['id', 'revision']) },
  cancel: { description: 'Cancel future task admissions using the current definition revision. Already queued or running work is unaffected; use its conversation controls to stop it.', parameters: object({ id: string(), revision: { type: 'integer', minimum: 1 } }, ['id', 'revision']) },
};

function instant(value) {
  if (typeof value !== 'string' || !/T.*(?:Z|[+-]\d\d:\d\d)$/.test(value) || !Number.isFinite(Date.parse(value))) throw new Error('invalid');
  return new Date(value).toISOString();
}
function patch(args, context) {
  const result = {};
  for (const key of ['title', 'prompt', 'notify', 'state']) if (args[key] !== undefined) result[key] = args[key];
  if (args.expires_at !== undefined) result.expires_at = instant(args.expires_at);
  if (args.trigger) {
    const t = args.trigger;
    result.trigger_kind = t.kind; result.timezone = t.timezone; result.cron = ''; result.run_at = '';
    if (t.kind === 'at') {
      if ((t.at === undefined) === (t.after_minutes === undefined)) throw new Error('invalid');
      result.run_at = t.at === undefined ? new Date(Date.parse(context.reference_time) + t.after_minutes * 60000).toISOString() : instant(t.at);
    } else if (t.kind === 'cron') result.cron = t.cron;
    else if (t.kind === 'interval') result.cron = `@every ${t.minutes}m`;
  }
  return result;
}
const knownErrors = new Set(['invalid', 'conflict', 'not_found', 'unavailable', 'capacity', 'draining']);

// Dependencies can be supplied by the deterministic qualification fixture. The
// production default reads only runner-owned configuration, never model args.
export function registerScheduleTools(pi, config, transport = fetch) {
  const endpoint = new URL(config.endpoint);
  if (!['http:', 'https:'].includes(endpoint.protocol) || endpoint.pathname !== '/platform/v1/schedules' || endpoint.search || endpoint.hash || endpoint.username || endpoint.password || !config.credential || !config.session_id) throw new Error('Scheduling configuration unavailable');
  async function post(body, signal) {
    for (let attempt = 0; attempt < 3; attempt++) {
      if (signal?.aborted) throw new Error('outcome_unknown');
      try {
        const response = await transport(endpoint.href, {
          method: 'POST', redirect: 'error', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${config.credential}` },
          body, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(5000)]) : AbortSignal.timeout(5000),
        });
        const raw = await response.text();
        if (raw.length > 2097152) throw new Error('outcome_unknown');
        let data; try { data = JSON.parse(raw); } catch { throw new Error('outcome_unknown'); }
        if (response.ok) return data;
        if (response.status >= 400 && response.status < 500 && knownErrors.has(data.error)) throw new Error(data.error);
      } catch (error) {
        if (knownErrors.has(error.message)) throw error;
        if (signal?.aborted) throw new Error('outcome_unknown');
      }
      if (attempt < 2) await new Promise(resolve => setTimeout(resolve, 100 * (attempt + 1)));
    }
    throw new Error('outcome_unknown');
  }
  for (const [operation, definition] of Object.entries(definitions)) {
    pi.registerTool({ name: `schedules_${operation}`, label: `Schedules: ${operation}`, ...definition,
      async execute(toolCallId, args, signal, _onUpdate, ctx) {
        // Capture before any await: parallel results may advance the native leaf.
        const nativeSession = ctx.sessionManager.getSessionId();
        const leaf = ctx.sessionManager.getLeafId();
        if (!nativeSession || !leaf || !toolCallId) return failure('unavailable');
        const requestId = createHash('sha256').update(JSON.stringify([config.session_id, nativeSession, leaf, toolCallId])).digest('hex');
        let sent = false;
        try {
          const binding = (await post(JSON.stringify({ action: 'context', timezone: args.trigger?.timezone || '' }), signal)).context;
          if (!binding?.run_id || binding.session_id !== config.session_id) return failure('unavailable');
          const mutation = ['create', 'update', 'cancel'].includes(operation);
          const request = {
            action: 'execute', operation, expected_run_id: binding.run_id,
            ...(mutation ? { request_id: requestId, tool_call_id: toolCallId } : {}),
            ...(operation === 'create' ? { id: `schedule-${requestId}` } : args.id ? { id: args.id } : {}),
            ...(args.revision ? { revision: args.revision } : {}),
            ...(operation === 'create' || operation === 'update' ? { patch: patch(args, binding), ...(args.target ? { target: args.target } : {}) } : {}),
            ...(operation === 'list' ? { after: args.after || '', limit: args.limit || 20 } : {}),
            ...(operation === 'get' ? { history_limit: args.history_limit ?? 5 } : {}),
          };
          // Resolve relative times once; every transport retry uses these bytes
          // and this Run, even if the incarnation later executes another Run.
          const bytes = JSON.stringify(request);
          if (Buffer.byteLength(bytes) > 2097152) return failure('invalid');
          sent = true;
          const result = await post(bytes, signal);
          if (mutation) {
            const receipt = result.receipt;
            if (!receipt?.schedule?.id || receipt.request_id !== requestId) return failure('outcome_unknown');
            const { prompt: _prompt, ...metadata } = receipt.schedule;
            const details = { kind: 'schedule_receipt', version: 1, request_id: requestId, reference_time: receipt.reference_time, schedule_id: metadata.id, project_id: metadata.project_id, schedule: metadata, admitted: receipt.admitted };
            return { content: [{ type: 'text', text: `Task saved: ${metadata.title}. State: ${metadata.state}. Next: ${metadata.next_at || 'none'}. Timezone: ${metadata.timezone}. ID: ${metadata.id}.` }], details };
          }
          return { content: [{ type: 'text', text: JSON.stringify(result) }], details: { kind: 'schedule_view', version: 1, ...result } };
        } catch (error) {
          return failure(error.code || (knownErrors.has(error.message) ? error.message : sent ? 'outcome_unknown' : 'unavailable'));
        }
      },
    });
  }
}
function failure(code) {
  const error = new Error(code === 'outcome_unknown'
    ? 'Scheduling outcome_unknown: inspect saved tasks before creating another; the request may have committed.'
    : `Scheduling ${code}: no successful receipt was received.`);
  error.code = code;
  throw error;
}
export default function platformSchedules(pi) {
  const filename = process.env.PROJECT_PLATFORM_CONFIG;
  if (!filename) throw new Error('Scheduling configuration unavailable');
  let config; try { config = JSON.parse(readFileSync(filename, 'utf8')); } catch { throw new Error('Scheduling configuration unavailable'); }
  registerScheduleTools(pi, config);
}
