// Optional browser check of Needs you: the production page in Chromium and
// WebKit, with the actual Go service, Store, Manager and the lead's API behind
// a controlled MyGo bridge, and scripted pi processes (TestNeedsYouBrowserBridge).
// Everything lives in temporary directories; no real workspace, setting or
// model is used.
//   YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/check-needs-you-browser.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const fixtureDir = await mkdtemp(`${tmpdir()}/yesman-needs-you-browser-`);
const shots = process.env.YESMAN_NEEDS_YOU_SHOTS || fixtureDir;
const exe = `${fixtureDir}/needsyou.test${process.platform === 'win32' ? '.exe' : ''}`;

async function fixture() {
  const child = spawn(exe, ['-test.run=^TestNeedsYouBrowserBridge$', '-test.timeout=600s'], {
    cwd, env: { ...process.env, YESMAN_TEST_NEEDS_YOU_BROWSER: '1' }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  let output = '';
  child.stderr.on('data', (data) => { output += data; });
  const exited = new Promise((resolve) => child.once('exit', (code) => resolve(code)));
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error('Fixture did not start: ' + output)), 30000);
    child.stdout.on('data', (data) => {
      output += data;
      const match = output.match(/NEEDS_YOU_BROWSER_URL=(http:\/\/[^\s]+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    child.once('error', reject);
    child.once('exit', (code) => { clearTimeout(timer); reject(Error(`Fixture exited ${code}: ${output}`)); });
  }).catch(async (err) => { child.kill(); await exited; throw err; });
  return { url, close: async () => { child.stdin.end(); assert.equal(await exited, 0, output); } };
}

function bridge() {
  localStorage.setItem('yesman.info', 'hidden');
  window.handlers = {};
  window.calls = [];
  const forward = ['State', 'Messages', 'Answer', 'ResolveHandoff'];
  window.mygo = {
    locale: localStorage.getItem('fixture.locale') || 'en',
    on: (name, fn) => { handlers[name] = fn; },
    async call(method, ...args) {
      const m = method.replace('Yesman.', '');
      calls.push([m, ...args]);
      if (forward.includes(m)) {
        const response = await fetch('/fixture/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method: m, args }) });
        const body = await response.json();
        if (body.error) throw Error(body.error);
        if (m === 'State') { body.value.agents ??= []; window.fixtureState = body.value; }
        return body.value;
      }
      switch (m) {
        case 'Repos': case 'Clones': case 'Pulls': case 'ListFiles': return [];
        case 'Settings': return { appearance: 'system', autoFetch: 0 };
        case 'Fetches': return { fetching: [], errors: {} };
        case 'TakeFocus': return '';
        case 'Update': return { enabled: false };
        case 'Models': return { models: [], agent: fixtureState.agents.find((a) => a.id === args[0]) };
        case 'Info': return { knowledge: [], worktrees: [] };
        case 'Board': return { tasks: [] };
        case 'AgentMenu': return '';
        case 'WatchFiles': case 'Abort': case 'Stop': return;
        default: throw Error('Unmocked method ' + m);
      }
    },
  };
  window.control = async (op, fields = {}) => {
    const response = await fetch('/fixture/control', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ op, ...fields }) });
    const body = await response.json();
    if (body.error) throw Error(body.error);
    return body.value;
  };
}

const ask = (d) => 'ASK ' + JSON.stringify(d);
const needsYou = (body) => 'NEEDSYOU ' + JSON.stringify(body);
const results = [];

