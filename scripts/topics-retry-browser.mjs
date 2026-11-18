import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

// C1: a user explicitly retries a hidden Topics message without editing it.
// Equal-revision receipts and rejected bridge/registration calls must still
// reopen its original and Copy. Transport/payload retention here are synthetic.
const retryBridge = () => {
  localStorage.setItem('yesman.info', 'hidden');
  const saved = JSON.parse(sessionStorage.getItem('topic-retry') ?? 'null') ?? { seq: 0, calls: [], receipts: {}, payloads: {}, topics: {}, dispatches: 0 };
  const agents = ['alice', 'bob'].map((name) => ({ id: `${name}/lead`, yesman: name, name: 'lead', role: 'lead', since: '1', status: 'idle', model: 'm', provider: 'p' }));
  const handlers = {};
  const persist = () => sessionStorage.setItem('topic-retry', JSON.stringify(saved));
  const topicsOf = (y) => saved.topics[y] ??= [{ id: 'tp1', title: 'Retry original', status: 'open', revision: 1, summary: 'Keep the complete original accessible.', conclusions: [], questions: [], sources: [], sends: [] }];
  const emit = (agent, delivery) => {
    const event = { agent, seq: ++saved.seq, data: { type: 'delivery_update', delivery, retained: (saved.receipts[agent] ?? []).map((d) => d.id) } };
    for (const fn of handlers['agent-event'] ?? []) fn(event);
    persist();
  };
  window.retry = { saved, mode: 'same', state: 'unconfirmed', copied: [], unexpected: [], emit,
    addOther(id) {
      saved.receipts['bob/lead'] = [{ id, agentSince: '1', revision: 2, order: 1, state: 'interrupted', text: 'Bob original', imageCount: 0 }];
      persist();
    },
  };
  window.mygo = {
    platform: 'darwin', locale: 'en', on(name, fn) { (handlers[name] ??= []).push(fn); },
    async call(method, ...args) {
      const m = method.replace('Yesman.', '');
      saved.calls.push({ m, args });
      switch (m) {
        case 'State': return { yesmen: ['alice', 'bob'].map((name) => ({ name, displayName: name, hiredAt: '1' })), agents };
        case 'Messages': return structuredClone({ messages: [], live: [], deliveries: saved.receipts[args[0]] ?? [], seq: saved.seq, queue: { known: false } });
        case 'Topics': return structuredClone(topicsOf(args[0]));
        case 'Models': return { agent: agents.find((a) => a.id === args[0]), models: [] };
        case 'Repos': case 'Clones': return [];
        case 'Settings': return { appearance: 'light' };
        case 'NeedsYou': return { items: [] };
        case 'Context': return { available: false };
        case 'Shortcuts': case 'Update': return {};
        case 'TakeFocus': return '';
        case 'CopyValue': retry.copied.push(args[0]); return {};
        case 'SubmissionPayload': return structuredClone(saved.payloads[`${args[0]}/${args[1]}`]);
        case 'SubmissionText': return saved.payloads[`${args[0]}/${args[1]}`].text;
        case 'SubmitTopic': case 'SubmitCoalescing': {
          const topic = m === 'SubmitTopic';
          const [agent, id, text, images] = topic ? [`${args[0]}/lead`, args[3], args[4], args[5]] : args;
          const full = topic ? `[Topic tp1: Retry original]\n\n${text}` : text;
          const list = saved.receipts[agent] ??= [];
          let d = list.find((r) => r.id === id);
          if (d && retry.mode !== 'same') { persist(); throw Error(retry.mode === 'bridge' ? 'Bridge unavailable' : 'Topic registration refused'); }
          if (!d) {
            d = { id, agentSince: '1', revision: 2, order: 1, state: retry.state, error: 'Transport outcome unknown', text: full.slice(0, 128), preview: true, imageCount: images.length, originalPayload: true, originalText: true };
            list.push(d);
            saved.payloads[`${agent}/${id}`] = { text: full, images, members: [id] };
            if (topic) topicsOf(args[0])[0].sends.push({ id, context: '[Topic tp1: Retry original]', preview: text.slice(0, 128), truncated: true, state: d.state, imageCount: images.length });
            saved.dispatches++; // one modeled intake; duplicate responses do not execute again.
            emit(agent, structuredClone(d));
          }
          persist();
          return topic ? { delivery: structuredClone(d), text: full } : structuredClone(d);
        }
        default: retry.unexpected.push(m); throw Error('Unexpected bridge call: ' + m);
      }
    },
  };
};

