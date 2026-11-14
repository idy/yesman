// The production keys.js, shortcuts.js and palette.js in Node: key
// normalization on each platform, binding rules, dispatch by context and
// conflicts. The browser checks (shortcuts-browser.mjs) run them on the page.
import assert from 'node:assert/strict';
import { test } from 'node:test';

globalThis.document ??= { querySelector: () => null };

const keys = await import('../web/keys.js');
const { createShortcuts } = await import('../web/shortcuts.js');
const { rank, score } = await import('../web/palette.js');
const { parseBinding, formatBinding, canonicalBinding, readKey, bindingProblem, keycap, firesInText, detectPlatform, toPortable, bindingId } = keys;

// A keydown as a browser reports it.
const key = (k, { code, meta = false, ctrl = false, alt = false, shift = false, composing = false, keyCode = 0, repeat = false, altGraph = false, target = null } = {}) => ({
  key: k, code: code ?? (/^[a-z]$/i.test(k) ? `Key${k.toUpperCase()}` : /^[0-9]$/.test(k) ? `Digit${k}` : ''),
  metaKey: meta, ctrlKey: ctrl, altKey: alt, shiftKey: shift, isComposing: composing, keyCode, repeat,
  getModifierState: (m) => m === 'AltGraph' && altGraph, target, defaultPrevented: false,
  preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this.stopped = true; },
});

// A focused element: a text field, the editor, a menu or anything else.
const el = (kind = 'div', { inMenu = false, matches = () => false } = {}) => ({
  nodeType: 1, tagName: kind === 'input' ? 'INPUT' : kind === 'textarea' ? 'TEXTAREA' : 'DIV', type: 'text',
  isContentEditable: kind === 'contenteditable',
  closest: (sel) => (sel === '.cm-editor' ? kind === 'editor' : sel.includes('role="menu"') ? inMenu : null),
  matches,
});

const portable = (e, platform) => {
  const read = readKey(e, platform);
  return read.kind === 'key' ? formatBinding(toPortable(read.phys, platform)) : read.kind;
};

test('the platform comes from MyGo, else the browser', () => {
  assert.equal(detectPlatform({ platform: 'darwin' }), 'mac');
  assert.equal(detectPlatform({ platform: 'win32' }), 'windows');
  assert.equal(detectPlatform({ platform: 'linux' }), 'linux');
  assert.equal(detectPlatform(undefined, { platform: 'MacIntel' }), 'mac');
  assert.equal(detectPlatform(undefined, { userAgentData: { platform: 'Windows' } }), 'windows');
  assert.equal(detectPlatform(undefined, { platform: '' }), 'linux');
});

test('bindings parse portably and write one way', () => {
  for (const [input, want] of [
    ['Mod+K', 'Mod+K'], ['shift+mod+k', 'Mod+Shift+K'], ['CmdOrCtrl+Shift+P', 'Mod+Shift+P'], ['Cmd+Option+Down', 'Alt+Meta+ArrowDown'],
    ['Control+alt+pgdn', 'Ctrl+Alt+PageDown'], ['Mod+,', 'Mod+,'], ['Mod+Shift+/', 'Mod+/'], ['Mod++', 'Mod++'], ['Mod+Plus', 'Mod++'],
    ['f5', 'F5'], ['Mod+ö', 'Mod+Ö'], ['Mod+ß', 'Mod+ß'], ['Mod+ ', 'Mod+Space'], ['Ctrl+Meta+Mod+Q', 'Mod+Ctrl+Meta+Q'],
  ]) assert.equal(canonicalBinding(input), want, input);
  for (const bad of ['', 'Mod', 'Mod+', 'Mod+Mod+K', 'Hyper+K', 'Mod+F25', 'Mod+KK', 'Mod+\u0001', 'Mod+\u200b']) {
    assert.equal(parseBinding(bad), null, bad);
  }
});

