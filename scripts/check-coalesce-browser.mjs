// Production page -> controlled transport -> actual Go service/Manager ->
// production extension -> managed pi CLI/RPC (capability 4) -> local
// deterministic provider whose every call waits for its own gate.
//
// Later composer inputs join the newest still-pending message: one bubble,
// updated in its own element (inputs joined by newlines, in order), and pi
// takes the whole message once. Run with
// YESMAN_TEST_RUNTIME (prepared managed runtime) and Playwright.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, rm, writeFile, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { deflateSync } from 'node:zlib';
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || 'playwright');
const root = fileURLToPath(new URL('..', import.meta.url));
const out = process.env.YESMAN_COALESCE_BROWSER_EVIDENCE;
if (out) await mkdir(out, { recursive: true });

// Distinct small PNGs, so order and identity are observable.
const crcTable = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0; });
const crc = (buf) => { let c = 0xffffffff; for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
const chunk = (type, data) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const td = Buffer.concat([Buffer.from(type), data]); const c = Buffer.alloc(4); c.writeUInt32BE(crc(td)); return Buffer.concat([len, td, c]); };
function png(r, g, b, w = 64, hgt = 40) {
  const header = Buffer.alloc(13); header.writeUInt32BE(w, 0); header.writeUInt32BE(hgt, 4); header[8] = 8; header[9] = 2;
  const row = Buffer.concat([Buffer.from([0]), Buffer.from(Array.from({ length: w }, () => [r, g, b]).flat())]);
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', header), chunk('IDAT', deflateSync(Buffer.concat(Array(hgt).fill(row)))), chunk('IEND', Buffer.alloc(0))]);
}
const images = [png(220, 40, 40), png(40, 180, 60), png(40, 80, 220), png(200, 180, 30)];
const b64 = images.map((b) => b.toString('base64'));
const dataURL = (i) => `data:image/png;base64,${b64[i]}`;

const http = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    res.setHeader('Content-Type', path.endsWith('.js') ? 'application/javascript' : path.endsWith('.css') ? 'text/css' : path.endsWith('.woff2') ? 'font/woff2' : 'text/html');
    res.end(await readFile(`${root}/web${path === '/' ? '/index.html' : path}`));
  } catch { res.writeHead(404).end(); }
}).listen(0, '127.0.0.1');
await new Promise((r) => http.on('listening', r));
const reports = [];
const engines = Object.entries({ chromium, webkit }).filter(([name]) => !process.env.YESMAN_TEST_BROWSERS || process.env.YESMAN_TEST_BROWSERS.split(',').includes(name));
assert(engines.length, 'No browser engine selected');
const languages = (process.env.YESMAN_TEST_LANGUAGES ?? 'en,zh-Hans').split(',');
const L = (language, en, zh) => (language === 'en' ? en : zh);