export async function retryHiddenTopic(browser, engine, options, url) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = [], outside = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await context.route('**/*', (route) => route.request().url().startsWith(url) || route.request().url().startsWith('data:') ? route.continue() : (outside.push(route.request().url()), route.abort()));
  await page.addInitScript(retryBridge);
  const text = ('The complete original, with Markdown **kept** and line breaks.\n' + 'Every word stays available. '.repeat(12)).trim();
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==', 'base64');
  const submitCalls = () => page.evaluate(() => retry.saved.calls.filter((c) => ['SubmitTopic', 'SubmitCoalescing'].includes(c.m)));
  const hidden = () => page.evaluate(() => JSON.parse(localStorage.getItem('yesman.handledSubmissions') ?? '{}'));
  const topicOpen = async () => {
    if (!await page.locator('#topics').isVisible()) await page.getByRole('button', { name: 'Show the topics', exact: true }).click();
    if (!await page.locator('#topics [data-key="compose-tp1"]').count()) await page.click('#topics [data-key="topic-tp1"]');
  };
  try {
    await page.goto(url);
    await page.locator('[data-agent="alice/lead"]').first().click();
    await page.evaluate((state) => retry.state = state, options.state);
    if (!options.main) await topicOpen();
    const composer = page.locator(options.main ? '#dock textarea' : '#topics [data-key="compose-tp1"]');
    const main = page.locator('#dock textarea');
    if (!options.main) await main.fill('unsent main draft');
    await composer.fill(text);
    if (options.image) {
      await page.locator('#topics .topic-composer input[type="file"]').setInputFiles({ name: 'original.png', mimeType: 'image/png', buffer: png });
      await page.locator('#topics .composer-image img').waitFor();
    }
    await composer.press('Enter');
    await page.waitForFunction(() => retry.saved.dispatches === 1);
    const calls = await submitCalls();
    assert.equal(calls.length, 1, 'one explicit first attempt');
    const id = calls[0].args[options.main ? 1 : 3];
    const row = page.locator(`#thread > [data-delivery="${id}"]`);
    const full = async () => {
      await page.waitForFunction((id) => document.querySelector(`#thread > [data-delivery="${id}"] .message-copy`)?.disabled === false, id);
      assert.equal(await row.count(), 1);
      await row.locator('.message-copy').click();
      assert.equal(await page.evaluate(() => retry.copied.at(-1)), text, 'Copy is complete raw text without the topic marker');
      assert.equal(await row.locator('.topic-chip').count(), options.main ? 0 : 1);
      assert.equal(await row.locator('img').count(), options.image ? 1 : 0);
      if (options.image) {
        await page.waitForFunction((id) => document.querySelector(`#thread > [data-delivery="${id}"] img`)?.naturalWidth === 16, id);
        assert.equal(await row.locator('img').evaluate((img) => img.complete && img.src.split(',')[1]), png.toString('base64'));
      }
    };
    await full();
    const receiptBefore = await page.evaluate((id) => structuredClone(retry.saved.receipts['alice/lead'].find((d) => d.id === id)), id);
    if (options.hide !== false) {
      await row.locator('.message-dismiss').click();
      await frame(page);
      assert.equal(await row.count(), 0);
      // Background equal/lower revision and wrong-generation updates are not
      // explicit attempts, so they cannot lift Hide or manufacture a send.
      await page.evaluate((id) => {
        const d = retry.saved.receipts['alice/lead'].find((d) => d.id === id);
        retry.emit('alice/lead', { ...d, state: 'queued', nativePending: true });
        retry.emit('alice/lead', { ...d, agentSince: 'old', revision: d.revision + 1, state: 'queued', nativePending: true });
      }, id);
      await frame(page);
      assert.equal(await row.count(), 0);
      assert.equal((await submitCalls()).length, 1);
    }
    // Same exact ID in another conversation stays hidden when Alice retries.
    await page.evaluate((id) => retry.addOther(id), id);
    await page.locator('[data-agent="bob/lead"]').first().click();
    await row.locator('.message-dismiss').click();
    await frame(page);
    const bobHide = (await hidden())['bob/lead@1'];
    assert.deepEqual(bobHide, [id]);
    await page.locator('[data-agent="alice/lead"]').first().click();
    if (!options.main) await topicOpen();
    await page.evaluate((mode) => retry.mode = mode, options.mode);
    await composer.press('Enter');
    await page.waitForFunction(() => retry.saved.calls.filter((c) => ['SubmitTopic', 'SubmitCoalescing'].includes(c.m)).length === 2);
    await frame(page);
    const attempts = await submitCalls();
    assert.equal(attempts[1].args[options.main ? 1 : 3], id, 'unchanged retry keeps its immutable ID');
    assert.equal(await composer.inputValue(), text, 'failed retry keeps the whole draft');
    await full();
    assert.deepEqual((await hidden())['bob/lead@1'], bobHide, 'only the explicit attempt\'s conversation reopens');
    assert.ok(!((await hidden())['alice/lead@1'] ?? []).includes(id));
    assert.deepEqual(await page.evaluate((id) => retry.saved.receipts['alice/lead'].find((d) => d.id === id), id), receiptBefore, 'same revision and response semantics');
    if (!options.main) assert.equal(await main.inputValue(), 'unsent main draft');
    if (process.env.TOPICS_RETRY_OUT && options.image) {
      await mkdir(process.env.TOPICS_RETRY_OUT, { recursive: true });
      await page.screenshot({ path: `${process.env.TOPICS_RETRY_OUT}/${engine}-retry.png` });
    }
    await page.reload();
    await page.locator('[data-agent="alice/lead"]').first().click();
    await full();
    assert.equal((await submitCalls()).length, 2, 'reload does not submit again');
    assert.equal(await page.evaluate(() => retry.saved.dispatches), 1, 'modeled same-ID duplicate never replays intake');
    if (process.env.TOPICS_RETRY_OUT && options.image) await page.screenshot({ path: `${process.env.TOPICS_RETRY_OUT}/${engine}-reload.png` });
    assert.deepEqual(errors, []);
    assert.deepEqual(outside, []);
    assert.deepEqual(await page.evaluate(() => retry.unexpected), []);
    return { id, explicitAttempts: 2, modeledIntakes: 1, liveRows: 1, reloadRows: 1, fullCopy: true, ...options };
  } finally { await context.close(); }
}
