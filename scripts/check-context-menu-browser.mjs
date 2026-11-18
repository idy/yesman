// The page's context menu, on the production DOM with a controlled MyGo
// bridge in Chromium and WebKit: real right-clicks on text, Markdown, links,
// code, fields, dialogs and empty space are prevented, so the web view shows
// no menu of its own, and open one app menu. A Control-click on macOS opens
// the menu only (no link opened, no draft sent); the editor's Find box is a
// field of its own; Paste hands clipboard images to the composer. No OS clipboard, store, draft
// or agent is used: Yesman.PageMenu answers from the fixture, and CopyValue
// records what would reach the clipboard bridge.
//
//   YESMAN_TEST_PLAYWRIGHT_MODULE  playwright's index.mjs (default: playwright)
//   YESMAN_CONTEXT_MENU_WEB        the page to load (default: ../web)
//   YESMAN_CONTEXT_MENU_PROBE=1    only measure: for the page before the fix
//   YESMAN_CONTEXT_MENU_EVIDENCE   a directory for screenshots and results.json
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || 'playwright');
const root = process.env.YESMAN_CONTEXT_MENU_WEB || fileURLToPath(new URL('../web', import.meta.url));
const probeOnly = process.env.YESMAN_CONTEXT_MENU_PROBE === '1';
const out = process.env.YESMAN_CONTEXT_MENU_EVIDENCE;
if (out) await mkdir(out, { recursive: true });

const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    res.setHeader('Content-Type', path.endsWith('.js') ? 'application/javascript' : path.endsWith('.css') ? 'text/css' : path.endsWith('.woff2') ? 'font/woff2' : 'text/html');
    res.end(await readFile(root + (path === '/' ? '/index.html' : path)));
  } catch { res.writeHead(404).end(); }
}).listen(0, '127.0.0.1');
await new Promise((resolve) => server.on('listening', resolve));

const REPLY = [
  '定时任务每天运行。The schedule runs daily.',
  '',
  'Second paragraph line.',
  '',
  'See [the docs](https://example.com/docs) and [notes](/tmp/yesman-fixture/notes.md).',
  '',
  '```html',
  '<img src=x onerror="bad()"> & <b>not bold</b>',
  '```',
].join('\n');

const MENUS = ['PageMenu', 'AgentMenu', 'FileMenu'];
const results = [];

