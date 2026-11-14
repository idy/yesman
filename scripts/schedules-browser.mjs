// Tasks (scheduled tasks) in the production page, in Chromium and WebKit,
// with the actual Go service, scheduler and agent manager behind a
// controlled MyGo bridge. pi is the test binary's deterministic fake; the
// scheduler's clock moves only when this script says. No real settings,
// workspace, pi, model or system clock is touched.
//
//   YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/schedules-browser.mjs
//   YESMAN_SCHEDULES_SHOTS=/some/dir   also saves screenshots there
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const fixtureDir = await mkdtemp(`${tmpdir()}/yesman-schedules-browser-`);
const exe = `${fixtureDir}/schedules.test${process.platform === 'win32' ? '.exe' : ''}`;
const shots = process.env.YESMAN_SCHEDULES_SHOTS;
if (shots) await mkdir(shots, { recursive: true });
const results = [];

async function fixture() {
  const child = spawn(exe, ['-test.run=^TestSchedulesBrowserBridge$', '-test.timeout=600s'], {
    cwd, env: { ...process.env, YESMAN_TEST_SCHEDULES_BROWSER: '1' }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  let output = '';
  child.stderr.on('data', (data) => { output += data; });
  const exited = new Promise((resolve) => child.once('exit', (code) => resolve(code)));
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error('Fixture did not start: ' + output)), 30000);
    child.stdout.on('data', (data) => {
      output += data;
      const match = output.match(/SCHEDULES_BROWSER_URL=(http:\/\/[^\s]+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    child.once('exit', (code) => { clearTimeout(timer); reject(Error(`Fixture exited ${code}: ${output}`)); });
  });
  const post = async (path, body) => {
    const res = await fetch(url + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) });
    const json = await res.json();
    if (json.error) throw Error(json.error);
    return json.value;
  };
  const get = async (path) => (await (await fetch(url + path)).json()).value;
  return {
    url, post, get, close: async () => {
      child.stdin.end();
      assert.equal(await exited, 0, output);
    },
  };
}

