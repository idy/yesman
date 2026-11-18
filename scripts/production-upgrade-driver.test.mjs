// The page driver check-production-upgrade.py injects (DRIVER), run with
// in-memory bridge and network stand-ins (as t205's diagnostic fixture did):
// a rejected call is named in the terminal error report, a call that hangs
// is named by one beacon, and the report the runner's wait reads is as
// before. No application or updater runs here.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('./check-production-upgrade.py', import.meta.url), 'utf8');

// driverOf returns the page driver in the runner's source as Python reads
// it: with universal newlines, so a Windows checkout (CRLF) gives the same
// driver as LF. null: the source has no DRIVER block.
function driverOf(text) {
  const match = text.replace(/\r\n?/g, '\n').match(/DRIVER=r'''<script>\n([\s\S]*?)<\/script>'''/);
  return match ? match[1] : null;
}
const driver = driverOf(source);
const missing = "scripts/check-production-upgrade.py has no DRIVER=r'''<script>…</script>''' block to run";

test("the runner's page driver is found, with LF or CRLF line endings", () => {
  assert.ok(driver !== null, missing);
  const lf = source.replace(/\r\n?/g, '\n');
  assert.equal(driverOf(lf), driver);
  assert.equal(driverOf(lf.replace(/\n/g, '\r\n')), driver);
  assert.equal(driverOf('# no driver here\r\n'), null);
});

async function run(scenario) {
  const posts = [], calls = [], timers = [];
  let start;
  const call = (method) => {
    calls.push(method);
    if ((scenario === 'check-rejected' && method.endsWith('.CheckForUpdates')) || (scenario === 'about-rejected' && method.endsWith('.About'))) return Promise.reject(Error('synthetic ' + method + ' error'));
    if (scenario === 'state-pending' && method.endsWith('.State')) return new Promise(() => {});
    if (method.endsWith('.Update')) return Promise.resolve({ state: 'failed', version: '0.1.900003', error: 'synthetic sharing violation' });
    return Promise.resolve({});
  };
  const context = {
    window: { addEventListener: (name, fn) => { if (name === 'DOMContentLoaded') start = fn; }, mygo: { call } },
    document: { querySelector: () => ({}), visibilityState: 'visible' }, localStorage: { getItem: () => 'synthetic' },
    Math: { random: () => 0.5 }, Date, Set, Promise, JSON, String, Error,
    // Timers are only recorded: the poll's sleep never wakes, and the
    // test fires the driver's slow-call timer itself.
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clearTimeout: (n) => { if (timers[n - 1]) timers[n - 1].cleared = true; },
    fetch: async (url, options) => {
      if (url.endsWith('/command')) return { json: async () => ({ id: 3, page: '0.5', op: 'check' }) };
      posts.push({ path: new URL(url).pathname, body: JSON.parse(options.body) });
      return {};
    },
  };
  assert.ok(driver !== null, missing);
  vm.runInNewContext(driver.replaceAll('ENDPOINT', JSON.stringify('http://fixture.invalid')).replaceAll('TOKEN', JSON.stringify('synthetic')), context, { timeout: 500 });
  start();
  for (let i = 0; i < 4; i++) await new Promise(setImmediate);
  for (const t of timers) if (t.ms === 2000 && !t.cleared) t.fn();
  for (let i = 0; i < 4; i++) await new Promise(setImmediate);
  return { posts, calls, timers };
}

test('the report the runner waits on is as before', async () => {
  const { posts } = await run('expected-failed');
  const reports = posts.filter((p) => p.path === '/report');
  assert.equal(reports.length, 1);
  assert.deepEqual(reports[0].body.update, { state: 'failed', version: '0.1.900003', error: 'synthetic sharing violation' });
  assert.equal(reports[0].body.method, undefined);
  assert.equal(posts.filter((p) => p.path === '/awaiting').length, 0);
});

for (const [scenario, method] of [['check-rejected', 'CheckForUpdates'], ['about-rejected', 'About']]) {
  test(`a rejected ${method} is named in the terminal report`, async () => {
    const { posts } = await run(scenario);
    assert.deepEqual(posts.map((p) => p.path), ['/report']);
    assert.equal(posts[0].body.method, method);
    assert.match(posts[0].body.error, new RegExp(`synthetic Yesman\\.${method} error`));
  });
}

test('a call that hangs is named by one beacon, and no report is made', async () => {
  const { posts } = await run('state-pending');
  assert.deepEqual(posts.map((p) => p.path), ['/awaiting']);
  assert.equal(posts[0].body.method, 'State');
  assert.equal(posts[0].body.page, '0.5');
});
