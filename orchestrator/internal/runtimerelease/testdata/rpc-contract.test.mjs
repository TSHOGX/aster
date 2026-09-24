import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { AsyncLocalStorage } from 'node:async_hooks';
import { EventEmitter } from 'node:events';
import vm from 'node:vm';
import { test } from 'node:test';

const code = readFileSync(process.argv[2], 'utf8').replace(/^import .*;\n/gm, '').replace('export async function runRpcMode', 'async function runRpcMode');
const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve, reject; const promise = new Promise((a,b) => {resolve=a;reject=b;}); return {promise,resolve,reject}; }
async function fixture(mode = 'normal') {
  const outputs = [], queue = [], prompt = deferred(), preflight = deferred();
  let line, listener;
  const agent = {
    prompt(message) { agent.processEvents({type:'message_start',message}); },
    steer(message) { queue.push(message); },
    processEvents(event) { listener?.(event); },
    subscribe() { return () => {}; },
  };
  const session = {
    agent, bindExtensions: async () => {}, subscribe(fn) {listener=fn;return () => {};},
    async prompt(text, options) {
      if (mode === 'handled') return;
      if (mode === 'rejected') throw new Error('preflight rejected');
      options.preflightResult(true);
      agent.prompt({role:'user',content:text});
      await prompt.promise;
      if (mode === 'failed_after_acceptance') throw new Error('model failed');
    },
    async steer(text) { await preflight.promise; agent.steer({role:'user',content:text}); },
    clearQueue() { queue.length = 0; }, abortCompaction() {}, abortRetry() {},
    async abort() { prompt.resolve(); },
  };
  const fakeProcess = new EventEmitter();
  Object.assign(fakeProcess, {stdin:new EventEmitter(),hrtime:process.hrtime,platform:process.platform});
  const context = vm.createContext({AsyncLocalStorage,process:fakeProcess,queueMicrotask,console,
    takeOverStdout() {}, serializeJsonLine:JSON.stringify, writeRawStdout(raw) {outputs.push(JSON.parse(raw));},
    waitForRawStdoutBackpressure:async () => {}, attachJsonlLineReader(_input,fn) {line=fn;return () => {};},
  });
  vm.runInContext(code+'\nglobalThis.start = runRpcMode;',context);
  void context.start({session,setRebindSession() {}}); await tick();
  return {outputs,queue,prompt,preflight,agent,send(command) {line(JSON.stringify(command));}};
}
const normal = {type:'prompt',id:'run',input_token:'normal',message:'same text'};
const steering = {type:'steer',id:'steer',run_id:'run',input_token:'guidance',message:'same text'};

test('late asynchronous Steer cannot enqueue after native prompt settlement', async () => {
  const f=await fixture();f.send(normal);await tick();f.send(steering);await tick();
  f.prompt.resolve();await tick();
  assert.equal(f.outputs.some(e=>e.type==='prompt_end'),false,'settlement must join admitted preflights');
  f.preflight.resolve();await tick();await tick();
  assert.equal(f.queue.length,0);
  assert.equal(f.outputs.find(e=>e.id==='steer'&&e.type==='response').success,false);
  assert.equal(f.outputs.filter(e=>e.type==='inputs_reconciled').length,1);
  assert.equal(f.outputs.at(-1).type,'prompt_end');
});
test('Stop closes preflight admission and clears queued identities at settlement', async () => {
  const f=await fixture();f.send(normal);await tick();f.preflight.resolve();f.send(steering);await tick();
  assert.equal(f.queue.length,1);f.send({type:'abort',id:'stop',run_id:'run'});await tick();await tick();
  assert.equal(f.queue.length,0);
  assert.deepEqual(f.outputs.find(e=>e.type==='inputs_reconciled').removed_input_tokens,['guidance']);
  assert.equal(f.outputs.filter(e=>e.type==='input_consumed').length,1);
});
test('exact identities and elapsed evidence precede normal Session events',async()=>{
  const f=await fixture();f.send(normal);await tick();f.preflight.resolve();f.send(steering);await tick();
  f.agent.processEvents({type:'message_start',message:f.queue.shift()});
  const consumed=f.outputs.filter(e=>e.type==='input_consumed');
  assert.deepEqual(consumed.map(e=>e.input_token),['normal','guidance']);
  assert.ok(consumed.every(e=>e.run_id==='run'&&e.run_elapsed_ms>=0));
  assert.equal(JSON.stringify(f.outputs.filter(e=>e.type==='message_start')).includes('guidance'),false);
  f.prompt.resolve();await tick();await tick();
  assert.deepEqual(f.outputs.find(e=>e.type==='inputs_reconciled').removed_input_tokens,[]);
});
for(const mode of ['handled','rejected'])test(`${mode} preflight does not strand activePrompt or its ledger`,async()=>{
  const f=await fixture(mode);f.send(normal);await tick();await tick();
  assert.deepEqual(f.outputs.find(e=>e.type==='inputs_reconciled').removed_input_tokens,['normal']);
  assert.equal(f.outputs.some(e=>e.type==='input_consumed'),false);
  assert.equal(f.outputs.find(e=>e.type==='response').success,mode==='handled');
  f.send({...normal,id:'next',input_token:'next'});await tick();await tick();
  assert.equal(f.outputs.filter(e=>e.type==='inputs_reconciled').length,2);
});
test('failed prompt reconciles accepted inputs before terminal evidence',async()=>{
  const f=await fixture('failed_after_acceptance');f.send(normal);await tick();
  f.prompt.resolve();await tick();await tick();
  const types=f.outputs.map(e=>e.type);
  assert.ok(types.indexOf('inputs_reconciled')<types.indexOf('prompt_end'));
  assert.equal(f.outputs.find(e=>e.type==='prompt_end').outcome,'failed');
  f.send({...normal,id:'next',input_token:'next'});await tick();
  assert.equal(f.outputs.filter(e=>e.type==='response'&&e.command==='prompt').length,2);
});