try {
  for (const [engineName, engine] of Object.entries({ chromium, webkit })) {
    const browser = await engine.launch({ headless: true });
    try {
      for (const language of ['en', 'zh-Hans']) {
        const page = await browser.newPage({ viewport: { width: 1100, height: 760 } });
        const errors = [];
        page.on('pageerror', (error) => errors.push(String(error.stack || error)));
        await page.route('**/*', (route) => route.request().url().startsWith(`http://127.0.0.1:${server.address().port}`) ? route.continue() : route.abort());
        await page.addInitScript(({ language, reply }) => {
          localStorage.setItem('yesman.info', 'hidden');
          const agents = ['lead', 'worker'].map((name) => ({ id: `alice/${name}`, name, yesman: 'alice', role: name, since: '1', status: 'idle', model: 'm', provider: 'p', effort: 'high', effortLevels: ['high'], title: name === 'worker' ? 'Fixture work' : '' }));
          window.bad = () => { window.fixture.bad = true; };
          window.fixture = { calls: [], copied: [], next: [], events: [], bad: false };
          // Every contextmenu event, to read whether it ended up prevented.
          addEventListener('contextmenu', (e) => window.fixture.events.push(e), true);
          window.mygo = { platform: 'darwin', locale: language, on: () => {}, call: async (method, ...args) => {
            const name = method.replace('Yesman.', '');
            window.fixture.calls.push({ method: name, args: structuredClone(args) });
            switch (name) {
              case 'State': return { yesmen: [{ name: 'alice' }], agents };
              case 'Repos': case 'Clones': case 'Pulls': return [];
              case 'Info': return { knowledge: { notes: 0, index: false }, worktrees: [] };
              case 'Messages': return { messages: args[0] === 'alice/lead' ? [{ role: 'user', timestamp: 1, content: 'Show the schedule' }, { role: 'assistant', timestamp: 2, content: [{ type: 'text', text: reply }] }] : [], deliveries: [], seq: 0, queue: { known: true, steering: 0, followUp: 0 } };
              case 'Models': return { agent: agents.find((a) => a.id === args[0]), models: [{ id: 'm', provider: 'p', name: 'Model' }] };
              case 'PageMenu': case 'AgentMenu': case 'FileMenu': return window.fixture.next.shift() ?? (name === 'PageMenu' ? { action: '' } : '');
              case 'CopyValue': window.fixture.copied.push(args[0]); return null;
              case 'Crew': return { members: [], options: [] };
              case 'Schedules': return { plans: [] }; // the info panel's Tasks row: no scheduled tasks
              case 'TakeFocus': return '';
              default: return {};
            }
          } };
        }, { language, reply: REPLY });
        await page.goto(`http://127.0.0.1:${server.address().port}`);
        await page.locator('[data-agent="alice/lead"]').first().click();
        await page.locator('#thread .msg-assistant .md').first().waitFor();

        const mark = () => page.evaluate(() => ({ calls: window.fixture.calls.length, events: window.fixture.events.length, copied: window.fixture.copied.length }));
        // What happened since m: whether each contextmenu event ended up
        // prevented, and the menus asked for.
        const since = async (m) => {
          await page.waitForTimeout(60);
          return page.evaluate((m) => ({
            prevented: window.fixture.events.slice(m.events).map((e) => e.defaultPrevented),
            menus: window.fixture.calls.slice(m.calls).filter((c) => ['PageMenu', 'AgentMenu', 'FileMenu'].includes(c.method)),
            copied: window.fixture.copied.slice(m.copied),
            opened: window.fixture.calls.slice(m.calls).filter((c) => c.method === 'OpenURL' || c.method === 'Reveal'),
            submits: window.fixture.calls.slice(m.calls).filter((c) => c.method === 'Submit' || c.method === 'SubmitCoalescing').length,
          }), m);
        };
        const next = (choice) => page.evaluate((c) => window.fixture.next.push(c), choice);
        // selectText selects needle in the first element of selector that has
        // it, as a user's drag does, and returns the middle of its first box.
        const selectText = (selector, needle, end) => page.evaluate(({ selector, needle, end }) => {
          const walker = document.createTreeWalker(document.querySelector(selector), NodeFilter.SHOW_TEXT);
          let start = null; let finish = null;
          for (let n = walker.nextNode(); n; n = walker.nextNode()) {
            if (!start && n.nodeValue.includes(needle)) start = [n, n.nodeValue.indexOf(needle)];
            if (start && end && n.nodeValue.includes(end)) { finish = [n, n.nodeValue.indexOf(end) + end.length]; break; }
          }
          if (!start) throw new Error(`no ${needle} in ${selector}`);
          finish ??= [start[0], start[1] + needle.length];
          const r = document.createRange();
          r.setStart(...start);
          r.setEnd(...finish);
          getSelection().removeAllRanges();
          getSelection().addRange(r);
          const box = r.getClientRects()[0];
          return { x: box.left + box.width / 2, y: box.top + box.height / 2 };
        }, { selector, needle, end });
        const rightClick = async (p) => page.mouse.click(p.x, p.y, { button: 'right' });
        const center = async (locator) => {
          const b = await locator.boundingBox();
          return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
        };
        const record = (name, r) => { results.push({ engine: engineName, language, case: name, prevented: r.prevented, menus: r.menus.map((m) => ({ method: m.method, target: m.args[2] ?? null })) }); return r; };

        // ---- Measured before and after the fix ----
        let m = await mark();
        await rightClick(await selectText('#thread .msg-assistant .md', '定时'));
        const zh = record('selected Chinese word', await since(m));
        m = await mark();
        await rightClick(await selectText('#thread .msg-assistant .md', 'schedule'));
        const en = record('selected English word', await since(m));
        m = await mark();
        const body = await page.locator('#body').boundingBox();
        await page.evaluate(() => getSelection().removeAllRanges());
        await rightClick({ x: body.x + body.width - 30, y: body.y + body.height - 140 });
        const blank = record('empty space', await since(m));
        if (probeOnly) {
          for (const r of [zh, en, blank]) assert.equal(r.prevented.length, 1);
          await page.close();
          continue;
        }

        // ---- A selection: prevented, one page menu, Copy copies it ----
        for (const r of [zh, en]) {
          assert.deepEqual(r.prevented, [true]);
          assert.equal(r.menus.length, 1);
          assert.equal(r.menus[0].method, 'PageMenu');
          assert.deepEqual(r.menus[0].args[2], { link: false, editable: false, readOnly: false, secret: false, selection: true, content: false, images: false });
        }
        assert.deepEqual(blank.prevented, [true]);
        assert.deepEqual(blank.menus.map((c) => [c.method, c.args[2].selection]), [['PageMenu', false]]);
        await next({ action: 'copy' });
        m = await mark();
        await rightClick(await selectText('#thread .msg-assistant .md', '定时'));
        assert.deepEqual((await since(m)).copied, ['定时']);
        // Across paragraphs, as plain text with its line breaks.
        await next({ action: 'copy' });
        m = await mark();
        await rightClick(await selectText('#thread .msg-assistant .md', 'The schedule', 'paragraph line.'));
        const lines = (await since(m)).copied[0];
        assert.match(lines, /^The schedule runs daily\.\n+Second paragraph line\.$/);
        // Nothing selected: Copy copies nothing.
        await next({ action: 'copy' });
        m = await mark();
        await page.evaluate(() => getSelection().removeAllRanges());
        await rightClick({ x: body.x + body.width - 30, y: body.y + body.height - 140 });
        assert.deepEqual((await since(m)).copied, []);

        // ---- Links: Open Link takes the page's own path, Copy Link copies it ----
        const docs = page.locator('#thread a[data-external]').first();
        await page.evaluate(() => getSelection().removeAllRanges());
        await next({ action: 'open-link' });
        m = await mark();
        await rightClick(await center(docs));
        let r = record('link', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => [c.method, c.args[2].link]), [['PageMenu', true]]);
        assert.deepEqual(r.opened.map((c) => [c.method, c.args[0]]), [['OpenURL', 'https://example.com/docs']]);
        await next({ action: 'copy-link' });
        m = await mark();
        await rightClick(await center(docs));
        assert.deepEqual((await since(m)).copied, ['https://example.com/docs']);
        await next({ action: 'copy-link' });
        m = await mark();
        await rightClick(await center(page.locator('#thread a[data-path]').first()));
        assert.deepEqual((await since(m)).copied, ['/tmp/yesman-fixture/notes.md']);

        // ---- Code with markup in it: escaped, prevented, copied as text ----
        await next({ action: 'copy' });
        m = await mark();
        await rightClick(await selectText('#thread .msg-assistant pre', '<img', 'bold</b>'));
        r = record('code block', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.equal(r.menus.length, 1);
        assert.deepEqual(r.copied, ['<img src=x onerror="bad()"> & <b>not bold</b>']);
        assert.equal(await page.evaluate(() => window.fixture.bad), false);
        assert.equal(await page.locator('#thread .msg-assistant img, #thread .msg-assistant pre b').count(), 0);

        // ---- Rows with menus of their own keep them: one menu, theirs ----
        await page.evaluate(() => getSelection().removeAllRanges());
        m = await mark();
        await rightClick(await center(page.locator('[data-agent="alice/worker"]').first()));
        r = record('agent row', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => [c.method, c.args[0]]), [['AgentMenu', 'alice/worker']]);
        // A handler that prevents without stopping, as the file tree's does.
        await page.evaluate(() => {
          const row = document.createElement('button');
          row.id = 'own-menu';
          row.textContent = 'Fixture file row';
          row.oncontextmenu = (e) => { e.preventDefault(); window.mygo.call('Yesman.FileMenu', 'alice', 'notes.md', false, e.clientX, e.clientY); };
          document.querySelector('#yesmen').append(row);
        });
        m = await mark();
        await rightClick(await center(page.locator('#own-menu')));
        r = record('own menu, prevented only', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => c.method), ['FileMenu']);

        // ---- Info, the current Needs You pane and the sidebar ----
        await page.locator('#bar-actions button[aria-pressed]').first().click();
        m = await mark();
        await rightClick(await center(page.locator('[data-info-entry="needs-you"]')));
        r = record('Info Needs You row', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu']);
        await page.locator('[data-info-entry="needs-you"]').focus();
        await page.keyboard.press('Enter');
        m = await mark();
        await rightClick(await center(page.locator('#needs-you .needs-you-empty')));
        r = record('Needs You pane', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu']);
        await page.locator('#needs-you [data-pane-control="close"]').click();
        await page.locator('#bar-actions button[aria-pressed]').first().click();
        for (const [name, locator] of [['sidebar foot', page.locator('#side-foot')]]) {
          m = await mark();
          await rightClick(await center(locator));
          r = record(name, await since(m));
          assert.deepEqual(r.prevented, [true], name);
          assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu'], name);
        }
        const side = await page.locator('#side').boundingBox();
        m = await mark();
        await rightClick({ x: side.x + side.width / 2, y: side.y + side.height - 120 });
        r = record('sidebar empty space', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.equal(r.menus.length, 1);

        // ---- The composer: the draft, its caret and selection stay ----
        const composer = page.locator('.composer textarea');
        const draft = 'keep this draft\nand caret';
        await composer.fill(draft);
        await composer.evaluate((el) => { el.focus(); el.setSelectionRange(4, 4); });
        m = await mark();
        const box = await composer.boundingBox();
        // On "draft", away from the caret, which WebKit on macOS would select.
        await rightClick({ x: box.x + 90, y: box.y + 14 });
        r = record('composer caret', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => c.args[2]), [{ link: false, editable: true, readOnly: false, secret: false, selection: false, content: true, images: true }]);
        assert.deepEqual(await composer.evaluate((el) => [el.value, el.selectionStart, el.selectionEnd, document.activeElement === el]), [draft, 4, 4, true]);
        // Paste goes where the caret is, as typing does.
        await next({ action: 'paste', text: ' X\nY' });
        await rightClick({ x: box.x + 90, y: box.y + 14 });
        await page.waitForTimeout(60);
        assert.deepEqual(await composer.evaluate((el) => [el.value, el.selectionStart]), ['keep X\nY this draft\nand caret', 8]);
        // Cut takes the selection to the clipboard's bridge, then out.
        await composer.evaluate((el) => el.setSelectionRange(0, 4));
        await next({ action: 'cut' });
        m = await mark();
        await rightClick({ x: box.x + 18, y: box.y + 14 });
        r = await since(m);
        assert.deepEqual(r.menus.map((c) => c.args[2].selection), [true]);
        assert.deepEqual(r.copied, ['keep']);
        assert.equal(await composer.inputValue(), ' X\nY this draft\nand caret');
        await next({ action: 'select-all' });
        await rightClick({ x: box.x + 90, y: box.y + 14 });
        await page.waitForTimeout(60);
        const length = (await composer.inputValue()).length;
        assert.deepEqual(await composer.evaluate((el) => [el.selectionStart, el.selectionEnd]), [0, length]);

        // ---- Pasted images go to the composer's own paste handling ----
        const red = 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==';
        await composer.fill('image draft');
        await composer.evaluate((el) => { el.focus(); el.setSelectionRange(5, 5); });
        await next({ action: 'paste', text: 'caption', images: [{ mimeType: 'image/png', data: red }, { mimeType: 'image/png', data: red }] });
        m = await mark();
        const one = await composer.boundingBox();
        await rightClick({ x: one.x + 18, y: one.y + one.height / 2 });
        r = record('composer image paste', await since(m));
        await page.locator('.composer-image img').nth(1).waitFor();
        assert.equal(r.menus[0].args[2].images, true);
        // As ⌘V: the images are attached; the draft and its caret stay.
        assert.equal(await page.locator('.composer-image img').count(), 2);
        assert.deepEqual(await page.locator('.composer textarea').evaluate((el) => [el.value, el.selectionStart]), ['image draft', 5]);
        await page.locator('.composer-image button').first().click();
        await page.locator('.composer-image button').first().click();

        // ---- A Control-click on macOS is the menu only ----
        await page.evaluate(() => getSelection().removeAllRanges());
        m = await mark();
        await page.locator('#thread a[data-external]').first().click({ modifiers: ['Control'] });
        r = record('Control-click link', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu']);
        assert.deepEqual(r.opened, []);
        await page.locator('.composer textarea').fill('ctrl draft');
        await page.locator('.composer textarea').evaluate((el) => el.setSelectionRange(2, 5));
        m = await mark();
        await page.locator('.composer .composer-action').click({ modifiers: ['Control'] });
        r = record('Control-click Send', await since(m));
        assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu']);
        assert.equal(r.submits, 0);
        assert.deepEqual(await page.locator('.composer textarea').evaluate((el) => [el.value, el.selectionStart, el.selectionEnd]), ['ctrl draft', 2, 5]);
        await page.locator('.composer textarea').fill('');

        // ---- Read-only, password and secret fields; editable content ----
        await page.evaluate(() => {
          const box = document.createElement('div');
          box.id = 'fields';
          box.innerHTML = '<input id="ro" readonly value="read only text"> <input id="pw" type="password" value="hunter2-secret">'
            + '<div class="env-cell-edit env-cell-edit--secret"><textarea id="sv">sk-fixture-value</textarea></div>'
            + '<div id="ce" contenteditable="true">editable words here</div>';
          document.querySelector('#body').prepend(box);
        });
        await page.locator('#ro').evaluate((el) => { el.focus(); el.setSelectionRange(0, 4); });
        await next({ action: 'cut' });
        m = await mark();
        await rightClick(await center(page.locator('#ro')));
        r = record('read-only input', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus[0].args[2], { link: false, editable: true, readOnly: true, secret: false, selection: true, content: true, images: false });
        // Cut is disabled, and the page cuts nothing even if asked.
        assert.equal(await page.locator('#ro').inputValue(), 'read only text');
        assert.deepEqual(r.copied, []);
        for (const id of ['pw', 'sv']) {
          await page.locator(`#${id}`).evaluate((el) => { el.focus(); el.select(); });
          await next({ action: 'copy' });
          m = await mark();
          await rightClick(await center(page.locator(`#${id}`)));
          r = record(`${id === 'pw' ? 'password' : 'secret value'}`, await since(m));
          assert.deepEqual(r.prevented, [true]);
          assert.equal(r.menus[0].args[2].secret, true);
          assert.deepEqual(r.copied, []);
        }
        assert.equal(await page.evaluate(() => JSON.stringify(window.fixture.calls).includes('hunter2') || JSON.stringify(window.fixture.calls).includes('sk-fixture')), false);
        await page.locator('#ce').evaluate((el) => {
          el.focus();
          const t = el.firstChild;
          getSelection().setBaseAndExtent(t, 0, t, 8);
        });
        await next({ action: 'copy' });
        m = await mark();
        await rightClick(await center(page.locator('#ce')));
        r = record('contenteditable', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.copied, ['editable']);

        // ---- The files pane's editor: its state, not the lines drawn ----
        await page.evaluate(async () => {
          const { createEditor, editorViewOf } = await import('/editor.js');
          window.editorChanges = 0;
          const ed = await createEditor({ onChange() { window.editorChanges++; }, onSave() {} });
          const host = document.createElement('div');
          host.id = 'cm-host';
          host.style.cssText = 'height: 200px; display: flex; flex-direction: column';
          host.append(ed.dom);
          document.querySelector('#body').prepend(host);
          ed.show('alice\nnotes.txt', { text: 'alpha line\nbeta line\ngamma line', lang: '', readOnly: false });
          window.fixtureEditor = { ed, view: editorViewOf(ed.dom.querySelector('.cm-content')) };
        });
        const cmLine = (n) => page.locator('#cm-host .cm-line').nth(n);
        const cmState = () => page.evaluate(() => { const { view } = window.fixtureEditor; const s = view.state.selection.main; return [view.state.doc.toString(), s.from, s.to]; });
        await page.evaluate(() => { const { view } = window.fixtureEditor; view.focus(); view.dispatch({ selection: { anchor: 0 } }); });
        m = await mark();
        const gamma = await cmLine(2).boundingBox();
        await rightClick({ x: gamma.x + 20, y: gamma.y + gamma.height / 2 });
        r = record('editor caret', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus[0].args[2], { link: false, editable: true, readOnly: false, secret: false, selection: false, content: true, images: false });
        await page.waitForTimeout(100);
        assert.deepEqual(await cmState(), ['alpha line\nbeta line\ngamma line', 0, 0]);
        await page.evaluate(() => { const { view } = window.fixtureEditor; view.dispatch({ selection: { anchor: 11, head: 20 } }); });
        await next({ action: 'copy' });
        m = await mark();
        await rightClick(await center(cmLine(1)));
        assert.deepEqual((await since(m)).copied, ['beta line']);
        // The Find box is a field of its own: its Copy, Paste and Cut never
        // touch the file (its text, its changes, hence its dirty state).
        await page.keyboard.press('Meta+f');
        const find = page.locator('#cm-host .cm-search input[name=search]');
        await find.waitFor();
        await find.fill('gamma');
        await find.evaluate((el) => el.select());
        await next({ action: 'copy' });
        m = await mark();
        await rightClick(await center(find));
        r = record('editor Find box', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.copied, ['gamma']);
        assert.equal(r.menus[0].args[2].editable, true);
        await next({ action: 'paste', text: 'ZZZ' });
        await rightClick(await center(find));
        await page.waitForTimeout(60);
        await find.evaluate((el) => el.select());
        await next({ action: 'cut' });
        m = await mark();
        await rightClick(await center(find));
        assert.deepEqual((await since(m)).copied, ['ZZZ']);
        assert.equal(await find.inputValue(), '');
        assert.deepEqual([...(await cmState()), await page.evaluate(() => window.editorChanges)], ['alpha line\nbeta line\ngamma line', 11, 20, 0]);
        await page.keyboard.press('Escape');
        await page.evaluate(() => { const { view } = window.fixtureEditor; view.focus(); view.dispatch({ selection: { anchor: 11, head: 20 } }); });
        await next({ action: 'paste', text: 'BETA' });
        await rightClick(await center(cmLine(1)));
        await page.waitForTimeout(60);
        assert.deepEqual(await cmState(), ['alpha line\nBETA\ngamma line', 15, 15]);
        await page.evaluate(() => { const { ed, view } = window.fixtureEditor; ed.setReadOnly('alice\nnotes.txt', true); view.dispatch({ selection: { anchor: 0, head: 5 } }); });
        await next({ action: 'cut' });
        m = await mark();
        await rightClick(await center(cmLine(0)));
        r = await since(m);
        assert.deepEqual(r.menus[0].args[2], { link: false, editable: true, readOnly: true, secret: false, selection: true, content: true, images: false });
        assert.deepEqual(r.copied, []);
        assert.equal((await cmState())[0], 'alpha line\nBETA\ngamma line');
        await page.evaluate(() => document.querySelector('#cm-host').remove());

        // ---- A dialog ----
        await page.locator('.side-group .mds-icon-button').click();
        await page.locator('#hire-dialog[open]').waitFor();
        m = await mark();
        await rightClick(await center(page.locator('#hire-dialog .dialog-hint').first()));
        r = record('dialog', await since(m));
        assert.deepEqual(r.prevented, [true]);
        assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu']);
        await page.keyboard.press('Escape');
        await page.locator('#hire-dialog[open]').waitFor({ state: 'detached' }).catch(() => {});

        // ---- Menu keys: the focused element's menu, at it ----
        await composer.focus();
        m = await mark();
        await page.keyboard.press('Shift+F10');
        r = record('Shift+F10 in the composer', await since(m));
        assert.deepEqual(r.menus.map((c) => [c.method, c.args[2].editable]), [['PageMenu', true]]);
        const cbox = await composer.boundingBox();
        const [kx, ky] = r.menus[0].args;
        assert(kx >= cbox.x && kx <= cbox.x + cbox.width && ky >= cbox.y && ky <= cbox.y + cbox.height, `menu at ${kx},${ky}`);
        assert.equal(await composer.evaluate((el) => document.activeElement === el), true);
        await page.locator('[data-agent="alice/worker"]').first().focus();
        m = await mark();
        await page.keyboard.press('Shift+F10');
        r = record('Shift+F10 on an agent row', await since(m));
        assert.deepEqual(r.menus.map((c) => c.method), ['AgentMenu']);
        await page.locator('#own-menu').focus();
        m = await mark();
        await page.keyboard.press('Shift+F10');
        assert.deepEqual((await since(m)).menus.map((c) => c.method), ['FileMenu']);

        // ---- Dark and narrow ----
        for (const width of [1100, 520]) for (const theme of ['light', 'dark']) {
          await page.setViewportSize({ width, height: 760 });
          await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; document.documentElement.dataset.appearance = theme; }, theme);
          const md = page.locator('#thread .msg-assistant .md').first();
          await md.scrollIntoViewIfNeeded();
          m = await mark();
          await rightClick(await selectText('#thread .msg-assistant .md', '定时'));
          r = record(`selected word ${width} ${theme}`, await since(m));
          assert.deepEqual(r.prevented, [true]);
          assert.deepEqual(r.menus.map((c) => c.method), ['PageMenu']);
          if (out) await page.screenshot({ path: `${out}/${engineName}-${language}-${width}-${theme}.png` });
        }
        assert.deepEqual(errors, []);
        await page.close();
      }
    } finally { await browser.close(); }
  }
} finally { await new Promise((resolve) => server.close(resolve)); }
if (out) await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2) + '\n');
if (!probeOnly) console.log('PASS: selections, Markdown, links, code, empty space, sidebar, Needs You, dialog, fields, secrets, editor and its Find box, composer image Paste, macOS Control-click, own-menu rows and menu keys in Chromium/WebKit, en/zh-Hans, light/dark, wide/narrow (controlled bridge).');
console.log(JSON.stringify(results.filter((r) => /selected (Chinese|English) word|empty space$/.test(r.case)).map((r) => [r.engine, r.language, r.case, r.prevented, r.menus.map((x) => x.method)])));
