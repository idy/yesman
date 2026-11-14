// Topics' timing and attribution cases in the production page, in Chromium
// and WebKit, behind an in-page MyGo bridge that holds calls on demand: what
// ties a message to a topic, typing while a save is on its way, a save that
// returns after the user moved to another yesman, and a lead history that
// arrives after its topic is open. No Go, pi, model or user data is used.
//
//   YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/topics-races-browser.mjs
//
// TOPICS_WEB serves another copy of web/ (to show an older version failing);
// TOPICS_REPORT=1 prints each case's result instead of stopping at the first.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { extname, join, normalize } from 'node:path';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = process.env.TOPICS_WEB || fileURLToPath(new URL('../web/', import.meta.url));
const report = process.env.TOPICS_REPORT === '1';
const types = { '.js': 'application/javascript', '.css': 'text/css', '.html': 'text/html', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.json': 'application/json' };

const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname;
  const file = normalize(join(root, path === '/' ? 'index.html' : path));
  if (!file.startsWith(normalize(root))) return res.writeHead(404).end();
  try {
    const data = await readFile(file);
    res.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream' }).end(data);
  } catch {
    res.writeHead(404).end();
  }
}).listen(0, '127.0.0.1');
await new Promise((r) => server.once('listening', r));

const bridge = () => {
  localStorage.setItem('yesman.info', 'hidden');
  const since = '2026-01-01T00:00:00Z';
  const agents = ['alice', 'bob'].map((n) => ({ id: `${n}/lead`, yesman: n, name: 'lead', role: 'lead', since, status: 'idle', model: 'm', provider: 'p', effort: 'high' }));
  const topic = (id, title) => ({ id, title, status: 'open', revision: 1, summary: 'original summary', conclusions: [], questions: [], sources: [], sends: [], updatedAt: since });
  const state = { yesmen: ['alice', 'bob'].map((name) => ({ name, displayName: name, path: `/fixture/${name}`, hiredAt: since })), agents };
  const topics = { alice: [topic('tp1', 'Alpha'), topic('tp2', 'Beta')], bob: [topic('tp1', 'Bob topic')] };
  const messages = { 'alice/lead': [], 'bob/lead': [] };
  const handlers = {};
  let seq = 0;
  const hold = {};
  const held = (name) => (hold[name] ? new Promise((r) => { hold[name] = r; }) : null);
  window.fx = {
    topics, messages, calls: [],
    hold(name) { hold[name] = true; },
    holding: (name) => typeof hold[name] === 'function',
    release(name) { const r = hold[name]; hold[name] = null; if (typeof r === 'function') r(); },
    user: (text, origin) => ({ role: 'user', timestamp: 1800000000000 + (++seq), content: [{ type: 'text', text }], ...(origin ? { yesman: origin } : {}) }),
    send(yesman, id, submission, context) {
      topics[yesman].find((x) => x.id === id).sends.push({ id: submission, at: since, preview: 'p', state: 'accepted', context });
      for (const fn of handlers.topics ?? []) fn(yesman);
    },
    emit(agent, m) {
      messages[agent].push(m);
      for (const fn of handlers['agent-event'] ?? []) fn({ agent, agentSince: since, seq: ++seq, data: { type: 'message_end', message: m } });
    },
  };
  window.mygo = {
    locale: 'en',
    on(name, fn) { (handlers[name] ??= []).push(fn); },
    async call(method, ...args) {
      const m = method.replace('Yesman.', '');
      fx.calls.push(m);
      switch (m) {
        case 'State': return structuredClone(state);
        case 'Messages': {
          if (hold[`Messages:${args[0]}`]) await held(`Messages:${args[0]}`);
          return structuredClone({ messages: messages[args[0]], seq, live: [], deliveries: [], queue: { known: false } });
        }
        case 'Topics': return structuredClone(topics[args[0]]);
        case 'UpdateTopic': {
          const [name, , id, p] = args;
          const x = topics[name].find((t) => t.id === id);
          if (p.revision !== x.revision) return { topic: structuredClone(x), conflict: true };
          const patch = structuredClone(p);
          if (hold.save) await held('save');
          for (const f of ['title', 'summary', 'questions', 'conclusions']) if (f in patch) x[f] = patch[f];
          x.revision++;
          return { topic: structuredClone(x) };
        }
        case 'Models': return { agent: agents.find((a) => a.id === args[0]), models: [] };
        case 'Board': return { tasks: [] };
        case 'Info': return { knowledge: { notes: 0 }, board: { open: 0 }, topics: { open: 2 }, worktrees: [] };
        case 'Repos': case 'Clones': case 'ListFiles': case 'Pulls': return [];
        case 'TakeFocus': return '';
        default: return {};
      }
    },
  };
};

const results = [];
function check(engine, name, fn) {
  return fn().then(
    (detail) => results.push({ engine, name, pass: true, detail }),
    (err) => {
      results.push({ engine, name, pass: false, error: err.message.split('\n')[0] });
      if (!report) throw err;
    });
}