async function scenario(engineName, page) {
  const ctl = (op, fields) => page.evaluate(([op, fields]) => control(op, fields), [op, fields]);
  const until = async (what, fn, timeout = 15000) => {
    const deadline = Date.now() + timeout;
    for (;;) {
      const value = await fn();
      if (value) return value;
      if (Date.now() > deadline) throw Error(`${engineName}: timed out waiting for ${what}`);
      await page.waitForTimeout(100);
    }
  };
  const agentIs = (id, ok) => until(`${id} state`, async () => ok(await ctl('agent', { agent: id })));
  const observed = () => ctl('observed');
  const prompted = (id, prefix) => until(`${id} prompted ${prefix}`, async () => ((await observed()).prompts[id] ?? []).some((p) => p.startsWith(prefix)));
  const prompts = async (id, prefix) => ((await observed()).prompts[id] ?? []).filter((p) => p.startsWith(prefix)).length;
  const entry = page.locator('[data-sidebar-entry="needs-you-page"]');
  const count = async () => Number(await entry.getAttribute('data-count'));
  // The app pushes changes; the bridge refreshes its whole State instead.
  const settle = (n) => until(`count ${n}`, async () => {
    await page.evaluate(() => handlers.changed(true));
    await page.waitForTimeout(250);
    return (await count()) === n;
  });
  const shot = (name) => page.screenshot({ path: `${shots}/needs-you-${engineName}-${name}.png` });
  const focusedRequest = () => page.evaluate(() => document.activeElement?.closest('[data-request]')?.dataset.request ?? null);

  await entry.waitFor();
  assert.equal(await count(), 0);
  assert.equal(await entry.getAttribute('aria-label'), 'Needs you: nothing waiting');
  await entry.click();
  await page.locator('.needs-you-empty').waitFor();
  assert.equal(await page.locator('#crumbs').innerText(), 'Needs you');
  await shot('empty');

  // A worker's question goes to its lead: it is not the user's.
  await ctl('start-worker', { yesman: 'alice', name: 'w1', message: ask({ id: 'q1', method: 'select', title: 'Allow rm -rf build?', options: ['Allow', 'Block'] }) });
  await agentIs('alice/w1', (i) => i.status === 'waiting');
  await prompted('alice/lead', '[yesman] Worker "w1" asks:');
  await settle(0);
  assert.equal(await page.locator('#yesmen [data-request^="d:alice/w1"]').count(), 0);

  // A lead's own dialog is.
  await ctl('prompt', { agent: 'bob/lead', message: ask({ id: 'qb', method: 'confirm', title: 'Deploy to staging?', message: 'The build passed.' }) });
  await agentIs('bob/lead', (i) => i.status === 'waiting');
  await settle(1);

  // A manual handoff, through the lead's authenticated API; a retried tool
  // call registers and announces nothing twice.
  const login = { add: { type: 'login', title: 'Log in to the npm registry', detail: 'Run `npm login` in your own terminal. Never paste the token here.', task: '#1' }, toolCall: 'call-login' };
  await ctl('prompt', { agent: 'alice/lead', message: needsYou(login) });
  await settle(2);
  await ctl('prompt', { agent: 'alice/lead', message: needsYou(login) });
  await until('the retried call', async () => (await prompts('alice/lead', 'NEEDSYOU')) === 2);
  await agentIs('alice/lead', (i) => i.status === 'idle');
  await settle(2);
  assert.equal((await observed()).notes.filter((n) => n.body === 'Log in to the npm registry').length, 1);

  // The lead hands the worker's question to the user; a stale request ID
  // changes nothing.
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ handOver: { worker: 'w1', requestId: 'q0' }, toolCall: 'call-stale' }) });
  await until('the stale handover', async () => (await prompts('alice/lead', 'NEEDSYOU')) === 3);
  await agentIs('alice/lead', (i) => i.status === 'idle');
  await settle(2);
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ handOver: { worker: 'w1', requestId: 'q1' }, toolCall: 'call-hand' }) });
  await settle(3);
  const notes = (await observed()).notes;
  assert.equal(notes.filter((n) => n.id === 'alice/w1').length, 1, JSON.stringify(notes));

  // The list: what to do, where from, what kind, how long.
  await entry.click();
  await page.locator('.needs-you-card').nth(2).waitFor();
  const cards = await page.locator('.needs-you-card').evaluateAll((els) => els.map((el) => ({ type: el.dataset.type, text: el.innerText })));
  assert.deepEqual(cards.map((c) => c.type).sort(), ['confirm', 'login', 'select']);
  const card = (type) => cards.find((c) => c.type === type).text;
  assert.match(card('login'), /Log in[\s\S]*Log in to the npm registry[\s\S]*From Alice 团队[\s\S]*Waiting/);
  assert.match(card('select'), /Choice[\s\S]*Allow rm -rf build\?[\s\S]*From Alice 团队 › a1 Clean the build output/);
  assert.match(card('confirm'), /Yes or no[\s\S]*Deploy to staging\?[\s\S]*The build passed\.[\s\S]*From bob/);
  assert.equal(await page.locator('#yesmen .needs-you-row').count(), 3);
  await shot('list');

  // Opening a handoff lands in its lead's conversation, on it, with the
  // unsent draft kept; where it was asked is found in the work log.
  await page.locator('[data-agent="alice/lead"][data-sidebar-entry="yesman"]').click();
  await page.locator('.composer textarea').fill('unsent alice draft');
  await entry.click();
  await page.locator('.needs-you-card[data-type="login"]').click();
  await until('the handoff focused', async () => (await focusedRequest())?.startsWith('h:alice@'));
  assert.equal(await page.locator('#crumbs').innerText(), 'Alice 团队');
  assert.equal(await page.locator('.composer textarea').inputValue(), 'unsent alice draft');
  assert.equal(await page.locator('.ask.handoff.needs-you-target').count(), 1);
  assert.match(await page.locator('.ask.handoff').innerText(), /npm login[\s\S]*Task t1[\s\S]*checks it before relying on it/);
  await page.getByRole('button', { name: 'Show where it was asked' }).click();
  const tool = page.locator('#thread [data-tool="call-login"].needs-you-target');
  await tool.waitFor();
  assert.ok(await tool.evaluate((el) => { const r = el.getBoundingClientRect(); const s = document.querySelector('#scroll').getBoundingClientRect(); return r.bottom > s.top && r.top < s.bottom; }));
  await shot('handoff');
  await page.getByRole('button', { name: 'Back to Needs you' }).click();
  await until('focus back on its card', async () => page.evaluate(() => document.activeElement?.matches('.needs-you-card[data-type="login"]')));

  // A handed-over worker question opens on that worker, answered there.
  await page.locator('.needs-you-card[data-type="select"]').click();
  await until('the question focused', async () => (await focusedRequest())?.startsWith('d:alice/w1@'));
  assert.equal((await page.locator('#crumbs').innerText()).split(/\s*\n\s*/).join(''), 'Alice 团队/w1');
  assert.match(await page.locator('.ask').innerText(), /Alice 团队 handed this question to you\./);
  await page.locator('.ask').getByRole('button', { name: 'Block', exact: true }).click();
  await agentIs('alice/w1', (i) => i.status !== 'waiting');
  await settle(2);

  // By keyboard from the sidebar: the lead's own question.
  await page.locator('#yesmen [data-request^="d:bob/lead@"]').focus();
  await page.keyboard.press('Enter');
  await until('the dialog focused', async () => (await focusedRequest())?.startsWith('d:bob/lead@'));
  await page.keyboard.press('Enter'); // Yes, the first action
  await agentIs('bob/lead', (i) => i.status !== 'waiting');
  await settle(1);

  // Done reaches the lead as yesman's notice, and only then leaves the list.
  await page.locator('#yesmen [data-request^="h:alice@"]').click();
  await page.locator('.ask.handoff').getByRole('button', { name: 'Mark done' }).click();
  await settle(0);
  await prompted('alice/lead', '[yesman] The user marked n1 (login) "Log in to the npm registry", for task t1 as done. That is their own confirmation');
  assert.equal(await page.locator('.composer textarea').inputValue(), 'unsent alice draft');
  await entry.click();
  await page.locator('.needs-you-empty').waitFor();
  await page.locator('.needs-you-history summary').click();
  assert.match(await page.locator('.needs-you-history').innerText(), /Log in to the npm registry[\s\S]*Marked done/);

  // A dialog that times out is no longer offered, and cannot be answered late.
  await ctl('prompt', { agent: 'alice/lead', message: ask({ id: 'qt', method: 'input', title: 'Pick a tag', timeout: 1500 }) });
  await settle(1);
  await settle(0);
  await assert.rejects(page.evaluate(() => mygo.call('Yesman.Answer', 'alice/lead', 'qt', { value: 'late' })), /no longer waiting/);

  // Archiving takes a yesman's handoffs with it; a stale window cannot close
  // the same-name successor's, which starts with none.
  await ctl('prompt', { agent: 'bob/lead', message: needsYou({ add: { type: 'manual', title: 'Plug in the test phone' }, toolCall: 'call-phone' }) });
  await settle(1);
  const oldHired = await page.evaluate(() => fixtureState.yesmen.find((y) => y.name === 'bob').hiredAt);
  await ctl('dismiss', { yesman: 'bob' });
  await settle(0);
  await ctl('hire', { yesman: 'bob' });
  await settle(0);
  await assert.rejects(page.evaluate((hired) => mygo.call('Yesman.ResolveHandoff', 'bob', hired, 'n1', 'done'), oldHired), /replaced|no handoff/);
  assert.deepEqual(await page.evaluate(() => fixtureState.yesmen.find((y) => y.name === 'bob').handoffs), []);

  // Removing a worker ends its handed-over question.
  await ctl('start-worker', { yesman: 'alice', name: 'w2', message: ask({ id: 'q2', method: 'confirm', title: 'Overwrite the lockfile?' }) });
  await agentIs('alice/w2', (i) => i.status === 'waiting');
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ handOver: { worker: 'w2', requestId: 'q2' }, toolCall: 'call-w2' }) });
  await settle(1);
  await ctl('remove-worker', { agent: 'alice/w2' });
  await settle(0);

  // A reload counts what the app knows: persisted handoffs.
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ add: { type: 'decision', title: 'Choose the release channel', detail: 'Stable or beta. I recommend beta.' }, toolCall: 'call-channel' }) });
  await settle(1);
  await page.reload();
  await entry.waitFor();
  await until('count after reload', async () => (await count()) === 1);

  // Chinese, dark and narrow.
  await page.evaluate(() => localStorage.setItem('fixture.locale', 'zh-Hans'));
  await page.reload();
  await entry.waitFor();
  assert.match(await entry.innerText(), /需要你\s*1/);
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.setViewportSize({ width: 640, height: 760 });
  await entry.click();
  await page.locator('.needs-you-card[data-type="decision"]').waitFor();
  assert.match(await page.locator('.needs-you-card').innerText(), /决定[\s\S]*Choose the release channel[\s\S]*来自 Alice 团队/);
  const overflow = await page.evaluate(() => [...document.querySelectorAll('#yesmen, .needs-you-page')].some((el) => el.scrollWidth > el.clientWidth + 1));
  assert.equal(overflow, false);
  await shot('zh-dark-narrow');
  await page.evaluate(() => localStorage.removeItem('fixture.locale'));
}

try {
  const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
  assert.equal(built.status, 0, built.stderr || built.stdout);
  for (const [engineName, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    const backend = await fixture();
    let browser;
    try {
      browser = await engine.launch({ headless: true });
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 } });
      const errors = [];
      page.on('pageerror', (err) => { errors.push(err.message); console.error(err.stack); });
      await page.addInitScript(bridge);
      await page.goto(backend.url);
      await scenario(engineName, page).catch(async (err) => {
        await page.screenshot({ path: `${shots}/needs-you-${engineName}-failure.png` }).catch(() => {});
        throw err;
      });
      assert.deepEqual(errors, []);
      results.push(`${engineName}: PASS`);
      console.log(`${engineName}: Needs you sources, count, jump/focus/draft, answer, done, expiry, archive/rehire, remove, reload, zh/dark/narrow passed`);
    } finally {
      await browser?.close();
      await backend.close();
    }
  }
} finally {
  if (!process.env.YESMAN_NEEDS_YOU_SHOTS) await rm(fixtureDir, { recursive: true, force: true });
}