// The bridge: Tasks, the board, the info panel and conversations go to the
// actual Go service; the rest of the app answers with empty values.
function bridge() {
  const handlers = {};
  window.calls = [];
  window.hold = {}; // method -> promise to wait on before answering
  const source = new EventSource('/fixture/events');
  source.onmessage = (e) => {
    const { name, value } = JSON.parse(e.data);
    for (const fn of handlers[name] ?? []) fn(value);
  };
  const local = {
    Repos: [], Clones: [], Update: { enabled: false }, TakeFocus: '', Pulls: [], Fetches: { fetching: [], errors: {} },
    Usage: { bytes: 0, files: 0, parts: [] }, AgentMenu: '', WatchFiles: null,
  };
  window.mygo = {
    locale: localStorage.getItem('fixture.locale') || 'en',
    on(name, fn) { (handlers[name] ??= []).push(fn); },
    async call(full, ...args) {
      const method = full.replace('Yesman.', '');
      window.calls.push([method, ...args]);
      if (window.hold[method]) await window.hold[method];
      if (method in local) return local[method];
      if (['Models', 'Context'].includes(method)) throw Error('not in this fixture');
      const res = await fetch('/fixture/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method, args }) });
      const body = await res.json();
      if (body.error) throw Error(body.error);
      return body.value;
    },
  };
}

const step = (name) => { results.push(name); console.log(`  ok ${name}`); };

// expand opens a task's details unless they are open.
async function expand(plan) {
  const toggle = plan.locator('.sched-toggle');
  if ((await toggle.getAttribute('aria-expanded')) !== 'true') await toggle.click();
  await plan.locator('.sched-details').waitFor();
}

async function run(engineName, engine, { lang = 'en', dark = false, width = 1280 } = {}) {
  const fx = await fixture();
  const browser = await engine.launch({ headless: true });
  const label = `${engineName} ${lang}${dark ? ' dark' : ''} ${width}`;
  console.log(label);
  try {
    const context = await browser.newContext({ viewport: { width, height: 860 }, colorScheme: dark ? 'dark' : 'light' });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (err) => errors.push(err.message));
    await page.addInitScript(([locale]) => {
      localStorage.setItem('yesman.info', 'shown');
      localStorage.setItem('fixture.locale', locale);
    }, [lang]);
    await page.addInitScript(bridge);
    await page.goto(fx.url);
    const zh = lang !== 'en';
    const T = (en, cn) => (zh ? cn : en);
    const shot = async (name) => { if (shots) await page.screenshot({ path: `${shots}/${engineName}-${lang}${dark ? '-dark' : ''}-${width}-${name}.png` }); };

    // The info panel lists Tasks beside Knowledge and the task log.
    const info = page.locator('#yesman-info');
    await info.getByRole('button', { name: new RegExp(T('^Tasks', '^定时任务')) }).waitFor();
    for (const name of [T('Knowledge', '知识'), T('Task log', '任务日志')]) await info.getByRole('button', { name: new RegExp(`^${name}`) }).waitFor();
    assert.match(await info.getByRole('button', { name: new RegExp(T('^Tasks', '^定时任务')) }).innerText(), new RegExp(T('None', '无')));
    // Keyboard: the Tasks entry opens with Enter.
    await info.getByRole('button', { name: new RegExp(T('^Tasks', '^定时任务')) }).focus();
    await page.keyboard.press('Enter');
    const pane = page.locator('#schedules');
    await pane.getByText(T('No scheduled tasks yet', '还没有定时任务')).waitFor();
    assert.equal(await page.locator('#board').isHidden(), true, 'Tasks shares the place of the board');
    step('info entry and empty state');
    await shot('empty');

    // Create: validation from the page and from Go.
    await pane.getByRole('button', { name: T('New task…', '新建定时任务…') }).first().click();
    const dialog = page.locator('#schedule-dialog');
    await dialog.getByRole('heading').waitFor();
    await dialog.getByRole('button', { name: T('Create', '创建') }).click();
    await dialog.getByRole('alert').getByText(T('Give the task a title.', '请给定时任务起个标题。')).waitFor();
    assert.equal(await dialog.locator('input[name="title"]').getAttribute('aria-invalid'), 'true');
    assert.equal(await page.evaluate(() => document.activeElement?.name), 'title');
    const longText = `${T('Check the overnight CI runs and summarise failures.', '检查夜间 CI 并总结失败原因。')}\n${'· detail '.repeat(300)}`;
    await dialog.locator('input[name="title"]').fill(T('Morning CI check', '早上检查 CI'));
    await dialog.locator('textarea[name="instructions"]').fill(longText);
    await dialog.locator('select[name="kind"]').selectOption('weekly');
    await dialog.locator('input[name="timezone"]').fill('Mars/Olympus');
    await dialog.getByRole('button', { name: T('Create', '创建') }).click();
    await dialog.getByRole('alert').getByText(/Mars\/Olympus/).waitFor();
    assert.equal(await dialog.locator('input[name="timezone"]').getAttribute('aria-invalid'), 'true');
    await dialog.locator('input[name="timezone"]').fill('Asia/Shanghai');
    await dialog.locator('input[name="time"]').fill('09:30');
    await shot('editor');
    await dialog.getByRole('button', { name: T('Create', '创建') }).click();
    await dialog.waitFor({ state: 'hidden' });
    const plan = pane.locator('.sched-plan').first();
    await plan.getByText(T('Morning CI check', '早上检查 CI')).waitFor();
    await plan.getByText(/Asia\/Shanghai/).first().waitFor();
    await plan.locator('.sched-next').getByText(T(/^Next:/, /^下次：/)).waitFor();
    await info.getByRole('button', { name: new RegExp(T('^Tasks', '^定时任务')) }).getByText(T(/1 on · next/, /1 个开启 · 下次/)).waitFor();
    step('create with validation');

    // A once plan in the past is refused, not run.
    await pane.getByRole('button', { name: T('New task…', '新建定时任务…') }).first().click();
    await dialog.locator('input[name="title"]').fill('Past');
    await dialog.locator('textarea[name="instructions"]').fill('Too late');
    await dialog.locator('select[name="kind"]').selectOption('once');
    await dialog.locator('input[name="date"]').fill('2020-01-01');
    await dialog.getByRole('button', { name: T('Create', '创建') }).click();
    await dialog.getByRole('alert').getByText(T(/That time has passed/, /这个时间已经过了/)).waitFor();
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'hidden' });
    assert.equal(await pane.locator('.sched-plan').count(), 1);
    step('once in the past refused');

    // Off with the keyboard, on again with the mouse; focus stays on it.
    await plan.getByRole('switch').focus();
    await page.keyboard.press('Space');
    await plan.locator('.sched-next').getByText(T('Turned off', '已关闭')).waitFor();
    assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'alice/s1:switch');
    assert.equal(await plan.getByRole('switch').isChecked(), false);
    await plan.locator('.mds-switch-track').click();
    await plan.locator('.sched-next').getByText(T(/^Next:/, /^下次：/)).waitFor();
    assert.equal(await plan.getByRole('switch').isChecked(), true);
    step('turn off and on');

    // Run now, held down: one request, one run.
    await page.evaluate(() => { window.hold.RunSchedule = new Promise((r) => { window.releaseRun = r; }); });
    const runButton = plan.getByRole('button', { name: new RegExp(T('^Run .* now$', '^立即运行')) });
    await runButton.focus();
    for (let i = 0; i < 5; i++) await page.keyboard.press('Enter');
    await plan.locator('[aria-busy="true"]').waitFor();
    await runButton.click({ force: true, trial: false }).catch(() => {});
    await page.evaluate(() => window.releaseRun());
    await page.evaluate(() => { delete window.hold.RunSchedule; });
    await expand(plan);
    await plan.locator('.sched-run').first().getByText(/t1/).waitFor();
    const runCalls = await page.evaluate(() => window.calls.filter((c) => c[0] === 'RunSchedule'));
    assert.equal(new Set(runCalls.map((c) => c[3])).size, 1, `one click, one request ID: ${JSON.stringify(runCalls)}`);
    let prompts = await fx.get('/fixture/prompts');
    assert.equal(prompts.length, 1, JSON.stringify(prompts));
    assert.equal(prompts[0].behavior, 'followUp');
    await plan.locator('.sched-run').first().locator('.sched-chip').getByText(T('Sent to the lead', '已发给主管')).waitFor();
    step('run now once per click');
    await shot('history');

    // The conversation shows the run as a scheduled task's, from the origin
    // recorded beside it, never as a person's message.
    const thread = page.locator('#thread');
    const scheduledNote = thread.locator('.note--scheduled').filter({ hasText: T('Scheduled task s1', '定时任务 s1') }).first();
    await scheduledNote.waitFor();
    assert.equal(await thread.locator('.msg-user').filter({ hasText: 'Scheduled task s1' }).count(), 0, 'a scheduled run shown as a person\'s message');
    await scheduledNote.getByText(T('The request it sent', '发出的请求')).click();
    await scheduledNote.locator('.md').getByText(T(/Check the overnight CI runs/, /检查夜间 CI/)).waitFor();
    await shot('conversation');
    await pane.getByRole('button', { name: T('Close the tasks', '关闭定时任务') }).click();
    await pane.waitFor({ state: 'hidden' });
    await scheduledNote.getByRole('button', { name: T('Open in Tasks', '在定时任务中打开') }).click();
    await pane.locator('.sched-plan').first().locator('.sched-details').waitFor();
    step('conversation shows the scheduled run');

    // The run's board task, and back.
    await plan.locator('.sched-run').first().getByRole('button', { name: 't1' }).click();
    const board = page.locator('#board');
    await board.locator('[data-key$="/t1"][aria-expanded="true"]').waitFor();
    await board.getByRole('button', { name: T('Scheduled', '定时') }).click();
    await pane.locator('.sched-plan').first().waitFor();
    step('board task link and back');

    // The board's status is the outcome.
    await fx.post('/fixture/board', { yesman: 'alice', id: 't1', status: 'done', note: 'All green' });
    await pane.locator('.sched-run').first().locator('.sched-chip').getByText(T('Done', '已完成')).waitFor();
    step('board done is the outcome');

    // Asleep for three days: one missed entry, caught up once.
    await fx.post('/fixture/clock', { add: '76h' });
    await fx.post('/fixture/tick');
    const missed = pane.locator('.sched-run[data-status="missed"]').first();
    await missed.waitFor();
    assert.match(await missed.innerText(), /×\s?\d/);
    await info.locator('.info-attention').waitFor();
    await missed.getByRole('button', { name: T('Run it now', '现在运行') }).click();
    await pane.locator('.sched-run').filter({ hasText: T('Catch-up', '补跑') }).first().waitFor();
    prompts = await fx.get('/fixture/prompts');
    assert.equal(prompts.length, 2, JSON.stringify(prompts));
    assert.equal(await missed.getByRole('button', { name: T('Run it now', '现在运行') }).count(), 0);
    step('missed coalesced and caught up once');
    await shot('missed');

    // Refused by pi: not sent, sent again once it accepts.
    await fx.post('/fixture/board', { yesman: 'alice', id: 't2', status: 'done', note: '' });
    await fx.post('/fixture/clock', { add: '1m' }); // runs claimed at one instant would tie in the history
    await fx.post('/fixture/mode', { mode: 'reject' });
    await pane.locator('.sched-plan').first().getByRole('button', { name: new RegExp(T('^Run .* now$', '^立即运行')) }).click();
    const failed = pane.locator('.sched-run[data-status="failed"]').first();
    await failed.waitFor();
    await fx.post('/fixture/mode', { mode: '' });
    await failed.getByRole('button', { name: T('Send again', '重新发送') }).click();
    await pane.locator('.sched-run').first().locator('.sched-chip').getByText(T('Sent to the lead', '已发给主管')).waitFor();
    step('refused, then sent again');

    // A busy lead: queued as a follow-up, after a confirmation that the last
    // run is still open.
    await fx.post('/fixture/clock', { add: '1m' });
    await fx.post('/fixture/mode', { mode: 'busy' });
    await pane.locator('.sched-plan').first().getByRole('button', { name: new RegExp(T('^Run .* now$', '^立即运行')) }).click();
    await page.locator('#confirm-dialog').getByRole('button', { name: T('Run now', '立即运行') }).click();
    await pane.locator('.sched-run').first().locator('.sched-chip').getByText(T('Queued for the lead', '已排队等主管处理')).waitFor();
    prompts = await fx.get('/fixture/prompts');
    assert.equal(prompts.at(-1).behavior, 'followUp');
    // Still in pi's queue, its receipt shows as the scheduled run it is.
    await thread.locator('.note--scheduled[data-delivery] .delivery-status[data-state="queued"]').first().waitFor();
    assert.equal(await thread.locator('.msg-user').filter({ hasText: /Scheduled task/ }).count(), 0);
    step('busy lead queues a follow-up');

    // Save conflict keeps the input.
    await pane.locator('.sched-plan').first().getByRole('button', { name: T('Edit…', '编辑…') }).click();
    await dialog.locator('input[name="title"]').fill(T('Edited title', '改过的标题'));
    const view = await fx.post('/fixture/call', { method: 'Schedules', args: ['alice'] });
    const p = view.plans[0];
    await fx.post('/fixture/call', { method: 'SetScheduleEnabled', args: ['alice', p.id, !p.enabled, p.revision] });
    await dialog.getByRole('button', { name: T('Save', '保存') }).click();
    await dialog.getByText(T(/changed since you opened it/, /已被修改/)).waitFor();
    assert.equal(await dialog.locator('input[name="title"]').inputValue(), T('Edited title', '改过的标题'));
    await dialog.getByRole('button', { name: T('Use the latest version', '改用最新版本') }).click();
    await dialog.getByRole('button', { name: T('Save', '保存') }).waitFor();
    assert.equal(await dialog.locator('input[name="title"]').inputValue(), T('Morning CI check', '早上检查 CI'));
    await dialog.getByRole('button', { name: T('Cancel', '取消') }).click();
    step('save conflict');

    // Another yesman: a late answer for the first one does not show here.
    await page.evaluate(() => { window.hold.Schedules = new Promise((r) => { window.releaseSchedules = r; }); });
    await fx.post('/fixture/call', { method: 'SaveSchedule', args: ['alice', { title: 'Late', instructions: 'x', schedule: { kind: 'daily', time: '10:00' }, timezone: 'UTC', enabled: false }] });
    await page.locator('#yesmen').getByText('鲍勃 Bob').click();
    await page.evaluate(() => { delete window.hold.Schedules; window.releaseSchedules(); });
    await pane.getByText(T('No scheduled tasks yet', '还没有定时任务')).waitFor();
    await page.waitForTimeout(300);
    assert.equal(await pane.getByText('Late').count(), 0);
    assert.equal(await pane.getByText(T('Morning CI check', '早上检查 CI')).count(), 0);
    await page.locator('#yesmen').getByText('Alice').first().click();
    await pane.getByText('Late').waitFor();
    step('another yesman and late answers');

    // Archive.
    await expand(pane.locator('.sched-plan', { hasText: 'Late' }));
    await pane.locator('.sched-plan', { hasText: 'Late' }).getByRole('button', { name: T('Archive…', '归档…') }).click();
    await page.locator('#confirm-dialog').getByRole('button', { name: T('Archive', '归档') }).click();
    await pane.locator('.board-done summary').getByText(T(/Archived 1/, /已归档 1/)).waitFor();
    step('archive');

    // Narrow window, the info panel hidden as there is no room for it (the
    // board has the same limit): the pane fits and nothing overflows it.
    await page.setViewportSize({ width: 640, height: 860 });
    await page.getByRole('button', { name: T('Hide the info', '隐藏信息栏') }).click();
    await page.waitForTimeout(100);
    const box = await pane.evaluate((el) => { const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, vw: innerWidth }; });
    assert.ok(box.right <= box.vw + 1 && box.left >= 0, `the pane leaves the window at 640px: ${JSON.stringify(box)}`);
    const overflow = await pane.evaluate((el) => [...el.querySelectorAll('*')].filter((x) => x.scrollWidth > x.clientWidth + 1 && getComputedStyle(x).overflowX !== 'auto' && getComputedStyle(x).overflowX !== 'hidden' && x.clientWidth > 0).map((x) => x.className).slice(0, 5));
    assert.deepEqual(overflow, [], `overflow at 640px: ${overflow}`);
    await shot('narrow');
    step('narrow window');

    await page.getByRole('button', { name: T('Show the info', '显示信息栏') }).click();
    await page.setViewportSize({ width, height: 860 });

    // A tasks file that cannot be read is shown, not written over.
    await fx.post('/fixture/corrupt', { yesman: 'alice' });
    await fx.post('/fixture/tick');
    await page.locator('#yesmen').getByText('鲍勃 Bob').click();
    await page.locator('#yesmen').getByText('Alice').first().click();
    await pane.locator('.sched-notice[data-tone="error"]').waitFor();
    assert.equal(await pane.getByRole('button', { name: T('New task…', '新建定时任务…') }).count(), 0);
    step('unreadable file');
    await shot('error');

    assert.deepEqual(errors, []);
    await context.close();
  } catch (err) {
    // Leave what the page showed, for whoever reads the failure.
    const pages = browser.contexts().flatMap((c) => c.pages());
    if (pages[0]) {
      const dir = shots || fixtureDir;
      await pages[0].screenshot({ path: `${dir}/failure-${engineName}-${lang}.png` }).catch(() => {});
      const text = await pages[0].evaluate(() => ({ pane: document.querySelector('#schedules')?.innerText, calls: window.calls?.slice(-15) })).catch(() => null);
      console.error('page at failure:', JSON.stringify(text, null, 1)?.slice(0, 4000));
    }
    throw err;
  } finally {
    await browser.close();
    await fx.close();
  }
}

const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr || built.stdout);
for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) {
  await run(name, engine);
  await run(name, engine, { lang: 'zh-Hans', dark: true, width: 1100 });
}
console.log(`schedules-browser: ${results.length} checks passed`);
