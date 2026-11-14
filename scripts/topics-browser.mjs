// Topics in the production page, in Chromium and WebKit, with the actual Go
// service, Manager and server behind a controlled MyGo bridge
// (TestTopicsBrowserBridge). Agents run the installed pi with yesman's
// extension and a local deterministic provider: no real model, settings,
// workspace or network. Screenshots go to YESMAN_TOPICS_SHOTS when set.
// YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/topics-browser.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const fixtureDir = await mkdtemp(`${tmpdir()}/yesman-topics-browser-`);
const exe = `${fixtureDir}/topics.test`;
const shots = process.env.YESMAN_TOPICS_SHOTS;
if (shots) await mkdir(shots, { recursive: true });
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==', 'base64');

async function fixture() {
  const child = spawn(exe, ['-test.run=^TestTopicsBrowserBridge$', '-test.timeout=600s'], {
    cwd, env: { ...process.env, YESMAN_TEST_TOPICS_BROWSER: '1' }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  let output = '';
  child.stderr.on('data', (d) => { output += d; });
  const exited = new Promise((resolve) => child.once('exit', (code) => resolve(code)));
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error('Fixture did not start: ' + output)), 30000);
    child.stdout.on('data', (d) => {
      output += d;
      const m = output.match(/TOPICS_BROWSER_URL=(http:\/\/[^\s]+)/);
      if (m) { clearTimeout(timer); resolve(m[1]); }
    });
    child.once('exit', (code) => { clearTimeout(timer); reject(Error(`Fixture exited ${code}: ${output}`)); });
  });
  return { url, close: async () => { child.stdin.end(); assert.equal(await exited, 0, output); } };
}

const bridge = () => {
  localStorage.setItem('yesman.info', 'shown');
  const handlers = {};
  const forwarded = ['State', 'Messages', 'Submit', 'Abort', 'Board', 'Info', 'Topics', 'CreateTopic', 'UpdateTopic', 'SubmitTopic'];
  window.fixtureCalls = [];
  window.mygo = {
    locale: localStorage.getItem('fixture.locale') || 'en',
    on(name, fn) { (handlers[name] ??= []).push(fn); },
    async call(method, ...args) {
      const m = method.replace('Yesman.', '');
      fixtureCalls.push([m, ...args.map((a) => (typeof a === 'string' ? a : typeof a))]);
      if (forwarded.includes(m)) {
        const r = await fetch('/fixture/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method: m, args }) });
        const body = await r.json();
        if (body.error) throw Error(body.error);
        if (m === 'State') window.fixtureAgents = body.value.agents ?? [];
        return body.value;
      }
      switch (m) {
        case 'Repos': case 'Clones': case 'Pulls': case 'ListFiles': return [];
        case 'Settings': return { appearance: 'system', autoFetch: 0 };
        case 'Fetches': return { fetching: [], errors: {} };
        case 'TakeFocus': return '';
        case 'Update': return { enabled: false };
        case 'Models': return { models: [], agent: (window.fixtureAgents ?? []).find((a) => a.id === args[0]) };
        default: return null;
      }
    },
  };
  let next = 0;
  let busy = false;
  setInterval(async () => {
    if (busy) return;
    busy = true;
    try {
      const r = await (await fetch(`/fixture/events?after=${next}`)).json();
      next = r.next;
      for (const e of r.events) {
        if (e.name === 'agents') window.fixtureAgents = e.data;
        for (const fn of handlers[e.name] ?? []) fn(e.data);
      }
    } catch {}
    busy = false;
  }, 40);
};

async function post(url, path, body) {
  const r = await fetch(new URL(path, url), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  return r.status === 204 ? null : r.json();
}

async function shot(page, name) {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage: false });
}