test('Go and the page keep the same canonical form', async () => {
  // shortcuts_test.go checks the same table on the Go side.
  const table = { 'Shift+Mod+k': 'Mod+Shift+K', 'option+shift+PAGEUP': 'Alt+Shift+PageUp', 'Mod+Shift+1': 'Mod+Shift+1', 'Alt+F24': 'Alt+F24' };
  for (const [input, want] of Object.entries(table)) assert.equal(canonicalBinding(input), want);
});

test('key presses read the same on each layout and platform', () => {
  // ⌘K on a Mac, Ctrl+K on Windows: the same portable binding.
  assert.equal(portable(key('k', { meta: true }), 'mac'), 'Mod+K');
  assert.equal(portable(key('k', { ctrl: true }), 'windows'), 'Mod+K');
  // A Mac's Control key and Windows' Win key stay what they are.
  assert.equal(portable(key('k', { ctrl: true }), 'mac'), 'Ctrl+K');
  assert.equal(portable(key('k', { meta: true }), 'windows'), 'Meta+K');
  // Option changes the character; the key's position names it.
  assert.equal(portable(key('˚', { code: 'KeyK', alt: true, meta: true }), 'mac'), 'Mod+Alt+K');
  assert.equal(portable(key('å', { code: 'KeyA', alt: true }), 'mac'), 'Alt+A');
  // A Russian layout's letter, by position; a German one's own letter.
  assert.equal(portable(key('л', { code: 'KeyK', ctrl: true }), 'windows'), 'Mod+K');
  assert.equal(portable(key('ö', { code: 'Semicolon', ctrl: true }), 'windows'), 'Mod+Ö');
  // AZERTY's digit row types symbols unshifted: digits by position.
  assert.equal(portable(key('&', { code: 'Digit1', meta: true }), 'mac'), 'Mod+1');
  assert.equal(portable(key('!', { code: 'Digit1', meta: true, shift: true }), 'mac'), 'Mod+Shift+1');
  // A shifted symbol is its own key, without Shift.
  assert.equal(portable(key('?', { code: 'Slash', meta: true, shift: true }), 'mac'), 'Mod+?');
  assert.equal(portable(key('/', { code: 'Slash', ctrl: true }), 'windows'), 'Mod+/');
  // Named and function keys.
  assert.equal(portable(key('ArrowDown', { code: 'ArrowDown', alt: true, meta: true }), 'mac'), 'Mod+Alt+ArrowDown');
  assert.equal(portable(key('PageDown', { code: 'PageDown', ctrl: true }), 'windows'), 'Mod+PageDown');
  assert.equal(portable(key('F5', { code: 'F5', shift: true }), 'linux'), 'Shift+F5');
  // Not shortcuts: an input method composing, AltGr's characters, a modifier.
  assert.equal(portable(key('Process', { composing: true, ctrl: true }), 'windows'), 'ime');
  assert.equal(portable(key('k', { keyCode: 229, meta: true }), 'mac'), 'ime');
  assert.equal(portable(key('€', { code: 'KeyE', ctrl: true, alt: true }), 'windows'), 'altgraph');
  assert.equal(portable(key('e', { code: 'KeyE', ctrl: true, alt: true, altGraph: true }), 'linux'), 'altgraph');
  assert.equal(portable(key('@', { code: 'KeyQ', ctrl: true, alt: true }), 'windows'), 'altgraph');
  assert.equal(portable(key('Meta', { code: 'MetaLeft', meta: true }), 'mac'), 'modifier');
  assert.equal(portable(key('Shift', { code: 'ShiftLeft', shift: true }), 'windows'), 'modifier');
  // Ctrl+Alt+E on a keyboard without AltGr is a shortcut.
  assert.equal(portable(key('e', { code: 'KeyE', ctrl: true, alt: true }), 'windows'), 'Mod+Alt+E');
});

