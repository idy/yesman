// Production DOM + controlled MyGo bridge. No OS clipboard or real agent is used.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || 'playwright');
const root = fileURLToPath(new URL('../web', import.meta.url));
const out = process.env.YESMAN_ACTIONS_EVIDENCE;
if (out) await mkdir(out, { recursive: true });
const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    res.setHeader('Content-Type', path.endsWith('.js') ? 'application/javascript' : path.endsWith('.css') ? 'text/css' : path.endsWith('.woff2') ? 'font/woff2' : 'text/html');
    res.end(await readFile(root + (path === '/' ? '/index.html' : path)));
  } catch { res.writeHead(404).end(); }
}).listen(0, '127.0.0.1');
await new Promise((resolve) => server.on('listening', resolve));
const results = [];
try {
  for (const [engineName, engine] of Object.entries({ chromium, webkit })) {
    const browser = await engine.launch({ headless: true });
    try {
      for (const language of ['en', 'zh-Hans']) {
        const page = await browser.newPage({ viewport: { width: 900, height: 700 } });
        const errors = [];
        page.on('pageerror', (error) => errors.push(String(error)));
        await page.route('**/*', (route) => route.request().url().startsWith(`http://127.0.0.1:${server.address().port}`) ? route.continue() : route.abort());
        await page.addInitScript((language) => {
          localStorage.setItem('yesman.info', 'hidden');
          const image = { type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==' };
          const raw = '  **literal** <img onerror="bad()">\n' + '完整消息 '.repeat(300) + '\nend  ';
          const agents = ['lead', 'worker'].map((name) => ({ id: `alice/${name}`, name, yesman: 'alice', role: name, since: '1', status: 'working', model: 'm', provider: 'p', effort: 'high', effortLevels: ['high'] }));
          const messages = [
            { role: 'user', timestamp: 1, content: [{ type: 'text', text: raw }, image, image] },
            { role: 'user', timestamp: 2, content: [image, image] },
            { role: 'user', timestamp: 3, content: '[yesman] Worker "w" finished a run. Its final message:\n\nInternal report' },
          ];
          const deliveries = [
            { id: 'short', revision: 1, order: 1, state: 'steering', text: 'Full receipt', imageCount: 1 },
            { id: 'preview', revision: 1, order: 2, state: 'queued', text: 'truncated', imageCount: 0, preview: true },
            { id: 'image', revision: 1, order: 3, state: 'accepted', text: '', imageCount: 2 },
          ];
          window.events = {};
          window.fixture = { raw, messages, deliveries, copied: [], calls: [], fail: false, hold: false, release: null, seq: 0 };
          window.mygo = { locale: language, on: (name, fn) => { window.events[name] = fn; }, call: async (method, ...args) => {
            window.fixture.calls.push({ method, args });
            switch (method.replace('Yesman.', '')) {
              case 'State': return { yesmen: [{ name: 'alice' }], agents };
              case 'Repos': case 'Clones': return [];
              case 'Messages': return structuredClone({ messages: args[0] === 'alice/lead' ? messages : [{ role: 'user', timestamp: 4, content: 'Worker text' }], deliveries: args[0] === 'alice/lead' ? deliveries : [], seq: 0, queue: { known: true, steering: 1, followUp: 1 } });
              case 'Models': return { agent: agents.find((a) => a.id === args[0]), models: [{ id: 'm', provider: 'p', name: 'Model' }] };
              case 'CopyValue':
                window.fixture.copied.push(args[0]);
                if (window.fixture.hold) await new Promise((resolve) => { window.fixture.release = resolve; });
                if (window.fixture.fail) throw new Error('clipboard unavailable');
                return;
              case 'TakeFocus': return '';
              default: return {};
            }
          } };
        }, language);
        await page.goto(`http://127.0.0.1:${server.address().port}`);
        await page.locator('#thread > .msg-user').first().waitFor();
        const userCopy = page.locator('#thread > .msg-user .message-copy');
        assert.equal(await userCopy.count(), 2);
        assert.equal(await userCopy.nth(1).isDisabled(), true);
        assert.equal(await page.locator('#thread > .note .message-actions').count(), 0);
        assert.equal(await page.locator('#thread .message-actions button').count(), 5);
        assert.equal(await page.locator('#thread .message-actions button:not(.message-copy)').count(), 0);
        await page.locator('.composer textarea').fill('keep this draft\nand caret');
        await userCopy.first().click();
        await page.waitForFunction(() => window.fixture.copied.length === 1);
        assert.equal(await page.evaluate(() => window.fixture.copied[0] === window.fixture.raw), true);
        assert.equal(await page.locator('.composer textarea').inputValue(), 'keep this draft\nand caret');
        assert.equal(await page.locator('[data-delivery="preview"] .message-copy').isDisabled(), true);
        assert.equal(await page.locator('[data-delivery="image"] .message-copy').isDisabled(), true);
        await page.locator('[data-delivery="short"] .message-copy').click();
        assert.equal(await page.evaluate(() => window.fixture.copied.at(-1)), 'Full receipt');
        // Preview replaced by an authoritative complete receipt of the same state.
        await page.evaluate(() => {
          const d = window.fixture.deliveries[1];
          Object.assign(d, { preview: false, text: 'now complete', revision: 2 });
          window.events['agent-event']({ agent: 'alice/lead', seq: ++window.fixture.seq, data: { type: 'delivery_update', delivery: d } });
        });
        await page.locator('[data-delivery="preview"] .message-copy').click();
        assert.equal(await page.evaluate(() => window.fixture.copied.at(-1)), 'now complete');
        // Pending double activation makes only one clipboard call and retains focus.
        await page.evaluate(() => { window.fixture.hold = true; });
        await userCopy.first().focus();
        await userCopy.first().press('Enter');
        await userCopy.first().press('Enter');
        assert.equal(await page.evaluate(() => window.fixture.copied.length), 4);
        assert.equal(await userCopy.first().getAttribute('aria-busy'), 'true');
        await page.locator('[data-agent="alice/worker"]').first().click();
        await page.locator('.composer textarea').fill('worker draft');
        await page.evaluate(() => { window.fixture.release(); window.fixture.hold = false; });
        assert.equal(await page.locator('.composer textarea').inputValue(), 'worker draft');
        await page.locator('#thread > .msg-user .message-copy').click();
        assert.equal(await page.evaluate(() => window.fixture.copied.at(-1)), 'Worker text');
        await page.locator('[data-agent="alice/lead"]').first().click();
        await page.evaluate(() => { window.fixture.fail = true; });
        await userCopy.first().click();
        await page.getByText('clipboard unavailable', { exact: true }).waitFor();
        await page.evaluate(() => { window.fixture.fail = false; });
        await userCopy.first().click();
        assert.equal(await userCopy.first().getAttribute('aria-busy'), null);
        // ArrowUp must remain ordinary composer input until real cancellation exists.
        await page.locator('.composer textarea').fill('');
        await page.locator('.composer textarea').press('ArrowUp');
        assert.equal(await page.evaluate(() => window.fixture.calls.some((c) => /Cancel|Abort|Submit|Clear/.test(c.method))), false);
        // New native messages and refreshed history receive the same copy actions.
        await page.evaluate(() => window.events['agent-event']({ agent: 'alice/lead', seq: ++window.fixture.seq, data: { type: 'message_end', message: { role: 'user', timestamp: 9, content: [{ type: 'text', text: 'live one' }, { type: 'text', text: 'live two' }] } } }));
        await page.locator('#thread > .msg-user').last().locator('.message-copy').click();
        assert.equal(await page.evaluate(() => window.fixture.copied.at(-1)), 'live one\nlive two');
        await page.reload();
        await page.locator('#thread > .msg-user').first().waitFor();
        assert.equal(await page.locator('[data-delivery="preview"] .message-copy').isDisabled(), true);
        for (const width of [900, 480]) for (const theme of ['light', 'dark']) {
          await page.setViewportSize({ width, height: 700 });
          await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; document.documentElement.dataset.appearance = theme; }, theme);
          const action = page.locator('[data-delivery="short"] .message-copy');
          await action.scrollIntoViewIfNeeded();
          await action.focus();
          const dimensions = await action.evaluate((el) => {
            const r = el.getBoundingClientRect();
            return { x: r.x, right: r.right, width: r.width, height: r.height, outline: getComputedStyle(el).outlineWidth, font: getComputedStyle(el.firstChild).fontFamily };
          });
          assert(dimensions.x >= 0 && dimensions.right <= width);
          assert(dimensions.width >= 28 && dimensions.height >= 28);
          assert.match(dimensions.font, /Phosphor/);
          if (out) await page.screenshot({ path: `${out}/${engineName}-${language}-${width}-${theme}.png` });
          results.push({ engine: engineName, language, width, theme, dimensions });
        }
        assert.deepEqual(errors, []);
        await page.close();
      }
    } finally { await browser.close(); }
  }
} finally { await new Promise((resolve) => server.close(resolve)); }
if (out) await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2) + '\n');
console.log(`PASS: ${results.length} message-action layouts plus exact text, receipts, images, failure/retry, keyboard, refresh and agent isolation in Chromium/WebKit (controlled bridge).`);
