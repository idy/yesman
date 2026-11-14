// Optional browser checks of keyboard shortcuts on the complete production
// page: install Playwright outside the worktree, then run
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/shortcuts-browser.mjs
//
// A synthetic MyGo bridge says the platform is macOS (darwin) or Windows
// (win32), as MyGo's runtime does, and keeps shortcuts in the page's
// localStorage, as settings.json would across a restart. Nothing touches a
// real workspace or settings. The Windows runs are the page's Windows rules
// in Chromium and WebKit, not WebView2 on Windows.
// YESMAN_SHORTCUTS_OUT names a folder for screenshots and results.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_SHORTCUTS_OUT || '';
if (out) await mkdir(out, { recursive: true });

const bridge = `<script>
const params = new URLSearchParams(location.search);
const handlers = {};
window.calls = [];
window.unmocked = [];
window.agents = [
  {id:'alice/lead',name:'lead',yesman:'alice',role:'lead',status:'idle',provider:'p',model:'m1',effort:'low',effortLevels:['low','high']},
  {id:'alice/worker-one',name:'worker-one',yesman:'alice',role:'worker',number:1,title:'Synthetic worker',status:'working',runStartedAt:new Date().toISOString(),provider:'p',model:'m1',effort:'low',effortLevels:['low','high']},
  {id:'bob/lead',name:'lead',yesman:'bob',role:'lead',status:'waiting',provider:'p',model:'m1',effort:'low',effortLevels:['low','high']},
];
const saved = () => JSON.parse(localStorage.getItem('synthetic.shortcuts') || '{}');
window.fileText = 'const a = 1;\\n';
window.mygo = {platform: params.get('platform') || 'darwin', locale: params.get('locale') || 'en', on:(name,fn)=>handlers[name]=fn, call:async(method,...args)=>{
  const m = method.replace('Yesman.','');
  calls.push([m, ...JSON.parse(JSON.stringify(args))]);
  switch (m) {
  case 'State': return {home:'/synthetic',pool:'/synthetic/repos',yesmen:[{name:'alice',displayName:'Alice',path:'/synthetic/yesmen/alice',worktrees:[]},{name:'bob',displayName:'Bob',path:'/synthetic/yesmen/bob',worktrees:[]}],agents};
  case 'Repos': case 'Clones': case 'Pulls': case 'FindFiles': return [];
  case 'Settings': return {appearance:'system',autoFetch:0};
  case 'Fetches': return {fetching:[],errors:{}};
  case 'Messages': return {messages:[]};
  case 'TakeFocus': return '';
  case 'Update': return {enabled:false};
  case 'Models': return {models:[{provider:'p',id:'m1',name:'Model One'},{provider:'p',id:'m2',name:'Model Two'}],agent:agents.find(a=>a.id===args[0])};
  case 'Info': return {knowledge:[],worktrees:[]};
  case 'Board': return {tasks:[]};
  case 'Crew': return {members:[],yesman:''};
  case 'Context': return null;
  case 'ListFiles': return args[1] === '.' || args[1] === '' ? [{name:'main.js',path:'main.js',size:13}] : [];
  case 'ReadFile': return {path:args[1],size:fileText.length,modTime:'2026-11-14T00:00:00Z',kind:'text',text:fileText,hash:'h1'};
  case 'WriteFile': fileText = args[2]; return {path:args[1],size:args[2].length,modTime:'2026-11-14T00:00:01Z',kind:'text',text:args[2],hash:'h'+calls.length};
  case 'FilesMenu': return '';
  case 'Submit': return {id:args[1],revision:1,state:'accepted',agentSince:''};
  case 'Shortcuts': return saved();
  case 'SetShortcuts': {
    const next = saved();
    for (const [id, list] of Object.entries(args[0])) { if (list === null) delete next[id]; else next[id] = list; }
    if (window.failSave) throw Error('Synthetic save failure');
    localStorage.setItem('synthetic.shortcuts', JSON.stringify(next));
    return next;
  }
  case 'ResetShortcuts': localStorage.removeItem('synthetic.shortcuts'); return {};
  case 'Abort': case 'Stop': case 'WatchFiles': case 'Reveal': case 'FetchAll': return;
  default: unmocked.push(m); return null;
  }
}};
window.setStatus = (id, status) => { agents.find(a=>a.id===id).status = status; handlers.agents(JSON.parse(JSON.stringify(agents))); };
window.lastKey = null;
addEventListener('keydown', (e) => { window.lastKey = e; }, true);
// A key as script sends it, for what a keyboard cannot type here.
window.sendKey = (init) => {
  const e = new KeyboardEvent('keydown', {bubbles:true, cancelable:true, ...init});
  (document.activeElement || document.body).dispatchEvent(e);
  return e.defaultPrevented;
};
</script>`;

