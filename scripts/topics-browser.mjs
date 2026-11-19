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
// With YESMAN_TEST_RUNTIME, the bridge runs the pinned runtime and its native queue.
const native = !!process.env.YESMAN_TEST_RUNTIME;
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
  const forwarded = ['State', 'Messages', 'Submit', 'SubmitCoalescing', 'Abort', 'Board', 'Info', 'Topics', 'CreateTopic', 'UpdateTopic', 'SubmitTopic', 'ListFiles', 'ReadFile'];
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
        case 'Repos': case 'Clones': case 'Pulls': return [];
        case 'Settings': return { appearance: 'system', autoFetch: 0 };
        case 'Fetches': return { fetching: [], errors: {} };
        case 'TakeFocus': return '';
        case 'Update': return { enabled: false };
        case 'RestartStatus': return { pending: false };
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

async function checkComposerLayout(page, state, singleLine) {
  const [chat, topic] = await page.locator('#dock .composer-input, #topics .composer-input').evaluateAll((rows) => rows.map((row) => {
    const [attach, textarea, send] = row.children;
    const frame = row.getBoundingClientRect();
    const plus = attach.getBoundingClientRect();
    const text = textarea.getBoundingClientRect();
    const arrow = send.getBoundingClientRect();
    const style = getComputedStyle(textarea);
    return {
      height: frame.height,
      attachInset: plus.left - frame.left,
      sendInset: frame.right - arrow.right,
      textStartGap: text.left - plus.right,
      textEndGap: arrow.left - text.right,
      attachBottom: frame.bottom - plus.bottom,
      sendBottom: frame.bottom - arrow.bottom,
      firstLineCenter: text.top + parseFloat(style.paddingTop) + parseFloat(style.lineHeight) / 2 - frame.top,
      attachCenter: plus.top + plus.height / 2 - frame.top,
      sendCenter: arrow.top + arrow.height / 2 - frame.top,
    };
  }));
  for (const key of Object.keys(chat)) {
    assert.ok(Math.abs(topic[key] - chat[key]) <= 1, `${state} ${key}: Topics ${topic[key]} vs Chat ${chat[key]}`);
  }
  if (singleLine) {
    assert.ok(Math.abs(topic.firstLineCenter - topic.attachCenter) <= 1, `${state}: first line is centered with Attach`);
    assert.ok(Math.abs(topic.firstLineCenter - topic.sendCenter) <= 1, `${state}: first line is centered with Send`);
  }
}

