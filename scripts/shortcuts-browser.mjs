// Optional browser checks of keyboard shortcuts on the complete production
// page: install Playwright outside the worktree, then run
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/shortcuts-browser.mjs
//
// A synthetic MyGo bridge says the platform is macOS (darwin) or Windows
// (win32), as MyGo's runtime does, and keeps shortcuts in the page's
// localStorage, as settings.json would across a restart. Nothing touches a
// real workspace, settings or the system. The Windows runs check the page's
// Windows rules in Chromium and WebKit; they are not WebView2 on Windows,
// and the browser's own editing keys stay those of the system it runs on.
//
// YESMAN_SHORTCUTS_GROUPS (palette, settings, context, display, saving, search),
// YESMAN_SHORTCUTS_ENGINES (chromium, webkit) and YESMAN_SHORTCUTS_PLATFORMS
// (darwin, win32) choose what runs, all by default. YESMAN_SHORTCUTS_OUT
// names a folder for a JSON result per run and screenshots.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const playwright = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_SHORTCUTS_OUT || '';
if (out) await mkdir(out, { recursive: true });
const list = (name, all) => (process.env[name] ? process.env[name].split(',') : all);
const GROUPS = list('YESMAN_SHORTCUTS_GROUPS', ['palette', 'settings', 'context', 'display', 'saving', 'search']);
const ENGINES = list('YESMAN_SHORTCUTS_ENGINES', ['chromium', 'webkit']);
const PLATFORMS = list('YESMAN_SHORTCUTS_PLATFORMS', ['darwin', 'win32']);
// A 1×1 PNG, attached as a user would.
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=', 'base64');

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
window.importPlan = null;
// With holdSaves, each SetShortcuts and ResetShortcuts waits in held until
// the test answers it: held.shift().resolve() lets it save as Go does,
// held.shift().reject(err) fails it, saving nothing.
window.holdSaves = false;
window.held = [];
const hold = () => (holdSaves ? new Promise((resolve, reject) => held.push({resolve, reject})) : null);
// Each modal dialog opened, by name: the palette, hire, confirm, rename, ...
window.modalOpens = {};
const showModal = HTMLDialogElement.prototype.showModal;
HTMLDialogElement.prototype.showModal = function () {
  const name = this.classList.contains('palette-dialog') ? 'palette' : (this.id || this.className);
  modalOpens[name] = (modalOpens[name] || 0) + 1;
  return showModal.call(this);
};
window.mygo = {platform: params.get('platform') || 'darwin', locale: params.get('locale') || 'en', on:(name,fn)=>handlers[name]=fn, call:async(method,...args)=>{
  const m = method.replace('Yesman.','');
  calls.push([m, ...JSON.parse(JSON.stringify(args))]);
  // Values cross the bridge as JSON, so the page never shares these objects.
  return JSON.parse(JSON.stringify(await answer(m, args)) ?? 'null');
}};
async function answer(m, args) {
  switch (m) {
  case 'State': return {home:'/synthetic',pool:'/synthetic/repos',yesmen:[{name:'alice',displayName:'Alice',path:'/synthetic/yesmen/alice',worktrees:[]},{name:'bob',displayName:'Bob',path:'/synthetic/yesmen/bob',worktrees:[]}],agents};
  case 'Repos': case 'Clones': case 'Pulls': case 'FindFiles': return [];
  case 'Settings': return {appearance:'system',autoFetch:0,home:'/synthetic',pool:'/synthetic/repos'};
  case 'Fetches': return {fetching:[],errors:{}};
  case 'Messages': return {messages:[]};
  case 'TakeFocus': return '';
  case 'RestartStatus': return {};
  case 'Update': return {enabled:false};
  case 'Models': return {models:[{provider:'p',id:'m1',name:'Model One'},{provider:'p',id:'m2',name:'Model Two'}],agent:agents.find(a=>a.id===args[0])};
  case 'Info': return {knowledge:[],worktrees:[]};
  case 'Board': return {tasks:[]};
  case 'Crew': return {members:[],yesman:''};
  case 'Context': case 'Usage': return null;
  // The info panel's Tasks entry: no scheduled tasks.
  case 'Schedules': return {yesman:args[0],owner:true,plans:[],limits:{title:120,instructions:4000,minEvery:15,maxEvery:10080,history:60}};
  case 'ListFiles': return args[1] === '.' || args[1] === '' ? [{name:'main.js',path:'main.js',size:13}] : [];
  case 'ReadFile': return {path:args[1],size:fileText.length,modTime:'2026-11-14T00:00:00Z',kind:'text',text:fileText,hash:'h1'};
  case 'SaveWorkspaceFile': fileText = args[2]; return {path:args[1],size:args[2].length,modTime:'2026-11-14T00:00:01Z',kind:'text',text:args[2],hash:'h'+calls.length};
  case 'FilesMenu': return '';
  case 'Submit': case 'SubmitCoalescing': return {id:args[1],revision:1,state:'accepted',agentSince:''};
  case 'Shortcuts': return saved();
  // Lead conversations read their yesman's topics, to name messages sent from one.
  case 'Topics': return [];
  case 'SetShortcuts': {
    await hold();
    const next = saved();
    for (const [id, list] of Object.entries(args[0])) { if (list === null) delete next[id]; else next[id] = list; }
    if (window.failSave) throw Error('Synthetic save failure');
    localStorage.setItem('synthetic.shortcuts', JSON.stringify(next));
    return next;
  }
  case 'ResetShortcuts': await hold(); localStorage.removeItem('synthetic.shortcuts'); return {};
  case 'SetAppearance': return;
  case 'ChooseConfigFile': return '/synthetic/in.toml';
  case 'PreviewImport': return importPlan;
  case 'ImportConfig': {
    // As Go does: the file's shortcuts merge into those kept.
    localStorage.setItem('synthetic.shortcuts', JSON.stringify({...saved(), 'pane.board':['Mod+Shift+J']}));
    return {mode:args[1],cloning:[],removed:[],stopped:[],kept:[],env:{}};
  }
  case 'Abort': case 'Stop': case 'WatchFiles': case 'Reveal': case 'FetchAll': return;
  // Called only if a guard were skipped; recorded in calls either way.
  case 'PrepareWorkspaceDeletion': return {entry:args[0],label:'Alice',instance:'fixture'};
  case 'DeleteWorkspace': case 'RemoveWorker': case 'Rename': case 'HireNamed': return null;
  default: unmocked.push(m); return null;
  }
}
window.setStatus = (id, status) => { agents.find(a=>a.id===id).status = status; handlers.agents(JSON.parse(JSON.stringify(agents))); };
window.lastKey = null;
addEventListener('keydown', (e) => { window.lastKey = e; }, true);
// A key as script sends it, for what a keyboard cannot type here: AltGr,
// an input method, Option's characters, a held key's repeat.
window.sendKey = (init, to) => {
  const e = new KeyboardEvent('keydown', {bubbles:true, cancelable:true, ...init});
  if ('keyCode' in init) Object.defineProperty(e, 'keyCode', {get: () => init.keyCode});
  (to ? document.querySelector(to) : (document.activeElement || document.body)).dispatchEvent(e);
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

// open loads the page in a new browser context, with keys of the user's own
// saved: ⌥⌘E (Ctrl+Alt+E) shows the info.
async function open(browser, platform, { locale = 'en', browserLocale, viewport = { width: 1180, height: 780 }, seed = { 'pane.info': ['Mod+Alt+E'] } } = {}) {
  const context = await browser.newContext({ viewport, ...(browserLocale ? { locale: browserLocale } : {}) });
  // The vendored editor (CodeMirror) reads the platform from navigator: on
  // the Windows runs say Win32 to it too, so its Mod is Ctrl as on Windows.
  // This lines the fixture up; it is not a run on Windows.
  if (platform === 'win32') await context.addInitScript(() => Object.defineProperty(Navigator.prototype, 'platform', { get: () => 'Win32' }));
  await context.addInitScript((seed) => {
    if (!localStorage.getItem('synthetic.seeded')) {
      localStorage.setItem('synthetic.seeded', '1');
      localStorage.setItem('synthetic.shortcuts', JSON.stringify(seed));
      localStorage.setItem('yesman.info', 'hidden');
      localStorage.setItem('yesman.files.autosave', 'off'); // so that saving is only ⌘S here
    }
  }, seed);
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const mac = platform === 'darwin';
  const x = {
    context, page, errors, platform, mac,
    MOD: mac ? 'Meta' : 'Control',
    cap: (macText, winText) => (mac ? macText : winText),
    lead: page.locator('[data-agent="alice/lead"][data-sidebar-entry="yesman"]'),
    worker: page.locator('[data-agent="alice/worker-one"]'),
    textarea: page.locator('.composer textarea'),
    palette: page.locator('dialog.palette-dialog'),
    count: (method) => page.evaluate((m) => calls.filter((c) => c[0] === m).length, method),
    callsOf: (method) => page.evaluate((m) => calls.filter((c) => c[0] === m), method),
    opens: (name) => page.evaluate((n) => modalOpens[n] ?? 0, name),
    prevented: () => page.evaluate(() => window.lastKey?.defaultPrevented ?? null),
    sendKey: (init, to) => page.evaluate(([init, to]) => sendKey(init, to), [init, to]),
    active: () => page.evaluate(() => {
      const el = document.activeElement;
      return el ? (el.id || el.getAttribute('aria-label') || el.className || el.tagName) : null;
    }),
    shownAgent: () => page.evaluate(() => document.querySelector('.agent-row[aria-current="page"]')?.dataset.agent ?? null),
    boardShown: () => page.evaluate(() => !document.querySelector('#board').hidden),
    filesShown: () => page.evaluate(() => !document.querySelector('#files').hidden),
    infoShown: () => page.evaluate(() => !document.querySelector('#yesman-info').hidden),
    async load() {
      await page.goto(`${base}?platform=${platform}&locale=${locale}`);
      await x.lead.waitFor();
      await x.textarea.waitFor();
      // The keys kept have loaded once the info button shows the one saved.
      if (seed['pane.info']) await page.waitForFunction(() => /\(/.test(document.querySelector('#bar-actions button[aria-pressed]')?.title ?? ''));
      else await page.waitForFunction(() => calls.some((c) => c[0] === 'Shortcuts'));
    },
  };
  await x.load();
  return x;
}

// ---------- 1. The palette ----------
async function palette(x, check) {
  const { page, MOD, cap } = x;
  await x.textarea.click();
  await x.textarea.fill('draft');
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  assert.equal(await x.active(), 'Search commands');
  // The dispatcher ran palette.open for this key, so it prevented it, once.
  assert.equal(await x.prevented(), true);
  assert.equal(await x.opens('palette'), 1);
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'hidden' });
  // The palette's field took its own keys to close it. Had the document's
  // dispatcher run too, it would have opened again.
  assert.equal(await x.prevented(), true);
  assert.equal(await x.opens('palette'), 1);
  assert.equal(await x.active(), 'Message');
  assert.equal(await x.textarea.inputValue(), 'draft');
  await page.keyboard.press(`${MOD}+Shift+KeyP`);
  await x.palette.waitFor({ state: 'visible' });
  assert.equal(await x.opens('palette'), 2);
  await page.keyboard.press('Escape');
  await x.palette.waitFor({ state: 'hidden' });
  assert.equal(await x.active(), 'Message');
  check(`${cap('⌘K', 'Ctrl+K')} and ${cap('⇧⌘P', 'Ctrl+Shift+P')} open the palette once each from the composer; its own keys and Esc close it, focus and draft back`);

  // Every command it lists says what it is, and why when it cannot run.
  await page.keyboard.press(`${MOD}+KeyK`);
  const options = await page.locator('.palette-option').evaluateAll((els) => els.map((el) => ({
    key: el.dataset.key, label: el.querySelector('.palette-option-label').textContent, disabled: el.getAttribute('aria-disabled') === 'true',
    reason: el.querySelector('.palette-option-reason')?.textContent ?? '', keys: [...el.querySelectorAll('.keycap')].map((k) => k.textContent),
  })));
  x.options = options;
  assert.equal(options.length, 43); // 40 actions (including Restart and continue) and 3 conversations
  for (const o of options) {
    assert.ok(o.label, o.key);
    if (o.disabled) assert.ok(o.reason, o.key);
  }
  const option = (key) => options.find((o) => o.key === key);
  for (const id of ['agent.needs-you', 'files.task-log', 'repos.fetch-all']) assert.ok(option(`action:${id}`), id);
  // Every Settings page has its action, Grant's from 0.1.31 too.
  assert.deepEqual(options.filter((o) => o.key.startsWith('action:settings.page.')).map((o) => o.key.slice(21)),
    await page.locator('#settings-nav [data-page]').evaluateAll((els) => els.map((el) => el.dataset.page)));
  assert.deepEqual(option('action:settings.page.grant'), { ...option('action:settings.page.grant'), label: 'Settings: Grant', disabled: false });
  // Model usage too, from 0.1.33, with no key of its own.
  assert.deepEqual(option('action:settings.page.usage'), { ...option('action:settings.page.usage'), label: 'Settings: Model usage', disabled: false, keys: [] });
  assert.deepEqual(option('action:agent.stop'), { ...option('action:agent.stop'), disabled: true, reason: 'This agent is not running.' });
  assert.deepEqual(option('action:app.restart').keys, []);
  assert.deepEqual(option('action:yesman.delete').keys, []);
  assert.equal(option('action:yesman.dismiss'), undefined);
  assert.equal(option('action:worker.remove').disabled, true);
  assert.equal(option('action:agent.needs-you').disabled, false);
  assert.equal(option('item:alice/lead').reason, 'This is the conversation shown.');
  assert.deepEqual(option('action:pane.files').keys, [cap('⇧⌘E', 'Ctrl+Shift+E')]);
  // Scheduled tasks too, from 0.1.40, with no key of its own.
  assert.deepEqual(option('action:pane.tasks'), { ...option('action:pane.tasks'), label: 'Show or hide the scheduled tasks', disabled: false, keys: [] });
  assert.deepEqual(option('action:agent.next').keys, [cap('⌥⌘↓', 'Ctrl+PgDn')]);
  await page.keyboard.type('board');
  const first = x.palette.locator('[role="option"]').first();
  assert.equal(await first.locator('.palette-option-label').innerText(), 'Show or hide the board');
  assert.equal(await first.locator('.keycap').first().innerText(), cap('⇧⌘B', 'Ctrl+Shift+B'));
  assert.equal(await page.locator('.palette-input').getAttribute('aria-activedescendant'), await first.getAttribute('id'));
  if (out) await page.screenshot({ path: `${out}/palette-${x.engine}-${x.platform}.png` });
  await page.keyboard.press('Enter');
  await x.palette.waitFor({ state: 'hidden' });
  assert.equal(await x.boardShown(), true);
  assert.equal(await x.active(), 'Message');
  check('Each disabled action has a reason, and an entry for every Settings page; search ranks, shows the platform keycap, runs once and gives focus back');

  // The scheduled tasks are found and opened there too; the board comes back.
  await page.keyboard.press(`${MOD}+KeyK`);
  await page.keyboard.type('scheduled');
  assert.equal(await x.palette.locator('[role="option"]').first().locator('.palette-option-label').innerText(), 'Show or hide the scheduled tasks');
  await page.keyboard.press('Enter');
  await x.palette.waitFor({ state: 'hidden' });
  await page.locator('#schedules').waitFor();
  assert.equal(await x.boardShown(), false);
  await page.keyboard.press(`${MOD}+Shift+KeyB`);
  await page.locator('#schedules').waitFor({ state: 'hidden' });
  assert.equal(await x.boardShown(), true);
  check('the scheduled tasks are found in the palette, with no key, and open from it');

  // A held key repeats nothing, but stays taken.
  await page.keyboard.press(`${MOD}+Shift+KeyB`);
  assert.equal(await x.boardShown(), false);
  assert.equal(await x.prevented(), true);
  assert.equal(await x.sendKey({ key: 'B', code: 'KeyB', shiftKey: true, metaKey: x.mac, ctrlKey: !x.mac, repeat: true }), true);
  assert.equal(await x.boardShown(), false);
  check('a shortcut runs once a press; its repeat is taken and runs nothing');

  // A disabled command says why and stays open.
  await page.keyboard.press(`${MOD}+KeyK`);
  await page.keyboard.type('lead');
  assert.match(await x.palette.locator('[role="option"]').first().innerText(), /Go to this yesman’s lead[\s\S]*Open a worker’s conversation first/);
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowUp');
  await page.keyboard.press('Enter');
  assert.equal(await x.palette.isVisible(), true);
  assert.match(await x.palette.locator('.palette-status').innerText(), /cannot run now: Open a worker’s conversation first/);
  await page.keyboard.press('Escape');
  await x.palette.waitFor({ state: 'hidden' });
  assert.equal(await x.active(), 'Message');
  check('↑/↓ choose; Enter on a disabled command explains and does nothing');

  // It follows the agent: the run starting makes Stop available, and Stop
  // runs on the agent shown, once. The composer is drawn again meanwhile;
  // focus goes back to it.
  await page.keyboard.press(`${MOD}+KeyK`);
  await page.keyboard.type('stop the');
  const stop = x.palette.locator('[data-key="action:agent.stop"]');
  assert.equal(await stop.getAttribute('aria-disabled'), 'true');
  await page.evaluate(() => setStatus('alice/lead', 'working'));
  await page.waitForFunction(() => !document.querySelector('[data-key="action:agent.stop"]').hasAttribute('aria-disabled'));
  // The palette's own dialog does not count as a dialog in the way.
  await page.locator('.palette-input').fill('hire');
  assert.equal(await x.palette.locator('[data-key="action:yesman.hire"]').getAttribute('aria-disabled'), null);
  await page.locator('.palette-input').fill('stop the');
  await page.keyboard.press('Enter');
  await x.palette.waitFor({ state: 'hidden' });
  assert.deepEqual(await x.callsOf('Abort'), [['Abort', 'alice/lead']]);
  const focusAfterStop = await page.evaluate(() => {
    const el = document.activeElement;
    return { composer: el === document.querySelector('#dock textarea'), tag: el?.tagName, cls: String(el?.className ?? ''), label: el?.getAttribute?.('aria-label') };
  });
  assert.equal(focusAfterStop.composer, true, JSON.stringify(focusAfterStop));
  assert.equal(await x.textarea.inputValue(), 'draft');
  await page.evaluate(() => setStatus('alice/lead', 'idle'));
  check('the palette follows status changes; Stop runs once on the shown agent; focus returns to its redrawn composer');

  // A dialog an action opens keeps focus.
  await page.keyboard.press(`${MOD}+KeyK`);
  await page.keyboard.type('hire');
  await page.keyboard.press('Enter');
  const hire = page.locator('#hire-dialog');
  await hire.waitFor({ state: 'visible' });
  await page.waitForTimeout(300); // nothing takes focus later
  assert.equal(await page.evaluate(() => document.querySelector('#hire-dialog').contains(document.activeElement)), true);
  await page.keyboard.press('Escape');
  await hire.waitFor({ state: 'hidden' });
  assert.equal(await x.count('HireNamed'), 0);
  check('Hire from the palette: its dialog keeps focus; Esc hires nothing');

  // Choosing a conversation shows it, and focus is in its own message box.
  await page.keyboard.press(`${MOD}+KeyK`);
  await page.keyboard.type('synthetic');
  await page.keyboard.press('Enter');
  assert.equal(await x.shownAgent(), 'alice/worker-one');
  assert.equal(await page.evaluate(() => document.activeElement.getAttribute('placeholder')), 'Message worker worker-one…');
  check('a conversation chosen in the palette shows, with focus in its message box, not the old one');

  // Side effects go through their own confirmations.
  const run = async (query) => {
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.keyboard.type(query);
    await page.keyboard.press('Enter');
  };
  await run('remove this worker');
  await page.locator('#confirm-dialog').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#confirm-title').innerText(), 'Remove worker-one?');
  await page.keyboard.press('Escape');
  await run('delete yes man');
  const deletion = page.locator('#delete-workspace-dialog');
  await deletion.waitFor({ state: 'visible' });
  assert.equal(await deletion.locator('h2').innerText(), 'Delete “Alice”?');
  await deletion.getByRole('button', { name: 'Cancel' }).click();
  await run('rename this');
  await page.locator('#rename-dialog').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#rename-form input').inputValue(), 'Alice');
  await page.keyboard.press('Escape');
  await page.locator('#rename-dialog').waitFor({ state: 'hidden' });
  assert.deepEqual([await x.count('RemoveWorker'), await x.count('DeleteWorkspace'), await x.count('Rename')], [0, 0, 0]);
  await run('sleep');
  assert.deepEqual(await x.callsOf('Stop'), [['Stop', 'alice/worker-one']]);
  check('remove worker, delete workspace and rename ask first and do nothing on cancel; sleep acts on the worker shown');

  // Settings from the palette: focus goes to its item in Settings.
  await run('open settings');
  await page.waitForFunction(() => document.querySelector('#app').dataset.view === 'settings');
  assert.equal(await page.evaluate(() => document.activeElement.matches('#settings-nav [aria-current="page"]')), true);
  check('Go to Settings from the palette (found by “open settings”) focuses the page’s item in Settings');
}