test('a binding is refused where the platform or text editing needs its keys', () => {
  const problem = (b, p, opts) => bindingProblem(parseBinding(b), p, opts);
  assert.equal(problem('Mod+K', 'mac'), null);
  assert.equal(problem('Mod+K', 'windows'), null);
  assert.equal(problem('Meta+K', 'windows'), 'meta');
  assert.equal(problem('Meta+K', 'linux'), 'meta');
  assert.equal(problem('Meta+K', 'mac'), null); // Meta is ⌘, the same as Mod there
  for (const b of ['K', 'Shift+K', '1', 'Shift+/', '?']) assert.equal(problem(b, 'mac'), 'modifier', b);
  for (const b of ['Mod+Enter', 'Mod+Space', 'Mod+Tab', 'Alt+Tab', 'Escape', 'Mod+Escape']) assert.equal(problem(b, 'windows'), 'key', b);
  assert.equal(problem('Mod+C', 'mac'), 'editing');
  assert.equal(problem('Mod+V', 'windows'), 'editing');
  assert.equal(problem('Mod+Z', 'linux'), 'editing');
  assert.equal(problem('Mod+Shift+Z', 'mac'), 'editing');
  assert.equal(problem('Mod+Y', 'windows'), 'editing');
  assert.equal(problem('Mod+Y', 'mac'), null); // ⌘Y is free on a Mac
  for (const b of ['Mod+Q', 'Mod+W', 'Mod+H', 'Mod+M', 'Mod+R', 'Mod+0', 'Mod+=', 'Mod+-', 'Mod+Alt+I', 'Mod+Ctrl+F', 'Mod+`']) assert.equal(problem(b, 'mac'), 'system', b);
  assert.equal(problem('Alt+F4', 'windows'), 'system');
  assert.equal(problem('Shift+F10', 'windows'), 'system');
  assert.equal(problem('Mod+Q', 'windows'), null); // Windows has no ⌘Q
  // Moving and deleting through text keep their keys.
  for (const b of ['Mod+ArrowLeft', 'Alt+ArrowUp', 'Mod+Backspace', 'Ctrl+ArrowUp', 'Shift+ArrowDown']) assert.equal(problem(b, 'mac'), 'caret', b);
  for (const b of ['Mod+ArrowLeft', 'Mod+Home', 'Mod+Backspace', 'Mod+Insert', 'Shift+Insert']) assert.equal(problem(b, 'windows'), 'caret', b);
  assert.equal(problem('Mod+Alt+ArrowDown', 'mac'), null);
  assert.equal(problem('Mod+PageDown', 'windows'), null);
  assert.equal(problem('Alt+PageUp', 'windows'), null);
  assert.equal(problem('F5', 'windows'), null);
  // A context action may take a bare key it names, and only that one.
  assert.equal(problem('ArrowUp', 'mac', { bare: ['ArrowUp'] }), null);
  assert.equal(problem('ArrowUp', 'mac'), 'caret');
  assert.equal(problem('ArrowDown', 'mac', { bare: ['ArrowUp'] }), 'caret');
});

test('keycaps show as each platform writes them, and in words', () => {
  const cap = (b, p) => keycap(parseBinding(b), p);
  assert.deepEqual(cap('Mod+Shift+E', 'mac'), { text: '⇧⌘E', spoken: 'Shift Command E' });
  assert.deepEqual(cap('Mod+Shift+E', 'windows'), { text: 'Ctrl+Shift+E', spoken: 'Ctrl Shift E' });
  assert.deepEqual(cap('Ctrl+Alt+K', 'mac'), { text: '⌃⌥K', spoken: 'Control Option K' });
  assert.equal(cap('Mod+Alt+ArrowDown', 'mac').text, '⌥⌘↓');
  assert.equal(cap('Mod+PageDown', 'windows').text, 'Ctrl+PgDn');
  assert.equal(cap('Mod+,', 'mac').spoken, 'Command Comma');
  assert.equal(cap('Mod+K', 'linux').text, 'Ctrl+K');
  assert.equal(cap('Meta+K', 'mac').text, '⌘K');
});