async function run(engineName, engine, scenario) {
  const backend = await fixture();
  const browser = await engine.launch({ headless: true });
  try {
    const viewport = { width: 1360, height: 860 };
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (err) => errors.push(err.message));
    await page.addInitScript(bridge);
    await page.goto(backend.url);
    const lead = page.locator('.agent-row--yesman').first();
    await lead.waitFor();
    await lead.click();
    if (scenario) {
      await scenario(page, backend.url, engineName);
      assert.deepEqual(errors, []);
      return;
    }
    const topicsRow = page.locator('#yesman-info [data-info-entry="topics"]');
    await topicsRow.waitFor();

    // The info panel's one card lists Knowledge, Working Logs, Needs You,
    // Topics and Tasks, a row each, with what each counts; no Board row.
    await page.waitForFunction(() => /Empty/.test(document.querySelector('#yesman-info [data-info-entry="topics"]')?.innerText ?? ''));
    const entries = await page.$$eval('#yesman-info .info-entries .info-link', (els) => els.map((e) => ({ key: e.dataset.infoEntry, label: e.querySelector('.info-link-label')?.textContent, aside: e.querySelector('.info-aside')?.textContent })));
    assert.deepEqual(entries.map((e) => e.key), ['knowledge', 'working-logs', 'needs-you', 'topics', 'tasks'], JSON.stringify(entries));
    assert.deepEqual(entries.map((e) => e.label), ['Knowledge', 'Working Logs', 'Needs You', 'Topics', 'Tasks'], JSON.stringify(entries));
    assert.equal(entries[0].aside, '2 notes');
    assert.equal(entries[3].aside, 'Empty');
    assert.equal(entries[4].aside, 'None');
    assert.equal(await page.locator('#yesman-info .info-entries').count(), 1, 'one card');
    assert.equal(await page.$eval('#yesman-info', (el) => el.firstElementChild?.classList.contains('info-section--entries') && el.children.length > 1), true, 'Storage and worktrees follow the card');
    await shot(page, `${engineName}-info`);

    // Board still opens its own pane, from the titlebar; Topics replaces it
    // beside the conversation.
    await page.locator('#bar-actions button[aria-label="Show the board"]').click();
    await page.locator('#board .board-task').first().waitFor();
    await topicsRow.click();
    await page.locator('#topics .board-empty', { hasText: 'No topics yet' }).waitFor();
    assert.equal(await page.locator('#board').isHidden(), true);
    // At 1360px the info makes way for Topics' two columns; the titlebar's
    // Show the info keeps it beside this pane, one column at a time, as the
    // rest of this scenario reads it (resource explorer).
    assert.equal(await page.locator('#yesman-info').isVisible(), false);
    await page.locator('#bar-actions button[aria-label="Show the info"]').click();
    await topicsRow.waitFor();
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

    // With a classic scrollbar, growing the draft can narrow the same field
    // that just measured its wrapped text. It should still fit below the cap.
    const topicDraft = page.locator('#topics [data-key="compose-tp2"]');
    const classicScrollbar = await page.addStyleTag({ content: '#topics .explorer-content::-webkit-scrollbar { width: 18px; }' });
    const draft = 'A longer draft with normal words that wraps when this panel narrows. It must stay readable when we type, hide, and restore it.';
    await page.setViewportSize({ width: 760, height: 1200 });
    await page.evaluate(() => document.fonts.ready);
    // Resolve the live field inside the measurement because an asynchronous
    // Topics redraw can replace a locator's element before evaluate runs.
    const wrapped = await page.evaluate((draft) => {
      const el = document.querySelector('#topics [data-key="compose-tp2"]');
      const column = el.closest('.explorer-content');
      el.focus();
      el.value = draft;
      el.dispatchEvent(new Event('input', { bubbles: true }));
      // Match the column's limit to the rendered detail. Keep the viewport
      // fixed and sample this same render before an asynchronous redraw.
      const threshold = document.createElement('style');
      threshold.id = 'fixture-scroll-threshold';
      threshold.textContent = `#topics .explorer-content { max-height: ${Math.ceil(column.firstElementChild.getBoundingClientRect().height) + 3}px; }`;
      document.head.append(threshold);
      const before = column.scrollHeight > column.clientHeight;
      el.value = `${draft}\nx\ny\nz`;
      el.setSelectionRange(el.value.length, el.value.length);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      return {
        before, after: column.scrollHeight > column.clientHeight,
        height: el.clientHeight,
        content: el.scrollHeight,
        maxHeight: parseFloat(getComputedStyle(el).maxHeight),
        focused: document.activeElement === el,
        selection: [el.selectionStart, el.selectionEnd],
        value: el.value,
      };
    }, draft);
    assert.deepEqual([wrapped.before, wrapped.after], [false, true], `the edit makes the Topics column scroll: ${JSON.stringify(wrapped)}`);
    if (shots) {
      await page.locator('#topics .explorer-content').evaluate((el) => { el.scrollTop = el.scrollHeight; });
      await shot(page, `${engineName}-narrow-wrapped-edit`);
    }
    assert.ok(wrapped.height < wrapped.maxHeight, 'draft is below the textarea cap');
    assert.ok(wrapped.content <= wrapped.height + 1, `all wrapped lines fit: ${wrapped.content} px of text in ${wrapped.height} px`);
    assert.equal(wrapped.focused, true);
    assert.deepEqual(wrapped.selection, [draft.length + 6, draft.length + 6]);
    assert.equal(wrapped.value, `${draft}\nx\ny\nz`);
    await topicDraft.fill('');
    await page.locator('#fixture-scroll-threshold').evaluate((style) => style.remove());
    await classicScrollbar.evaluate((style) => style.remove());
    await page.setViewportSize(viewport);
    await page.click('#topics [data-key="back"]');
    // Back returns focus to the row; a redraw from the topic just created may still be settling.
    await page.waitForFunction(() => document.activeElement?.dataset.key === 'topic-tp2', null, { timeout: 5000 });

    // The info panel follows: Topics now counts the two open topics.
    try {
      await page.waitForFunction(() => document.querySelector('#yesman-info [data-info-entry="topics"] .info-aside')?.textContent === '2 open', null, { timeout: 8000 });
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

    const main = page.locator('#dock textarea');
    const composeA = page.locator('#topics [data-key="compose-tp1"]');
    await page.evaluate(() => document.fonts.ready);
    // The earlier viewport change can leave the empty Chat field at the
    // height it measured at the narrower width. Measure both at this width.
    await main.evaluate((el) => el.dispatchEvent(new Event('input', { bubbles: true })));
    await checkComposerLayout(page, 'empty', true);
    await main.fill('First line');
    await composeA.fill('First line');
    assert.equal(await composeA.evaluate((el) => el === document.activeElement), true);
    await checkComposerLayout(page, 'focused single line', true);
    await main.press('Shift+Enter');
    await composeA.press('Shift+Enter');
    assert.equal(await composeA.inputValue(), 'First line\n');
    await checkComposerLayout(page, 'multiline', false);
    const longDraft = 'Long draft line\n'.repeat(30);
    await main.fill(longDraft);
    await composeA.fill(longDraft);
    await checkComposerLayout(page, 'long multiline', false);
    await page.waitForFunction((value) => {
      const el = document.querySelector('#topics [data-key="compose-tp1"]');
      if (el?.value !== value || el.scrollHeight <= el.clientHeight) return false;
      el.scrollTop = el.scrollHeight;
      return el.scrollTop > 0;
    }, longDraft); // wait for the current field if a topic refresh redraws it

    // Send from topics while the lead's run is held: the main draft stays,
    // A then B (with an image) reach the lead's one conversation.
    await main.fill('my unsent main draft');
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
    if (native) {
      // With the pinned runtime, topic B waits in pi's own steering queue:
      // its receipt row says so under its chip, with the queue's actions.
      await page.waitForFunction(() => [...document.querySelectorAll('#thread .msg-delivery')].some((el) => el.querySelector('.topic-chip')?.innerText.includes('tp2') &&
        /Steering · waiting for input checkpoint/.test(el.querySelector('.delivery-status')?.textContent ?? '')), null, { timeout: 10000 });
    }
    await shot(page, `${engineName}-sent`);
    // A topic line typed or pasted into the main composer is only text: the
    // app did not send it from a topic, so it names none.
    await main.fill('[Topic tp1: Topics and tasks]\n\nPasted into the main line');
    await main.press('Enter');
    await page.waitForFunction(() => [...document.querySelectorAll('#thread .msg-user')].some((e) => e.innerText.includes('Pasted into the main line')));
    assert.equal(await page.locator('#thread .msg-user--topic', { hasText: 'Pasted into the main line' }).count(), 0, 'pasted text names no topic');
    await post(backend.url, '/fixture/gate?open=1');
    await page.waitForFunction(() => document.querySelectorAll('#thread .msg-assistant').length >= 1, null, { timeout: 30000 });
    if (native) {
      // pi took both: each is its native message, tied to its submission.
      await page.waitForFunction(() => [...document.querySelectorAll('#thread .msg-delivery[data-bound="true"]')].filter((el) => el.querySelector('.topic-chip') &&
        !el.querySelector('.delivery-status')).length === 2, null, { timeout: 30000 });
    }

    // After a reload the native history still names each topic; a chip opens it.
    await page.reload();
    await page.locator('.agent-row--yesman').first().click();
    await page.waitForFunction(() => document.querySelectorAll('#thread .msg-user--topic').length === 2 && [...document.querySelectorAll('#thread .msg-user')].some((e) => e.innerText.includes('Pasted into the main line')), null, { timeout: 30000 });
    assert.match(await page.locator('#thread .msg-user', { hasText: 'Pasted into the main line' }).innerText(), /^\[Topic tp1: Topics and tasks\]/, 'its text is shown whole');
    assert.equal(await page.locator('#thread .msg-user--topic', { hasText: 'Pasted into the main line' }).count(), 0);
    const chips = await page.$$eval('#thread .msg-user--topic .topic-chip', (els) => els.map((e) => e.innerText.replace(/\s+/g, ' ')));
    assert.match(chips[0], /tp1/);
    assert.match(chips[1], /tp2/);
    assert.equal(await page.locator('#thread .msg-user--topic img').count(), 1, 'the image stays with topic B');
    await page.locator('#thread .msg-user--topic .topic-chip', { hasText: 'tp2' }).click();
    await page.locator('#topics h3.topic-title', { hasText: 'Image limits' }).waitFor();
    // Opened anew after the reload, Topics has the info make way again;
    // keep it beside, one column at a time, as above.
    assert.equal(await page.locator('#yesman-info').isVisible(), false);
    await page.locator('#bar-actions button[aria-label="Show the info"]').click();
    await page.locator('#yesman-info [data-info-entry="topics"]').waitFor();
    // Its conversation lists the native message, and shows it exactly.
    await page.locator('#topics .topic-message button').first().click();
    await page.locator('#thread .msg-found:is(.msg-user--topic, :has(.msg-user--topic))').waitFor();

    await page.click('#topics [data-key="back"]');
    await page.click('#topics [data-key="topic-tp1"]');
    await page.locator('#topics .topic-message', { hasText: 'What belongs on the board?' }).waitFor();
    assert.equal(await page.locator('#topics .topic-message', { hasText: 'Pasted' }).count(), 0, 'topic A lists only what was sent from it');
    await page.click('#topics [data-key="back"]');
    await page.click('#topics [data-key="topic-tp2"]');

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
    // A status change reveals the topic in its new filter.
    assert.equal(await page.locator('#topics [data-key="filter-archived"]').getAttribute('aria-pressed'), 'true');
    assert.deepEqual(await page.locator('#topics-list [data-topic]').evaluateAll((rows) => rows.map((r) => r.dataset.topic)), ['tp2']);
    await page.click('#topics [data-key="topic-tp2"]');
    await page.click('#topics [data-key="restore"]');
    await page.locator('#topics [data-key="compose-tp2"]').waitFor();
    assert.equal(await page.locator('#topics [data-key="filter-open"]').getAttribute('aria-pressed'), 'true');

    // The chip reveals an archived topic even from a mismatched filter/query.
    await page.click('#topics [data-key="archive"]');
    await page.locator('#topics .topic-composer[data-archived]').waitFor();
    await page.click('#topics [data-key="back"]');
    await page.click('#topics [data-key="filter-resolved"]');
    await page.fill('#topics [data-key="search"]', 'unrelated query');
    await page.locator('#thread .msg-user--topic .topic-chip', { hasText: 'tp2' }).click();
    await page.locator('#topics .topic-composer[data-archived]').waitFor();
    await page.click('#topics [data-key="back"]');
    assert.equal(await page.locator('#topics [data-key="filter-archived"]').getAttribute('aria-pressed'), 'true');
    assert.equal(await page.locator('#topics [data-key="search"]').inputValue(), '');
    assert.equal(await page.locator('#topics [data-key="topic-tp2"]').getAttribute('aria-current'), 'true');
    await page.click('#topics [data-key="topic-tp2"]');
    await page.click('#topics [data-key="restore"]');
    await page.locator('#topics [data-key="compose-tp2"]').waitFor();

    // Keyboard: Enter on a row opens it and the back button returns focus.
    await page.click('#topics [data-key="back"]');
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
    await page.locator('#yesman-info [data-info-entry="topics"]', { hasText: '话题' }).click();
    await page.locator('#topics .files-title', { hasText: '话题' }).waitFor();
    await page.click('#topics [data-key="topic-tp1"]');
    await page.locator('#topics .topic-heading', { hasText: '已确认的结论' }).waitFor();
    const narrowCompose = page.locator('#topics [data-key="compose-tp1"]');
    await main.fill('Short draft');
    await narrowCompose.fill('Short draft');
    await checkComposerLayout(page, 'narrow single line', true);
    await main.fill('');
    await narrowCompose.fill('');
    // As narrow as the board: the same windows fit either beside the conversation.
    const box = await page.locator('#topics').boundingBox();
    const mainBox = await page.locator('main.main').boundingBox();
    await shot(page, `${engineName}-zh-dark-narrow`);
    await page.locator('#bar-actions button[aria-label="显示任务板"]').click();
    const boardBox = await page.locator('#board').boundingBox();
    console.log(`${engineName} narrow: topics ${JSON.stringify(box)} board ${JSON.stringify(boardBox)} main ${JSON.stringify(mainBox)}`);
    assert.ok(box.width <= boardBox.width && box.x >= boardBox.x, `topics pane ${JSON.stringify(box)} vs board ${JSON.stringify(boardBox)}`);
    // As the board does, it leaves the conversation room (v0.1.37's panes).
    assert.ok(mainBox.width >= 240, `the conversation beside the topics: ${JSON.stringify(mainBox)}`);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.locator('[data-info-entry="knowledge"]').click();
    const knowledge = page.locator('#files-tree-list');
    await page.locator('#files-view [id="knowledge"]').waitFor();
    assert.deepEqual(await knowledge.locator('.tree-name').allInnerTexts(), ['INDEX.md', 'Icon policy', 'Guides'], 'actual disk listing excludes alias and uses catalog order');
    await knowledge.getByRole('treeitem', { name: 'Guides', exact: true }).click();
    await knowledge.getByRole('treeitem', { name: 'desktop', exact: true }).click();
    const note = knowledge.getByRole('treeitem', { name: 'Navigation guide', exact: true });
    assert.equal(await note.getAttribute('aria-level'), '3');
    assert.equal(await page.evaluate(() => fixtureCalls.filter((c) => c[0] === 'ReadFile' && c[2] !== '.yesman/knowledge/INDEX.md').length), 0);
    await note.click();
    await page.locator('#files-view [id="keyboard"]:focus').waitFor();
    assert.equal(await knowledge.getByText('Keyboard', { exact: true }).count(), 0);
    await shot(page, `${engineName}-knowledge-go-bridge`);
    // WebKit reports the fixture's own event poll cancelled by page.reload().
    assert.deepEqual(errors.filter((e) => !/\/fixture\/events\?after=\d+ due to access control checks/.test(e)), []);
    console.log(`${engineName}: PASS`);
  } finally {
    await browser.close();
    await backend.close();
  }
}