try {
  for (const [name, engine] of engines) for (const language of languages) {
    // English: wide and light. Chinese: narrow, dark, with reduced motion asked.
    const reduced = language !== 'en';
    const viewport = reduced ? { width: 480, height: 740 } : { width: 1000, height: 760 };
    const temp = await mkdtemp(`${tmpdir()}/coalesce-browser-`), ready = `${temp}/ready`;
    const executable = process.env.YESMAN_TEST_BRIDGE_BIN || 'go';
    const args = process.env.YESMAN_TEST_BRIDGE_BIN ? ['-test.run=^TestCoalesceBrowserBridge$', '-test.timeout=170s'] : ['test', '-run', '^TestCoalesceBrowserBridge$', '-count=1', '-timeout=170s', '.'];
    const fixture = spawn(executable, args, { cwd: root, env: { ...process.env, YESMAN_COALESCE_BRIDGE: ready }, stdio: ['ignore', 'pipe', 'pipe'] });
    let log = '', exit;
    fixture.stdout.on('data', (d) => (log += d)); fixture.stderr.on('data', (d) => (log += d)); fixture.on('exit', (c) => (exit = c));
    const deadline = Date.now() + 120000;
    let api;
    while (!api) {
      try { api = await readFile(ready, 'utf8'); } catch {
        if (exit !== undefined || Date.now() > deadline) { fixture.kill('SIGTERM'); await rm(temp, { recursive: true, force: true }); throw Error(`bridge startup: ${log}`); }
        await new Promise((r) => setTimeout(r, 20));
      }
    }
    const control = async (method, ...args) => { const res = await (await fetch(api, { method: 'POST', body: JSON.stringify({ method, args }) })).json(); if (res.error) throw Error(res.error); return res.value; };
    const browser = await engine.launch({ headless: true });
    let activePage;
    const report = { engine: name, language, reducedMotion: reduced, viewport };
    try {
      const page = activePage = await browser.newPage({ viewport, reducedMotion: reduced ? 'reduce' : 'no-preference', colorScheme: reduced ? 'dark' : 'light' });
      const errors = [];
      page.on('pageerror', (e) => errors.push(String(e)));
      await page.addInitScript(({ api, language }) => {
        localStorage.setItem('yesman.info', 'hidden');
        window.events = {}; window.sent = []; window.copied = []; window.grows = [];
        const animate = Element.prototype.animate;
        Element.prototype.animate = function (keyframes, options) {
          if (this.classList?.contains('msg-user')) window.grows.push({ duration: typeof options === 'number' ? options : options?.duration });
          return animate.call(this, keyframes, options);
        };
        window.mygo = { locale: language, on: (n, fn) => (window.events[n] = fn), call: async (method, ...args) => {
          window.sent.push({ method, args });
          if (method === 'Yesman.CopyValue') { window.copied.push(args[0]); return; }
          const result = await (await fetch(api, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ method, args }) })).json();
          if (result.error) throw Error(result.error);
          return result.value;
        } };
        let cursor = 0, agents = '', stopped = false, timer, inFlight = Promise.resolve();
        const pollErrors = [];
        const poll = () => {
          if (stopped) return;
          inFlight = (async () => {
            try {
              const response = await fetch(`${api}/events?cursor=${cursor}`);
              if (!response.ok) throw Error(`event poll HTTP ${response.status}`);
              const r = await response.json(); cursor = r.cursor;
              for (const e of r.events ?? []) window.events['agent-event']?.(e);
              const s = JSON.stringify(r.agents); if (s !== agents) { agents = s; window.events.agents?.(r.agents); }
            } catch (error) { pollErrors.push(String(error)); } finally { if (!stopped) timer = setTimeout(poll, 15); }
          })();
        };
        window.stopFixturePolling = async () => { stopped = true; clearTimeout(timer); await inFlight; return pollErrors; };
        window.addEventListener('pagehide', () => { stopped = true; clearTimeout(timer); });
        timer = setTimeout(poll, 30);
      }, { api, language });
      await page.goto(`http://127.0.0.1:${http.address().port}`);
      if (reduced) await page.evaluate(() => (document.documentElement.dataset.theme = 'dark'));
      const input = page.locator('.composer textarea');
      await input.waitFor();
      const stopPolling = async () => assert.deepEqual(await page.evaluate(() => window.stopFixturePolling()), [], 'Unexpected bridge poll failure');
      const reload = async () => { await stopPolling(); await page.reload(); await input.waitFor(); if (reduced) await page.evaluate(() => (document.documentElement.dataset.theme = 'dark')); };
      const submits = () => page.evaluate(() => window.sent.filter((s) => /^Yesman\.Submit(Coalescing)?$/.test(s.method)));
      const attach = async (list) => {
        await page.locator('.composer input[type=file]').setInputFiles(list.map((i, n) => ({ name: `input-${n}.png`, mimeType: 'image/png', buffer: images[i] })));
        await page.locator('.composer-image').nth(list.length - 1).waitFor();
      };
      const idle = () => page.waitForFunction(() => !document.querySelector('.composer input[type=file]').disabled && !document.querySelector('.composer textarea').readOnly);
      const submit = async (text, list = [], { settle = true } = {}) => {
        await idle();
        await input.fill(text);
        if (list.length) await attach(list);
        const before = (await submits()).length;
        await input.press('Enter');
        await page.waitForFunction((n) => window.sent.filter((s) => /^Yesman\.Submit(Coalescing)?$/.test(s.method)).length > n, before);
        if (settle) await page.waitForFunction(() => !document.querySelector('.composer textarea').readOnly && document.querySelector('.composer textarea').value === '' && !document.querySelector('.composer-image'));
        return (await submits()).at(-1).args[1];
      };
      const row = (id) => page.locator(`#thread [data-delivery="${id}"]`);
      const bubble = (id) => page.evaluate((id) => {
        const el = document.querySelector(`#thread [data-delivery="${id}"]`);
        const b = el?.querySelector('.msg-user');
        return b && { text: [...b.childNodes].filter((n) => !n.classList?.contains('message-images')).map((n) => n.textContent).join(''),
          images: [...b.querySelectorAll('.message-images img')].map((img) => img.getAttribute('src')),
          status: el.querySelector('.delivery-status')?.textContent, state: el.querySelector('.delivery-status')?.dataset.state,
          preparing: el.querySelector('.delivery-status')?.dataset.preparing === 'true', original: el.dataset.original === 'true',
          recall: !!el.querySelector('.message-recall') };
      }, id);
      const pendingRows = () => page.evaluate(() => [...document.querySelectorAll('#thread > .msg-delivery:not([data-bound])')].map((el) => el.dataset.delivery));
      const audit = async () => (await control('Audit')).map((call) => ({ call: call.call, users: call.users.map((m) => ({
        text: (Array.isArray(m.content) ? m.content : [{ type: 'text', text: m.content }]).filter((b) => b.type === 'text').map((b) => b.text).join(''),
        images: (Array.isArray(m.content) ? m.content : []).filter((b) => b.type === 'image').map((b) => b.data), timestamp: m.timestamp })) }));
      const introductions = (calls, text) => { const seen = new Map(); for (const c of calls) for (const m of c.users) if (m.text === text) seen.set(m.timestamp, m); return [...seen.values()]; };
      const since = async () => (await submits()).at(-1).args[4];

      // 1. A, then B and image-only C join it: one bubble, the same element.
      const aText = '  alpha 原文\n  keep  spacing  ', bText = 'beta 补充\n第二行', abText = `${aText}\n${bText}`;
      const idA = await submit(aText, [0]);
      await page.waitForFunction((id) => document.querySelector(`#thread [data-delivery="${id}"] .delivery-status`)?.dataset.state === 'steering', idA);
      await page.evaluate((id) => (window.rowA = document.querySelector(`#thread [data-delivery="${id}"]`)), idA);
      const idB = await submit(bText, [1, 2]);
      await page.waitForFunction(({ idA, idB, abText }) => {
        const b = document.querySelector(`#thread [data-delivery="${idA}"] .msg-user`);
        return !document.querySelector(`#thread [data-delivery="${idB}"]`) && b && [...b.childNodes].filter((n) => !n.classList?.contains('message-images')).map((n) => n.textContent).join('') === abText;
      }, { idA, idB, abText });
      let a = await bubble(idA);
      assert.deepEqual(a.images, [dataURL(0), dataURL(1), dataURL(2)]);
      assert.equal(await page.evaluate((id) => window.rowA === document.querySelector(`#thread [data-delivery="${id}"]`) && window.rowA.isConnected, idA), true, 'the bubble was rebuilt');
      const idC = await submit('', [3]);
      await page.waitForFunction(({ idA, idC }) => !document.querySelector(`#thread [data-delivery="${idC}"]`) && document.querySelectorAll(`#thread [data-delivery="${idA}"] .message-images img`).length === 4, { idA, idC });
      a = await bubble(idA);
      assert.equal(a.text, abText, 'an image-only input adds no text or separator');
      assert.deepEqual(a.images, [0, 1, 2, 3].map(dataURL));
      assert.deepEqual(await pendingRows(), [idA], 'one bubble for the whole pending message');
      assert.equal(await page.evaluate(() => document.activeElement?.matches('.composer textarea')), true, 'composer focus kept');
      assert.deepEqual(await page.evaluate(() => window.grows), [], 'the bubble updates in place, without animation');
      if (out) await page.screenshot({ path: `${out}/${name}-${language}-merged.png` });

      // 2. Copy, a stale snapshot of A and a duplicate B leave the whole message.
      await row(idA).locator('.message-footer .message-copy:not(.message-recall):not(.message-promote)').click();
      await page.waitForFunction(() => window.copied.length === 1);
      assert.equal(await page.evaluate(() => window.copied[0]), abText);
      const agentSince = await since();
      await page.evaluate(({ idA, since }) => window.events['agent-event']({ agent: 'alice/lead', agentSince: since, seq: 1e9,
        data: { type: 'delivery_update', delivery: { id: idA, agentSince: since, revision: 1, state: 'steering', text: 'stale', imageCount: 1, nativePending: true, canCancel: true, originalPayload: true } } }), { idA, since: agentSince });
      await input.fill('unsent draft');
      const retried = await control('RetrySubmit', 'alice/lead', idB, bText, [1, 2].map((i) => ({ type: 'image', mimeType: 'image/png', data: b64[i] })), agentSince);
      assert.equal(retried.mergedInto, idA, 'a duplicate B answers with its receipt');
      await page.waitForTimeout(150);
      a = await bubble(idA);
      assert.equal(a.text, abText); assert.equal(a.images.length, 4);
      assert.equal(await input.inputValue(), 'unsent draft', 'the draft is untouched');
      await input.fill('');

      // 3. A lost bridge answer: the page keeps the draft, the receipt still
      //    joins it, and the unchanged retry reuses its ID.
      const dText = 'delta 丢失回执';
      await control('DropNextReply');
      await idle();
      await input.fill(dText);
      const beforeLost = (await submits()).length;
      await input.press('Enter');
      await page.waitForFunction((n) => window.sent.filter((s) => /^Yesman\.Submit(Coalescing)?$/.test(s.method)).length > n && !document.querySelector('.composer textarea').readOnly, beforeLost);
      assert.equal(await input.inputValue(), dText, 'the draft stays after a lost answer');
      const idD = (await submits()).at(-1).args[1];
      const abdText = `${abText}\n${dText}`;
      await page.waitForFunction(({ idA, idD, text }) => !document.querySelector(`#thread [data-delivery="${idD}"]`) &&
        [...(document.querySelector(`#thread [data-delivery="${idA}"] .msg-user`)?.childNodes ?? [])].filter((n) => !n.classList?.contains('message-images')).map((n) => n.textContent).join('') === text, { idA, idD, text: abdText });
      await input.press('Enter');
      await page.waitForFunction(() => document.querySelector('.composer textarea').value === '' && !document.querySelector('.composer textarea').readOnly);
      const dCalls = (await submits()).filter((s) => s.args[1] === idD);
      assert.equal(dCalls.length, 2, 'the retry reused the same ID');
      assert.deepEqual(await pendingRows(), [idA]);

      // 4. Reload: this window no longer holds the inputs; the bubble shows the
      //    Manager's whole message, once, in order.
      await reload();
      await page.waitForFunction((id) => document.querySelector(`#thread [data-delivery="${id}"]`)?.dataset.original === 'true', idA);
      a = await bubble(idA);
      assert.equal(a.text, abdText);
      assert.deepEqual(a.images, [0, 1, 2, 3].map(dataURL));
      assert.equal(a.state, 'steering');
      assert.deepEqual(await pendingRows(), [idA]);
      if (out) await page.screenshot({ path: `${out}/${name}-${language}-reloaded.png` });

      // 5. ArrowUp recalls the whole message: every input's text and image,
      //    one cancellation; an edit is a fresh intent of its own.
      await input.focus();
      await input.press('ArrowUp');
      await page.waitForFunction((text) => document.querySelector('.composer textarea').value === text && document.querySelectorAll('.composer-image').length === 4, abdText);
      assert.deepEqual(await page.locator('.composer-image img').evaluateAll((list) => list.map((img) => img.getAttribute('src'))), [0, 1, 2, 3].map(dataURL));
      await page.waitForFunction(() => !document.querySelector('#thread > .msg-delivery:not([data-bound])'));
      for (const id of [idB, idC, idD]) assert.equal(await row(id).count(), 0);
      const eText = `${abdText} EDITED`;
      await input.fill(eText);
      await input.press('Enter');
      await page.waitForFunction(() => document.querySelector('.composer textarea').value === '' && !document.querySelector('.composer-image') && !document.querySelector('.composer textarea').readOnly);
      const idE = (await submits()).at(-1).args[1];
      assert(![idA, idB, idC, idD].includes(idE), 'an edited recall is a new intent');
      await page.waitForFunction((id) => document.querySelector(`#thread [data-delivery="${id}"] .delivery-status`)?.dataset.state === 'steering', idE);

      // 6. Eleven more inputs join E: more than this window keeps (8), and the
      //    bubble still holds the complete message. Reading above stays put.
      const fTexts = Array.from({ length: 11 }, (_, i) => `follow ${i + 1}\n  line two of ${i + 1}\nline three`);
      const fIDs = [];
      let scrollChecked = false;
      for (const [i, text] of fTexts.entries()) {
        if (i === 9) {
          const scrollable = await page.evaluate(() => { const s = document.getElementById('scroll'); return s.scrollHeight > s.clientHeight + 100; });
          if (scrollable) {
            await page.evaluate(() => { const s = document.getElementById('scroll'); s.scrollTop = 0; s.dispatchEvent(new Event('scroll')); });
            scrollChecked = true;
          }
        }
        fIDs.push(await submit(text));
        if (scrollChecked && i >= 9) {
          await page.waitForFunction((id) => !document.querySelector(`#thread [data-delivery="${id}"]`), fIDs.at(-1));
          await page.waitForTimeout(300);
          assert.equal(await page.evaluate(() => document.getElementById('scroll').scrollTop), 0, 'a merge pulled the reader down');
        }
      }
      report.scrollAnchor = scrollChecked ? 'kept at top while two inputs joined' : 'thread not scrollable at this size';
      const eWhole = [eText, ...fTexts].join('\n');
      await page.waitForFunction(({ id, text }) => [...(document.querySelector(`#thread [data-delivery="${id}"] .msg-user`)?.childNodes ?? [])].filter((n) => !n.classList?.contains('message-images')).map((n) => n.textContent).join('') === text, { id: idE, text: eWhole });
      assert.deepEqual(await pendingRows(), [idE]);
      assert.deepEqual((await bubble(idE)).images, [0, 1, 2, 3].map(dataURL));
      await row(idE).locator('.message-footer .message-copy:not(.message-recall):not(.message-promote)').click();
      // (the reload above started a new page: this is its first copy)
      await page.waitForFunction(() => window.copied.length === 1);
      assert.equal(await page.evaluate(() => window.copied[0]), eWhole);
      await page.evaluate(() => { const s = document.getElementById('scroll'); s.scrollTop = s.scrollHeight; });

      // 7. pi takes the message at its checkpoint: one provider introduction,
      //    one canonical bubble, no input left as a receipt row.
      await control('ReleaseCall', '1');
      let calls;
      const waitAudit = async (what, ok) => { const end = Date.now() + 20000; for (;;) { calls = await audit(); if (ok(calls)) return; if (Date.now() > end) throw Error(`provider never ${what}: ${JSON.stringify(calls.map((c) => c.users.map((u) => u.text)))}`); await new Promise((r) => setTimeout(r, 20)); } };
      await waitAudit('received the merged message', (cs) => introductions(cs, eWhole).length > 0);
      const merged = introductions(calls, eWhole);
      assert.equal(merged.length, 1, 'pi took the merged message once');
      assert.deepEqual(merged[0].images, b64, 'all images once, in order');
      await page.waitForFunction((text) => [...document.querySelectorAll('#thread > .msg-delivery[data-bound="true"] .msg-user')].some((b) => b.textContent === text), eWhole);
      assert.equal(await page.locator('#thread > .msg-delivery[data-bound="true"]').evaluateAll((list, text) => list.filter((el) => el.querySelector('.msg-user').textContent === text).length, eWhole), 1);
      for (const id of fIDs) assert.equal(await page.locator(`[data-delivery="${id}"]`).count(), 0);
      assert.deepEqual(await pendingRows(), []);

      // 8. After pi took it, a new input is its own message; the next joins that.
      const gText = 'gamma after take', hText = 'eta joins gamma';
      const idG = await submit(gText);
      await page.waitForFunction((id) => document.querySelector(`#thread [data-delivery="${id}"] .delivery-status`)?.dataset.state === 'steering', idG);
      const idH = await submit(hText);
      await page.waitForFunction(({ idG, idH }) => !document.querySelector(`#thread [data-delivery="${idH}"]`) && document.querySelector(`#thread [data-delivery="${idG}"] .msg-user`)?.textContent.includes('eta joins gamma'), { idG, idH });
      assert.deepEqual(await pendingRows(), [idG]);

      // 9. Stop keeps the pending message, whole and recallable. The next input
      //    starts a new turn: while that turn is being prepared the message is
      //    still pending, never shown as taken, and pi takes it once.
      await page.locator('.composer .round--stop').click();
      await page.waitForFunction(() => !document.querySelector('.composer .round--stop'));
      assert.equal((await bubble(idG)).state, 'steering');
      await control('HoldContext');
      const kText = 'kappa starts the next turn';
      const idK = await submit(kText, [], { settle: false });
      { const end = Date.now() + 10000; while (!(await control('Counts')).contextHeld) { assert(Date.now() < end, 'production context gate not entered'); await new Promise((r) => setTimeout(r, 10)); } }
      const g = await bubble(idG);
      assert.equal(g.text, `${gText}\n${hText}`);
      assert.equal(g.state, 'steering', 'still pending while the next turn is prepared');
      assert.equal(g.recall, true);
      assert.deepEqual(await pendingRows(), [idG, idK]);
      if (out) await page.screenshot({ path: `${out}/${name}-${language}-stopped.png` });
      await control('ReleaseContext');
      await control('ReleaseCall', '2');
      await control('ReleaseCall', '3');
      const ghText = `${gText}\n${hText}`;
      await waitAudit('received the claimed message and the new turn', (cs) => introductions(cs, ghText).length > 0 && introductions(cs, kText).length > 0);
      await page.waitForFunction(() => !document.querySelector('.composer textarea').readOnly);
      await page.waitForTimeout(300);
      calls = await audit();
      for (const [text, n] of [[eWhole, 1], [ghText, 1], [kText, 1], [gText, 0], [hText, 0], [aText, 0], [bText, 0], [dText, 0], [abdText, 0], ...fTexts.map((t) => [t, 0])]) {
        assert.equal(introductions(calls, text).length, n, `provider introductions of ${JSON.stringify(text.slice(0, 30))}`);
      }
      const raw = JSON.stringify(await control('Audit'));
      for (const id of [idA, idB, idC, idD, idE, idG, idH, idK, ...fIDs]) assert(!raw.includes(id), 'a private submission ID reached the provider');
      await page.waitForFunction((text) => [...document.querySelectorAll('#thread > .msg-delivery[data-bound="true"] .msg-user')].some((b) => b.textContent === text), ghText);
      assert.equal(await row(idH).count(), 0);
      if (out) await page.screenshot({ path: `${out}/${name}-${language}-final.png`, fullPage: true });
      assert.deepEqual([...new Set((await submits()).map((s) => s.method))], ['Yesman.SubmitCoalescing'], 'the composer sends through SubmitCoalescing');
      const counts = await control('Counts');
      await stopPolling();
      assert.deepEqual(errors, []);
      Object.assign(report, { counts, providerCalls: calls.length, mergedImages: 'exact, in order', copy: 'whole message', recall: 'whole message, one cancel', afterStop: 'pending, whole, recallable while the next turn is prepared', afterTake: 'own bubble' });
      reports.push(report);
    } finally {
      if (activePage && !activePage.isClosed()) await activePage.evaluate(() => window.stopFixturePolling?.()).catch(() => {});
      await browser.close();
      await fetch(`${api}/stop`).catch(() => {});
      await new Promise((r) => (exit !== undefined ? r() : fixture.once('exit', r)));
      if (out) await writeFile(`${out}/${name}-${language}-bridge.log`, log);
      await rm(temp, { recursive: true, force: true });
    }
    assert.equal(exit, 0, log);
  }
} finally {
  await new Promise((r) => http.close(r));
}
if (out) await writeFile(`${out}/results.json`, JSON.stringify(reports, null, 2) + '\n');
console.log(JSON.stringify(reports, null, 2));