test('only shortcuts with ⌘, or Ctrl elsewhere, work while typing', () => {
  assert.equal(firesInText(parseBinding('Mod+K'), 'mac'), true);
  assert.equal(firesInText(parseBinding('Alt+K'), 'mac'), false); // types ˚
  assert.equal(firesInText(parseBinding('Ctrl+K'), 'mac'), false); // deletes to the line's end
  assert.equal(firesInText(parseBinding('Mod+K'), 'windows'), true);
  assert.equal(firesInText(parseBinding('Alt+K'), 'windows'), false);
  assert.equal(firesInText(parseBinding('F6'), 'windows'), true);
});

function setup(platform, actions, overrides = {}) {
  const calls = [];
  const saved = { current: overrides };
  const s = createShortcuts({
    platform, builtIn: ['Mod+S'],
    call: async (method, arg) => {
      calls.push([method, arg]);
      if (method === 'Shortcuts') return saved.current;
      if (method === 'SetShortcuts') {
        const next = { ...saved.current };
        for (const [id, list] of Object.entries(arg)) { if (list === null) delete next[id]; else next[id] = list; }
        saved.current = next;
        return next;
      }
      if (method === 'ResetShortcuts') { saved.current = {}; return {}; }
      throw new Error(method);
    },
  });
  s.register(actions);
  return { s, calls, saved };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

test('dispatch runs a matched action once, and only then prevents the key', async () => {
  const ran = [];
  let enabled = true;
  const { s } = setup('mac', [
    { id: 'palette.open', label: 'Palette', keys: ['Mod+K'], run: () => ran.push('palette') },
    { id: 'pane.files', label: 'Files', keys: ['Mod+Shift+E'], when: () => (enabled ? true : 'Open a conversation first.'), run: () => ran.push('files') },
    { id: 'move.next', label: 'Next', keys: { mac: ['Mod+Alt+ArrowDown'], other: ['Ctrl+PageDown'] }, repeat: true, run: () => ran.push('next') },
    { id: 'away', label: 'Away', keys: ['Alt+K'], run: () => ran.push('away') },
  ]);
  await s.load();
  const body = el();
  let e = key('k', { meta: true, target: body });
  s.onKeyDown(e);
  await tick();
  assert.deepEqual(ran, ['palette']);
  assert.equal(e.defaultPrevented, true);
  assert.equal(e.stopped, true);
  // Held down, it does not run again; a repeating action does.
  e = key('k', { meta: true, target: body, repeat: true });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, true);
  s.onKeyDown(key('ArrowDown', { code: 'ArrowDown', meta: true, alt: true, repeat: true, target: body }));
  await tick();
  assert.deepEqual(ran, ['palette', 'next']);
  // An action that cannot run now leaves the key alone.
  enabled = false;
  e = key('e', { meta: true, shift: true, target: body });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, false);
  // Keys no action has, copy among them, stay the page's.
  e = key('c', { meta: true, target: el('textarea') });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, false);
  // Typing in a field: ⌥K types ˚ there, and runs its action elsewhere.
  e = key('˚', { code: 'KeyK', alt: true, target: el('textarea') });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, false);
  s.onKeyDown(key('˚', { code: 'KeyK', alt: true, target: body }));
  await tick();
  assert.deepEqual(ran, ['palette', 'next', 'away']);
  // ⌘K works in a field, the editor and a contenteditable.
  for (const kind of ['input', 'textarea', 'editor', 'contenteditable']) s.onKeyDown(key('k', { meta: true, target: el(kind) }));
  await tick();
  assert.equal(ran.filter((r) => r === 'palette').length, 5);
  // Ctrl+K on a Mac is not ⌘K.
  e = key('k', { ctrl: true, target: body });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, false);
});

