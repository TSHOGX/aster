import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, cpSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomBytes } from 'node:crypto';
const repo = fileURLToPath(new URL('../../../', import.meta.url));
const scratch = mkdtempSync(path.join(tmpdir(), 'evolution-space-browser-'));
const env = { ...process.env, GOTOOLCHAIN: 'go1.24.13', GOSUMDB: 'sum.golang.org', EVOLUTION_SPACE_IDENTITY_BROWSER_FIXTURE: '1', EVOLUTION_SPACE_BROWSER_ORIGIN: 'https://127.0.0.1:18543', EVOLUTION_SPACE_BROWSER_CLIENT_SECRET: randomBytes(32).toString('base64url'), EVOLUTION_SPACE_CENTRAL_SECRET: randomBytes(32).toString('base64url') };
const children = [];
let closing = false;
function close() {
  if (closing) return;
  closing = true;
  for (const child of children) child.kill('SIGTERM');
  setTimeout(() => { for (const child of children) child.kill('SIGKILL'); rmSync(scratch, { recursive: true, force: true }); process.exit(0); }, 1000).unref();
}
process.on('SIGTERM', close); process.on('SIGINT', close);
function run(command, args) {
  const result = spawnSync(command, args, { cwd: repo, env, stdio: command === 'openssl' ? 'pipe' : 'inherit' });
  if (result.status !== 0) { close(); throw new Error(`fixture preparation failed: ${command}`); }
}
function start(command, args, cwd = repo, extra = {}) {
  const child = spawn(command, args, { cwd, env: { ...env, ...extra }, stdio: 'inherit' }); children.push(child);
  child.on('exit', code => { if (!closing) { console.error(`fixture process exited (${code})`); close(); } });
}
run('go', ['-C', 'orchestrator', 'test', '-c', '-o', path.join(scratch, 'identity-fixture'), './internal/producthttp']);
run('go', ['-C', 'evolution-space/backend', 'build', '-o', path.join(scratch, 'evolution-space'), './cmd/evolution-space']);
run('go', ['-C', 'volumes-studio/backend', 'build', '-o', path.join(scratch, 'front-door'), './cmd/front-door']);
run('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', path.join(scratch, 'key.pem'), '-out', path.join(scratch, 'cert.pem'), '-days', '1', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1']);
start(path.join(scratch, 'identity-fixture'), ['-test.run=^TestEvolutionSpaceBrowserIdentityFixture$', '-test.timeout=20m']);
start(path.join(scratch, 'evolution-space'), ['-listen', '127.0.0.1:18584', '-browser-listen', '127.0.0.1:18585', '-state-directory', path.join(scratch, 'evolution-space-state'), '-public-origin', env.EVOLUTION_SPACE_BROWSER_ORIGIN, '-identity-endpoint', 'http://127.0.0.1:18583', '-identity-secret-env', 'EVOLUTION_SPACE_BROWSER_CLIENT_SECRET', '-frontend', 'http://127.0.0.1:18502']);
cpSync(path.join(repo, 'evolution-space/frontend/.next/static'), path.join(repo, 'evolution-space/frontend/.next/standalone/evolution-space/frontend/.next/static'), { recursive: true });
start(process.execPath, ['.next/standalone/evolution-space/frontend/server.js'], path.join(repo, 'evolution-space/frontend'), { HOSTNAME: '127.0.0.1', PORT: '18502' });
start(path.join(scratch, 'front-door'), ['-listen', '127.0.0.1:18543', '-public-origin', env.EVOLUTION_SPACE_BROWSER_ORIGIN, '-application', 'http://127.0.0.1:18581', '-studio', 'http://127.0.0.1:18582', '-evolution-space', 'http://127.0.0.1:18585', '-cert', path.join(scratch, 'cert.pem'), '-key', path.join(scratch, 'key.pem')]);