async function open(browser) {
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await page.addInitScript(bridge);
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  await page.locator('[data-agent="alice/lead"]').first().click();
  await page.getByRole('button', { name: 'Show the topics', exact: true }).click();
  await page.locator('#topics [data-key="topic-tp1"]').waitFor();
  return { page, errors };
}
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
async function pick(page, id) {
  if (await page.locator('#topics [data-key="back"]').count()) await page.click('#topics [data-key="back"]');
  await page.click(`#topics [data-key="topic-${id}"]`);
}
const settle = (page) => page.waitForTimeout(80);
const listed = (page) => page.locator('#topics .topic-message').allInnerTexts();
const S = (c) => c.repeat(48 / c.length);

async function run(engine, launcher) {
  const browser = await launcher.launch({ headless: true });
  try {
    // F1: only the app's record ties a message to a topic.
    {
      const { page, errors } = await open(browser);
      await check(engine, 'pasted or unknown topic lines name no topic', async () => {
        await pick(page, 'tp2');
        await page.evaluate((s) => {
          fx.emit('alice/lead', fx.user('[Topic tp2: Beta]\n\npasted into the main line', { source: 'user', submission: s }));
          fx.emit('alice/lead', fx.user('[Topic tp2: Beta]\n\nimported history of unknown origin'));
        }, S('d1'));
        await frame(page);
        assert.deepEqual(await listed(page), [], 'tp2 lists no message');
        assert.equal(await page.locator('#thread .topic-chip').count(), 0, 'no chip');
        assert.equal(await page.locator('#thread .msg-user', { hasText: '[Topic tp2: Beta]' }).count(), 2, 'their text is shown whole');
      });
      await check(engine, 'a sent message rewritten to another topic\'s line stays with its own topic', async () => {
        await page.evaluate((s) => fx.send('alice', 'tp1', s, '[Topic tp1: Alpha]'), S('a1'));
        await settle(page);
        await page.evaluate((s) => fx.emit('alice/lead', fx.user('[Topic tp2: rewritten]\n\nbody a hook rewrote', { source: 'user', submission: s })), S('a1'));
        await frame(page);
        assert.deepEqual(await listed(page), [], 'not listed under tp2');
        await pick(page, 'tp1');
        const a = await listed(page);
        assert.equal(a.length, 1, 'listed under tp1');
        assert.match(a[0], /\[Topic tp2: rewritten\]/, 'a line it was not sent with stays in the text');
        const chips = await page.locator('#thread .topic-chip').allInnerTexts();
        assert.equal(chips.length, 1);
        assert.match(chips[0], /tp1/, 'the chip names the topic it was sent from');
      });
      await check(engine, 'a message sent from the topic is listed on its message_end alone', async () => {
        await page.evaluate((s) => fx.send('alice', 'tp1', s, '[Topic tp1: Alpha]'), S('b2'));
        await settle(page);
        const before = (await listed(page)).length;
        await page.evaluate((s) => fx.emit('alice/lead', fx.user('[Topic tp1: Alpha]\n\nlive question', { source: 'user', submission: s })), S('b2'));
        await frame(page);
        const after = await listed(page);
        assert.equal(after.length, before + 1);
        assert.ok(after.some((t) => t.startsWith('live question')), 'shown without its line');
      });
      assert.deepEqual(errors, []);
      await page.close();
    }

    // F4: typing while a save is on its way is kept, and the saved revision too.
    {
      const { page, errors } = await open(browser);
      await check(engine, 'typing during a save is kept for the next save', async () => {
        await pick(page, 'tp1');
        await page.click('#topics [data-key="edit"]');
        await page.fill('#topics [data-key="edit-summary"]', 'first save');
        await page.evaluate(() => fx.hold('save'));
        await page.click('#topics [data-key="edit-save"]');
        await page.waitForFunction(() => fx.holding('save'));
        await page.fill('#topics [data-key="edit-summary"]', 'typed during save');
        await page.evaluate(() => fx.release('save'));
        await page.locator('#topics .topic-notice[role="status"]').waitFor();
        assert.equal(await page.evaluate(() => fx.topics.alice[0].summary), 'first save');
        assert.equal(await page.inputValue('#topics [data-key="edit-summary"]'), 'typed during save', 'still in the form');
        await page.click('#topics [data-key="edit-save"]');
        await page.locator('#topics [data-key="edit"]').waitFor();
        const saved = await page.evaluate(() => ({ summary: fx.topics.alice[0].summary, revision: fx.topics.alice[0].revision }));
        assert.deepEqual(saved, { summary: 'typed during save', revision: 3 }, 'saved at the revision the first save made, without a conflict');
        assert.equal(await page.locator('#topics .topic-notice[data-tone="warn"]').count(), 0);
      });
      await check(engine, 'a source being typed survives redraws', async () => {
        await page.click('#topics [data-key="edit"]');
        await page.selectOption('#topics [data-key="pick-kind"]', 'link');
        await page.fill('#topics [data-key="pick-ref"]', 'https://example.com/half-typed');
        await page.evaluate((s) => fx.send('alice', 'tp2', s, '[Topic tp2: Beta]'), S('e4'));
        await settle(page);
        await page.click('#topics [data-key="edit-questions-add"]');
        await frame(page);
        assert.deepEqual(await page.evaluate(() => ({ kind: document.querySelector('#topics [data-key="pick-kind"]').value, ref: document.querySelector('#topics [data-key="pick-ref"]').value })),
          { kind: 'link', ref: 'https://example.com/half-typed' });
        await page.click('#topics [data-key="pick-add"]');
        assert.equal(await page.locator('#topics .topic-edit-source', { hasText: 'example.com' }).count(), 1);
        assert.equal(await page.inputValue('#topics [data-key="pick-ref"]'), '', 'cleared once added');
        await page.click('#topics [data-key="edit-cancel"]');
      });
      await check(engine, 'a save typed over nothing ends the edit and focuses Edit', async () => {
        await page.click('#topics [data-key="edit"]');
        await page.fill('#topics [data-key="edit-title"]', 'Alpha renamed');
        await page.click('#topics [data-key="edit-save"]');
        await page.locator('#topics [data-key="edit"]').waitFor();
        assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'edit');
      });
      assert.deepEqual(errors, []);
      await page.close();
    }

    // C2: a save returning after the user went to another yesman takes no focus.
    {
      const { page, errors } = await open(browser);
      await check(engine, 'a late save does not take focus from another yesman', async () => {
        await pick(page, 'tp1');
        await page.click('#topics [data-key="edit"]');
        await page.fill('#topics [data-key="edit-title"]', 'late Alice');
        await page.evaluate(() => fx.hold('save'));
        await page.click('#topics [data-key="edit-save"]');
        await page.waitForFunction(() => fx.holding('save'));
        await page.locator('[data-agent="bob/lead"]').first().click();
        await pick(page, 'tp1');
        const dock = page.locator('#dock textarea');
        await dock.fill('bob draft');
        await dock.focus();
        await page.evaluate(() => fx.release('save'));
        await page.waitForFunction(() => fx.topics.alice[0].title === 'late Alice');
        await settle(page);
        const now = await page.evaluate(() => ({ focus: document.activeElement === document.querySelector('#dock textarea'), draft: document.querySelector('#dock textarea').value, title: document.querySelector('#topics h3')?.textContent }));
        assert.deepEqual(now, { focus: true, draft: 'bob draft', title: 'Bob topic' });
      });
      assert.deepEqual(errors, []);
      await page.close();
    }

    // F6: a lead history read after its topic is open fills the topic.
    {
      const { page, errors } = await open(browser);
      await check(engine, 'history arriving after the topic opened is listed', async () => {
        await page.evaluate((s) => {
          fx.hold('Messages:bob/lead');
          fx.messages['bob/lead'].push(fx.user('[Topic tp1: Bob topic]\n\nfrom an earlier session', { source: 'user', submission: s }));
          fx.topics.bob[0].sends.push({ id: s, at: '2026-01-01T00:00:00Z', preview: 'from an earlier session', state: 'accepted', context: '[Topic tp1: Bob topic]' });
        }, S('c3'));
        await page.locator('[data-agent="bob/lead"]').first().click();
        await pick(page, 'tp1');
        await page.fill('#topics [data-key="compose-tp1"]', 'bob topic draft');
        await page.focus('#topics [data-key="compose-tp1"]');
        await page.waitForFunction(() => fx.holding('Messages:bob/lead'));
        await page.evaluate(() => fx.release('Messages:bob/lead'));
        await page.locator('#thread .msg-user').first().waitFor();
        await frame(page);
        const texts = await listed(page);
        assert.equal(texts.length, 1, 'listed once the history arrives');
        assert.match(texts[0], /^from an earlier session/);
        assert.equal(await page.locator('#thread .msg-user--topic').count(), 1, 'the conversation names it');
        const kept = await page.evaluate(() => ({ draft: document.querySelector('#topics [data-key="compose-tp1"]')?.value, focus: document.activeElement?.dataset.key }));
        assert.deepEqual(kept, { draft: 'bob topic draft', focus: 'compose-tp1' }, 'the redraw keeps the draft and focus');
      });
      assert.deepEqual(errors, []);
      await page.close();
    }
  } finally {
    await browser.close();
  }
}

try {
  for (const [name, launcher] of [['chromium', chromium], ['webkit', webkit]]) await run(name, launcher);
} finally {
  server.close();
  console.log(JSON.stringify(results, null, 1));
}
assert.ok(results.length && results.every((r) => r.pass), 'some cases failed');
console.log('topics races: PASS');
