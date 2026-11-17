import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';

// Run the production spawn environment function, with no real agent secrets.
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'));
const start = source.indexOf('function shellEnvironment(');
const end = source.indexOf('// refreshHidden', start);
function environment(platform, own, base, injected) {
  return runInNewContext(`${source.slice(start, end)}\nshellEnvironment(base, injected)`, {
    process: { platform, env: own }, API: 'http://127.0.0.1:12345', TOKEN: 'current-worker-credential', base, injected,
  });
}
for (const platform of ['darwin', 'linux', 'win32']) {
  test(`App wrapper is command-scoped and preserves personal credentials on ${platform}`, () => {
    const separator = platform === 'win32' ? ';' : ':';
    const shim = platform === 'win32' ? 'C:\\yesman data\\.runtime\\gh' : '/tmp/yesman data/gh';
    const own = { YESMAN_GH_SHIM: shim, YESMAN_GH_REAL: '/installed/gh' };
    const base = { PATH: ['/original', shim].join(separator), GH_TOKEN: 'inherited-personal', OPENAI_API_KEY: 'unrelated', YESMAN_TOKEN: 'old-parent-credential' };
    const injected = { GH_TOKEN: 'explicit-personal', GITHUB_TOKEN: 'other-personal', YESMAN_TOKEN: 'attempted-override', YESMAN_GH_REAL: '/wrong/gh' };
    const out = environment(platform, own, base, injected);
    assert.equal(out.PATH, [shim, '/original'].join(separator));
    assert.equal(out.GH_TOKEN, 'explicit-personal');
    assert.equal(out.GITHUB_TOKEN, 'other-personal');
    assert.equal(out.OPENAI_API_KEY, 'unrelated');
    assert.equal(out.YESMAN_TOKEN, 'current-worker-credential');
    assert.equal(out.YESMAN_GH_REAL, '/installed/gh');
    assert.equal(out.YESMAN_API, 'http://127.0.0.1:12345');
    assert.deepEqual(base, { PATH: ['/original', shim].join(separator), GH_TOKEN: 'inherited-personal', OPENAI_API_KEY: 'unrelated', YESMAN_TOKEN: 'old-parent-credential' });
    assert.equal(Object.values(out).some((v) => String(v).includes('ghs_') || String(v).includes('PRIVATE KEY')), false);
    // Even failed /v1/env retrieval still prepends the broker wrapper.
    const failed = environment(platform, own, base, {});
    assert.ok(failed.PATH.startsWith(shim + separator));
    assert.equal(failed.YESMAN_TOKEN, 'current-worker-credential');
  });
}
test('Windows PATH casing does not leave an earlier unwrapped alias', () => {
  const out = environment('win32', { YESMAN_GH_SHIM: 'C:\\shim', YESMAN_GH_REAL: 'C:\\gh.exe' }, { Path: 'C:\\old', PATH: 'C:\\duplicate' }, { Path: 'C:\\chosen' });
  assert.equal(out.PATH, 'C:\\shim;C:\\chosen');
  assert.equal(out.Path, undefined);
});
test('processes without installed wrapper retain the existing command environment', () => {
  const out = environment('darwin', {}, { PATH: '/original', GH_TOKEN: 'original' }, { GH_TOKEN: 'configured' });
  assert.equal(out.PATH, '/original'); assert.equal(out.GH_TOKEN, 'configured');
  assert.equal(out.YESMAN_API, undefined);
});