async function run(engineName, engine) {
  const backend = await fixture();
  const browser = await engine.launch({ headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1360, height: 860 } });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (err) => errors.push(err.message));
    await page.addInitScript(bridge);
    await page.goto(backend.url);
    const lead = page.locator('.agent-row--yesman').first();
    await lead.waitFor();
    await lead.click();
    const topicsRow = page.locator('#yesman-info .info-link', { hasText: 'Topics' });
    await topicsRow.waitFor();

    // The info panel lists Knowledge, Task log, Board and Topics side by side.
    await page.waitForFunction(() => document.querySelector('#yesman-info')?.innerText.includes('2 open'));
    const rows = await page.$$eval('#yesman-info .info-section--link .info-link', (els) => els.map((e) => e.innerText.replace(/\s+/g, ' ').trim()));
    assert.deepEqual(rows.slice(0, 4).map((r) => r.split(' ')[0]), ['Knowledge', 'Task', 'Board', 'Topics'], rows.join(' | '));
    assert.match(rows[4] ?? '', /^Storage/, 'Storage keeps its row after them');
    assert.match(rows[0], /1 note/);
    assert.match(rows[2], /2 open/);
    assert.match(rows[3], /Empty/);
    await shot(page, `${engineName}-info`);

    // Board still opens its own pane; Topics replaces it beside the conversation.
    await page.locator('#yesman-info .info-link', { hasText: 'Board' }).click();
    await page.locator('#board .board-task').first().waitFor();
    await topicsRow.click();
    await page.locator('#topics .board-empty', { hasText: 'No topics yet' }).waitFor();
    assert.equal(await page.locator('#board').isHidden(), true);
    assert.equal(await topicsRow.getAttribute('aria-current'), 'page');

    // Create two topics; Markdown renders, HTML stays text.
    const create = async (title, summary, conclusion, question) => {
      await page.click('#topics [data-key="new"]');
      await page.fill('#topics [data-key="edit-title"]', title);
      await page.fill('#topics [data-key="edit-summary"]', summary);
      if (conclusion) {
        await page.click('#topics [data-key="edit-conclusions-add"]');
        await page.fill('#topics [data-key="edit-conclusions-0"]', conclusion);
      }
      if (question) {
        await page.click('#topics [data-key="edit-questions-add"]');
        await page.fill('#topics [data-key="edit-questions-0"]', question);
      }
      await page.click('#topics [data-key="edit-save"]');
      await page.locator('#topics h3.topic-title', { hasText: title }).waitFor();
    };
    await create('How do topics relate to tasks?', 'Scope: **boundaries** <img src=x onerror="window.pwned=1"> <b>raw</b>', 'Tasks stay on the board', 'Should topics have tags?');
    assert.equal(await page.locator('#topics .topic-section .md strong').count(), 1);
    assert.equal(await page.locator('#topics .topic-section .md img, #topics .topic-section .md b').count(), 0);
    assert.equal(await page.evaluate(() => window.pwned), undefined);
    assert.equal(await page.locator('#topics [data-kind="conclusion"] li').count(), 1);
    assert.equal(await page.locator('#topics [data-kind="question"] li').count(), 1);
    await shot(page, `${engineName}-detail`);
    await page.click('#topics [data-key="back"]');
    await create('Image limits', 'What images can a topic message carry?');
    await page.click('#topics [data-key="back"]');
    // Back returns focus to the row; a redraw from the topic just created may still be settling.
    await page.waitForFunction(() => document.activeElement?.dataset.key === 'topic-tp2', null, { timeout: 5000 });

    // The info panel follows: Topics now counts the two open topics.
    try {
      await page.waitForFunction(() => /Topics\s*›?\s*2 open/.test(document.querySelector('#yesman-info')?.innerText.replace(/\s+/g, ' ') ?? '') || [...document.querySelectorAll('#yesman-info .info-link')].some((e) => e.innerText.includes('Topics') && e.innerText.includes('2 open')), null, { timeout: 8000 });
    } catch (err) {
      console.log('Info calls:', JSON.stringify(await page.evaluate(() => fixtureCalls.filter((c) => c[0] === 'Info').length)), await page.locator('#yesman-info').innerText());
      throw err;
    }

    // Search and status filters.
    await page.fill('#topics [data-key="search"]', 'image');
    assert.equal(await page.locator('#topics .topic-row').count(), 1);
    await page.fill('#topics [data-key="search"]', 'tags');
    assert.equal(await page.locator('#topics .topic-row').count(), 1, 'search covers open questions');
    await page.fill('#topics [data-key="search"]', '');
    assert.equal(await page.locator('#topics .topic-row').count(), 2);

    // A lead change meanwhile: other fields merge, the same field is the user's call.
    await page.click('#topics [data-key="topic-tp1"]');
    await page.click('#topics [data-key="edit"]');
    await page.fill('#topics [data-key="edit-title"]', 'Topics and tasks');
    await post(backend.url, '/fixture/lead', { id: 'tp1', patch: { summary: 'Lead summary' } });
    await page.waitForTimeout(300);
    assert.equal(await page.inputValue('#topics [data-key="edit-title"]'), 'Topics and tasks', 'the form survives a lead change');
    await page.click('#topics [data-key="edit-save"]');
    await page.locator('#topics .topic-notice', { hasText: 'together with a change made meanwhile' }).waitFor();
    assert.equal(await page.locator('#topics h3.topic-title').innerText(), 'Topics and tasks');
    assert.match(await page.locator('#topics .topic-section').first().innerText(), /Lead summary/);
    await page.click('#topics [data-key="edit"]');
    await page.fill('#topics [data-key="edit-summary"]', 'Mine');
    await post(backend.url, '/fixture/lead', { id: 'tp1', patch: { summary: 'Theirs' } });
    await page.click('#topics [data-key="edit-save"]');
    await page.locator('#topics .topic-notice[data-tone="warn"]', { hasText: 'changed while you edited it' }).waitFor();
    assert.equal(await page.inputValue('#topics [data-key="edit-summary"]'), 'Mine', 'nothing is overwritten silently');
    await shot(page, `${engineName}-conflict`);
    await page.click('#topics [data-key="conflict-keep"]');
    await page.locator('#topics h3.topic-title', { hasText: 'Topics and tasks' }).waitFor();
    assert.match(await page.locator('#topics .topic-section').first().innerText(), /Mine/);
    // Saving my version of a conflicting field keeps a field only they changed.
    await page.click('#topics [data-key="edit"]');
    await page.fill('#topics [data-key="edit-summary"]', 'Mine again');
    await post(backend.url, '/fixture/lead', { id: 'tp1', patch: { summary: 'Theirs again', addQuestions: ['Lead-only question'] } });
    await page.click('#topics [data-key="edit-save"]');
    await page.locator('#topics [data-key="conflict-keep"]').click();
    await page.locator('#topics [data-kind="question"] li', { hasText: 'Lead-only question' }).waitFor();
    assert.match(await page.locator('#topics .topic-section').first().innerText(), /Mine again/);
    assert.equal(await page.locator('#topics [data-kind="question"] li').count(), 2, 'the lead\'s question survives');

    // Send from topics while the lead's run is held: the main draft stays,
    // A then B (with an image) reach the lead's one conversation.
    const main = page.locator('#dock textarea');
    await main.fill('my unsent main draft');
    const composeA = page.locator('#topics [data-key="compose-tp1"]');
    await composeA.fill('What belongs on the board?');
    assert.match(await page.locator('#topics .topic-context').first().innerText(), /\[Topic tp1: Topics and tasks\]/);
    await composeA.press('Enter');
    await page.waitForFunction(() => [...document.querySelectorAll('#thread .topic-chip')].some((c) => c.innerText.includes('tp1')));
    await page.waitForFunction(() => !document.querySelector('#topics [data-key="compose-tp1"]')?.value);
    // The open topic lists the message once pi records it, without a redraw by hand.
    await page.locator('#topics .topic-message', { hasText: 'What belongs on the board?' }).waitFor();
    await page.click('#topics [data-key="back"]');
    await page.click('#topics [data-key="topic-tp2"]');
    await page.locator('#topics .topic-composer input[type="file"]').setInputFiles({ name: 'dot.png', mimeType: 'image/png', buffer: PNG });
    await page.locator('#topics .composer-image img').waitFor();
    await page.fill('#topics [data-key="compose-tp2"]', 'Can it take images?');
    await page.click('#topics [data-key="send-tp2"]');
    await page.waitForFunction(() => [...document.querySelectorAll('#thread .topic-chip')].some((c) => c.innerText.includes('tp2')));
    await page.waitForFunction(() => !document.querySelector('#topics .composer-image'));
    assert.equal(await main.inputValue(), 'my unsent main draft', 'the main composer keeps its draft');
    await shot(page, `${engineName}-sent`);
    await post(backend.url, '/fixture/gate?open=1');
    await page.waitForFunction(() => document.querySelectorAll('#thread .msg-assistant').length >= 1, null, { timeout: 30000 });

    // After a reload the native history still names each topic; a chip opens it.
    await page.reload();
    await page.locator('.agent-row--yesman').first().click();
    await page.waitForFunction(() => document.querySelectorAll('#thread .msg-user--topic').length === 2, null, { timeout: 30000 });
    const chips = await page.$$eval('#thread .msg-user--topic .topic-chip', (els) => els.map((e) => e.innerText.replace(/\s+/g, ' ')));
    assert.match(chips[0], /tp1/);
    assert.match(chips[1], /tp2/);
    assert.equal(await page.locator('#thread .msg-user--topic img').count(), 1, 'the image stays with topic B');
    await page.locator('#thread .msg-user--topic .topic-chip', { hasText: 'tp2' }).click();
    await page.locator('#topics h3.topic-title', { hasText: 'Image limits' }).waitFor();
    // Its conversation lists the native message, and shows it exactly.
    await page.locator('#topics .topic-message button').first().click();
    await page.locator('#thread .msg-found:is(.msg-user--topic, :has(.msg-user--topic))').waitFor();

    // Sources: a picked message is shown exactly; one not in history says so.
    await page.click('#topics [data-key="edit"]');
    await page.selectOption('#topics [data-key="pick-message"]', '0');
    await page.click('#topics [data-key="edit-save"]');
    await page.locator('#topics .topic-source').first().waitFor();
    await post(backend.url, '/fixture/lead', { id: 'tp2', patch: { addSources: [{ kind: 'message', agent: 'lead', role: 'user', timestamp: 1, label: 'gone' }] } });
    await page.waitForFunction(() => document.querySelectorAll('#topics .topic-source').length === 2);
    await page.locator('#topics .topic-source button').first().click();
    await page.locator('#thread .msg-found').waitFor();
    await page.locator('#topics .topic-source button').nth(1).click();
    await page.locator('#topics .topic-notice', { hasText: 'not in the conversation’s current history' }).waitFor();

    // Archive keeps the record but takes no messages; restore brings it back.
    await page.click('#topics [data-key="archive"]');
    await page.locator('#topics .topic-composer[data-archived]').waitFor();
    await page.click('#topics [data-key="back"]');
    assert.equal(await page.locator('#topics .topic-row').count(), 1);
    await page.click('#topics [data-key="filter-archived"]');
    await page.click('#topics [data-key="topic-tp2"]');
    await page.click('#topics [data-key="restore"]');
    await page.locator('#topics [data-key="compose-tp2"]').waitFor();

    // Keyboard: Enter on a row opens it and the back button returns focus.
    await page.click('#topics [data-key="back"]');
    await page.click('#topics [data-key="filter-all"]');
    await page.focus('#topics [data-key="topic-tp1"]');
    await page.keyboard.press('Enter');
    await page.locator('#topics h3.topic-title', { hasText: 'Topics and tasks' }).waitFor();
    await page.keyboard.press('Enter'); // the back button has focus
    assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'topic-tp1');

    // Chinese, dark and narrow.
    await page.evaluate(() => localStorage.setItem('fixture.locale', 'zh-Hans'));
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.setViewportSize({ width: 760, height: 820 });
    await page.reload();
    await page.locator('.agent-row--yesman').first().click();
    await page.locator('#yesman-info .info-link', { hasText: '话题' }).click();
    await page.locator('#topics .files-title', { hasText: '话题' }).waitFor();
    await page.click('#topics [data-key="topic-tp1"]');
    await page.locator('#topics .topic-heading', { hasText: '已确认的结论' }).waitFor();
    // As narrow as the board: the same windows fit either beside the conversation.
    const box = await page.locator('#topics').boundingBox();
    const mainBox = await page.locator('main.main').boundingBox();
    await shot(page, `${engineName}-zh-dark-narrow`);
    await page.locator('#yesman-info .info-link', { hasText: '任务板' }).click();
    const boardBox = await page.locator('#board').boundingBox();
    console.log(`${engineName} narrow: topics ${JSON.stringify(box)} board ${JSON.stringify(boardBox)} main ${JSON.stringify(mainBox)}`);
    assert.ok(box.width <= boardBox.width && box.x >= boardBox.x, `topics pane ${JSON.stringify(box)} vs board ${JSON.stringify(boardBox)}`);
    // WebKit reports the fixture's own event poll cancelled by page.reload().
    assert.deepEqual(errors.filter((e) => !/\/fixture\/events\?after=\d+ due to access control checks/.test(e)), []);
    console.log(`${engineName}: PASS`);
  } finally {
    await browser.close();
    await backend.close();
  }
}

try {
  const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
  assert.equal(built.status, 0, built.stderr || built.stdout);
  for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) await run(name, engine);
} finally {
  await rm(fixtureDir, { recursive: true, force: true });
}
