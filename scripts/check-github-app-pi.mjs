// Actual pi + production extension + compiled desktop gh wrapper. The local
// broker is controlled here; real gh→mock GitHub is tested separately in Go.
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import assert from 'node:assert/strict';
const root = fileURLToPath(new URL('..', import.meta.url));
const temp = await mkdtemp(join(tmpdir(), 'yesman-github-pi-'));
const shimDir = join(temp, 'shim with spaces'); await mkdir(shimDir);
const binary = join(shimDir, process.platform === 'win32' ? 'gh.exe' : 'gh');
const built = spawnSync('go', ['build', '-o', binary, '.'], { cwd: root, encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr);
const originalGH = spawnSync(process.platform === 'win32' ? 'where' : 'which', ['gh'], { encoding: 'utf8' }).stdout.trim().split(/\r?\n/)[0];
assert.ok(originalGH);
let failEnv = false, failMode = false;
const creates = [], tokens = [];
const api = createServer(async (req, res) => {
  res.setHeader('Content-Type', req.url === '/v1/context' ? 'text/plain' : 'application/json');
  tokens.push(req.headers.authorization);
  if (req.url === '/v1/context') return res.end('Use gh pr create for the synthetic App fixture.');
  if (req.url === '/v1/env') {
    if (failEnv) { res.statusCode = 503; return res.end('{}'); }
    return res.end(JSON.stringify({ env: { GH_TOKEN: 'configured-personal-fixture' }, hide: [{ name: 'GH_TOKEN', value: 'configured-personal-fixture' }] }));
  }
  if (req.url === '/v1/github-app/mode') {
    if (failMode) { res.statusCode = 503; return res.end('{}'); }
    return res.end(JSON.stringify({ mode: 'app' }));
  }
  if (req.url === '/v1/github-app/pulls') {
    let body = ''; for await (const part of req) body += part;
    creates.push({ token: req.headers.authorization, input: JSON.parse(body) });
    return res.end(JSON.stringify({ number: 77, html_url: 'https://github.com/idy/yesman/pull/77', user: { type: 'Bot', login: 'yesman-fixture[bot]' } }));
  }
  res.statusCode = 404; res.end('{}');
});
await new Promise((resolve) => api.listen(0, '127.0.0.1', resolve));
async function run(name, expectedCreate) {
  const dir = join(temp, name); await mkdir(dir); await mkdir(join(dir, 'config'));
  await writeFile(join(dir, 'config', 'settings.json'), JSON.stringify({ packages: [], extensions: [], skills: [], promptTemplates: [], themes: [], compaction: { enabled: false }, retry: { enabled: false }, defaultProjectTrust: 'no' }));
  const audit = join(dir, 'audit.jsonl');
  const env = { PATH: process.env.PATH, HOME: dir, USERPROFILE: dir, TMPDIR: tmpdir(),
    ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    PI_CODING_AGENT_DIR: join(dir, 'config'), PI_OFFLINE: '1', PI_TELEMETRY: '0',
    YESMAN_API: `http://127.0.0.1:${api.address().port}`, YESMAN_TOKEN: `${name}-credential`, YESMAN_AGENT: `alice/${name}`, YESMAN_ROLE: 'worker', YESMAN_WORKSPACE: dir,
    YESMAN_GH_SHIM: shimDir, YESMAN_GH_REAL: originalGH, GH_TOKEN: 'inherited-personal-fixture', GITHUB_APP_AUDIT: audit };
  const child = spawn(process.env.YESMAN_TEST_PI || 'pi', ['--mode', 'rpc', '--provider', 'github-app-fixture', '--model', 'm', '--session-dir', join(dir, 'sessions'), '--tools', 'bash', '-e', join(root, 'pi', 'yesman.ts'), '-e', join(root, 'scripts', 'fixtures', 'github-app-provider.ts')], { cwd: dir, env });
  let stderr = '', buffer = ''; const events = [];
  child.stderr.on('data', (b) => { stderr += b; });
  const ended = new Promise((resolve) => child.once('exit', resolve));
  const settled = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`pi fixture timed out: ${stderr}`)), 25000);
    child.once('error', (e) => { clearTimeout(timer); reject(e); });
    child.stdout.on('data', (b) => { buffer += b; let newline; while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1); if (!line.trim()) continue;
      const event = JSON.parse(line); events.push(event); if (event.type === 'agent_settled') { clearTimeout(timer); resolve(); }
    } });
  });
  const before = creates.length;
  try {
    child.stdin.write(JSON.stringify({ type: 'prompt', id: 'fixture', message: 'Run the synthetic App PR fixture once.' }) + '\n');
    await settled;
    assert.equal(creates.length - before, expectedCreate);
    if (expectedCreate) assert.equal(creates.at(-1).token, `Bearer ${name}-credential`);
    const provider = await readFile(audit, 'utf8');
    const sessions = await readdir(join(dir, 'sessions'));
    const persisted = (await Promise.all(sessions.filter((p) => p.endsWith('.jsonl')).map((p) => readFile(join(dir, 'sessions', p), 'utf8')))).join('\n');
    for (const text of [provider, persisted, JSON.stringify(events)]) {
      assert.equal(text.includes('configured-personal-fixture'), false, 'personal token leaked into model/session');
      assert.equal(text.includes('inherited-personal-fixture'), false);
      assert.equal(text.includes('BEGIN RSA PRIVATE KEY'), false);
    }
    const result = events.find((e) => e.type === 'tool_execution_end' && e.toolName === 'bash');
    assert.ok(result, JSON.stringify(events));
    assert.equal(!!result.isError, !expectedCreate);
  } finally { child.kill('SIGTERM'); await ended; }
}
try {
  await run('first-worker', 1);
  failEnv = true; await run('nested-worker', 1); // fresh worker credential, same routing even if command env fails
  failEnv = false; failMode = true; await run('failed-broker', 0);
  assert.equal(creates.length, 2);
  console.log('PASS: actual pi shell → production gh wrapper, worker credentials, missing env, no personal fallback, model/session redaction');
} finally { api.closeAllConnections(); await new Promise((resolve) => api.close(resolve)); await rm(temp, { recursive: true, force: true }); }