// Test conversation navigation in its own workspace, with real submissions
// producing both chips and no edit conflict or queue timing prerequisites.
async function editorNavigation(page, url, engineName) {
  const { value: state } = await post(url, '/fixture/call', { method: 'State', args: [] });
  const y = state.yesmen[0];
  for (const title of ['Editing target', 'Archived target']) {
    const r = await post(url, '/fixture/call', { method: 'CreateTopic', args: [y.name, y.hiredAt, { title }] });
    assert.ok(r.value?.topic, JSON.stringify(r));
  }
  await post(url, '/fixture/gate?open=1');
  await page.setViewportSize({ width: 1120, height: 900 });
  await page.locator('[data-info-entry="topics"]').click();
  const pane = page.locator('#topics');
  const control = (key) => pane.locator(`[data-key="${key}"]`);
  const chip = (id) => page.locator('#thread .msg-user--topic .topic-chip', { hasText: id });
  for (const id of ['tp1', 'tp2']) {
    await control(`topic-${id}`).click();
    await control(`compose-${id}`).fill(`A real message from ${id}`);
    await control(`compose-${id}`).press('Enter');
    await chip(id).waitFor();
    if (id === 'tp2') {
      await control('archive').click();
      await pane.locator('.topic-composer[data-archived]').waitFor();
    }
    await control('back').click();
  }

  const editTitle = control('edit-title');
  const editSummary = control('edit-summary');
  const assertEditor = async (heading, title, summary) => {
    assert.equal(await editTitle.isVisible(), true, `${heading} stays visible after opening a conversation chip`);
    assert.equal(await pane.locator('.topic-editor h3').innerText(), heading);
    assert.equal(await editTitle.inputValue(), title);
    assert.equal(await editSummary.inputValue(), summary);
    for (const key of ['edit-save', 'edit-cancel']) assert.ok(await control(key).isVisible() && await control(key).isEnabled());
    await page.waitForFunction(() => document.activeElement?.dataset.key === 'edit-title');
  };
  const mutations = () => page.evaluate(() => fixtureCalls.filter(([method]) => ['SubmitTopic', 'CreateTopic', 'UpdateTopic'].includes(method)).length);
  const before = await mutations();
  await control('filter-open').click();
  await control('topic-tp1').click();
  await control('edit').click();
  await editTitle.fill('Unsaved title');
  await editSummary.fill('Unsaved summary');
  await chip('tp2').click();
  await shot(page, `${engineName}-edit-after-archived-chip`);
  await assertEditor('Edit tp1', 'Unsaved title', 'Unsaved summary');
  assert.equal(await pane.locator('.explorer-nav').isVisible(), false, 'the editor has the narrow pane');
  assert.equal(await control('filter-open').getAttribute('aria-pressed'), 'true');
  await chip('tp1').click();
  await assertEditor('Edit tp1', 'Unsaved title', 'Unsaved summary');

  await page.setViewportSize({ width: 1600, height: 900 });
  await control('filter-archived').click();
  await control('topic-tp2').click();
  await pane.locator('.topic-composer[data-archived]').waitFor();
  await pane.getByRole('button', { name: 'Close the topics' }).click();
  await page.setViewportSize({ width: 1120, height: 900 });
  await page.locator('[data-info-entry="topics"]').press('Enter');
  await assertEditor('Edit tp1', 'Unsaved title', 'Unsaved summary');
  await control('edit-cancel').click();
  assert.equal(await pane.locator('h3.topic-title').innerText(), 'Editing target');

  await control('new').click();
  await editTitle.fill('Unsaved new topic');
  await editSummary.fill('Unsaved new summary');
  await pane.getByRole('button', { name: 'Close the topics' }).click();
  await chip('tp2').click();
  await assertEditor('New topic', 'Unsaved new topic', 'Unsaved new summary');
  await shot(page, `${engineName}-new-after-archived-chip`);
  await control('edit-cancel').click();
  assert.equal(await pane.locator('.topic-composer[data-archived]').count(), 0, 'Cancel does not open the chip target');

  await control('filter-resolved').click();
  await control('search').fill('unrelated query');
  await chip('tp2').click();
  await pane.locator('.topic-composer[data-archived]').waitFor();
  await page.waitForFunction(() => document.activeElement?.dataset.key === 'back');
  await shot(page, `${engineName}-archived-chip-after-cancel`);
  await control('back').click();
  assert.equal(await control('filter-archived').getAttribute('aria-pressed'), 'true');
  assert.equal(await control('search').inputValue(), '');
  assert.equal(await control('topic-tp2').getAttribute('aria-current'), 'true');
  assert.equal(await mutations(), before, 'the chips and Cancel make no writes or sends');
  console.log(`${engineName} editor navigation: PASS`);
}

try {
  const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
  assert.equal(built.status, 0, built.stderr || built.stdout);
  for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    await run(name, engine, editorNavigation);
    await run(name, engine);
  }
} finally {
  await rm(fixtureDir, { recursive: true, force: true });
}
