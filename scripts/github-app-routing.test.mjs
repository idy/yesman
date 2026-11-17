import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'));
const start = source.indexOf('async function requireGitHubRouting(');
const end = source.indexOf('// refreshHidden', start);
function guard(env, answer) {
  return runInNewContext(`${source.slice(start, end)}\nrequireGitHubRouting()`, {
    process: { env }, AbortSignal,
    call: async (method, path) => { assert.equal(method, 'GET'); assert.equal(path, '/v1/github-app/mode'); return answer(); },
  });
}
test('wrapper failure retains a saved App selection instead of using personal gh', async () => {
  await assert.rejects(guard({ YESMAN_GH_REQUIRED: '1' }, () => ({ mode: 'app' })), /no command ran with fallback credentials/);
  await assert.rejects(guard({ YESMAN_GH_REQUIRED: '1' }, () => { throw new Error('offline'); }), /mode could not be verified/);
  await assert.rejects(guard({ YESMAN_GH_REQUIRED: '1' }, () => ({ mode: 'unknown' })), /no command ran/);
  await guard({ YESMAN_GH_REQUIRED: '1' }, () => ({ mode: 'personal' }));
});
test('unused App identity and a working wrapper leave normal shell availability intact', async () => {
  const unexpected = () => { throw new Error('mode lookup should not run'); };
  await guard({}, unexpected);
  await guard({ YESMAN_GH_REQUIRED: '1', YESMAN_GH_SHIM: '/working/shim' }, unexpected);
});
