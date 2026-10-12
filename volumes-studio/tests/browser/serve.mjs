import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, cpSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomBytes } from 'node:crypto';
const repo = fileURLToPath(new URL('../../../', import.meta.url));
const scratch = mkdtempSync(path.join(tmpdir(), 'studio-browser-'));
const env = { ...process.env, GOTOOLCHAIN: 'go1.24.13', GOSUMDB: 'sum.golang.org', STUDIO_IDENTITY_BROWSER_FIXTURE: '1', STUDIO_BROWSER_ORIGIN: 'https://127.0.0.1:18443', STUDIO_BROWSER_CLIENT_SECRET: randomBytes(32).toString('base64url') };
const children = [];
let closing = false;
function close() {
  if (closing) return;
  closing = true;
  for (const child of children) child.kill('SIGTERM');
  setTimeout(() => { for (const child of children) child.kill('SIGKILL'); rmSync(scratch, { recursive: true, force: true }); process.exit(0); }, 1000).unref();
}
process.on('SIGTERM', close); process.on('SIGINT', close);
function run(command, args, cwd = repo) {
  const result = spawnSync(command, args, { cwd, env, stdio: command === 'openssl' ? 'pipe' : 'inherit' });
  if (result.status !== 0) { close(); throw new Error(`fixture preparation failed: ${command}`); }
}
function start(command, args, cwd = repo) {
  const child = spawn(command, args, { cwd, env, stdio: 'inherit' }); children.push(child);
  child.on('exit', code => { if (!closing) { console.error(`fixture process exited (${code})`); close(); } });
}
run('go', ['-C', 'orchestrator', 'test', '-c', '-o', path.join(scratch, 'identity-fixture'), './internal/producthttp']);
run('go', ['-C', 'volumes-studio/backend', 'build', '-o', path.join(scratch, 'studio'), './cmd/studio']);
run('go', ['-C', 'volumes-studio/backend', 'build', '-o', path.join(scratch, 'front-door'), './cmd/front-door']);
run('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', path.join(scratch, 'key.pem'), '-out', path.join(scratch, 'cert.pem'), '-days', '1', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1']);
start(path.join(scratch, 'identity-fixture'), ['-test.run=^TestStudioBrowserIdentityFixture$', '-test.timeout=20m']);
start(path.join(scratch, 'studio'), ['-listen', '127.0.0.1:18482', '-state-directory', path.join(scratch, 'studio-state'), '-public-origin', env.STUDIO_BROWSER_ORIGIN, '-identity-endpoint', 'http://127.0.0.1:18483', '-identity-secret-env', 'STUDIO_BROWSER_CLIENT_SECRET', '-publisher-id', 'browser-publisher', '-frontend', 'http://127.0.0.1:18402']);
cpSync(path.join(repo,'volumes-studio/frontend/.next/static'),path.join(repo,'volumes-studio/frontend/.next/standalone/.next/static'),{recursive:true});
const frontend=spawn(process.execPath,['.next/standalone/server.js'],{cwd:path.join(repo,'volumes-studio/frontend'),env:{...env,HOSTNAME:'127.0.0.1',PORT:'18402'},stdio:'inherit'});children.push(frontend);
start(path.join(scratch, 'front-door'), ['-listen', '127.0.0.1:18443', '-public-origin', env.STUDIO_BROWSER_ORIGIN, '-application', 'http://127.0.0.1:18481', '-studio', 'http://127.0.0.1:18482', '-cert', path.join(scratch, 'cert.pem'), '-key', path.join(scratch, 'key.pem')]);