const server = createServer(async (req, res) => {
  try {
    const path = req.url.split('?')[0] === '/' ? 'index.html' : req.url.slice(1).split('?')[0];
    if (path.includes('..')) throw Error('invalid path');
    let body = await readFile(root + path);
    res.setHeader('Content-Type', path.endsWith('.html') ? 'text/html' : path.endsWith('.js') ? 'text/javascript' : path.endsWith('.css') ? 'text/css' : path.endsWith('.woff2') ? 'font/woff2' : 'application/octet-stream');
    if (path === 'index.html') body = body.toString().replace('<head>', '<head>' + bridge);
    res.end(body);
  } catch { res.statusCode = 404; res.end(); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/`;
const results = [];

async function run(engineName, engine, platform) {
  const mac = platform === 'darwin';
  const MOD = mac ? 'Meta' : 'Control';
  const cap = (macText, winText) => (mac ? macText : winText);
  const browser = await engine.launch({ headless: true });
  const checks = [];
  const check = (name) => checks.push(name);
  try {
    const context = await browser.newContext({ viewport: { width: 1180, height: 780 } });
    // A key of its own from an earlier run: ⌥⌘E (Ctrl+Alt+E) shows the info.
    await context.addInitScript(() => {
      if (!localStorage.getItem('synthetic.seeded')) {
        localStorage.setItem('synthetic.seeded', '1');
        localStorage.setItem('synthetic.shortcuts', JSON.stringify({ 'pane.info': ['Mod+Alt+E'] }));
        localStorage.setItem('yesman.info', 'hidden');
      }
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await page.goto(`${base}?platform=${platform}`);
    const lead = page.locator('[data-agent="alice/lead"][data-sidebar-entry="yesman"]');
    const worker = page.locator('[data-agent="alice/worker-one"]');
    const textarea = page.locator('.composer textarea');
    const palette = page.locator('dialog.palette-dialog');
    await lead.waitFor();
    await page.waitForFunction(() => calls.some((c) => c[0] === 'Shortcuts'));
    await page.waitForTimeout(100);
    const count = (method) => page.evaluate((m) => calls.filter((c) => c[0] === m).length, method);
    const callsOf = (method) => page.evaluate((m) => calls.filter((c) => c[0] === m), method);
    const prevented = () => page.evaluate(() => window.lastKey?.defaultPrevented ?? null);
    const active = () => page.evaluate(() => {
      const el = document.activeElement;
      return el ? (el.id || el.getAttribute('aria-label') || el.className || el.tagName) : null;
    });
    const boardShown = () => page.evaluate(() => !document.querySelector('#board').hidden);
    const filesShown = () => page.evaluate(() => !document.querySelector('#files').hidden);
    const infoShown = () => page.evaluate(() => !document.querySelector('#yesman-info').hidden);

    // ---------- The palette ----------
    await textarea.click();
    await textarea.fill('draft');
    await page.keyboard.press(`${MOD}+KeyK`);
    await palette.waitFor({ state: 'visible' });
    assert.equal(await active(), 'Search commands');
    assert.equal(await prevented(), false); // the palette's own field had it: no action ran twice
    await page.keyboard.press(`${MOD}+KeyK`);
    await palette.waitFor({ state: 'hidden' });
    assert.equal(await active(), 'Message');
    assert.equal(await textarea.inputValue(), 'draft');
    check('palette opens with its keys in the composer and closes with them, focus back in the composer');

    await page.keyboard.press(`${MOD}+KeyK`);
    await page.keyboard.type('board');
    const first = palette.locator('[role="option"]').first();
    assert.equal(await first.locator('.palette-option-label').innerText(), 'Show or hide the board');
    assert.equal(await first.locator('.keycap').first().innerText(), cap('⇧⌘B', 'Ctrl+Shift+B'));
    assert.equal(await page.locator('.palette-input').getAttribute('aria-activedescendant'), await first.getAttribute('id'));
    if (out) await page.screenshot({ path: `${out}/palette-${engineName}-${platform}.png` });
    await page.keyboard.press('Enter');
    await palette.waitFor({ state: 'hidden' });
    assert.equal(await boardShown(), true);
    assert.equal(await active(), 'Message');
    check('palette search ranks, shows the platform keycap, runs the action once and restores focus');

    await page.keyboard.press(`${MOD}+Shift+KeyB`);
    assert.equal(await boardShown(), false);
    assert.equal(await prevented(), true);
    const repeat = await page.evaluate((mac) => sendKey({ key: 'B', code: 'KeyB', shiftKey: true, metaKey: mac, ctrlKey: !mac, repeat: true }), mac);
    assert.equal(repeat, true);
    assert.equal(await boardShown(), false);
    check('a shortcut runs once per press; a held key repeats nothing but stays taken');

    // An action that cannot run says why and stays open; Esc closes.
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.keyboard.type('lead');
    const leadOption = palette.locator('[role="option"]', { hasText: 'Go to this yesman’s lead' });
    assert.equal(await leadOption.getAttribute('aria-disabled'), 'true');
    assert.match(await leadOption.innerText(), /Open a worker’s conversation first/);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowUp');
    await page.keyboard.press('Enter');
    assert.equal(await palette.isVisible(), true);
    assert.match(await palette.locator('.palette-status').innerText(), /cannot run now/);
    await page.keyboard.press('Escape');
    await palette.waitFor({ state: 'hidden' });
    assert.equal(await active(), 'Message');
    check('a disabled action lists why, does not run, and Esc returns focus');

    // ---------- Stop and the agent shown ----------
    await page.keyboard.press(`${MOD}+Period`);
    assert.equal(await count('Abort'), 0);
    assert.equal(await prevented(), false);
    await page.evaluate(() => setStatus('alice/lead', 'working'));
    await page.waitForTimeout(50);
    await textarea.focus();
    await page.keyboard.press('Escape');
    assert.equal(await count('Abort'), 0);
    await page.keyboard.press(`${MOD}+Period`);
    assert.deepEqual(await callsOf('Abort'), [['Abort', 'alice/lead']]);
    await worker.click();
    await page.keyboard.press(`${MOD}+Period`);
    assert.deepEqual((await callsOf('Abort')).map((c) => c[1]), ['alice/lead', 'alice/worker-one']);
    assert.equal(await page.locator('.composer-action').getAttribute('title'), `Stop the current run (${cap('⌘.', 'Ctrl+.')})`);
    await page.evaluate(() => setStatus('alice/lead', 'idle'));
    check('Stop runs only while the shown agent runs, on that agent, and Esc never stops it');

    // ---------- Going between conversations ----------
    const NEXT = mac ? `${MOD}+Alt+ArrowDown` : `${MOD}+PageDown`;
    const PREV = mac ? `${MOD}+Alt+ArrowUp` : `${MOD}+PageUp`;
    const shownAgent = () => page.evaluate(() => document.querySelector('.agent-row[aria-current="page"]')?.dataset.agent);
    assert.equal(await shownAgent(), 'alice/worker-one');
    await page.keyboard.press(NEXT);
    assert.equal(await shownAgent(), 'bob/lead');
    await page.keyboard.press(NEXT);
    assert.equal(await shownAgent(), 'alice/lead');
    await page.keyboard.press(PREV);
    assert.equal(await shownAgent(), 'bob/lead');
    await page.keyboard.press(`${MOD}+Digit1`);
    assert.equal(await shownAgent(), 'alice/lead');
    await page.keyboard.press(`${MOD}+Digit2`);
    assert.equal(await shownAgent(), 'bob/lead');
    await page.keyboard.press(`${MOD}+Digit3`);
    assert.equal(await prevented(), false);
    assert.equal(await shownAgent(), 'bob/lead');
    await lead.click();
    check('next/previous follow the sidebar; ⌘1–9 go to yesmen by place; a missing one leaves the key');

    // ---------- Text fields keep their keys ----------
    await textarea.fill('hello');
    await page.keyboard.press(`${MOD}+KeyA`);
    assert.equal(await prevented(), false);
    assert.deepEqual(await textarea.evaluate((t) => [t.selectionStart, t.selectionEnd]), [0, 5]);
    await page.keyboard.press(`${MOD}+KeyC`);
    assert.equal(await prevented(), false);
    await page.keyboard.press('End');
    await page.keyboard.press('Shift+Enter');
    await page.keyboard.type('there');
    assert.equal(await textarea.inputValue(), 'hello\nthere');
    await page.keyboard.press('ArrowUp');
    assert.equal(await prevented(), false);
    await page.keyboard.press('Enter');
    await page.waitForFunction(() => calls.filter((c) => c[0] === 'Submit').length === 1);
    await page.waitForTimeout(100);
    assert.equal(await count('Submit'), 1);
    assert.equal((await callsOf('Submit'))[0][1], 'alice/lead');
    check('select all, copy, arrows, Shift+Enter and Enter send keep their jobs in the composer; Enter sends once');

    // An input method composing keeps every key.
    await textarea.focus();
    await page.evaluate(() => document.querySelector('.composer textarea').dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })));
    await page.keyboard.press(`${MOD}+KeyK`);
    assert.equal(await palette.isVisible(), false);
    await page.evaluate(() => document.querySelector('.composer textarea').dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: '你好' })));
    assert.equal(await page.evaluate((mac) => sendKey({ key: 'k', code: 'KeyK', metaKey: mac, ctrlKey: !mac, isComposing: true }), mac), false);
    assert.equal(await palette.isVisible(), false);
    await page.keyboard.press(`${MOD}+KeyK`);
    await palette.waitFor({ state: 'visible' });
    await page.keyboard.press('Escape');
    await palette.waitFor({ state: 'hidden' });
    check('input method composition (composition events, isComposing) keeps the keys');

    // A key of the user's own with ⌥ (Alt); AltGr's characters stay text.
    assert.equal(await infoShown(), false);
    await lead.focus();
    await page.keyboard.press(`${MOD}+Alt+KeyE`);
    assert.equal(await infoShown(), true);
    await textarea.focus();
    const euro = await page.evaluate(() => sendKey({ key: '€', code: 'KeyE', ctrlKey: true, altKey: true }));
    assert.equal(euro, false);
    assert.equal(await infoShown(), true);
    await page.keyboard.press(`${MOD}+Alt+KeyE`);
    assert.equal(await infoShown(), false);
    check(mac ? 'a saved ⌥⌘E runs everywhere, with ⌘, including the composer' : 'a saved Ctrl+Alt+E runs; AltGr+E (€) in the composer stays text');

    // ---------- Menus and dialogs come first ----------
    const picker = page.locator('.composer-choice--model [role="combobox"]');
    await picker.click();
    assert.equal(await picker.getAttribute('aria-expanded'), 'true');
    await page.keyboard.press(`${MOD}+KeyK`);
    assert.equal(await palette.isVisible(), false);
    await page.keyboard.press('Escape');
    assert.equal(await picker.getAttribute('aria-expanded'), 'false');
    check('an open menu keeps the keys');

    await textarea.focus();
    await page.keyboard.press(`${MOD}+KeyN`);
    const hire = page.locator('#hire-dialog');
    await hire.waitFor({ state: 'visible' });
    await page.keyboard.press(`${MOD}+KeyK`);
    assert.equal(await palette.isVisible(), false);
    await page.keyboard.press('Escape');
    await hire.waitFor({ state: 'hidden' });
    // From the palette, the dialog an action opens keeps focus.
    await textarea.focus();
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.keyboard.type('hire');
    await page.keyboard.press('Enter');
    await hire.waitFor({ state: 'visible' });
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => document.querySelector('#hire-dialog').contains(document.activeElement)), true);
    await page.keyboard.press('Escape');
    await hire.waitFor({ state: 'hidden' });
    check('a modal dialog keeps the keys; an action the palette runs keeps the focus it gives');

    // Choosing a conversation in the palette shows it.
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.keyboard.type('synthetic');
    await page.keyboard.press('Enter');
    assert.equal(await shownAgent(), 'alice/worker-one');
    await lead.click();
    check('the palette lists conversations by their stable IDs');

    // ---------- Files and the editor ----------
    await page.keyboard.press(`${MOD}+Shift+KeyE`);
    assert.equal(await filesShown(), true);
    await page.keyboard.press(`${MOD}+KeyP`);
    assert.equal(await active(), 'files-find');
    await page.locator('#files-tree-list [data-path="main.js"]').click();
    const content = page.locator('#files .cm-content');
    await content.waitFor();
    await page.waitForTimeout(400); // its language loads
    await content.click();
    await page.keyboard.press(`${MOD}+Slash`);
    await page.waitForTimeout(50);
    assert.equal(await page.evaluate(() => document.querySelector('#app').dataset.view), 'agent');
    assert.match(await content.innerText(), /^\/\/ const a = 1;/);
    await page.keyboard.press(`${MOD}+KeyS`);
    await page.waitForFunction(() => calls.some((c) => c[0] === 'WriteFile'));
    await page.waitForTimeout(150);
    assert.equal(await count('WriteFile'), 1);
    // ⌘K still opens the palette from the editor.
    await page.keyboard.press(`${MOD}+KeyK`);
    await palette.waitFor({ state: 'visible' });
    await page.keyboard.press('Escape');
    await palette.waitFor({ state: 'hidden' });
    assert.equal(await page.evaluate(() => !!document.activeElement.closest('.cm-editor')), true);
    // The files menu shows the actions' keys.
    await page.locator('#files button[aria-label="More"]').first().click().catch(() => {});
    const menu = (await callsOf('FilesMenu')).at(-1);
    if (menu) assert.deepEqual(menu[7], { 'files.find': 'Mod+P', 'files.tree': 'Mod+Shift+Y' });
    check(`the editor keeps its keys (${cap('⌘/', 'Ctrl+/')} comments, ${cap('⌘S', 'Ctrl+S')} saves once); ${cap('⌘P', 'Ctrl+P')} finds a file${menu ? '; its menu gets the keys' : ''}`);
    await page.keyboard.press(`${MOD}+Shift+KeyE`);
    assert.equal(await filesShown(), false);

    // ---------- Settings › Shortcuts ----------
    await lead.focus();
    await page.keyboard.press(`${MOD}+Slash`);
    await page.locator('#shortcut-list').waitFor();
    assert.equal(await page.locator('#crumbs').innerText(), 'Settings\n/\nShortcuts'.replace(/\n/g, '\n'));
    const row = (id) => page.locator(`[data-action="${id}"]`);
    const chips = (id) => row(id).locator('.shortcut-chip .keycap').allInnerTexts();
    assert.deepEqual(await chips('palette.open'), [cap('⌘K', 'Ctrl+K'), cap('⇧⌘P', 'Ctrl+Shift+P')]);
    assert.deepEqual(await chips('pane.info'), [cap('⌥⌘E', 'Ctrl+Alt+E')]);
    assert.match(await row('pane.info').innerText(), /Changed\. It has no shortcut by default\./);
    if (!mac) assert.match(await row('pane.info').innerText(), /AltGr/);
    assert.match(await row('settings.shortcuts').innerText(), /In the file editor/);
    await page.locator('#shortcut-search').fill('board');
    assert.equal(await page.locator('#shortcut-list [data-action]').count(), 1);
    await page.locator('#shortcut-search').fill('');
    check('Settings › Shortcuts lists defaults, saved keys and their limits, and finds actions');

    // Recording: refusals, a conflict, Esc; nothing recorded runs.
    await row('pane.board').locator('.shortcut-add').click();
    const recorder = page.locator('#shortcut-recorder');
    assert.equal(await active(), 'shortcut-recorder');
    const recError = page.locator('#shortcut-recorder-error');
    await page.keyboard.press('KeyK');
    assert.match(await recError.innerText(), /types text/);
    await page.keyboard.press(`${MOD}+KeyC`);
    assert.match(await recError.innerText(), /copy, paste/);
    if (!mac) {
      await page.evaluate(() => sendKey({ key: '€', code: 'KeyE', ctrlKey: true, altKey: true }));
      assert.match(await recError.innerText(), /AltGr/);
      await page.keyboard.press('Meta+KeyK');
      assert.match(await recError.innerText(), /Windows key/);
    }
    await page.evaluate((mac) => sendKey({ key: 'Process', code: 'KeyK', keyCode: 229, isComposing: true, metaKey: mac, ctrlKey: !mac }), mac);
    assert.match(await recError.innerText(), /input method/);
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.locator('.shortcut-conflict').waitFor();
    assert.match(await page.locator('.shortcut-conflict').innerText(), /“Show all commands”/);
    assert.equal(await palette.isVisible(), false);
    await page.getByRole('button', { name: 'Choose other keys' }).click();
    assert.equal(await active(), 'shortcut-recorder');
    await page.keyboard.press('Escape');
    assert.equal(await recorder.count(), 0);
    assert.equal(await count('SetShortcuts'), 0);
    check('recording refuses a bare key, copy, IME' + (mac ? '' : ', AltGr and the Win key') + ', offers a conflict, and runs nothing');

    await row('pane.board').locator('.shortcut-add').click();
    await page.keyboard.press(`${MOD}+Shift+KeyO`);
    await page.waitForFunction(() => calls.some((c) => c[0] === 'SetShortcuts'));
    assert.deepEqual((await callsOf('SetShortcuts'))[0][1], { 'pane.board': ['Mod+Shift+B', 'Mod+Shift+O'] });
    await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.board"] .shortcut-chip').length === 2);
    assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B'), cap('⇧⌘O', 'Ctrl+Shift+O')]);
    assert.equal(await page.evaluate(() => document.activeElement.classList.contains('shortcut-add')), true);
    if (out) await page.screenshot({ path: `${out}/settings-${engineName}-${platform}.png`, fullPage: false });
    check('a recorded shortcut saves as Mod+Shift+O and shows as the platform writes it');

    // It stays after a restart, and runs.
    await page.reload();
    await lead.waitFor();
    await page.waitForFunction(() => calls.some((c) => c[0] === 'Shortcuts'));
    await page.waitForTimeout(100);
    await lead.click();
    await page.keyboard.press(`${MOD}+Shift+KeyO`);
    assert.equal(await boardShown(), true);
    await page.keyboard.press(`${MOD}+Shift+KeyB`);
    assert.equal(await boardShown(), false);
    assert.match(await page.getByRole('button', { name: 'Show the board' }).getAttribute('title'), new RegExp(cap('⇧⌘B', 'Ctrl\\+Shift\\+B')));
    check('saved shortcuts load again after a reload and run, beside the defaults');

    // Reassigning keys another action has, removing, resetting, all.
    await page.keyboard.press(`${MOD}+Slash`);
    await page.locator('#shortcut-list').waitFor();
    await row('pane.files').locator('.shortcut-add').click();
    await page.keyboard.press(`${MOD}+Shift+KeyO`);
    await page.locator('.shortcut-conflict').waitFor();
    assert.match(await page.locator('.shortcut-conflict').innerText(), /“Show or hide the board”/);
    await page.getByRole('button', { name: 'Use for “Show or hide the files”' }).click();
    await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.files"] .shortcut-chip').length === 2);
    assert.deepEqual((await callsOf('SetShortcuts')).at(-1)[1], { 'pane.files': ['Mod+Shift+E', 'Mod+Shift+O'], 'pane.board': null });
    assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B')]);
    assert.equal(await row('pane.board').locator('.shortcut-reset').count(), 0);
    await row('pane.files').getByRole('button', { name: new RegExp(`^Remove ${cap('Shift Command E', 'Ctrl Shift E')} from`) }).click();
    await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.files"] .shortcut-chip').length === 1);
    assert.deepEqual((await callsOf('SetShortcuts')).at(-1)[1], { 'pane.files': ['Mod+Shift+O'] });
    await row('pane.files').locator('.shortcut-reset').click();
    await page.waitForFunction(() => !document.querySelector('[data-action="pane.files"] .shortcut-reset'));
    assert.deepEqual((await callsOf('SetShortcuts')).at(-1)[1], { 'pane.files': null });
    // Clearing every key of the palette disables them.
    for (let i = 0; i < 2; i++) {
      await row('palette.open').locator('.shortcut-chip button').first().click();
      await page.waitForFunction((n) => document.querySelectorAll('[data-action="palette.open"] .shortcut-chip').length === n, 1 - i);
    }
    assert.match(await row('palette.open').innerText(), /None/);
    await lead.focus();
    await page.keyboard.press(`${MOD}+KeyK`);
    assert.equal(await palette.isVisible(), false);
    assert.equal(await prevented(), false);
    // A failed save says so and keeps the keys.
    await page.evaluate(() => { window.failSave = true; });
    await row('pane.board').locator('.shortcut-chip button').first().click();
    await row('pane.board').getByText('Synthetic save failure').waitFor();
    assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B')]);
    await page.evaluate(() => { window.failSave = false; });
    await page.locator('#shortcut-reset-all').click();
    await page.locator('#confirm-ok').click();
    await page.waitForFunction(() => calls.some((c) => c[0] === 'ResetShortcuts'));
    await page.waitForFunction(() => document.querySelectorAll('[data-action="palette.open"] .shortcut-chip').length === 2);
    assert.deepEqual(await chips('pane.info'), []);
    await lead.focus();
    await page.keyboard.press(`${MOD}+KeyK`);
    await palette.waitFor({ state: 'visible' });
    await page.keyboard.press('Escape');
    check('reassigning moves keys atomically; remove, reset, disable all keys, a failed save and reset all work');

    // Narrow and dark.
    if (out) {
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
      await page.setViewportSize({ width: 560, height: 780 });
      await page.waitForTimeout(100);
      await page.screenshot({ path: `${out}/settings-narrow-dark-${engineName}-${platform}.png` });
      await page.evaluate(() => document.documentElement.removeAttribute('data-theme'));
      await page.setViewportSize({ width: 1180, height: 780 });
    }
    const overflow = await page.evaluate(() => document.querySelector('.page').scrollWidth > document.querySelector('.page').clientWidth);
    assert.equal(overflow, false);

    assert.deepEqual(errors, []);
    assert.deepEqual(await page.evaluate(() => [...new Set(unmocked)]), []);
    await context.close();

    // Simplified Chinese, a narrow window.
    const zh = await browser.newPage({ viewport: { width: 760, height: 760 } });
    const zhErrors = [];
    zh.on('pageerror', (e) => zhErrors.push(e.message));
    await zh.goto(`${base}?platform=${platform}&locale=zh-Hans`);
    await zh.locator('[data-agent="alice/lead"]').first().waitFor();
    await zh.waitForFunction(() => calls.some((c) => c[0] === 'Shortcuts'));
    await zh.waitForTimeout(100);
    await zh.locator('[data-agent="alice/lead"][data-sidebar-entry="yesman"]').focus();
    await zh.keyboard.press(`${MOD}+Slash`);
    await zh.locator('#shortcut-list').waitFor();
    assert.match(await zh.locator('#shortcut-list').innerText(), /显示全部命令/);
    if (out) await zh.screenshot({ path: `${out}/settings-zh-${engineName}-${platform}.png` });
    await zh.keyboard.press(`${MOD}+KeyK`);
    await zh.locator('dialog.palette-dialog').waitFor({ state: 'visible' });
    assert.equal(await zh.locator('.palette-input').getAttribute('placeholder'), '输入命令、yesman 或助手');
    if (out) await zh.screenshot({ path: `${out}/palette-zh-${engineName}-${platform}.png` });
    assert.deepEqual(zhErrors, []);
    check('Simplified Chinese page and palette');
    results.push({ engine: engineName, platform, checks });
    console.log(`${engineName} ${platform}: ${checks.length} groups passed`);
  } finally {
    await browser.close();
  }
}

try {
  for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    for (const platform of ['darwin', 'win32']) await run(name, engine, platform);
  }
  if (out) await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2));
} finally {
  server.close();
}