test('dispatch leaves keys to input methods, AltGr, menus, dialogs, recording and owners', async () => {
  const ran = [];
  const { s } = setup('windows', [
    { id: 'palette.open', label: 'Palette', keys: ['Mod+K'], run: () => ran.push('palette') },
    { id: 'euro', label: 'Euro', keys: ['Mod+Alt+E'], run: () => ran.push('euro') },
  ]);
  await s.load();
  const body = el();
  s.onKeyDown(key('Process', { code: 'KeyK', ctrl: true, composing: true, target: body }));
  s.onKeyDown(key('k', { ctrl: true, keyCode: 229, target: body }));
  s.onKeyDown(key('€', { code: 'KeyE', ctrl: true, alt: true, target: el('textarea') }));
  s.onKeyDown(key('k', { ctrl: true, target: el('div', { inMenu: true }) }));
  const owned = key('k', { ctrl: true, target: body });
  owned.defaultPrevented = true; // the editor or the composer used it
  s.onKeyDown(owned);
  s.recording = true;
  s.onKeyDown(key('k', { ctrl: true, target: body }));
  s.recording = false;
  globalThis.document = { querySelector: (sel) => (sel === 'dialog[open]' ? {} : null) };
  s.onKeyDown(key('k', { ctrl: true, target: body }));
  globalThis.document = { querySelector: () => null };
  await tick();
  assert.deepEqual(ran, []);
  // The Win key is not Ctrl.
  const win = key('k', { meta: true, target: body });
  s.onKeyDown(win);
  assert.equal(win.defaultPrevented, false);
  s.onKeyDown(key('k', { ctrl: true, target: body }));
  await tick();
  assert.deepEqual(ran, ['palette']);
});

test('a context action takes a bare key only where its context says', async () => {
  const ran = [];
  let empty = true;
  const composer = el('textarea', { matches: () => true });
  const { s } = setup('mac', [
    { id: 'composer.recall', label: 'Recall', keys: ['ArrowUp'], context: { id: 'composer', label: 'in the message box', test: (target) => target === composer, bare: ['ArrowUp'] },
      when: () => (empty ? true : 'The message box has text.'), run: () => ran.push('recall') },
  ]);
  await s.load();
  assert.equal(s.bindingsOf('composer.recall')[0].problem, null);
  // In a text field elsewhere, ArrowUp moves the caret.
  let e = key('ArrowUp', { code: 'ArrowUp', target: el('textarea') });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, false);
  // In the composer with text, it moves the caret too.
  empty = false;
  e = key('ArrowUp', { code: 'ArrowUp', target: composer });
  s.onKeyDown(e);
  assert.equal(e.defaultPrevented, false);
  empty = true;
  s.onKeyDown(key('ArrowUp', { code: 'ArrowUp', target: composer }));
  await tick();
  assert.deepEqual(ran, ['recall']);
});

