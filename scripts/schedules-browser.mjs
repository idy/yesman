// Tasks (scheduled tasks) in the production page, in Chromium and WebKit,
// with the actual Go service, scheduler and agent manager behind a
// controlled MyGo bridge. pi is the test binary's deterministic fake; the
// scheduler's clock moves only when this script says. No real settings,
// workspace, pi, model or system clock is touched.
//
//   YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/schedules-browser.mjs
//   YESMAN_SCHEDULES_SHOTS=/some/dir   also saves screenshots there
//   YESMAN_SCHEDULES_ONLY=focused      runs only the focused checks (drafts
//                                      while saving, archived plans)
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
    // Its finished task opens in the board, out of the folded Done section.
    await pane.locator('.sched-run').first().getByRole('button', { name: 't1' }).click();
    await board.locator('.board-done[open] [data-key$="/t1"][aria-expanded="true"]').waitFor();
    assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'alice/t1');
    await board.getByRole('button', { name: T('Scheduled', '定时') }).click();
    await pane.locator('.sched-plan').first().locator('.sched-details').waitFor();
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

// Focused checks on a fixture of their own, each from a fresh page: a draft
// typed while a save is on its way, a save answered after its dialog closed,
// and an archived plan opened from the board and from the conversation.
async function focused(engineName, engine) {
  const fx = await fixture();
  const browser = await engine.launch({ headless: true });
  console.log(`${engineName} focused`);
  const errors = [];
  const open = async (before) => {
    const context = await browser.newContext({ viewport: { width: 1280, height: 860 } });
    const page = await context.newPage();
    page.on('pageerror', (err) => errors.push(err.message));
    await page.addInitScript(() => localStorage.setItem('yesman.info', 'shown'));
    await page.addInitScript(bridge);
    if (before) await before(page);
    await page.goto(fx.url);
    return { context, page, pane: page.locator('#schedules'), dialog: page.locator('#schedule-dialog') };
  };
  const plans = async () => (await fx.post('/fixture/call', { method: 'Schedules', args: ['alice'] })).plans;
  const plan = async (id) => (await plans()).find((p) => p.id === id);
  // hold keeps the page's next call of method waiting until release().
  const hold = (page, method) => page.evaluate((m) => {
    window.hold[m] = new Promise((resolve) => { window.release = () => { delete window.hold[m]; resolve(); }; });
  }, method);
  const focusKey = (page) => page.evaluate(() => document.activeElement?.dataset.key ?? null);
  const waitFocus = (page, key) => page.waitForFunction((k) => document.activeElement?.dataset.key === k, key);
  const writes = (page) => page.evaluate(() => window.calls.filter((c) => ['SaveSchedule', 'SetScheduleEnabled', 'ArchiveSchedule', 'RunSchedule', 'RetryScheduleRun'].includes(c[0])).length);
  const tasksEntry = (page) => page.locator('#yesman-info').getByRole('button', { name: /^Tasks/ });
  try {
    {
      const { context, page, pane, dialog } = await open();
      await tasksEntry(page).click();
      await pane.getByText('No scheduled tasks yet').waitFor();

      // Typed while a new task is being created: the task is created as it
      // was sent, and the dialog stays with what was typed, its caret and
      // selection; saving again changes that task, not a second one.
      await pane.getByRole('button', { name: 'New task…' }).first().click();
      await dialog.locator('[name=title]').fill('Draft before save');
      const box = dialog.locator('[name=instructions]');
      await box.fill('Original instructions');
      await dialog.locator('[name=enabled]').uncheck();
      await hold(page, 'SaveSchedule');
      await dialog.getByRole('button', { name: 'Create', exact: true }).click();
      await dialog.locator('form[aria-busy="true"]').waitFor();
      assert.equal(await box.isEditable(), true);
      await box.fill('NEW DRAFT WHILE SAVING');
      await box.evaluate((el) => el.setSelectionRange(4, 9));
      await page.evaluate(() => window.release());
      await dialog.locator('.sched-saved').waitFor();
      assert.equal(await dialog.evaluate((d) => d.open), true);
      assert.equal(await box.inputValue(), 'NEW DRAFT WHILE SAVING');
      assert.deepEqual(await box.evaluate((el) => [document.activeElement === el, el.selectionStart, el.selectionEnd]), [true, 4, 9]);
      assert.equal(await dialog.getByRole('heading').innerText(), 'Edit task');
      let all = await plans();
      assert.equal(all.length, 1);
      assert.deepEqual([all[0].id, all[0].instructions, all[0].revision, all[0].enabled], ['s1', 'Original instructions', 1, false]);
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await dialog.waitFor({ state: 'hidden' });
      all = await plans();
      assert.equal(all.length, 1, 'saving again made another task');
      assert.deepEqual([all[0].title, all[0].instructions, all[0].revision, all[0].enabled], ['Draft before save', 'NEW DRAFT WHILE SAVING', 2, false]);
      await waitFocus(page, 'alice/s1:open');
      step('a draft typed while creating is kept');

      // Typed while an edit saves: the fields not typed in show what was
      // saved (the title as yesman keeps it), the typed one stays, and the
      // next save is at the saved revision.
      const s1 = pane.locator('.sched-plan[data-plan="alice/s1"]');
      await expand(s1);
      await s1.getByRole('button', { name: 'Edit…' }).click();
      await dialog.locator('[name=title]').fill('  Spaced   title  ');
      await hold(page, 'SaveSchedule');
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await dialog.locator('form[aria-busy="true"]').waitFor();
      await dialog.locator('[name=time]').fill('10:15');
      await page.evaluate(() => window.release());
      await dialog.locator('.sched-saved').waitFor();
      assert.equal(await dialog.locator('[name=title]').inputValue(), 'Spaced title');
      assert.equal(await dialog.locator('[name=time]').inputValue(), '10:15');
      let p = await plan('s1');
      assert.deepEqual([p.title, p.schedule.time, p.revision], ['Spaced title', '09:00', 3]);
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await dialog.waitFor({ state: 'hidden' });
      p = await plan('s1');
      assert.deepEqual([p.title, p.schedule.time, p.instructions, p.revision], ['Spaced title', '10:15', 'NEW DRAFT WHILE SAVING', 4]);
      step('a draft typed while editing is kept at the saved revision');

      // Kept open, then changed by someone else: saving again is a conflict
      // that keeps the text, and the other change is not saved over.
      await s1.getByRole('button', { name: 'Edit…' }).click();
      await hold(page, 'SaveSchedule');
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await dialog.locator('form[aria-busy="true"]').waitFor();
      await box.fill('Third draft');
      await page.evaluate(() => window.release());
      await dialog.locator('.sched-saved').waitFor();
      p = await plan('s1');
      assert.equal(p.revision, 5);
      await fx.post('/fixture/call', { method: 'SetScheduleEnabled', args: ['alice', 's1', true, p.revision] });
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await dialog.getByText(/changed since you opened it/).waitFor();
      assert.equal(await box.inputValue(), 'Third draft');
      p = await plan('s1');
      assert.deepEqual([p.instructions, p.enabled, p.revision], ['NEW DRAFT WHILE SAVING', true, 6]);
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      await dialog.waitFor({ state: 'hidden' });
      step('a kept draft is not saved over a newer change');
      await context.close();
    }

    {
      // Answered after its dialog closed (as a browser may force it) and
      // another yesman shows: the focus stays where the user is, then and
      // when they come back, and the task saved is listed.
      const { context, page, pane, dialog } = await open();
      await tasksEntry(page).click();
      await pane.locator('.sched-plan[data-plan="alice/s1"]').waitFor();
      await pane.getByRole('button', { name: 'New task…' }).first().click();
      await dialog.locator('[name=title]').fill('Late save');
      await dialog.locator('[name=instructions]').fill('Answered late');
      await dialog.locator('[name=enabled]').uncheck();
      await hold(page, 'SaveSchedule');
      await dialog.getByRole('button', { name: 'Create', exact: true }).click();
      await dialog.locator('form[aria-busy="true"]').waitFor();
      await dialog.evaluate((d) => d.close());
      await dialog.waitFor({ state: 'hidden' });
      await page.locator('#yesmen').getByText('鲍勃 Bob').click();
      await pane.getByText('No scheduled tasks yet').waitFor();
      // Where the switch to Bob leaves the focus, once it settled.
      await page.waitForTimeout(300);
      const where = () => page.evaluate(() => { const el = document.activeElement; return `${el?.tagName}#${el?.id}.${el?.className}[${el?.dataset?.key ?? ''}]`; });
      await page.evaluate(() => { window.before = document.activeElement; });
      const settled = await where();
      await page.evaluate(() => window.release());
      for (let n = 0; n < 100 && !(await plan('s2')); n++) await new Promise((r) => setTimeout(r, 20));
      assert.equal((await plan('s2'))?.title, 'Late save');
      await page.waitForTimeout(300);
      assert.equal(await page.evaluate(() => document.activeElement === window.before), true, `a late answer moved the focus: ${settled} -> ${await where()}`);
      await page.locator('#yesmen').getByText('Alice').first().click();
      await pane.locator('.sched-plan[data-plan="alice/s2"]').waitFor();
      await page.waitForTimeout(300);
      assert.notEqual(await focusKey(page), 'alice/s2:open', 'a late answer took the focus on coming back');
      assert.equal(await page.evaluate(() => !!document.activeElement?.closest('#schedules')), false);
      step('a save answered after its dialog closed moves nothing');
      await context.close();
    }

    {
      // An archived plan, from its run's board task and from the
      // conversation: Archived opens on it, by its ID, and it stays
      // archived and off.
      const made = await fx.post('/fixture/call', { method: 'SaveSchedule', args: ['alice', { title: 'Archived plan', instructions: 'Synthetic archived', schedule: { kind: 'daily', time: '09:00' }, timezone: 'UTC', enabled: false }] });
      const id = made.plan.id;
      // A click as the page makes it: its time, then random.
      const click = Date.now().toString(16).padStart(12, '0').slice(-12) + 'abababababab';
      await fx.post('/fixture/call', { method: 'RunSchedule', args: ['alice', id, click, ''] });
      let run;
      for (let n = 0; n < 100; n++) {
        run = (await plan(id)).runs[0];
        if (run?.board) break;
        await new Promise((r) => setTimeout(r, 20));
      }
      assert.ok(run?.board, 'the run has a board task');
      await fx.post('/fixture/call', { method: 'ArchiveSchedule', args: ['alice', id, made.plan.revision] });
      const archived = await plan(id);
      assert.equal(archived.state, 'archived');

      const { context, page, pane } = await open();
      const board = page.locator('#board');
      const row = pane.locator(`.sched-plan[data-plan="alice/${id}"]`);
      const shown = async () => {
        await waitFocus(page, `alice/${id}:open`);
        assert.equal(await pane.locator('.board-done').evaluate((d) => d.open), true);
        assert.equal(await row.locator('.sched-toggle').getAttribute('aria-expanded'), 'true');
        assert.ok(await row.isVisible());
      };
      await page.getByRole('button', { name: 'Show the board', exact: true }).click();
      await board.locator('.board-task', { hasText: 'Archived plan' }).getByRole('button', { name: 'Scheduled' }).click();
      await shown();
      assert.equal(await row.locator('.sched-next').innerText(), 'Archived');
      assert.equal(await row.getByRole('switch').count(), 0);
      assert.equal(await row.getByRole('button', { name: /^Run .* now$/ }).count(), 0);
      assert.equal(await row.getByRole('button', { name: 'Edit…' }).count(), 0);
      step('an archived plan opens from its board task');

      // Keyboard: the plan's row has the focus and folds with Enter.
      await page.keyboard.press('Enter');
      await row.locator('.sched-toggle[aria-expanded="false"]').waitFor();
      assert.equal(await focusKey(page), `alice/${id}:open`);
      await page.keyboard.press('Enter');
      await row.locator('.sched-details').waitFor();

      // Back and forth: its run's board task, and from there the plan again.
      await row.locator('.sched-run').getByRole('button', { name: run.board, exact: true }).click();
      await board.locator(`[data-key="alice/${run.board}"][aria-expanded="true"]`).waitFor();
      await waitFocus(page, `alice/${run.board}`);
      await board.locator('.board-task', { hasText: 'Archived plan' }).getByRole('button', { name: 'Scheduled' }).click();
      await shown();

      // Folded by the user, Archived stays folded when the pane redraws.
      await pane.locator('.board-done > summary').click();
      assert.equal(await pane.locator('.board-done').evaluate((d) => d.open), false);
      await fx.post('/fixture/board', { yesman: 'alice', id: run.board, status: 'doing', note: '' });
      await row.locator('.sched-run .sched-chip', { hasText: 'In progress' }).waitFor({ state: 'attached' });
      assert.equal(await pane.locator('.board-done').evaluate((d) => d.open), false);

      // From the conversation's Open in Tasks.
      await pane.getByRole('button', { name: 'Close the tasks' }).click();
      await pane.waitFor({ state: 'hidden' });
      await page.locator('#thread .note--scheduled', { hasText: `Scheduled task ${id}` }).first().getByRole('button', { name: 'Open in Tasks' }).click();
      await shown();
      step('an archived plan opens from the conversation, and back');

      assert.equal(await writes(page), 0, 'opening an archived plan changed it');
      const after = await plan(id);
      assert.deepEqual([after.state, after.enabled, after.revision], ['archived', false, archived.revision]);
      await context.close();
    }

    {
      // A board task whose plan is no longer kept (forgotten among the
      // oldest archived ones): Tasks says so, and shows no other plan for it.
      const { context, page, pane } = await open(async (page) => {
        await page.route('**/fixture/call', async (route) => {
          const req = JSON.parse(route.request().postData() || '{}');
          if (req.method !== 'Board') return route.continue();
          const res = await route.fetch();
          const body = await res.json();
          if (body.value) {
            const now = new Date().toISOString();
            body.value.tasks = [...(body.value.tasks ?? []), { id: 't99', title: 'Run of a forgotten plan', status: 'todo', createdAt: now, updatedAt: now, schedule: { plan: 's99' } }];
          }
          await route.fulfill({ response: res, json: body });
        });
      });
      await page.getByRole('button', { name: 'Show the board', exact: true }).click();
      await page.locator('#board .board-task', { hasText: 'Run of a forgotten plan' }).getByRole('button', { name: 'Scheduled' }).click();
      await pane.locator('.sched-notice[role="status"]', { hasText: 'Scheduled task s99 is no longer kept here.' }).waitFor();
      assert.equal(await pane.locator('[data-plan="alice/s99"]').count(), 0);
      assert.equal(await page.evaluate(() => !!document.activeElement?.closest('.sched-plan')), false);
      step('a plan no longer kept is said to be gone');
      await context.close();
    }

    {
      // A click carrying a time more than a day ahead of yesman's clock,
      // set back since, is refused before anything is recorded; the next
      // click is a new one, not that click again. Once the clock is right,
      // a click runs.
      const made = await fx.post('/fixture/call', { method: 'SaveSchedule', args: ['alice', { title: 'Clock check', instructions: 'Synthetic clock', schedule: { kind: 'daily', time: '09:00' }, timezone: 'UTC', enabled: false }] });
      const id = made.plan.id;
      const before = (await fx.get('/fixture/prompts')).length;
      await fx.post('/fixture/clock', { add: '-48h' });
      const { context, page, pane } = await open();
      try {
        await tasksEntry(page).click();
        const row = pane.locator(`[data-plan="alice/${id}"]`);
        const runButton = row.getByRole('button', { name: /^Run .* now$/ });
        const calls = () => page.evaluate((p) => window.calls.filter((c) => c[0] === 'RunSchedule' && c[2] === p).map((c) => c[3]), id);
        for (const n of [1, 2]) {
          await runButton.click();
          await page.waitForFunction(([p, k]) => window.calls.filter((c) => c[0] === 'RunSchedule' && c[2] === p).length === k, [id, n]);
          await row.locator('[aria-busy="true"]').waitFor({ state: 'detached' });
        }
        const ids = await calls();
        assert.equal(new Set(ids).size, 2, `the refused click was sent again: ${JSON.stringify(ids)}`);
        assert.equal((await plan(id)).runs.length, 0, 'a click ahead of the clock was recorded');
        assert.equal((await fx.get('/fixture/prompts')).length, before);
      } finally {
        await fx.post('/fixture/clock', { add: '48h' });
      }
      await page.reload();
      await tasksEntry(page).click();
      const row = pane.locator(`[data-plan="alice/${id}"]`);
      await row.getByRole('button', { name: /^Run .* now$/ }).click();
      for (let n = 0; n < 200 && !(await plan(id)).runs.length; n++) await new Promise((r) => setTimeout(r, 20));
      assert.equal((await plan(id)).runs.length, 1, 'a click with the right time did not run');
      step('a click ahead of the clock is refused, and the next is new');
      await context.close();
    }

    {
      // A missed run of a plan whose last run is still open asks first, as
      // Run now does; one at a time per plan, whichever control asked: a
      // cancel sends nothing, a confirmation sends the missed run once
      // under its own name, and clicks meanwhile send nothing more. With
      // no run open, it runs at once.
      const made = await fx.post('/fixture/call', { method: 'SaveSchedule', args: ['alice', { title: 'Overlap check', instructions: 'Synthetic overlap', schedule: { kind: 'daily', time: '09:00' }, timezone: 'UTC', enabled: false }] });
      const id = made.plan.id;
      const click = Date.now().toString(16).padStart(12, '0').slice(-12) + 'cdcdcdcdcdcd';
      await fx.post('/fixture/call', { method: 'RunSchedule', args: ['alice', id, click, ''] });
      // Sent, and its board task still to do: the plan's last run is open.
      for (let n = 0; n < 200 && !(await plan(id)).open; n++) await new Promise((r) => setTimeout(r, 20));
      let p = await plan(id);
      assert.ok(p.open, `the manual run is open: ${JSON.stringify(p.runs.map((r) => [r.id, r.state, r.status]))}`);
      await fx.post('/fixture/call', { method: 'SaveSchedule', args: ['alice', { ...p, schedule: { kind: 'interval', every: 15 }, enabled: true }] });
      await fx.post('/fixture/clock', { add: '70m' });
      await fx.post('/fixture/tick');
      p = await plan(id);
      const missedRun = p.runs.find((r) => r.canCatchUp);
      assert.ok(missedRun, `a missed run: ${JSON.stringify(p.runs.map((r) => [r.id, r.status]))}`);
      const mine = new RegExp(`scheduled task ${id} "`);
      const sentFor = async () => (await fx.get('/fixture/prompts')).filter((m) => mine.test(m.message)).length;
      const before = await sentFor();

      const { context, page, pane } = await open();
      await tasksEntry(page).click();
      const row = pane.locator(`[data-plan="alice/${id}"]`);
      await expand(row);
      const catchUpButton = row.locator(`[data-key="alice/${missedRun.id}:catchup"]`);
      const runButton = row.locator(`[data-key="alice/${id}:run"]`);
      const dialog = page.locator('#confirm-dialog');
      const calls = () => page.evaluate((plan) => window.calls.filter((c) => c[0] === 'RunSchedule' && c[2] === plan).length, id);
      const frames = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      // Run now asks, as before; so does the missed run's Run it now.
      for (const button of [runButton, catchUpButton]) {
        await button.click();
        await page.locator('#confirm-dialog[open]').waitFor();
        if (button === catchUpButton) {
          // Run now of the same plan meanwhile asks nothing more.
          await runButton.evaluate((el) => el.click());
          await frames();
          assert.equal(await page.locator('#confirm-dialog[open]').count(), 1);
        }
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
        await dialog.waitFor({ state: 'hidden' });
        await frames();
      }
      assert.equal(await calls(), 0, 'a cancelled run was sent');
      assert.equal(await page.locator('#confirm-dialog[open]').count(), 0);

      // Confirmed: the missed run, once, whatever is clicked meanwhile.
      await page.evaluate(() => { window.hold.RunSchedule = new Promise((r) => { window.releaseRun = r; }); });
      await catchUpButton.click();
      await page.locator('#confirm-dialog[open]').waitFor();
      await page.locator('#confirm-ok').click();
      await page.waitForFunction((plan) => window.calls.filter((c) => c[0] === 'RunSchedule' && c[2] === plan).length === 1, id);
      for (const button of [catchUpButton, catchUpButton, runButton]) await button.evaluate((el) => el.click());
      await frames();
      assert.equal(await calls(), 1);
      assert.equal(await page.locator('#confirm-dialog[open]').count(), 0);
      const sent = await page.evaluate((plan) => window.calls.find((c) => c[0] === 'RunSchedule' && c[2] === plan), id);
      assert.equal(sent[4], missedRun.id, 'the missed run is the one caught up');
      await page.evaluate(() => { delete window.hold.RunSchedule; window.releaseRun(); });
      for (let n = 0; n < 200 && !(await plan(id)).runs.some((r) => r.id === `${missedRun.id}-c` && r.board); n++) await new Promise((r) => setTimeout(r, 20));
      p = await plan(id);
      assert.equal(p.runs.filter((r) => r.kind === 'catchup').length, 1);
      for (let n = 0; n < 200 && (await sentFor()) < before + 1; n++) await new Promise((r) => setTimeout(r, 20));
      assert.equal(await sentFor(), before + 1);
      step('a missed run of a plan with an open run asks first, and is sent once');

      // No run open: the next missed run is caught up at once. (The tick
      // runs the occurrence on time itself; its task is closed too.)
      const closeAll = async () => {
        for (const r of (await plan(id)).runs.filter((r) => r.board && r.task && r.task.status !== 'done')) await fx.post('/fixture/board', { yesman: 'alice', id: r.board, status: 'done', note: '' });
      };
      await closeAll();
      await fx.post('/fixture/clock', { add: '70m' });
      await fx.post('/fixture/tick');
      for (let n = 0; n < 200 && (await plan(id)).runs.some((r) => r.state === 'claimed' || r.state === 'boarded' || r.state === 'sending'); n++) await new Promise((r) => setTimeout(r, 20));
      await closeAll();
      const sentBefore = await sentFor();
      p = await plan(id);
      assert.equal(p.open ?? null, null, 'a run is still open');
      const next = p.runs.find((r) => r.canCatchUp);
      assert.ok(next, 'a second missed run');
      await page.reload();
      await tasksEntry(page).click();
      await expand(row);
      await row.locator(`[data-key="alice/${next.id}:catchup"]`).click();
      await page.waitForFunction((plan) => window.calls.filter((c) => c[0] === 'RunSchedule' && c[2] === plan).length === 1, id);
      assert.equal(await page.locator('#confirm-dialog[open]').count(), 0, 'asked with no run open');
      for (let n = 0; n < 200 && (await sentFor()) < sentBefore + 1; n++) await new Promise((r) => setTimeout(r, 20));
      assert.equal(await sentFor(), sentBefore + 1);
      step('with no run open, a missed run is caught up at once');
      await context.close();
    }
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
    await fx.close();
  }
}

const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
assert.equal(built.status, 0, built.stderr || built.stdout);
for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) {
  if (process.env.YESMAN_SCHEDULES_ONLY !== 'focused') {
    await run(name, engine);
    await run(name, engine, { lang: 'zh-Hans', dark: true, width: 1100 });
  }
  await focused(name, engine);
}
console.log(`schedules-browser: ${results.length} checks passed`);
