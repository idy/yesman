// Optional browser check of Needs you: the production page in Chromium and
// WebKit, with the actual Go service, Store, Manager and the lead's API behind
// a controlled MyGo bridge, and scripted pi processes (TestNeedsYouBrowserBridge).
// Everything lives in temporary directories; no real workspace, setting or
// model is used.
//   YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/check-needs-you-browser.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
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
  window.unmocked = []; // the page called something this fixture does not answer
  window.fixtureActive = 0;
  const forward = ['State', 'Messages', 'Answer', 'ResolveHandoff', 'RespondHandoff', 'ResetCallMemory', 'Info', 'Usage', 'Shortcuts', 'ListFiles', 'ReadFile'];
  window.mygo = {
    locale: localStorage.getItem('fixture.locale') || 'en',
    on: (name, fn) => { handlers[name] = fn; },
    async call(method, ...args) {
      if (window.fixtureNavigating) return new Promise(() => {});
      const m = method.replace('Yesman.', '');
      calls.push([m, ...args]);
      if (forward.includes(m)) {
        if (m === 'RespondHandoff' && window.failResponse === 'before') {
          window.failResponse = null;
          throw Error('synthetic bridge failure before write');
        }
        window.fixtureActive++;
        let body;
        try {
          const response = await fetch('/fixture/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method: m, args }) });
          body = await response.json();
        } finally {
          window.fixtureActive--;
        }
        // A test can hold a response read already, to return it late.
        if (m === 'Messages' && window.holdMessages) {
          window.heldSeq = body.value?.seq ?? 0;
          await new Promise((resolve) => { window.releaseMessages = resolve; });
        }
        if (m === 'Info' && window.holdInfo === args[0]) await new Promise((resolve) => { window.releaseInfo = resolve; });
        // A test can hold a reset's answer, the reset already made.
        if (m === 'ResetCallMemory' && window.holdReset) await new Promise((resolve) => { window.releaseReset = resolve; });
        if (body.error) throw Error(body.error);
        if (m === 'RespondHandoff' && window.failResponse === 'after') {
          window.failResponse = null;
          throw Error('synthetic lost bridge reply after write');
        }
        if (m === 'State') { body.value.agents ??= []; window.fixtureState = body.value; }
        return body.value;
      }
      switch (m) {
        case 'WorkspaceCheckouts': return { entries: [] };
        case 'Repos': case 'Clones': case 'Pulls': return [];
        case 'Settings': return { appearance: 'system', autoFetch: 0 };
        case 'Fetches': return { fetching: [], errors: {} };
        case 'TakeFocus': return '';
        case 'RestartStatus': return {};
        case 'Update': return { enabled: false };
        case 'Models': return { models: [], agent: fixtureState.agents.find((a) => a.id === args[0]) };
        case 'Board': return { tasks: [] };
        // The info panel's Tasks row: this yesman has no scheduled tasks.
        case 'Schedules': return { plans: [] };
        case 'AgentMenu': return '';
        case 'WatchFiles': case 'Abort': case 'Stop': return;
        // Reading it would start the agent: its context is not measured here.
        case 'Context': return null;
        default: window.unmocked.push(m); throw Error('Unmocked method ' + m);
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
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');
const results = [];
const evidence = [];

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
  const scopeFor = (a, hiredAt) => ({ hiredAt, since: a.since, askedAt: a.dialogAt, handedOverOnly: true });
  const scope = async (id, name = id.split('/')[0]) => scopeFor(await ctl('agent', { agent: id }),
    await page.evaluate((name) => fixtureState.yesmen.find((y) => y.name === name).hiredAt, name));
  const prompted = (id, prefix) => until(`${id} prompted ${prefix}`, async () => ((await observed()).prompts[id] ?? []).some((p) => p.startsWith(prefix)));
  const prompts = async (id, prefix) => ((await observed()).prompts[id] ?? []).filter((p) => p.startsWith(prefix)).length;
  const pane = page.locator('#needs-you');
  const rows = pane.locator('[data-request]');
  const infoRow = (key) => page.locator(`#yesman-info [data-info-entry="${key}"]`);
  const lead = (name) => page.locator(`[data-agent="${name}/lead"][data-sidebar-entry="yesman"]`);
  const composer = page.locator('.composer textarea');
  // What waits on the user from every yesman, counted from the State the
  // page holds: the app pushes changes; the bridge refreshes its whole State.
  const total = () => page.evaluate(async () => (await import('/needsyou.js')).needsYouItems(fixtureState).length);
  const refresh = async () => {
    await page.evaluate(() => handlers.changed(true));
    await page.waitForTimeout(250);
  };
  const settle = (n) => until(`${n} waiting`, async () => {
    await refresh();
    return (await total()) === n;
  });
  const rowsAre = (n) => until(`${n} rows in the pane`, async () => (await rows.count()) === n);
  const shot = async (name) => {
    const path = `${shots}/needs-you-${engineName}-${name}.png`;
    await page.mouse.move(1, 760); // no row shows as hovered
    await page.screenshot({ path });
    evidence.push({ engine: engineName, name, path });
  };
  // Where focus is: a request in the pane or the dock, the pane's heading or
  // close button, the composer, or an info row.
  const focused = () => page.evaluate(() => {
    const a = document.activeElement;
    const request = a?.closest('[data-request]');
    if (request) return `${request.closest('#needs-you') ? 'pane' : request.closest('#dock') ? 'dock' : 'other'}:${request.dataset.request}`;
    if (a?.matches('#needs-you .needs-you-heading')) return 'heading';
    if (a?.dataset?.paneControl) return `pane-${a.dataset.paneControl}`;
    if (a?.matches('#dock .composer textarea')) return 'composer';
    if (a?.dataset?.infoEntry) return `info:${a.dataset.infoEntry}`;
    return a?.tagName ?? null;
  });
  const called = (m) => page.evaluate((m) => calls.filter((c) => c[0] === m).length, m);
  const reload = async () => {
    // Keep the WebKit bridge from tearing down a fetch at navigation.
    await page.evaluate(() => { window.fixtureNavigating = true; });
    await page.waitForFunction(() => window.fixtureActive === 0);
    await page.reload();
  };
  const palette = async (label) => {
    await page.keyboard.press('ControlOrMeta+KeyK');
    await page.locator('.palette-input').waitFor();
    await page.keyboard.type(label);
    const option = page.locator('.palette-option', { hasText: label }).first();
    await option.waitFor();
    return option;
  };
  const showInfo = async () => {
    if (!(await page.locator('#yesman-info [data-info-entry]').count())) await page.getByRole('button', { name: /^(Show the info|显示信息栏)$/ }).click();
    await infoRow('needs-you').waitFor();
  };

  // Nothing gathers every yesman's requests: no sidebar entry, no rows.
  await lead('alice').waitFor();
  assert.equal(await page.locator('#yesmen :is([data-sidebar-entry="needs-you-page"], .needs-you-entry, .needs-you-row, .needs-you-more)').count(), 0);
  await lead('alice').click();
  await composer.waitFor();
  await showInfo();
  await until('the Needs You row', async () => /^Needs You[\s\S]*Nothing waiting/.test(await infoRow('needs-you').innerText()));
  await composer.fill('unsent alice draft');
  await page.locator('.composer input[type=file]').setInputFiles({ name: 'draft.png', mimeType: 'image/png', buffer: png });
  await page.locator('.composer-image img').waitFor();

  // The info panel's row opens the pane beside the conversation, which stays.
  await infoRow('needs-you').click();
  await pane.locator('.needs-you-empty').waitFor();
  assert.equal(await focused(), 'heading');
  assert.equal(await page.locator('#crumbs').innerText(), 'Alice 团队');
  assert.equal(await page.locator('#thread').count(), 1);
  assert.equal(await composer.inputValue(), 'unsent alice draft');
  assert.equal(await page.locator('.composer-image').count(), 1);
  assert.match(await pane.innerText(), /^Needs You[\s\S]*Nothing from Alice 团队 needs you right now\./);
  assert.equal(await infoRow('needs-you').getAttribute('aria-current'), 'page');
  assert.equal(await page.getByRole('button', { name: /Show every yesman|Back to the conversation/ }).count(), 0);
  await shot('empty');

  // The board takes the same place: one or the other, by the palette. The
  // palette's Show Needs You has no key and opens this yesman's pane.
  await composer.focus();
  await palette('Show or hide the board');
  await page.keyboard.press('Enter');
  await page.locator('#board:not([hidden])').waitFor();
  assert.equal(await pane.isHidden(), true);
  assert.equal(await focused(), 'composer');
  const option = await palette('Show Needs You');
  assert.equal(await option.locator('.keycap').count(), 0);
  await page.keyboard.press('Enter');
  await pane.locator('.needs-you-heading').waitFor();
  assert.equal(await page.locator('#board').isHidden(), true);
  await until('the heading focused', async () => (await focused()) === 'heading');
  // Closing by keyboard gives focus back to the info panel's row, which
  // opens the pane again. (WebKit, as macOS by default, tabs to buttons with
  // Option.)
  await page.keyboard.press(engineName === 'webkit' ? 'Alt+Tab' : 'Tab');
  assert.equal(await focused(), 'pane-refresh');
  await page.keyboard.press(engineName === 'webkit' ? 'Alt+Tab' : 'Tab');
  assert.equal(await focused(), 'pane-close');
  await page.keyboard.press('Enter');
  await until('the pane closed', () => pane.isHidden());
  assert.equal(await focused(), 'info:needs-you');
  await page.keyboard.press('Enter');
  await pane.locator('.needs-you-heading').waitFor();
  assert.equal(await focused(), 'heading');
  assert.equal(await composer.inputValue(), 'unsent alice draft');

  // A worker's question goes to its lead: it is not the user's.
  await ctl('start-worker', { yesman: 'alice', name: 'w1', message: ask({ id: 'q1', method: 'select', title: 'Allow rm -rf build?', options: ['Allow', 'Block'] }) });
  await agentIs('alice/w1', (i) => i.status === 'waiting');
  await prompted('alice/lead', '[yesman] Worker "w1" asks:');
  await settle(0);
  assert.equal(await rows.count(), 0);

  // Bob's lead's own question is Bob's: Alice's pane and row show none of it.
  await ctl('prompt', { agent: 'bob/lead', message: ask({ id: 'qb', method: 'confirm', title: 'Deploy to staging?', message: 'The build passed.' }) });
  await agentIs('bob/lead', (i) => i.status === 'waiting');
  await settle(1);
  assert.equal(await rows.count(), 0);
  assert.match(await infoRow('needs-you').innerText(), /Nothing waiting/);

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
  assert.equal((await observed()).notes.filter((n) => n.id === 'alice/w1').length, 1);

  // Alice's pane: her two requests, as rows of the board, with who asks.
  await rowsAre(2);
  const items = await pane.locator('.needs-you-item').evaluateAll((els) => els.map((el) => ({ type: el.dataset.type, text: el.innerText, source: el.querySelector('.needs-you-source').textContent })));
  assert.deepEqual(items.map((i) => i.type).sort(), ['login', 'select']);
  const item = (type) => items.find((i) => i.type === type);
  assert.match(item('login').text, /Log in to the npm registry[\s\S]*Run npm login in your own terminal\. Never paste the token here\.[\s\S]*Log in[\s\S]*Waiting/);
  assert.equal(item('login').source, 'Lead');
  assert.match(item('select').text, /Allow rm -rf build\?[\s\S]*Choice[\s\S]*Waiting/);
  assert.equal(item('select').source, 'a1');
  assert.match(await pane.locator('.files-head').innerText(), /Needs You[\s\S]*2 waiting/);
  assert.match(await infoRow('needs-you').innerText(), /2 waiting/);
  assert.equal(await page.locator('.composer-image').count(), 1);
  await shot('alice');

  // Bob's conversation shows Bob's only; back on Alice, hers, with her draft.
  await lead('bob').click();
  await until('Bob\'s request', async () => (await rows.count()) === 1 && /^d:bob\/lead@/.test(await rows.first().getAttribute('data-request')));
  assert.match(await pane.innerText(), /Deploy to staging\?[\s\S]*The build passed\.[\s\S]*Yes or no/);
  assert.equal(await page.locator('#crumbs').innerText(), 'bob');
  assert.match(await infoRow('needs-you').innerText(), /1 waiting/);
  await shot('bob');
  await lead('alice').click();
  await rowsAre(2);
  assert.equal(await composer.inputValue(), 'unsent alice draft');
  assert.equal(await page.locator('.composer-image').count(), 1);

  // Choosing a request shows it in full beside the list; Open in the
  // conversation lands in its lead's conversation,
  // on it. Neither answers it, and the pane stays, marking it.
  await pane.locator('.needs-you-item[data-type="login"] [data-request]').click();
  await pane.locator('.needs-you-detail-title', { hasText: 'Log in to the npm registry' }).waitFor();
  assert.match(await pane.locator('.needs-you-detail').innerText(), /npm login[\s\S]*Task t1[\s\S]*checks it before relying on it[\s\S]*Mark done[\s\S]*Not needed/);
  assert.ok((await focused())?.startsWith('pane:h:alice@'), 'beside the list: focus stays on the row chosen');
  assert.equal(await pane.locator('.explorer-nav').isVisible() && await pane.locator('.explorer-content').isVisible(), true);
  assert.equal(await called('ResolveHandoff'), 0);
  await pane.locator('[data-detail-control="open"]').click();
  await until('the handoff focused', async () => (await focused())?.startsWith('dock:h:alice@'));
  assert.equal(await page.locator('#crumbs').innerText(), 'Alice 团队');
  assert.equal(await composer.inputValue(), 'unsent alice draft');
  assert.equal(await page.locator('.composer-image').count(), 1);
  assert.equal(await page.locator('.ask.handoff.needs-you-target').count(), 1);
  assert.match(await page.locator('.ask.handoff').innerText(), /npm login[\s\S]*Task t1[\s\S]*checks it before relying on it/);
  assert.equal(await pane.locator('.needs-you-item[data-type="login"] [data-request]').getAttribute('aria-current'), 'true');
  assert.equal(await called('ResolveHandoff'), 0);
  assert.equal(await called('Answer'), 0);
  await page.locator('#dock').getByRole('button', { name: 'Show where it was asked' }).click();
  const tool = page.locator('#thread [data-tool="call-login"].needs-you-target');
  await tool.waitFor();
  assert.ok(await tool.evaluate((el) => { const r = el.getBoundingClientRect(); const s = document.querySelector('#scroll').getBoundingClientRect(); return r.bottom > s.top && r.top < s.bottom; }));
  await shot('handoff');
  await page.getByRole('button', { name: 'Back to Needs You' }).click();
  await until('focus back on its row', async () => (await focused())?.startsWith('pane:h:alice@'));

  // A handed-over worker question, by keyboard: it opens on that worker.
  await pane.locator('.needs-you-item[data-type="select"] [data-request]').focus();
  await page.keyboard.press('Enter');
  await pane.locator('.needs-you-detail-title', { hasText: 'Allow rm -rf build?' }).waitFor();
  assert.equal(await pane.locator('.needs-you-ask button').count(), 3, 'the dialog\'s own choices, answerable here');
  await pane.locator('[data-detail-control="open"]').click();
  await until('the question focused', async () => (await focused())?.startsWith('dock:d:alice/w1@'));
  assert.equal((await page.locator('#crumbs').innerText()).split(/\s*\n\s*/).join(''), 'Alice 团队/w1');
  assert.match(await page.locator('.ask').innerText(), /Alice 团队 handed this question to you\./);
  assert.equal(await rows.count(), 2);
  assert.equal(await called('Answer'), 0);
  await page.locator('.ask').getByRole('button', { name: 'Block', exact: true }).click();
  await agentIs('alice/w1', (i) => i.status !== 'waiting');
  await settle(2);
  await rowsAre(1);

  // Bob's lead's question, by keyboard, from Bob's pane.
  await lead('bob').click();
  await rowsAre(1);
  await rows.first().focus();
  await page.keyboard.press('Enter');
  await pane.locator('[data-detail-control="open"]').click();
  await until('the dialog focused', async () => (await focused())?.startsWith('dock:d:bob/lead@'));
  await page.keyboard.press('Enter'); // Yes, the first action
  await agentIs('bob/lead', (i) => i.status !== 'waiting');
  await settle(1);
  await rowsAre(0);
  await pane.locator('.needs-you-empty').waitFor();

  // Done reaches the lead as yesman's notice, and only then leaves the pane,
  // for its history.
  await lead('alice').click();
  await rowsAre(1);
  await rows.first().click();
  await page.locator('.ask.handoff').getByRole('button', { name: 'Mark done' }).click();
  const dockConfirm = page.getByRole('dialog', { name: 'Mark this request done?' });
  await dockConfirm.waitFor();
  await page.evaluate(() => { window.openingCard = document.querySelector('#dock .handoff'); });
  await refresh();
  assert.equal(await page.evaluate(() => openingCard.isConnected), false, 'State rebuilt the dock');
  await dockConfirm.getByRole('button', { name: 'Mark done' }).click();
  await settle(0);
  await prompted('alice/lead', '[yesman] The user marked n1 (login) "Log in to the npm registry", for task t1 as done. That is their own confirmation');
  assert.equal(await composer.inputValue(), 'unsent alice draft');
  await rowsAre(0);
  await pane.locator('.needs-you-history summary').click();
  assert.match(await pane.locator('.needs-you-history').innerText(), /Recently handled \(1\)[\s\S]*Log in to the npm registry[\s\S]*Marked done/);

  // A dialog that times out shows when it goes on, then is no longer offered,
  // and cannot be answered late.
  await ctl('prompt', { agent: 'alice/lead', message: ask({ id: 'qt', method: 'input', title: 'Pick a tag', timeout: 4000 }) });
  await settle(1);
  await rowsAre(1);
  assert.match(await pane.innerText(), /Pick a tag[\s\S]*Text[\s\S]*Goes on without you after 4s/);
  const expired = await scope('alice/lead');
  await settle(0);
  await rowsAre(0);
  await assert.rejects(page.evaluate((scope) => mygo.call('Yesman.Answer', 'alice/lead', scope, { requestId: 'qt', value: 'late' }), expired), /no longer open/);

  // In the pane's detail, through the real service: a text answer typed
  // there survives showing Bob and coming back, and Submit answers that very
  // dialog; Not needed and Mark done close Alice's handoffs for her current
  // hire, and her lead hears of them.
  await ctl('prompt', { agent: 'alice/lead', message: ask({ id: 'qpane', method: 'input', title: 'Name the pane branch' }) });
  await agentIs('alice/lead', (i) => i.status === 'waiting');
  await settle(1);
  await rowsAre(1);
  await rows.first().click();
  const paneAnswer = pane.locator('[data-detail-control="answer-text"]');
  const answers = await called('Answer');
  await paneAnswer.fill('pane-branch');
  await lead('bob').click();
  await pane.locator('.needs-you-empty').waitFor();
  await refresh();
  await lead('alice').click();
  await rowsAre(1);
  assert.equal(await paneAnswer.inputValue(), 'pane-branch', 'the unsent answer survives another yesman and a refresh');
  assert.equal(await called('Answer'), answers);
  await pane.locator('.needs-you-ask [data-detail-control="answer-0"]').click();
  await agentIs('alice/lead', (i) => i.status !== 'waiting');
  assert.ok(await page.evaluate(() => calls.some((c) => c[0] === 'Answer' && c[1] === 'alice/lead' && c[3]?.requestId === 'qpane' && c[3]?.value === 'pane-branch')));
  await settle(0);
  await rowsAre(0);
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ add: { type: 'decision', title: 'Pick the pane width' } }) });
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ add: { type: 'manual', title: 'Plug in the pane phone' } }) });
  await settle(2);
  await rowsAre(2);
  const resolves = await called('RespondHandoff');
  await pane.locator('.needs-you-item[data-type="decision"] [data-request]').click();
  await pane.locator('.needs-you-detail-title', { hasText: 'Pick the pane width' }).waitFor();
  await pane.locator('[data-detail-control="not-needed"]').click();
  await settle(1);
  await rowsAre(1);
  await pane.locator('.needs-you-detail-title', { hasText: 'Plug in the pane phone' }).waitFor();
  await pane.locator('[data-detail-control="respond"]').click();
  const confirm = page.getByRole('dialog', { name: 'Mark this request done?' });
  await confirm.waitFor();
  await shot('action-confirm');
  await page.keyboard.press('Escape');
  await confirm.waitFor({ state: 'hidden' });
  assert.equal(await called('RespondHandoff'), resolves + 1);
  await rowsAre(1);
  await pane.locator('[data-detail-control="respond"]').click();
  await confirm.getByRole('button', { name: 'Mark done' }).click();
  await settle(0);
  await rowsAre(0);
  assert.equal(await called('RespondHandoff'), resolves + 2);
  const hired = await page.evaluate(() => fixtureState.yesmen.find((y) => y.name === 'alice').hiredAt);
  assert.ok(await page.evaluate((hired) => calls.filter((c) => c[0] === 'RespondHandoff').slice(-2).every((c) => c[1] === 'alice' && c[2] === hired), hired));
  const told = (title, how) => until(`alice/lead told ${title}`, async () => ((await observed()).prompts['alice/lead'] ?? []).some((p) => p.startsWith('[yesman] The user marked ') && p.includes(`"${title}"`) && p.includes(how)));
  await told('Pick the pane width', 'not needed');
  await told('Plug in the pane phone', 'as done');
  assert.match(await pane.locator('.needs-you-history').innerText(), /Plug in the pane phone[\s\S]*Marked done[\s\S]*Pick the pane width[\s\S]*Marked not needed/);
  await shot('detail-resolved');

  // Durable text and choice use explicit data from the lead's tool. A
  // failed bridge attempt and an unrelated redraw leave the editor mounted.
  const add = (add, toolCall) => ctl('prompt', { agent: 'alice/lead', message: needsYou({ add, toolCall }) });
  await add({ type: 'input', title: 'Describe the result', task: 't1', response: { kind: 'text' } }, 'call-text');
  await settle(1);
  await rows.first().click();
  const reply = pane.getByRole('textbox', { name: 'Your reply' });
  const send = pane.getByRole('button', { name: 'Send reply' });
  const responseCount = await called('RespondHandoff');
  await send.click();
  assert.match(await pane.locator('.needs-you-response-error').innerText(), /Enter a reply/);
  assert.equal(await called('RespondHandoff'), responseCount);
  const dockReply = page.locator('#dock .handoff textarea');
  await dockReply.fill('editing 中文');
  await dockReply.evaluate((el) => {
    el.focus();
    el.setSelectionRange(1, 3);
    window.composingReply = el;
    el.dispatchEvent(new CompositionEvent('compositionstart', { data: '中' }));
  });
  await refresh();
  assert.deepEqual(await dockReply.evaluate(el => [el === composingReply, el === document.activeElement, el.selectionStart, el.selectionEnd]), [true, true, 1, 3]);
  await dockReply.evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', { data: '中' })));
  await reply.fill('已检查\nSecond line');
  await reply.evaluate((el) => {
    el.dataset.imeTest = 'same';
    el.setSelectionRange(1, 3);
    el.dispatchEvent(new CompositionEvent('compositionstart', { data: '' }));
  });
  await refresh();
  assert.equal(await reply.getAttribute('data-ime-test'), 'same');
  assert.deepEqual(await reply.evaluate((el) => [document.activeElement === el, el.selectionStart, el.selectionEnd]), [true, 1, 3]);
  await send.evaluate((el) => el.click());
  assert.equal(await called('RespondHandoff'), responseCount);
  await reply.evaluate((el) => el.dispatchEvent(new CompositionEvent('compositionend', { data: '已检查' })));
  await page.evaluate(() => { window.failResponse = 'before'; });
  await send.click();
  assert.match(await pane.locator('.needs-you-response-error').innerText(), /Your edits are kept/);
  assert.equal(await reply.inputValue(), '已检查\nSecond line');
  await shot('text-retry');
  // A lost reply after the durable write is also recoverable. Two same-tick
  // activations create a single write invocation. The next one retries it.
  await page.evaluate(() => { window.failResponse = 'after'; });
  await send.evaluate((el) => { el.click(); el.click(); });
  await until('lost response displayed', async () => (await pane.locator('.needs-you-response-error').innerText()).includes('Your edits'));
  assert.equal(await called('RespondHandoff'), responseCount + 2);
  await send.click();
  await settle(0);
  const textNotice = '[yesman] The user answered';
  const notice = async (title) => ((await observed()).prompts['alice/lead'] ?? []).filter((p) => p.startsWith(textNotice) && p.includes(`"${title}"`));
  await until('the lead receives the text once', async () => (await notice('Describe the result')).length === 1);
  assert.deepEqual(JSON.parse((await notice('Describe the result'))[0].split('\n').at(-1)), { text: '已检查\nSecond line' });
  assert.equal(await page.evaluate(() => fixtureState.yesmen[0].handoffs.some((h) => h.answer)), false, 'State does not broadcast response bodies');

  await add({ type: 'decision', title: 'What is visible on the demo screen?', task: 't1',
    response: { kind: 'choice', options: ['已恢复', '背光全灭', '背光亮但无画面'], allowNote: true } }, 'call-choice');
  await settle(1);
  await rows.first().click();
  const selected = pane.getByRole('radio', { name: '背光亮但无画面' });
  const note = pane.getByRole('textbox', { name: 'Optional note' });
  await pane.getByRole('radio', { name: '背光全灭' }).check();
  await pane.getByRole('radio', { name: '背光全灭' }).focus();
  await page.keyboard.press('ArrowDown');
  assert.equal(await selected.isChecked(), true);
  await note.fill('Synthetic bench note');
  await refresh();
  assert.equal(await selected.isChecked(), true);
  assert.equal(await note.inputValue(), 'Synthetic bench note');
  await shot('choice');
  await page.setViewportSize({ width: 760, height: 760 });
  assert.equal(await selected.isChecked(), true);
  assert.equal(await pane.locator('.explorer-content').evaluate((el) => el.scrollWidth <= el.clientWidth), true);
  await shot('choice-narrow');
  await page.setViewportSize({ width: 1600, height: 780 });
  await send.click();
  await settle(0);
  await until('the lead receives the choice once', async () => (await notice('What is visible on the demo screen?')).length === 1);
  assert.deepEqual(JSON.parse((await notice('What is visible on the demo screen?'))[0].split('\n').at(-1)), { choice: '背光亮但无画面', note: 'Synthetic bench note' });
  assert.equal(((await observed()).prompts['bob/lead'] ?? []).some((p) => p.startsWith(textNotice)), false);

  // While the user composes in the message box, with an IME, a request
  // that arrives redraws the pane only: the message box keeps its focus,
  // its composition and its draft. (A worker's handed-over question, in the
  // list of the agents the app pushes.)
  await composer.focus();
  await page.evaluate(() => {
    const ta = document.querySelector('#dock textarea');
    ta.dataset.mark = 'kept';
    ta.dispatchEvent(new CompositionEvent('compositionstart', { data: '' }));
    ta.dispatchEvent(new CompositionEvent('compositionupdate', { data: 'にほ' }));
  });
  await page.evaluate(() => {
    const now = new Date().toISOString();
    handlers.agents([...fixtureState.agents, { id: 'alice/wx', yesman: 'alice', name: 'wx', role: 'worker', number: 9, title: 'Check the IME', status: 'waiting', since: now,
      handedOver: true, dialogAt: now, dialog: { id: 'qx', method: 'confirm', title: 'Asked mid-composition?' } }]);
  });
  await rowsAre(1);
  assert.match(await pane.innerText(), /Asked mid-composition\?[\s\S]*a9/);
  assert.equal(await focused(), 'composer');
  assert.equal(await page.evaluate(() => document.querySelector('#dock textarea').dataset.mark), 'kept');
  assert.equal(await composer.inputValue(), 'unsent alice draft');
  await page.evaluate(() => document.querySelector('#dock textarea').dispatchEvent(new CompositionEvent('compositionend', { data: 'にほ' })));
  await refresh();
  await rowsAre(0);
  // Focus on a request in the pane stays across a full refresh; once it is
  // answered elsewhere, it moves to the pane's heading, not the composer.
  await ctl('prompt', { agent: 'alice/lead', message: ask({ id: 'qfocus', method: 'confirm', title: 'Keep focus here?' }) });
  await agentIs('alice/lead', (i) => i.status === 'waiting');
  await settle(1);
  await rowsAre(1);
  await rows.first().focus();
  await refresh();
  assert.match(await focused(), /^pane:d:alice\/lead@/);
  await page.evaluate((scope) => mygo.call('Yesman.Answer', 'alice/lead', scope, { requestId: 'qfocus', confirmed: false }), await scope('alice/lead'));
  await agentIs('alice/lead', (i) => i.status !== 'waiting');
  await settle(0);
  await rowsAre(0);
  assert.equal(await focused(), 'heading');

  // Archiving takes a yesman's requests with it; the same name hired again
  // starts with none, also when an older list of the agents arrives late,
  // and a stale window cannot close the successor's.
  await ctl('prompt', { agent: 'bob/lead', message: needsYou({ add: { type: 'manual', title: 'Plug in the test phone' }, toolCall: 'call-phone' }) });
  await ctl('prompt', { agent: 'bob/lead', message: ask({ id: 'qold', method: 'confirm', title: 'Old bob question?' }) });
  await agentIs('bob/lead', (i) => i.status === 'waiting');
  await settle(2);
  await lead('bob').click();
  await rowsAre(2);
  const stale = await page.evaluate(() => structuredClone(fixtureState.agents));
  const oldHired = await page.evaluate(() => fixtureState.yesmen.find((y) => y.name === 'bob').hiredAt);
  await rows.first().focus();
  await ctl('dismiss', { yesman: 'bob' });
  await settle(0);
  await until('Alice shown', async () => (await page.locator('#crumbs').innerText()) === 'Alice 团队');
  assert.ok(['heading', 'composer'].includes(await focused()), await focused());
  await ctl('hire', { yesman: 'bob' });
  await settle(0);
  await lead('bob').click();
  await pane.locator('.needs-you-empty').waitFor();
  await pane.locator('.needs-you-heading').focus();
  await page.evaluate((agents) => handlers.agents(agents), stale);
  await page.waitForTimeout(300);
  assert.equal(await rows.count(), 0);
  assert.match(await pane.innerText(), /Nothing from bob needs you right now\./);
  assert.match(await infoRow('needs-you').innerText(), /Nothing waiting/);
  assert.equal(await focused(), 'heading');
  await shot('rehired-stale-agents');
  await refresh();
  await assert.rejects(page.evaluate((hired) => mygo.call('Yesman.RespondHandoff', 'bob', hired, 'n1', 'done', null), oldHired), /replaced|no handoff/);
  const staleLead = stale.find((a) => a.id === 'bob/lead');
  await assert.rejects(page.evaluate((scope) => mygo.call('Yesman.Answer', 'bob/lead', scope, { requestId: 'qold', confirmed: true }), scopeFor(staleLead, oldHired)), /replaced/);
  assert.deepEqual(await page.evaluate(() => fixtureState.yesmen.find((y) => y.name === 'bob').handoffs), []);

  // Removing a worker ends its handed-over question.
  await ctl('start-worker', { yesman: 'alice', name: 'w2', message: ask({ id: 'q2', method: 'confirm', title: 'Overwrite the lockfile?' }) });
  await agentIs('alice/w2', (i) => i.status === 'waiting');
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ handOver: { worker: 'w2', requestId: 'q2' }, toolCall: 'call-w2' }) });
  await settle(1);
  await lead('alice').click();
  await rowsAre(1);
  await ctl('remove-worker', { agent: 'alice/w2' });
  await settle(0);
  await rowsAre(0);

  // A reload counts what the app knows: persisted handoffs.
  await ctl('prompt', { agent: 'alice/lead', message: needsYou({ add: { type: 'decision', title: 'Choose the release channel', detail: 'Stable or beta. I recommend beta.' }, toolCall: 'call-channel' }) });
  await settle(1);
  await reload();
  await lead('alice').click();
  await showInfo();
  await until('count after reload', async () => /1 waiting/.test(await infoRow('needs-you').innerText()));

  // What cannot be read is said, never shown as nothing waiting.
  await infoRow('needs-you').click();
  await rowsAre(1);
  await ctl('handoffs-file', { yesman: 'alice', message: 'break' });
  await refresh();
  const unreadable = pane.locator('.mds-notice[data-tone="error"]');
  await unreadable.waitFor();
  assert.match(await unreadable.innerText(), /What Alice 团队 handed to you cannot be read/);
  assert.equal(await pane.locator('.needs-you-empty').count(), 0);
  assert.match(await infoRow('needs-you').innerText(), /Cannot be read/);
  await shot('unreadable');
  await ctl('handoffs-file', { yesman: 'alice', message: 'restore' });
  await refresh();
  await rowsAre(1);
  assert.equal(await unreadable.count(), 0);

  // Long handoffs keep their rows short, the pane its width, and the
  // conversation and the composer in view.
  const long = (n) => `Step ${n}. ` + 'Check the signing identity and the notarization log before continuing. '.repeat(52);
  for (const n of [1, 2, 3]) {
    await ctl('prompt', { agent: 'alice/lead', message: needsYou({ add: { type: 'manual', title: `Long step ${n} ` + 'with a title that goes on '.repeat(4), detail: long(n) }, toolCall: `call-long-${n}` }) });
  }
  await settle(4);
  await rowsAre(4);
  const measure = () => page.evaluate(() => {
    const box = (sel) => { const el = document.querySelector(sel); const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height }; };
    const pane = document.querySelector('#needs-you');
    return {
      pane: box('#needs-you'), scroll: box('#scroll'), composer: box('.composer'), viewport: { width: innerWidth, height: innerHeight },
      rows: [...pane.querySelectorAll('.needs-you-item')].map((el) => el.getBoundingClientRect().height),
      overflow: [pane, document.scrollingElement, pane.querySelector('.explorer-nav-scroll'), pane.querySelector('.explorer-content')].some((el) => el.scrollWidth > el.clientWidth + 1),
      requests: document.querySelector('.dock-requests')?.getBoundingClientRect().height ?? 0,
    };
  });
  const roomy = (m) => {
    assert.ok(m.rows.every((h) => h < 150), JSON.stringify(m));
    assert.equal(m.overflow, false, JSON.stringify(m));
    assert.ok(m.scroll.width >= 120 && m.scroll.height >= 150, JSON.stringify(m));
    assert.ok(m.composer.bottom <= m.viewport.height && m.composer.right <= m.pane.left + 1, JSON.stringify(m));
    assert.ok(m.pane.right <= m.viewport.width + 1, JSON.stringify(m));
    assert.ok(m.requests <= 0.41 * m.viewport.height, JSON.stringify(m));
  };
  roomy(await measure());
  await shot('long');

  // Chinese, dark and narrow: the pane takes the info panel's place.
  await page.evaluate(() => localStorage.setItem('fixture.locale', 'zh-Hans'));
  await reload();
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.setViewportSize({ width: 760, height: 760 });
  await lead('alice').click();
  await composer.waitFor();
  await showInfo();
  await until('the row in Chinese', async () => /需要你[\s\S]*4 项等待中/.test(await infoRow('needs-you').innerText()));
  await infoRow('needs-you').click();
  await rowsAre(4);
  assert.equal(await page.locator('#yesman-info').isVisible(), false);
  assert.match(await pane.locator('.files-head').innerText(), /需要你[\s\S]*4 项等待中/);
  assert.match(await pane.locator('.needs-you-item[data-type="decision"]').innerText(), /Choose the release channel[\s\S]*决定/);
  assert.equal(await pane.locator('.needs-you-item[data-type="decision"] .needs-you-source').innerText(), '主管');
  roomy(await measure());
  await shot('zh-dark-narrow');
  // Closing it brings the info panel back, with focus on its row.
  await pane.getByRole('button', { name: '关闭“需要你”' }).click();
  await until('the info panel back', () => page.locator('#yesman-info').isVisible());
  assert.equal(await focused(), 'info:needs-you');

  // The request memory, in Chinese, dark and wide: Bob's is full, which his
  // pane and his Info row say; the user starts a new one by keyboard, and his
  // lead hears of it.
  await page.setViewportSize({ width: 1600, height: 800 });
  const memory = pane.locator('.needs-you-memory[data-memory="bob"]');
  const memoryState = (name = 'bob') => page.evaluate((name) => document.querySelector(`#needs-you .needs-you-memory[data-memory="${name}"]`)?.dataset.state ?? '', name);
  const memoryIs = (state, name = 'bob') => until(`${name}'s memory ${state || 'fine'}`, async () => {
    await refresh();
    return (await memoryState(name)) === state;
  });
  const dialog = page.locator('#confirm-dialog');
  const resets = () => called('ResetCallMemory');
  await lead('bob').click();
  await showInfo();
  await infoRow('needs-you').click();
  await ctl('call-memory', { yesman: 'bob', message: 'full' });
  await memoryIs('full');
  assert.equal(await memory.getAttribute('role'), 'alert');
  assert.match(await memory.innerText(), /bob 的 lead 无法再交给你新请求[\s\S]*262,144 \/ 262,144[\s\S]*开始新的记忆…/);
  await until('Bob\'s Info row', async () => /需要你[\s\S]*新请求被拒绝/.test(await infoRow('needs-you').innerText()));
  roomy({ ...(await measure()), requests: 0 });
  await shot('zh-dark-memory-full');
  await memory.getByRole('button').focus();
  await page.keyboard.press('Enter');
  await dialog.waitFor({ state: 'visible' });
  assert.equal(await page.locator('#confirm-title').innerText(), '为 bob 开始新的请求记忆？');
  assert.match(await page.locator('#confirm-body').innerText(), /清除 bob 的 lead 交给你请求时用过的 262,144 条较早工具调用的去重记录[\s\S]*可能会再出现一次[\s\S]*以后再次重置，它还可能再出现一次/);
  await page.locator('#confirm-ok').focus();
  await page.keyboard.press('Enter');
  await memoryIs('');
  await until('focus on the pane', async () => (await focused()) === 'heading');
  assert.equal(await resets(), 1);
  assert.equal((await ctl('call-memory', { yesman: 'bob' })).epoch, 1);
  await prompted('bob/lead', '[yesman] The user started a new tool-call memory for Needs you: 262144 older tool-call records were forgotten');

  // In English, light: nearly full. Cancelling forgets nothing; a
  // confirmation shown before the lead took another call forgets nothing
  // either and shows the memory again; confirming that one resets it.
  await page.evaluate(() => localStorage.removeItem('fixture.locale'));
  await reload();
  await page.emulateMedia({ colorScheme: 'light' });
  await lead('bob').click();
  await showInfo();
  await infoRow('needs-you').click();
  await ctl('call-memory', { yesman: 'bob', message: 'near' });
  await memoryIs('near');
  assert.equal(await memory.getAttribute('role'), 'status');
  assert.match(await memory.innerText(), /bob's request memory is nearly full[\s\S]*235,926 of 262,144/);
  await shot('memory-near');
  await memory.getByRole('button').focus();
  await page.keyboard.press('Enter');
  await dialog.waitFor({ state: 'visible' });
  assert.equal(await page.locator('#confirm-title').innerText(), 'Start a new request memory for bob?');
  assert.match(await page.locator('#confirm-body').innerText(), /This forgets 235,926 records[\s\S]*may show up once more[\s\S]*A later reset can let it back once more\./);
  // Escape, then Enter again at once: the close event of the first opening
  // can arrive after the second opened, and must not answer it.
  await page.keyboard.press('Escape');
  await dialog.waitFor({ state: 'hidden' });
  assert.equal(await resets(), 0); // this page's, since the reload
  await until('focus back on the button', () => page.evaluate(() => !!document.activeElement?.closest('#needs-you .needs-you-memory[data-memory="bob"]')));
  await page.keyboard.press('Enter');
  await dialog.waitFor({ state: 'visible' });
  await ctl('prompt', { agent: 'bob/lead', message: needsYou({ add: { type: 'manual', title: 'Rotate the signing key' }, toolCall: 'call-bob-key' }) });
  await until('the call taken', async () => (await ctl('call-memory', { yesman: 'bob' })).used === 235927);
  await page.locator('#confirm-ok').click();
  await until('the refusal', async () => /changed since it was shown/.test(await page.locator('.mds-toast').innerText().catch(() => '')));
  const kept = await ctl('call-memory', { yesman: 'bob' });
  assert.equal(kept.epoch, 1);
  assert.equal(kept.state, 'near');
  await until('the memory shown again', async () => /235,927 of 262,144/.test(await memory.innerText()));
  await until('focus kept on the button', () => page.evaluate(() => !!document.activeElement?.closest('#needs-you .needs-you-memory[data-memory="bob"]')));
  await page.keyboard.press('Enter');
  await dialog.waitFor({ state: 'visible' });
  await page.locator('#confirm-ok').click();
  await memoryIs('');
  const fresh = await ctl('call-memory', { yesman: 'bob' });
  assert.equal(fresh.epoch, 2);
  assert.equal(fresh.state ?? '', '');

  // Kept by a pre-release version: nothing to count, and still the way out.
  await ctl('call-memory', { yesman: 'bob', message: 'blocked' });
  await memoryIs('blocked');
  assert.match(await memory.innerText(), /bob's lead cannot hand you new requests[\s\S]*pre-release version/);
  await shot('memory-blocked');
  await memory.getByRole('button').click();
  await dialog.waitFor({ state: 'visible' });
  assert.match(await page.locator('#confirm-body').innerText(), /This forgets what a pre-release version remembered/);
  await page.locator('#confirm-ok').click();
  await memoryIs('');
  await ctl('prompt', { agent: 'bob/lead', message: needsYou({ add: { type: 'manual', title: 'Confirm the release notes' }, toolCall: 'call-bob-after' }) });
  await until('a new call taken after the reset', async () => (await ctl('call-memory', { yesman: 'bob' })).used === 2);

  // Two resets asked in one turn, Alice's cancelled in her pane and Bob's
  // confirmed in his, before the first close event: only Bob's memory starts
  // anew.
  const epoch = async (name) => (await ctl('call-memory', { yesman: name })).epoch;
  await ctl('call-memory', { yesman: 'alice', message: 'near' });
  await ctl('call-memory', { yesman: 'bob', message: 'near' });
  await lead('alice').click();
  await memoryIs('near', 'alice');
  const before = { alice: await epoch('alice'), bob: await epoch('bob') };
  await page.evaluate(() => {
    const d = document.querySelector('#confirm-dialog');
    document.querySelector('#needs-you [data-memory="alice"] button').click();
    d.close('cancel');
    document.querySelector('[data-agent="bob/lead"][data-sidebar-entry="yesman"]').click();
    document.querySelector('#needs-you [data-memory="bob"] button').click();
    d.close('ok');
  });
  await memoryIs('');
  assert.equal(await epoch('bob'), before.bob + 1);
  assert.equal(await epoch('alice'), before.alice);

  // A reset answered after the user went to Bob's pane and focused his
  // request memory leaves focus where they put it.
  await ctl('call-memory', { yesman: 'bob', message: 'near' });
  await lead('alice').click();
  await memoryIs('near', 'alice');
  await page.evaluate(() => { window.holdReset = true; });
  await pane.locator('[data-memory="alice"] button').click();
  await dialog.waitFor({ state: 'visible' });
  await page.locator('#confirm-ok').click();
  await until('the reset answer held', () => page.evaluate(() => !!window.releaseReset));
  await lead('bob').click();
  await memoryIs('near');
  await memory.getByRole('button').focus();
  await page.evaluate(() => { window.holdReset = false; window.releaseReset(); });
  await until('Alice reset', async () => (await epoch('alice')) === before.alice + 1);
  await refresh();
  await page.waitForTimeout(200);
  assert.equal(await page.evaluate(() => document.activeElement?.closest('[data-memory]')?.dataset.memory), 'bob', 'focus stayed on the control the user chose');
  assert.equal(await page.locator('#crumbs').innerText(), 'bob');
}

try {
  const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
  assert.equal(built.status, 0, built.stderr || built.stdout);
  for (const [engineName, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    const backend = await fixture();
    let browser;
    try {
      browser = await engine.launch({ headless: true });
      // Wide enough for the info panel beside the pane's two columns, as
      // this scenario reads the info row while the pane is open; at the
      // app's own 1180x780 the info makes way for the pane
      // (resource-explorer-browser.mjs, layout).
      const page = await browser.newPage({ viewport: { width: 1600, height: 780 } });
      const errors = [];
      page.on('pageerror', (err) => { errors.push(err.message); console.error(err.stack); });
      await page.addInitScript(bridge);
      await page.goto(backend.url);
      await scenario(engineName, page).catch(async (err) => {
        await page.screenshot({ path: `${shots}/needs-you-${engineName}-failure.png` }).catch(() => {});
        throw err;
      });
      assert.deepEqual(errors, []);
      assert.deepEqual(await page.evaluate(() => window.unmocked), []);
      results.push(`${engineName}: PASS`);
      console.log(`${engineName}: Needs you pane from Info and the palette beside the conversation (no gathered sidebar entry), Board and Needs you sharing the place, close by keyboard, the yesman shown only (Alice, then Bob), sources, retried and stale calls, count, open a handoff or a handed-over question without answering it, draft and image kept, back to the pane, answer, done, the pane detail's own answer kept across yesmen, bounded text and explicit choice with an optional note, retry before and after a saved write, IME draft preserved during refresh, Not needed and deliberate Mark done, history, expiry, IME and refresh focus, archive/rehire with a late older agent list, remove, reload, an unreadable file, long rows, zh/dark/narrow, request memory near/full/pre-release with a keyboard reset, a stale confirmation, the lead told, a cancelled reset never answered by another's confirmation and a late reset not taking focus passed`);
    } finally {
      await browser?.close();
      await backend.close();
    }
  }
  // What was checked, at which source, with its screenshots.
  if (process.env.YESMAN_NEEDS_YOU_SHOTS) {
    const head = spawnSync('git', ['rev-parse', 'HEAD'], { cwd, encoding: 'utf8' }).stdout.trim();
    const dirty = spawnSync('git', ['status', '--porcelain'], { cwd, encoding: 'utf8' }).stdout.trim() !== '';
    await writeFile(`${shots}/needs-you-evidence.json`, JSON.stringify({ head, dirty, results, screenshots: evidence }, null, 2) + '\n');
  }
} finally {
  if (!process.env.YESMAN_NEEDS_YOU_SHOTS) await rm(fixtureDir, { recursive: true, force: true });
}
