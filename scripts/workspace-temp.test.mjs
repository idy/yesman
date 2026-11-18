import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';

// The production shellEnvironment of pi/yesman.ts: the workspace temporary
// directories and caches the app set for the process win over the user's
// variables of any case, while everything else is kept as it was.
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'));
const start = source.indexOf('function shellEnvironment(');
const end = source.indexOf('// A saved App mode', start);
function environment(platform, own, base, injected) {
  return runInNewContext(`${source.slice(start, end)}\nshellEnvironment(base, injected)`, {
    process: { platform, env: own }, API: 'http://127.0.0.1:1', TOKEN: 'credential', base, injected,
  });
}

const managed = {
  TMPDIR: '/ws/.yesman/tmp/_agent/w/i', TMP: '/ws/.yesman/tmp/_agent/w/i', TEMP: '/ws/.yesman/tmp/_agent/w/i', GOTMPDIR: '/ws/.yesman/tmp/_agent/w/i',
  GOCACHE: '/ws/.yesman/tmp/_cache/go-build', GOMODCACHE: '/ws/.yesman/tmp/_cache/go-mod', npm_config_cache: '/ws/.yesman/tmp/_cache/npm',
  YESMAN_TMP: '/ws/.yesman/tmp/_agent/w/i', YESMAN_EVIDENCE: '/ws/.yesman/evidence/_agent/w/i',
};

for (const shim of [undefined, '/shim']) {
  for (const platform of ['darwin', 'linux', 'win32']) {
    test(`managed temp and cache paths win over user variables on ${platform}${shim ? ' with the gh wrapper' : ''}`, () => {
      const own = { ...managed, ...(shim ? { YESMAN_GH_SHIM: shim, YESMAN_GH_REAL: '/gh' } : {}) };
      const base = { ...managed, HOME: '/home/real', PATH: '/bin' };
      const injected = { TMP: '/tmp', Temp: 'C:\\Windows\\Temp', gocache: '/global/go', NPM_CONFIG_CACHE: '/global/npm', GH_TOKEN: 'personal', OTHER: 'kept' };
      const out = environment(platform, own, base, injected);
      for (const [key, value] of Object.entries(managed)) {
        const names = Object.keys(out).filter((name) => name.toUpperCase() === key.toUpperCase());
        assert.deepEqual(names, [key], `${key} has one spelling`);
        assert.equal(out[key], value, key);
      }
      assert.equal(out.HOME, '/home/real');
      assert.equal(out.GH_TOKEN, 'personal');
      assert.equal(out.OTHER, 'kept');
      assert.equal(out.YESMAN_TOKEN, shim ? 'credential' : undefined);
    });
  }
}

test('a process started without managed paths keeps the user variables', () => {
  const out = environment('linux', {}, { TMPDIR: '/tmp' }, { TMP: '/var/tmp' });
  assert.equal(out.TMPDIR, '/tmp');
  assert.equal(out.TMP, '/var/tmp');
});

test('the shell sets the same variables as the Go start', () => {
  const go = readFileSync(new URL('../internal/agent/workspace_temp.go', import.meta.url), 'utf8');
  const shell = source.slice(start, end);
  for (const key of ['TMPDIR', 'TMP', 'TEMP', 'GOTMPDIR', 'GOCACHE', 'GOMODCACHE', 'npm_config_cache', 'YESMAN_TMP', 'YESMAN_EVIDENCE', 'YESMAN_TMP_ROOT', 'YESMAN_EVIDENCE_ROOT', 'YESMAN_CACHE']) {
    assert.ok(shell.includes(`"${key}"`), `shell sets ${key}`);
    assert.ok(go.includes(`"${key}`), `Go sets ${key}`);
  }
});