// ---------- 2. Settings › Shortcuts ----------
async function settings(x, check) {
  const { page, MOD, cap } = x;
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+Slash`);
  await page.locator('#shortcut-list').waitFor();
  assert.equal(await x.active(), 'shortcut-search');
  assert.match(await page.locator('#crumbs').innerText(), /Settings[\s/]*Shortcuts/);
  const row = (id) => page.locator(`[data-action="${id}"]`);
  const chips = (id) => row(id).locator('.shortcut-chip .keycap').allInnerTexts();
  const setCalls = async () => (await x.callsOf('SetShortcuts')).map((c) => c[1]);
  assert.deepEqual(await chips('palette.open'), [cap('⌘K', 'Ctrl+K'), cap('⇧⌘P', 'Ctrl+Shift+P')]);
  assert.deepEqual(await chips('pane.info'), [cap('⌥⌘E', 'Ctrl+Alt+E')]);
  assert.match(await row('pane.info').innerText(), /Changed\. It has no shortcut by default\./);
  if (!x.mac) assert.match(await row('pane.info').innerText(), /AltGr/);
  assert.match(await row('settings.shortcuts').innerText(), /In the file editor/);
  // Every word must match: "board" alone also finds "keyboard shortcuts".
  await page.locator('#shortcut-search').fill('board');
  assert.deepEqual(await page.locator('#shortcut-list [data-action]').evaluateAll((els) => els.map((e) => e.dataset.action)), ['settings.shortcuts', 'pane.board']);
  await page.locator('#shortcut-search').fill('the board');
  assert.deepEqual(await page.locator('#shortcut-list [data-action]').evaluateAll((els) => els.map((e) => e.dataset.action)), ['pane.board']);
  await page.locator('#shortcut-search').fill(cap('⌘K', 'Ctrl+K'));
  assert.deepEqual(await page.locator('#shortcut-list [data-action]').evaluateAll((els) => els.map((e) => e.dataset.action)), ['palette.open']);
  await page.locator('#shortcut-search').fill('');
  check('the shortcut opens Settings › Shortcuts with focus in its search; defaults, saved keys and limits show; search by name or keycap');

  // Recording: refusals, held modifiers, a conflict, Esc. Nothing recorded runs.
  await row('pane.board').locator('.shortcut-add').click();
  assert.equal(await x.active(), 'shortcut-recorder');
  const error = page.locator('#shortcut-recorder-error');
  const recorder = page.locator('#shortcut-recorder');
  await page.keyboard.press('KeyK');
  assert.match(await error.innerText(), /types text/);
  await page.keyboard.press(`${MOD}+KeyC`);
  assert.match(await error.innerText(), /copy, paste/);
  await page.keyboard.press(`${MOD}+ArrowLeft`);
  assert.match(await error.innerText(), x.mac ? /Use two of/ : /moves the caret/);
  if (x.mac) await x.sendKey({ key: 'q', code: 'KeyQ', metaKey: true });
  else await x.sendKey({ key: 'F4', code: 'F4', altKey: true });
  assert.match(await error.innerText(), x.mac ? /macOS and yesman’s menus use these keys/ : /Windows uses these keys/);
  if (!x.mac) {
    await x.sendKey({ key: '€', code: 'KeyE', ctrlKey: true, altKey: true });
    assert.match(await error.innerText(), /AltGr/);
    await page.keyboard.press('Meta+KeyK');
    assert.match(await error.innerText(), /Windows key/);
  }
  await x.sendKey({ key: 'Process', code: 'KeyK', keyCode: 229, isComposing: true, metaKey: x.mac, ctrlKey: !x.mac });
  assert.match(await error.innerText(), /input method/);
  await page.keyboard.press(`${MOD}+Shift+KeyB`);
  assert.match(await error.innerText(), /is one of its shortcuts already/);
  await page.keyboard.down(MOD);
  assert.equal(await recorder.innerText(), cap('⌘', 'Ctrl+'));
  await page.evaluate(() => dispatchEvent(new Event('blur')));
  assert.equal(await recorder.innerText(), 'Press keys…');
  await page.keyboard.up(MOD);
  assert.equal(await x.sendKey({ key: 'O', code: 'KeyO', shiftKey: true, metaKey: x.mac, ctrlKey: !x.mac, repeat: true }), true);
  await page.keyboard.press(`${MOD}+KeyK`);
  await page.locator('.shortcut-conflict').waitFor();
  assert.match(await page.locator('.shortcut-conflict').innerText(), /runs “Show all commands” now/);
  assert.equal(await x.opens('palette'), 0);
  await page.getByRole('button', { name: 'Choose other keys' }).click();
  assert.equal(await x.active(), 'shortcut-recorder');
  await page.keyboard.press('Escape');
  assert.equal(await recorder.count(), 0);
  assert.equal(await page.evaluate(() => document.activeElement.matches('[data-action="pane.board"] .shortcut-add')), true);
  assert.deepEqual(await setCalls(), []);
  check(`recording refuses a bare key, copy, caret keys, ${cap('⌘Q', 'Alt+F4')}${x.mac ? '' : ', AltGr, the Win key'}, IME and duplicates; shows held keys, forgets them on blur, ignores repeats; offers a conflict without running ${cap('⌘K', 'Ctrl+K')}; Esc saves nothing`);

  await row('pane.board').locator('.shortcut-add').click();
  await page.keyboard.press(`${MOD}+Shift+KeyO`);
  await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.board"] .shortcut-chip').length === 2);
  assert.deepEqual(await setCalls(), [{ 'pane.board': ['Mod+Shift+B', 'Mod+Shift+O'] }]);
  assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B'), cap('⇧⌘O', 'Ctrl+Shift+O')]);
  assert.equal(await page.evaluate(() => document.activeElement.matches('[data-action="pane.board"] .shortcut-add')), true);
  if (out) await page.screenshot({ path: `${out}/settings-${x.engine}-${x.platform}.png` });
  check('a recorded shortcut saves once as Mod+Shift+O, shows as the platform writes it, focus back on Add');

  // Another Settings page saving keeps them.
  await page.locator('#settings-nav [data-page="general"]').click();
  await page.locator('select[aria-label="Theme"], select[aria-label="Appearance"]').first().selectOption('dark');
  await page.waitForFunction(() => calls.some((c) => c[0] === 'SetAppearance'));
  await page.locator('#settings-nav [data-page="shortcuts"]').click();
  await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.board"] .shortcut-chip').length === 2);
  check('saving the theme on General and coming back keeps the shortcuts');

  // After a restart they stay, and run.
  await x.load();
  await x.lead.click();
  await page.keyboard.press(`${MOD}+Shift+KeyO`);
  assert.equal(await x.boardShown(), true);
  await page.keyboard.press(`${MOD}+Shift+KeyB`);
  assert.equal(await x.boardShown(), false);
  assert.match(await page.getByRole('button', { name: 'Show the board' }).getAttribute('title'), new RegExp(cap('⇧⌘B', 'Ctrl\\+Shift\\+B')));
  check('after a reload the saved keys load and run beside the defaults');

  // Reassigning keys another action has, removing, resetting, disabling.
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+Slash`);
  await page.locator('#shortcut-list').waitFor();
  await row('pane.files').locator('.shortcut-add').click();
  await page.keyboard.press(`${MOD}+Shift+KeyO`);
  await page.locator('.shortcut-conflict').waitFor();
  assert.match(await page.locator('.shortcut-conflict').innerText(), /“Show or hide the board”/);
  await page.getByRole('button', { name: 'Use for “Show or hide the files”' }).click();
  await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.files"] .shortcut-chip').length === 2);
  assert.deepEqual((await setCalls()).at(-1), { 'pane.files': ['Mod+Shift+E', 'Mod+Shift+O'], 'pane.board': null });
  assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B')]);
  assert.equal(await row('pane.board').locator('.shortcut-reset').count(), 0);
  await row('pane.files').getByRole('button', { name: new RegExp(`^Remove ${cap('Shift Command E', 'Ctrl Shift E')} from`) }).click();
  await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.files"] .shortcut-chip').length === 1);
  assert.deepEqual((await setCalls()).at(-1), { 'pane.files': ['Mod+Shift+O'] });
  await row('pane.files').locator('.shortcut-reset').click();
  await page.waitForFunction(() => !document.querySelector('[data-action="pane.files"] .shortcut-reset'));
  assert.deepEqual((await setCalls()).at(-1), { 'pane.files': null });
  for (const left of [1, 0]) {
    await row('palette.open').locator('.shortcut-chip button').first().click();
    await page.waitForFunction((n) => document.querySelectorAll('[data-action="palette.open"] .shortcut-chip').length === n, left);
  }
  assert.deepEqual((await setCalls()).at(-1), { 'palette.open': [] });
  assert.match(await row('palette.open').innerText(), /None/);
  await x.lead.focus();
  await page.keyboard.press(`${MOD}+KeyK`);
  assert.equal(await x.palette.isVisible(), false);
  assert.equal(await x.prevented(), false);
  await page.evaluate(() => { window.failSave = true; });
  await row('pane.board').locator('.shortcut-chip button').first().click();
  await row('pane.board').getByText('Synthetic save failure').waitFor();
  assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B')]);
  await page.evaluate(() => { window.failSave = false; });
  check('reassigning moves the keys in one save; remove, reset one, removing every key disables it (the key then stays the page’s); a failed save keeps the keys and says why');

  // Importing a file with shortcuts: the plan says so, and the keys load again.
  await page.evaluate(() => { importPlan = { path: '/synthetic/in.toml', current: { appearance: 'system', autoFetch: 0 }, merge: { appearance: 'system', autoFetch: 0 },
    replace: { appearance: 'system', autoFetch: 0 }, add: [], present: [], extra: [], ignored: [], env: { clash: [], replaceClash: [] },
    shortcuts: { merge: ['pane.board'], replace: ['pane.board', 'palette.open'] } }; });
  await page.locator('#settings-nav [data-page="config"]').click();
  await page.getByRole('button', { name: 'Import…' }).click();
  const dialog = page.locator('#import-dialog');
  await dialog.waitFor({ state: 'visible' });
  const text = await dialog.innerText();
  assert.match(text, /Keyboard shortcuts: 1 action changes/);
  assert.match(text, /Keyboard shortcuts: 2 actions change · Replace only/);
  await dialog.getByRole('button', { name: 'Merge' }).click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'ImportConfig'));
  await page.locator('#settings-nav [data-page="shortcuts"]').click();
  await page.waitForFunction(() => document.querySelector('[data-action="pane.board"] .keycap')?.textContent?.endsWith('J'));
  assert.deepEqual(await chips('pane.board'), [cap('⇧⌘J', 'Ctrl+Shift+J')]);
  check('the import preview lists shortcut changes for Merge and Replace; after importing, the keys load again');

  await page.locator('#shortcut-reset-all').click();
  await page.locator('#confirm-ok').click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'ResetShortcuts'));
  await page.waitForFunction(() => document.querySelectorAll('[data-action="palette.open"] .shortcut-chip').length === 2);
  assert.deepEqual(await chips('pane.info'), []);
  assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B')]);
  await x.lead.focus();
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  await page.keyboard.press('Escape');
  check('Reset all asks, then every action has its defaults again');
}