test('overrides, defaults, conflicts and saving', async () => {
  const { s, calls, saved } = setup('windows', [
    { id: 'a.one', label: 'One', keys: ['Mod+K'], run() {} },
    { id: 'a.two', label: 'Two', keys: ['Mod+J'], run() {} },
    { id: 'a.three', label: 'Three', run() {} },
    { id: 'ctx.one', label: 'Context', keys: ['Mod+J'], context: { id: 'x', label: 'x', test: () => false }, run() {} },
  ], { 'a.three': ['Mod+K', 'Meta+K', 'Mod+S', 'Nope'], 'a.two': [], unknown: ['Mod+U'] });
  await s.load();
  assert.deepEqual(s.bindingsOf('a.two'), []);
  assert.deepEqual(s.bindingsOf('a.three').map((b) => b.problem), [null, 'meta', 'builtin', 'invalid']);
  // Two actions with the same keys: the one chosen over defaults runs.
  assert.equal(s.winner('Mod+K').id, 'a.three');
  assert.deepEqual(s.conflicts('a.one', 'Mod+K').map((a) => a.id), ['a.three']);
  // A context action does not conflict with one that works everywhere.
  assert.deepEqual(s.conflicts('a.one', 'Mod+J'), []);
  assert.deepEqual(s.keycaps('a.three').map((k) => k.text), ['Ctrl+K']);
  assert.equal(s.hint('Three', 'a.three'), 'Three (Ctrl+K)');
  assert.equal(s.primary('a.two'), '');
  // Changes save in order; the newest answer wins.
  const first = s.update({ 'a.two': ['Mod+Shift+J'] });
  const second = s.update({ 'a.two': null, 'a.one': ['Mod+Shift+K'] });
  await Promise.all([first, second]);
  assert.deepEqual(calls.filter((c) => c[0] === 'SetShortcuts').map((c) => c[1]), [{ 'a.two': ['Mod+Shift+J'] }, { 'a.two': null, 'a.one': ['Mod+Shift+K'] }]);
  assert.deepEqual(s.bindingsOf('a.two').map((b) => b.binding), ['Mod+J']);
  assert.equal(s.isCustom('a.two'), false);
  assert.deepEqual(saved.current.unknown, ['Mod+U']); // kept for another yesman
  await s.resetAll();
  assert.deepEqual(s.overrides(), {});
  assert.equal(s.winner('Mod+K').id, 'a.one');
  // A failed save leaves the keys as they were.
  const failing = createShortcuts({ platform: 'mac', call: async (m) => { if (m === 'Shortcuts') return {}; throw new Error('disk full'); } });
  failing.register({ id: 'a.one', label: 'One', keys: ['Mod+K'], run() {} });
  await failing.load();
  await assert.rejects(failing.update({ 'a.one': [] }), /disk full/);
  assert.deepEqual(failing.bindingsOf('a.one').map((b) => b.binding), ['Mod+K']);
});

test('the same keys keep their portable meaning across platforms', () => {
  assert.equal(bindingId('Mod+K', 'mac'), 'Meta+K');
  assert.equal(bindingId('Mod+K', 'windows'), 'Ctrl+K');
  assert.equal(bindingId('Ctrl+K', 'windows'), 'Ctrl+K'); // one key on Windows
  assert.equal(bindingId('Ctrl+K', 'mac'), 'Ctrl+K');
});

test('actions register once, with stable IDs', () => {
  const s = createShortcuts({ platform: 'mac', call: async () => ({}) });
  s.register({ id: 'a.one', label: 'One', run() {} });
  assert.throws(() => s.register({ id: 'a.one', label: 'Again', run() {} }), /twice/);
  assert.throws(() => s.register({ id: 'Bad ID', label: 'Bad', run() {} }), /Bad action ID/);
});

test('the palette ranks by label, then group and keywords', () => {
  const entries = [
    { label: 'Show or hide the files', group: 'Panels', keywords: ['explorer'], keys: ['⇧⌘E'] },
    { label: 'Open Settings', group: 'General', keywords: ['preferences'] },
    { label: 'Settings: Shortcuts', group: 'Settings', keywords: [] },
    { label: 'Fetch all repositories', group: 'Settings', keywords: ['pool'] },
  ];
  assert.deepEqual(rank(entries, 'set').map((e) => e.label), ['Settings: Shortcuts', 'Open Settings', 'Fetch all repositories']);
  assert.deepEqual(rank(entries, 'explorer').map((e) => e.label), ['Show or hide the files']);
  assert.deepEqual(rank(entries, 'open set').map((e) => e.label), ['Open Settings']);
  assert.deepEqual(rank(entries, '⇧⌘E').map((e) => e.label), ['Show or hide the files']);
  assert.equal(rank(entries, '').length, 4);
  assert.equal(score('shf', 'show or hide the files'), 10);
  assert.equal(score('zz', 'files'), 0);
});