// ---------- 3. Keys in context ----------
async function context(x, check) {
  const { page, MOD, cap } = x;
  const NEXT = x.mac ? `${MOD}+Alt+ArrowDown` : `${MOD}+PageDown`;
  const PREV = x.mac ? `${MOD}+Alt+ArrowUp` : `${MOD}+PageUp`;

  // Stop: only while the shown agent runs, on that agent; Esc never stops.
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+Period`);
  assert.equal(await x.prevented(), false);
  await page.evaluate(() => setStatus('alice/lead', 'working'));
  await page.locator('.composer-action[aria-label="Stop"]').waitFor();
  await x.textarea.focus();
  await page.keyboard.press('Escape');
  assert.equal(await x.count('Abort'), 0);
  await page.keyboard.press(`${MOD}+Period`);
  assert.deepEqual((await x.callsOf('Abort')).map((c) => c[1]), ['alice/lead']);
  assert.equal(await page.locator('.composer-action').getAttribute('title'), `Stop the current run (${cap('⌘.', 'Ctrl+.')})`);
  await page.locator('.composer-action').click();
  assert.deepEqual((await x.callsOf('Abort')).map((c) => c[1]), ['alice/lead', 'alice/lead']);
  // Switching quickly: each Stop goes to the agent shown when pressed.
  await x.worker.click();
  await page.keyboard.press(`${MOD}+Period`);
  await page.keyboard.press(NEXT);
  await page.keyboard.press(`${MOD}+Period`);
  await page.keyboard.press(NEXT);
  await page.evaluate(() => setStatus('alice/lead', 'idle'));
  await page.keyboard.press(`${MOD}+Period`);
  assert.equal(await x.shownAgent(), 'alice/lead');
  assert.deepEqual((await x.callsOf('Abort')).map((c) => c[1]), ['alice/lead', 'alice/lead', 'alice/worker-one', 'bob/lead']);
  check(`Stop (${cap('⌘.', 'Ctrl+.')}) runs only while the shown agent runs, on it, once; Esc never stops; the Stop button still works; fast switching keeps each target`);

  // Going between conversations.
  await x.worker.click();
  await page.keyboard.press(NEXT);
  assert.equal(await x.shownAgent(), 'bob/lead');
  await page.keyboard.press(NEXT);
  assert.equal(await x.shownAgent(), 'alice/lead');
  await page.keyboard.press(PREV);
  assert.equal(await x.shownAgent(), 'bob/lead');
  // A held key repeats going on.
  await x.sendKey(x.mac ? { key: 'ArrowDown', code: 'ArrowDown', metaKey: true, altKey: true, repeat: true } : { key: 'PageDown', code: 'PageDown', ctrlKey: true, repeat: true });
  assert.equal(await x.shownAgent(), 'alice/lead');
  await page.keyboard.press(`${MOD}+Digit2`);
  assert.equal(await x.shownAgent(), 'bob/lead');
  await page.keyboard.press(`${MOD}+Digit1`);
  assert.equal(await x.shownAgent(), 'alice/lead');
  await page.keyboard.press(`${MOD}+Digit3`);
  assert.equal(await x.prevented(), false);
  assert.equal(await x.shownAgent(), 'alice/lead');
  check(`${cap('⌥⌘↓/↑', 'Ctrl+PgDn/PgUp')} follow the sidebar and repeat; ${cap('⌘1–9', 'Ctrl+1–9')} go to yesmen by place; a missing one leaves the key`);

  // The composer keeps its keys.
  await x.textarea.click();
  await x.textarea.fill('hello');
  await page.keyboard.press('Shift+Enter');
  await page.keyboard.type('there');
  assert.equal(await x.textarea.inputValue(), 'hello\nthere');
  for (const k of ['KeyA', 'KeyC', 'KeyX', 'KeyZ', 'KeyV']) {
    await page.keyboard.press(`${MOD}+${k}`);
    assert.equal(await x.prevented(), false, k);
  }
  // The browser edits with the keys of the system it runs on; send this text.
  await x.textarea.fill('hello\nthere');
  await page.keyboard.press('ArrowUp');
  assert.equal(await x.prevented(), false);
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => calls.some((c) => c[0] === 'SubmitCoalescing'));
  await page.waitForTimeout(150); // a second send would have been issued by now
  assert.deepEqual((await x.callsOf('SubmitCoalescing')).map((c) => [c[1], c[3]]), [['alice/lead', 'hello\nthere']]);
  // Steering during a run, with an image attached.
  await page.evaluate(() => setStatus('alice/lead', 'working'));
  await page.locator('.composer-action[aria-label="Stop"]').waitFor();
  await page.locator('.composer input[type="file"]').setInputFiles({ name: 'dot.png', mimeType: 'image/png', buffer: PNG });
  await page.locator('.composer-image').waitFor();
  await x.textarea.click();
  await x.textarea.fill('steer with image');
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  await page.keyboard.press('Escape');
  await x.palette.waitFor({ state: 'hidden' });
  assert.equal(await x.active(), 'Message');
  assert.equal(await page.locator('.composer-image').count(), 1);
  assert.equal(await x.textarea.inputValue(), 'steer with image');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => calls.filter((c) => c[0] === 'SubmitCoalescing').length === 2);
  await page.waitForTimeout(150);
  const sent = await x.callsOf('SubmitCoalescing');
  assert.equal(sent.length, 2);
  assert.deepEqual([sent[1][1], sent[1][3], sent[1][4].length], ['alice/lead', 'steer with image', 1]);
  assert.equal(await x.count('Abort'), 4);
  await page.evaluate(() => setStatus('alice/lead', 'idle'));
  check('select all, copy, cut, undo, paste, bare ↑/↓ and Shift+Enter stay the composer’s; Enter sends once; steering with an image keeps draft and image through the palette and sends once, without stopping');

  // An input method keeps every key, including after the window loses focus mid-composition.
  await x.textarea.click();
  await page.evaluate(() => document.querySelector('.composer textarea').dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })));
  await page.keyboard.press(`${MOD}+KeyK`);
  assert.equal(await x.palette.isVisible(), false);
  assert.equal(await x.prevented(), false);
  await page.evaluate(() => document.querySelector('.composer textarea').dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: '你好' })));
  assert.equal(await x.sendKey({ key: 'k', code: 'KeyK', metaKey: x.mac, ctrlKey: !x.mac, isComposing: true }), false);
  assert.equal(await x.sendKey({ key: 'Process', code: 'KeyK', metaKey: x.mac, ctrlKey: !x.mac, keyCode: 229 }), false);
  assert.equal(await x.palette.isVisible(), false);
  await page.evaluate(() => { document.querySelector('.composer textarea').dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); dispatchEvent(new Event('blur')); });
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  await page.keyboard.press('Escape');
  check('composition events, isComposing and keyCode 229 keep the keys; a blur mid-composition does not leave them stuck');

  // Option's characters, AltGr's and a key of ⌥ alone (Alt+K, saved for the board here).
  await x.textarea.click();
  if (x.mac) {
    assert.equal(await x.sendKey({ key: '˚', code: 'KeyK', altKey: true }), false);
    assert.equal(await x.boardShown(), false);
    assert.equal(await x.sendKey({ key: '´', code: 'KeyE', altKey: true, metaKey: true }), true); // ⌥⌘E by the key's place
    assert.equal(await x.infoShown(), true);
    await x.lead.focus();
    assert.equal(await x.sendKey({ key: '˚', code: 'KeyK', altKey: true }), true);
    assert.equal(await x.boardShown(), true);
  } else {
    assert.equal(await x.sendKey({ key: '€', code: 'KeyE', ctrlKey: true, altKey: true }), false);
    assert.equal(await x.sendKey({ key: 'e', code: 'KeyE', ctrlKey: true, altKey: true, modifierAltGraph: true }), false);
    assert.equal(await x.infoShown(), false);
    await page.keyboard.press('Alt+KeyK');
    assert.equal(await x.prevented(), false);
    assert.equal(await x.boardShown(), false);
    await page.keyboard.press(`${MOD}+Alt+KeyE`); // no AltGr: Ctrl+Alt+E is the shortcut
    assert.equal(await x.infoShown(), true);
    await x.lead.focus();
    await page.keyboard.press('Alt+KeyK');
    assert.equal(await x.boardShown(), true);
  }
  await page.keyboard.press(`${MOD}+Shift+KeyB`);
  assert.equal(await x.boardShown(), false);
  await x.lead.focus();
  await page.keyboard.press(`${MOD}+Alt+KeyE`);
  assert.equal(await x.infoShown(), false);
  check(x.mac ? 'Option’s characters stay text in fields; ⌥⌘E runs by the key’s place; ⌥K runs outside fields only'
    : 'AltGr characters stay text; Ctrl+Alt+E runs; Alt+K types in fields and runs outside them');

  // Held modifiers do not stick when the window loses focus.
  await x.textarea.click();
  await x.textarea.fill('');
  await page.keyboard.down(MOD);
  await page.evaluate(() => dispatchEvent(new Event('blur')));
  await page.keyboard.up(MOD);
  await page.keyboard.press('KeyK');
  assert.equal(await x.textarea.inputValue(), 'k');
  assert.equal(await x.palette.isVisible(), false);
  check('a modifier held while the window loses focus does not stick');

  // Menus and dialogs come first.
  const picker = page.locator('.composer-choice--model [role="combobox"]');
  await picker.click();
  assert.equal(await picker.getAttribute('aria-expanded'), 'true');
  await page.keyboard.press(`${MOD}+KeyK`);
  assert.equal(await x.palette.isVisible(), false);
  assert.equal(await x.prevented(), false);
  await page.keyboard.press('Escape');
  assert.equal(await picker.getAttribute('aria-expanded'), 'false');
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+KeyN`);
  const hire = page.locator('#hire-dialog');
  await hire.waitFor({ state: 'visible' });
  await page.keyboard.press(`${MOD}+KeyK`);
  assert.equal(await x.palette.isVisible(), false);
  await page.keyboard.press(`${MOD}+KeyN`);
  assert.equal(await x.opens('hire-dialog'), 1);
  await page.keyboard.press('Escape');
  await hire.waitFor({ state: 'hidden' });
  check('an open menu and a modal dialog keep the keys; Hire opens one dialog');

  // The file editor keeps its keys; the files pane's keys work.
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+Shift+KeyE`);
  assert.equal(await x.filesShown(), true);
  await page.keyboard.press(`${MOD}+KeyP`);
  assert.equal(await x.active(), 'files-find');
  await page.locator('#files-tree-list [data-path="main.js"]').click();
  const content = page.locator('#files .cm-content');
  await content.waitFor();
  // Its language has loaded once its words are highlighted.
  await page.waitForFunction(() => document.querySelectorAll('#files .cm-content .cm-line span').length > 0);
  await page.locator('#files .cm-line').first().click();
  await page.keyboard.press(`${MOD}+Slash`);
  assert.equal(await x.prevented(), true);
  assert.equal(await page.evaluate(() => document.querySelector('#app').dataset.view), 'agent');
  assert.match(await content.innerText(), /^\/\/ const a = 1;/);
  await page.keyboard.press(`${MOD}+KeyS`);
  await page.waitForFunction(() => calls.some((c) => c[0] === 'SaveWorkspaceFile'));
  await page.waitForTimeout(150);
  assert.equal(await x.count('SaveWorkspaceFile'), 1);
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  await page.keyboard.press('Escape');
  await x.palette.waitFor({ state: 'hidden' });
  assert.equal(await page.evaluate(() => !!document.activeElement.closest('.cm-editor')), true);
  await page.keyboard.press(`${MOD}+KeyP`);
  assert.equal(await x.active(), 'files-find');
  await page.keyboard.press(`${MOD}+Shift+KeyY`);
  assert.equal(await page.locator('#files-tree-list').count(), 0);
  await page.keyboard.press(`${MOD}+Shift+KeyY`);
  await page.locator('#files-tree-list').waitFor();
  await page.getByRole('button', { name: 'More' }).first().click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'FilesMenu'));
  assert.deepEqual((await x.callsOf('FilesMenu')).at(-1)[7], { 'files.find': 'Mod+P', 'files.tree': 'Mod+Shift+Y', 'yesman.delete': '' });
  await page.keyboard.press(`${MOD}+Shift+KeyE`);
  assert.equal(await x.filesShown(), false);
  check(`the editor keeps ${cap('⌘/', 'Ctrl+/')} (comment) and saves once with ${cap('⌘S', 'Ctrl+S')}; ${cap('⌘K', 'Ctrl+K')} works there and gives focus back; ${cap('⌘P', 'Ctrl+P')} finds, ${cap('⇧⌘Y', 'Ctrl+Shift+Y')} toggles the tree; the files menu gets their keys`);
}

// ---------- 5. Saving: changes made while others save, failures, Esc ----------
async function saving(x, check) {
  const { page, MOD, cap } = x;
  const row = (id) => page.locator(`[data-action="${id}"]`);
  const chips = (id) => row(id).locator('.shortcut-chip .keycap').allInnerTexts();
  const resetAll = page.locator('#shortcut-reset-all');
  const kept = () => page.evaluate(() => JSON.parse(localStorage.getItem('synthetic.shortcuts') || '{}'));
  const sets = () => page.evaluate(() => calls.filter((c) => c[0] === 'SetShortcuts').map((c) => c[1]));
  const heldCount = () => page.evaluate(() => held.length);
  const answer = (ok = true) => page.evaluate((ok) => (ok ? held.shift().resolve() : held.shift().reject(new Error('Synthetic save failure'))), ok);
  // seed keeps keys as settings.json would, then restarts the page.
  const restart = async (keys) => {
    await page.evaluate((keys) => {
      localStorage.setItem('synthetic.shortcuts', JSON.stringify(keys));
      localStorage.setItem('yesman.info', 'hidden'); // an earlier part may have shown it
    }, keys);
    await x.load();
    assert.equal(await x.infoShown(), false);
    assert.equal(await x.boardShown(), false);
  };
  const openShortcuts = async () => {
    await x.textarea.click();
    await page.keyboard.press(`${MOD}+Slash`);
    await page.locator('#shortcut-list').waitFor();
  };
  // press runs keys on the lead's conversation, outside any text field.
  const press = async (keys) => {
    await x.lead.click();
    await x.lead.focus();
    await page.keyboard.press(keys);
  };
  // Each part starts from its own keys and runs even when one before it
  // failed, so that each finding is told apart.
  const failures = [];
  const part = async (name, fn) => {
    try { await fn(); } catch (err) { failures.push(`${name}: ${err.message.split('\n').slice(0, 6).join(' ')}`); }
  };

  // Reset all follows the keys: off with none changed, on after the first
  // change saves, off again after resetting, without leaving the page.
  await part('reset-all', async () => {
    await restart({});
    await openShortcuts();
    assert.equal(await resetAll.isDisabled(), true);
    await row('pane.info').locator('.shortcut-add').click();
    await page.keyboard.press(`${MOD}+Shift+KeyJ`);
    await page.locator('#shortcut-recorder').waitFor({ state: 'detached' });
    assert.deepEqual(await kept(), { 'pane.info': ['Mod+Shift+J'] });
    assert.equal(await resetAll.isDisabled(), false);
    await resetAll.click();
    assert.match(await page.locator('#confirm-dialog').innerText(), /1 action has keys of its own/);
    await page.locator('#confirm-ok').click();
    await page.waitForFunction(() => calls.some((c) => c[0] === 'ResetShortcuts'));
    await page.waitForFunction(() => document.querySelector('#shortcut-reset-all')?.disabled === true);
    assert.deepEqual(await chips('pane.info'), []);
    check('Reset all turns on as soon as the first change saves, counts it, and off again after resetting');
  });

  // Esc cancels a recording whose keys another action has, from the
  // conflict's buttons too; nothing saves and the keys keep their action.
  await part('escape', async () => {
    await restart({});
    await openShortcuts();
    await row('pane.info').locator('.shortcut-add').click();
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.locator('.shortcut-conflict').waitFor();
    assert.equal(await page.evaluate(() => document.activeElement.matches('.shortcut-conflict .mds-button--primary')), true);
    const before = (await sets()).length;
    await page.keyboard.press('Escape');
    await page.locator('#shortcut-recorder').waitFor({ state: 'detached' });
    assert.equal(await page.locator('.shortcut-conflict').count(), 0);
    assert.equal(await page.evaluate(() => document.activeElement.matches('[data-action="pane.info"] .shortcut-add')), true);
    assert.equal((await sets()).length, before);
    assert.deepEqual(await chips('palette.open'), [cap('⌘K', 'Ctrl+K'), cap('⇧⌘P', 'Ctrl+Shift+P')]);
    assert.equal(await page.evaluate(() => document.querySelector('#app').dataset.view), 'settings');
    // Enter on the conflict's choice still takes the keys.
    await row('pane.info').locator('.shortcut-add').click();
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.locator('.shortcut-conflict').waitFor();
    await page.keyboard.press('Enter');
    await page.locator('#shortcut-recorder').waitFor({ state: 'detached' });
    assert.deepEqual(await kept(), { 'pane.info': ['Mod+K'], 'palette.open': ['Mod+Shift+P'] });
    await press(`${MOD}+KeyK`);
    assert.equal(await x.infoShown(), true);
    assert.equal(await x.palette.isVisible(), false);
    check('Esc on a conflict’s buttons cancels the recording and saves nothing; Enter there still reassigns, and the keys run their new action');
  });

  // Two removals before the first saves: the second goes out after the
  // first answers, from what it saved, and both keys are gone.
  await part('two-removals', async () => {
    await restart({});
    await openShortcuts();
    await page.evaluate(() => { holdSaves = true; });
    await row('palette.open').locator('.shortcut-chip button').nth(0).click();
    await page.waitForFunction(() => held.length === 1);
    await row('palette.open').locator('.shortcut-chip button').nth(1).click();
    await page.waitForTimeout(100); // a second request would have gone out by now
    assert.equal((await sets()).length, 1);
    await answer();
    await page.waitForFunction(() => held.length === 1);
    await answer();
    await page.waitForFunction(() => document.querySelectorAll('[data-action="palette.open"] .shortcut-chip').length === 0);
    assert.deepEqual(await sets(), [{ 'palette.open': ['Mod+Shift+P'] }, { 'palette.open': [] }]);
    assert.deepEqual(await kept(), { 'palette.open': [] });
    // Removals on two actions, both pending: each keeps its own.
    await row('pane.files').locator('.shortcut-chip button').click();
    await row('pane.board').locator('.shortcut-chip button').click();
    await page.waitForFunction(() => held.length === 1);
    await answer();
    await page.waitForFunction(() => held.length === 1);
    await answer();
    await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.board"] .shortcut-chip').length === 0);
    assert.deepEqual(await kept(), { 'palette.open': [], 'pane.files': [], 'pane.board': [] });
    assert.equal(await heldCount(), 0);
    await page.evaluate(() => { holdSaves = false; });
    await press(`${MOD}+KeyK`);
    assert.equal(await x.palette.isVisible(), false);
    await page.keyboard.press(`${MOD}+Shift+KeyP`);
    assert.equal(await x.palette.isVisible(), false);
    await page.keyboard.press(`${MOD}+Shift+KeyB`);
    assert.equal(await x.boardShown(), false);
    await x.load();
    await press(`${MOD}+KeyK`);
    assert.equal(await x.palette.isVisible(), false);
    check('removing two keys before the first saves removes both, one request after another; removals on two actions keep both; after a reload neither runs');
  });

  // A save that succeeds, then one that fails: the first stays, and its
  // removed keys stop running; the failure says so on its own action.
  await part('success-then-failure', async () => {
    await restart({ 'pane.info': ['Mod+Alt+E'] });
    await openShortcuts();
    await page.evaluate(() => { holdSaves = true; });
    await row('pane.info').locator('.shortcut-chip button').click();
    await page.waitForFunction(() => held.length === 1);
    await row('pane.board').locator('.shortcut-chip button').click();
    await answer(true);
    await page.waitForFunction(() => held.length === 1);
    await answer(false);
    await row('pane.board').getByText('Synthetic save failure').waitFor();
    assert.deepEqual(await kept(), {});
    assert.deepEqual(await chips('pane.info'), []);
    assert.deepEqual(await chips('pane.board'), [cap('⇧⌘B', 'Ctrl+Shift+B')]);
    assert.equal(await resetAll.isDisabled(), true);
    await page.evaluate(() => { holdSaves = false; });
    await press(`${MOD}+Alt+KeyE`);
    assert.equal(await x.infoShown(), false);
    await page.keyboard.press(`${MOD}+Shift+KeyB`);
    assert.equal(await x.boardShown(), true);
    await page.keyboard.press(`${MOD}+Shift+KeyB`);
    check('a save that succeeds before one that fails keeps its change: its removed key stops running, the failed action keeps its keys and shows why');
  });

  // A change that saves, then Reset all failing: the change stays, Reset all
  // stays on and says why.
  await part('success-then-reset-failure', async () => {
    await restart({ 'pane.info': ['Mod+Alt+E'] });
    await openShortcuts();
    await page.evaluate(() => { holdSaves = true; });
    await row('pane.board').locator('.shortcut-chip button').click();
    await page.waitForFunction(() => held.length === 1);
    await resetAll.click();
    await page.locator('#confirm-ok').click();
    // Reset all has been asked for once its button turns off.
    await page.waitForFunction(() => document.querySelector('#shortcut-reset-all')?.disabled === true);
    await answer(true);
    await page.waitForFunction(() => held.length === 1);
    await answer(false);
    await page.locator('.mds-toolbar [role="alert"]').getByText('Synthetic save failure').waitFor();
    assert.deepEqual(await kept(), { 'pane.info': ['Mod+Alt+E'], 'pane.board': [] });
    assert.deepEqual(await chips('pane.board'), []);
    assert.deepEqual(await chips('pane.info'), [cap('⌥⌘E', 'Ctrl+Alt+E')]);
    assert.equal(await resetAll.isDisabled(), false);
    await page.evaluate(() => { holdSaves = false; });
    await press(`${MOD}+Shift+KeyB`);
    assert.equal(await x.boardShown(), false);
    await page.keyboard.press(`${MOD}+Alt+KeyE`);
    assert.equal(await x.infoShown(), true);
    check('a change that saves before Reset all fails stays; Reset all stays available and says why; the keys run as saved');
  });
  // An earlier change that saves while recording draws the list again: the
  // focus stays on the recorder, or its conflict's button, so keys record
  // and Esc cancels.
  await part('redraw-while-recording', async () => {
    await restart({});
    await openShortcuts();
    const focused = () => page.evaluate(() => document.activeElement?.id || document.activeElement?.dataset.conflict || document.activeElement?.tagName);
    await page.evaluate(() => { holdSaves = true; });
    await row('pane.board').locator('.shortcut-chip button').click();
    await page.waitForFunction(() => held.length === 1);
    await row('pane.info').locator('.shortcut-add').click();
    assert.equal(await focused(), 'shortcut-recorder');
    await answer(true);
    await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.board"] .shortcut-chip').length === 0);
    assert.equal(await focused(), 'shortcut-recorder');
    await page.keyboard.press(`${MOD}+Shift+KeyJ`);
    await page.waitForFunction(() => held.length === 1);
    await answer(true);
    await page.locator('#shortcut-recorder').waitFor({ state: 'detached' });
    assert.deepEqual(await kept(), { 'pane.board': [], 'pane.info': ['Mod+Shift+J'] });
    // The same on a conflict's button, then Esc from there.
    await row('pane.files').locator('.shortcut-chip button').click();
    await page.waitForFunction(() => held.length === 1);
    await row('pane.info').locator('.shortcut-add').click();
    await page.keyboard.press(`${MOD}+KeyK`);
    await page.locator('.shortcut-conflict').waitFor();
    assert.equal(await focused(), 'use');
    await answer(true);
    await page.waitForFunction(() => document.querySelectorAll('[data-action="pane.files"] .shortcut-chip').length === 0);
    assert.equal(await focused(), 'use');
    await page.keyboard.press('Escape');
    await page.locator('#shortcut-recorder').waitFor({ state: 'detached' });
    assert.equal(await heldCount(), 0);
    assert.deepEqual(await kept(), { 'pane.board': [], 'pane.info': ['Mod+Shift+J'], 'pane.files': [] });
    await page.evaluate(() => { holdSaves = false; });
    check('an earlier change saving while recording keeps the focus on the recorder or its conflict’s button: keys record, Esc cancels');
  });

  if (failures.length) throw new Error(`saving parts failed:\n${failures.join('\n')}`);
}

// ---------- 6. Search on a system with other case rules ----------
// The browser speaks Turkish, where "I" lowers to a dotless "ı"; the
// page is in English. Searching "import" still finds Import and export.
async function search(x, check) {
  const { page, MOD } = x;
  assert.equal(await page.evaluate(() => 'Import'.toLocaleLowerCase()), 'ımport'); // the system's rules apply here
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  await page.keyboard.type('import');
  assert.match(await x.palette.locator('[role="option"]').first().innerText(), /Import and export/);
  await page.keyboard.press('Escape');
  await x.palette.waitFor({ state: 'hidden' });
  await page.keyboard.press(`${MOD}+Slash`);
  await page.locator('#shortcut-list').waitFor();
  await page.locator('#shortcut-search').fill('import');
  assert.deepEqual(await page.locator('#shortcut-list [data-action]').evaluateAll((els) => els.map((e) => e.dataset.action)), ['settings.page.config']);
  check('with Turkish case rules in the browser, "import" finds Import and export in the palette and in Settings › Shortcuts');
}

// ---------- 4. Language, theme, narrow ----------
async function display(x, check) {
  const { page, MOD, cap } = x;
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
  await x.textarea.click();
  await page.keyboard.press(`${MOD}+Slash`);
  await page.locator('#shortcut-list').waitFor();
  const list = await page.locator('.page-inner').innerText();
  assert.match(list, /显示全部命令/);
  assert.match(list, /内置按键/);
  assert.deepEqual(await page.locator('[data-action="palette.open"] .shortcut-chip .keycap').allInnerTexts(), [cap('⌘K', 'Ctrl+K'), cap('⇧⌘P', 'Ctrl+Shift+P')]);
  // Its translated label (textContent: WebKit gives no innerText inside the drawer as it opens).
  assert.match(await page.locator('#settings-nav [data-page="shortcuts"]').textContent(), /快捷键/);
  const fit = await page.evaluate(() => {
    const p = document.querySelector('.page');
    const rows = [...document.querySelectorAll('.shortcut-row')].map((r) => r.getBoundingClientRect());
    return { overflow: p.scrollWidth - p.clientWidth, rowsOutside: rows.filter((r) => r.right > p.getBoundingClientRect().right + 1).length,
      width: innerWidth, keycapFont: getComputedStyle(document.querySelector('.keycap')).fontFamily };
  });
  x.measure = { settings: fit };
  assert.equal(fit.overflow, 0);
  assert.equal(fit.rowsOutside, 0);
  if (out) await page.screenshot({ path: `${out}/settings-zh-dark-640-${x.engine}-${x.platform}.png` });
  await x.lead.focus();
  await page.keyboard.press(`${MOD}+KeyK`);
  await x.palette.waitFor({ state: 'visible' });
  assert.equal(await page.locator('.palette-input').getAttribute('placeholder'), '输入命令、yesman 或助手');
  const box = await x.palette.evaluate((d) => { const r = d.getBoundingClientRect(); return { left: r.left, right: r.right, width: r.width, view: innerWidth }; });
  x.measure.palette = box;
  assert.ok(box.left >= 0 && box.right <= box.view, JSON.stringify(box));
  if (out) await page.screenshot({ path: `${out}/palette-zh-dark-640-${x.engine}-${x.platform}.png` });
  await page.keyboard.press('Escape');
  check('Simplified Chinese, dark, 640px: Settings › Shortcuts and the palette fit, keycaps stay the platform’s');
}

const groups = { palette, settings, context, display, saving, search };
const results = [];
let failed = 0;
try {
  for (const engineName of ENGINES) {
    const browser = await playwright[engineName].launch({ headless: true });
    try {
      for (const platform of PLATFORMS) {
        for (const group of GROUPS) {
          const options = group === 'display' ? { locale: 'zh-Hans', viewport: { width: 640, height: 760 } }
            : group === 'context' ? { seed: { 'pane.info': ['Mod+Alt+E'], 'pane.board': ['Mod+Shift+B', 'Alt+K'] } }
              : group === 'saving' ? { seed: {} }
              : group === 'search' ? { browserLocale: 'tr-TR' } : {};
          const result = { group, engine: engineName, platform, checks: [], ok: false, started: new Date().toISOString() };
          let x;
          try {
            x = await open(browser, platform, options);
            x.engine = engineName;
            await groups[group](x, (name) => result.checks.push(name));
            assert.deepEqual(x.errors, []);
            assert.deepEqual(await x.page.evaluate(() => [...new Set(unmocked)]), []);
            result.ok = true;
          } catch (err) {
            failed++;
            result.error = err.stack || String(err);
            if (x && out) await x.page.screenshot({ path: `${out}/fail-${group}-${engineName}-${platform}.png` }).catch(() => {});
          } finally {
            if (x?.options) result.options = x.options;
            if (x?.measure) result.measure = x.measure;
            result.errors = x?.errors ?? [];
            await x?.context.close();
          }
          result.finished = new Date().toISOString();
          results.push(result);
          if (out) await writeFile(`${out}/${group}-${engineName}-${platform}.json`, JSON.stringify(result, null, 2));
          console.log(`${result.ok ? 'PASS' : 'FAIL'} ${group} ${engineName} ${platform}: ${result.checks.length} checks${result.ok ? '' : `\n${result.error}`}`);
        }
      }
    } finally {
      await browser.close();
    }
  }
} finally {
  server.close();
}
process.exitCode = failed ? 1 : 0;
