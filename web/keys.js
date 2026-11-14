// Keys: what a key press is, on the platform yesman runs on, and how a
// shortcut is written and shown. shortcuts.js binds them to actions.
//
// A shortcut is kept portably, as "Mod+Shift+K": Mod is ⌘ on macOS and Ctrl
// on Windows and Linux, so a settings file means the same keys on each.
// Ctrl is the Control key of a Mac (Ctrl and Mod are one key elsewhere);
// Alt is Option on a Mac. The Windows and Super keys belong to the system.
//
// A key is its letter in the keyboard's layout (the key's position when the
// layout is not Latin, or Option changed the character), a digit by its
// place in the number row, a function key, a named key, or the symbol it
// types. A symbol already says whether Shift was held ("?" is ⇧/ on a US
// keyboard), so Shift is not written with symbols.

export const PLATFORMS = ['mac', 'windows', 'linux'];

// The platform from MyGo's runtime, or else the browser's.
export function detectPlatform(runtime = globalThis.mygo, nav = globalThis.navigator) {
  switch (runtime?.platform) {
    case 'darwin': return 'mac';
    case 'win32': return 'windows';
    case 'linux': return 'linux';
  }
  const name = nav?.userAgentData?.platform || nav?.platform || '';
  if (/mac|iphone|ipad/i.test(name)) return 'mac';
  if (/win/i.test(name)) return 'windows';
  return 'linux';
}

export const NAMED_KEYS = ['Enter', 'Space', 'Tab', 'Escape', 'Backspace', 'Delete', 'Insert', 'Home', 'End',
  'PageUp', 'PageDown', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'];
const F_KEY = /^F(?:[1-9]|1[0-9]|2[0-4])$/;
const KEY_ALIASES = {
  ' ': 'Space', spacebar: 'Space', space: 'Space', esc: 'Escape', escape: 'Escape', return: 'Enter', enter: 'Enter',
  up: 'ArrowUp', down: 'ArrowDown', left: 'ArrowLeft', right: 'ArrowRight', del: 'Delete', ins: 'Insert',
  pgup: 'PageUp', pgdn: 'PageDown', plus: '+',
};
const NAMED_LOWER = Object.fromEntries(NAMED_KEYS.map((k) => [k.toLowerCase(), k]));
const MODIFIER_NAMES = {
  mod: 'mod', cmdorctrl: 'mod', commandorcontrol: 'mod', ctrl: 'ctrl', control: 'ctrl',
  alt: 'alt', option: 'alt', opt: 'alt', shift: 'shift', meta: 'meta', cmd: 'meta', command: 'meta',
};
// The keys that only modify others: pressing one alone is no shortcut.
const MODIFIER_KEYS = new Set(['Meta', 'Control', 'Alt', 'Shift', 'AltGraph', 'CapsLock', 'Fn', 'FnLock', 'Hyper',
  'Super', 'OS', 'Symbol', 'SymbolLock', 'NumLock', 'ScrollLock']);
// The symbol a key types on a US keyboard, for a key whose character Option
// changed or a dead key.
const CODE_SYMBOLS = {
  Backquote: '`', Minus: '-', Equal: '=', BracketLeft: '[', BracketRight: ']', Backslash: '\\', IntlBackslash: '\\',
  Semicolon: ';', Quote: "'", Comma: ',', Period: '.', Slash: '/',
  NumpadAdd: '+', NumpadSubtract: '-', NumpadMultiply: '*', NumpadDivide: '/', NumpadDecimal: '.',
};

const isLetter = (key) => /^\p{L}$/u.test(key);
const isDigit = (key) => /^[0-9]$/.test(key);
// A single character typed that is neither a letter nor a digit.
export const isSymbol = (key) => [...key].length === 1 && !isLetter(key) && !isDigit(key);
export const isFunctionKey = (key) => F_KEY.test(key);

// upper upper-cases a letter, unless its capital is more than one letter
// (ß stays ß), as Go's unicode.ToUpper does.
function upper(key) {
  const up = key.toUpperCase();
  return [...up].length === 1 ? up : key;
}

function normalizeKey(name) {
  if (typeof name !== 'string' || !name) return null;
  if ([...name].length === 1) {
    if (/[\p{C}\s]/u.test(name)) return name === ' ' ? 'Space' : null;
    return isLetter(name) ? upper(name) : name;
  }
  const lower = name.toLowerCase();
  if (KEY_ALIASES[lower]) return KEY_ALIASES[lower];
  if (NAMED_LOWER[lower]) return NAMED_LOWER[lower];
  if (F_KEY.test(name.toUpperCase())) return name.toUpperCase();
  return null;
}

// parseBinding reads a shortcut such as "Mod+Shift+K", or returns null.
// It takes the names Electron style accelerators use too (CmdOrCtrl, Cmd,
// Option), and "+" as a key in "Mod++".
export function parseBinding(text) {
  if (typeof text !== 'string' || !text || text.length > 64) return null;
  let parts = text.split('+');
  if (text === '+') parts = ['+'];
  else if (text.endsWith('++')) parts = [...parts.slice(0, -2), '+'];
  const key = normalizeKey(parts.pop());
  if (!key) return null;
  const chord = { mod: false, ctrl: false, alt: false, shift: false, meta: false, key };
  for (const part of parts) {
    const name = MODIFIER_NAMES[part.toLowerCase()];
    if (!name || chord[name]) return null;
    chord[name] = true;
  }
  if (isSymbol(key)) chord.shift = false; // the symbol says it
  return chord;
}

// formatBinding writes a chord the one way it is kept.
export function formatBinding(chord) {
  const mods = [chord.mod && 'Mod', chord.ctrl && 'Ctrl', chord.alt && 'Alt', chord.shift && !isSymbol(chord.key) && 'Shift', chord.meta && 'Meta'];
  return [...mods.filter(Boolean), chord.key].join('+');
}

export function canonicalBinding(text) {
  const chord = parseBinding(text);
  return chord ? formatBinding(chord) : null;
}

// toPhysical says which keys a chord holds on a platform: Mod is the
// Command key of a Mac and the Control key elsewhere.
export function toPhysical(chord, platform) {
  const mac = platform === 'mac';
  return {
    meta: chord.meta || (mac && chord.mod),
    ctrl: chord.ctrl || (!mac && chord.mod),
    alt: chord.alt,
    shift: chord.shift && !isSymbol(chord.key),
    key: chord.key,
  };
}

// toPortable is the chord a key press on a platform is kept as.
export function toPortable(phys, platform) {
  const mac = platform === 'mac';
  return {
    mod: mac ? phys.meta : phys.ctrl,
    ctrl: mac ? phys.ctrl : false,
    alt: phys.alt,
    shift: phys.shift && !isSymbol(phys.key),
    meta: mac ? false : phys.meta,
    key: phys.key,
  };
}

// physicalId names the keys held, the same for every binding of them.
export function physicalId(phys) {
  return `${phys.meta ? 'Meta+' : ''}${phys.ctrl ? 'Ctrl+' : ''}${phys.alt ? 'Alt+' : ''}${phys.shift ? 'Shift+' : ''}${phys.key}`;
}

export const bindingId = (text, platform) => {
  const chord = parseBinding(text);
  return chord ? physicalId(toPhysical(chord, platform)) : null;
};

function eventKey(e, platform) {
  const key = e.key ?? '';
  const code = e.code ?? '';
  if (/^[a-z]$/i.test(key)) return key.toUpperCase();
  // Another layout's letter, a character Option made, or a dead key: the
  // letter of the key's position.
  if (/^Key[A-Z]$/.test(code)) return code.slice(3);
  // The number row by position, as AZERTY types symbols there unshifted.
  if (/^Digit[0-9]$/.test(code)) return code.slice(5);
  if (/^Numpad[0-9]$/.test(code) && isDigit(key)) return key;
  if (F_KEY.test(key)) return key;
  const named = KEY_ALIASES[key.toLowerCase()] ?? (NAMED_KEYS.includes(key) ? key : null);
  if (named && named !== '+') return named;
  if ((e.altKey && platform === 'mac') || key === 'Dead' || key === 'Unidentified' || !key) return CODE_SYMBOLS[code] ?? null;
  return normalizeKey(key);
}

// readKey reads a keydown. kind is "key" with phys, the keys held, or why it
// is no shortcut: "ime" while an input method composes, "altgraph" for a
// character AltGr types, "modifier" for a modifier key alone, "unknown".
export function readKey(e, platform) {
  if (e.isComposing || e.keyCode === 229) return { kind: 'ime' };
  const held = { meta: !!e.metaKey, ctrl: !!e.ctrlKey, alt: !!e.altKey, shift: !!e.shiftKey };
  if (MODIFIER_KEYS.has(e.key)) return { kind: 'modifier', held };
  // AltGr reports Ctrl and Alt on Windows, and types a character: one that
  // is not the key's own letter or digit is taken to be AltGr's. A Mac has
  // no AltGr: Option is Alt.
  if (platform !== 'mac' && (e.getModifierState?.('AltGraph')
    || (held.ctrl && held.alt && [...(e.key ?? '')].length === 1 && !/^[a-z0-9]$/i.test(e.key)))) return { kind: 'altgraph', held };
  const key = eventKey(e, platform);
  if (!key) return { kind: 'unknown', held };
  return { kind: 'key', phys: { ...held, shift: held.shift && !isSymbol(key), key } };
}

// ---------- What a shortcut may be ----------

// A Mac moves and deletes through text with ⌘, ⌥ or ⌃ and these keys;
// Windows and Linux with Ctrl, Shift and Alt.
const CARET_KEYS = new Set(['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown', 'Backspace', 'Delete', 'Insert']);
const OWN_JOB_KEYS = new Set(['Enter', 'Space', 'Tab', 'Escape']);

// Reserved keys, as on each platform's keyboard. system: the system and
// yesman's menus take them before the page; editing: copy, paste and undo.
const RESERVED = {
  mac: {
    system: ['Cmd+Q', 'Cmd+Shift+Q', 'Cmd+Alt+Shift+Q', 'Cmd+Ctrl+Q', 'Cmd+H', 'Cmd+Alt+H', 'Cmd+M', 'Cmd+Alt+M',
      'Cmd+W', 'Cmd+Alt+W', 'Cmd+`', 'Cmd+~', 'Cmd+Shift+3', 'Cmd+Shift+4', 'Cmd+Shift+5', 'Cmd+Shift+6',
      'Cmd+Ctrl+Shift+3', 'Cmd+Ctrl+Shift+4', 'Cmd+Alt+D', 'Cmd+Ctrl+D', 'Cmd+Ctrl+F', 'Cmd+R', 'Cmd+Shift+R',
      'Cmd+Alt+I', 'Cmd+0', 'Cmd+=', 'Cmd++', 'Cmd+-', 'Shift+F10'],
    editing: ['Cmd+Z', 'Cmd+Shift+Z', 'Cmd+X', 'Cmd+C', 'Cmd+V', 'Cmd+Alt+Shift+V', 'Cmd+A'],
  },
  windows: {
    system: ['Alt+F4', 'F10', 'Shift+F10'],
    editing: ['Ctrl+Z', 'Ctrl+Y', 'Ctrl+Shift+Z', 'Ctrl+X', 'Ctrl+C', 'Ctrl+V', 'Ctrl+A'],
  },
  linux: {
    system: ['Alt+F4', 'Alt+F1', 'Alt+F2', 'F10', 'Shift+F10', 'Ctrl+Alt+T', 'Ctrl+Alt+L'],
    editing: ['Ctrl+Z', 'Ctrl+Y', 'Ctrl+Shift+Z', 'Ctrl+X', 'Ctrl+C', 'Ctrl+V', 'Ctrl+A'],
  },
};
const reservedIds = Object.fromEntries(PLATFORMS.map((p) => [p, Object.fromEntries(Object.entries(RESERVED[p])
  .map(([kind, list]) => [kind, new Set(list.map((b) => bindingId(b, p)))]))]));

// bindingProblem says why a chord cannot be a shortcut on a platform, or
// null: "meta" (the Windows or Super key), "key" (Enter, Space, Tab, Esc
// keep their jobs), "modifier" (a letter, digit or symbol needs ⌘, ⌃ or ⌥),
// "caret" (moving or deleting through text), "system", "editing". bare
// lets a context action take the keys it names without a modifier.
export function bindingProblem(chord, platform, { bare = [] } = {}) {
  if (!chord) return 'invalid';
  const p = toPhysical(chord, platform);
  const id = physicalId(p);
  if (platform !== 'mac' && p.meta) return 'meta';
  const plain = !p.meta && !p.ctrl && !p.alt && !p.shift;
  if (plain && bare.includes(p.key)) return null;
  if (OWN_JOB_KEYS.has(p.key)) return 'key';
  if (CARET_KEYS.has(p.key)) {
    if (platform === 'mac') {
      if ([p.meta, p.alt, p.ctrl].filter(Boolean).length < 2) return 'caret';
    } else if (!(p.key.startsWith('Page') && (p.ctrl || p.alt))) return 'caret';
  }
  if (!F_KEY.test(p.key) && !CARET_KEYS.has(p.key) && !p.meta && !p.ctrl && !p.alt) return 'modifier';
  if (reservedIds[platform].system.has(id)) return 'system';
  if (reservedIds[platform].editing.has(id)) return 'editing';
  return null;
}

// firesInText says whether a shortcut works while the caret is in a text
// field: only with Mod (⌘, or Ctrl elsewhere) or on a function key, so
// typing, Option characters and a Mac's Control editing keys stay text.
export function firesInText(chord, platform) {
  const p = toPhysical(chord, platform);
  return F_KEY.test(p.key) || (platform === 'mac' ? p.meta : p.ctrl);
}

// mayBeAltGr says whether a chord is one a keyboard with AltGr types a
// character with on Windows and Linux.
export function mayBeAltGr(chord, platform) {
  const p = toPhysical(chord, platform);
  return platform !== 'mac' && p.ctrl && p.alt && [...p.key].length === 1;
}

// ---------- Showing keys ----------

const MAC_KEYS = { ArrowUp: '↑', ArrowDown: '↓', ArrowLeft: '←', ArrowRight: '→', Enter: '↩', Backspace: '⌫', Delete: '⌦',
  Escape: '⎋', Tab: '⇥', Space: 'Space', PageUp: '⇞', PageDown: '⇟', Home: '↖', End: '↘', Insert: 'Insert' };
const PC_KEYS = { ArrowUp: 'Up', ArrowDown: 'Down', ArrowLeft: 'Left', ArrowRight: 'Right', Escape: 'Esc', PageUp: 'PgUp',
  PageDown: 'PgDn', Delete: 'Del', Insert: 'Ins' };
const SPOKEN_KEYS = { ArrowUp: 'Up Arrow', ArrowDown: 'Down Arrow', ArrowLeft: 'Left Arrow', ArrowRight: 'Right Arrow',
  PageUp: 'Page Up', PageDown: 'Page Down', ',': 'Comma', '.': 'Period', '/': 'Slash', ';': 'Semicolon', "'": 'Quote',
  '[': 'Left Bracket', ']': 'Right Bracket', '\\': 'Backslash', '-': 'Minus', '=': 'Equals', '`': 'Backtick', '+': 'Plus' };

// keycap is how a chord shows on a platform: text as a Mac writes it
// (⇧⌘K) or as Windows does (Ctrl+Shift+K), and spoken, in words.
export function keycap(chord, platform) {
  const p = toPhysical(chord, platform);
  if (platform === 'mac') {
    const mods = [p.ctrl && '⌃', p.alt && '⌥', p.shift && '⇧', p.meta && '⌘'].filter(Boolean);
    const words = [p.ctrl && 'Control', p.alt && 'Option', p.shift && 'Shift', p.meta && 'Command'].filter(Boolean);
    const key = MAC_KEYS[p.key] ?? p.key;
    const spokenKey = { Enter: 'Return', Backspace: 'Delete', Delete: 'Forward Delete' }[p.key] ?? SPOKEN_KEYS[p.key] ?? p.key;
    return { text: mods.join('') + key, spoken: [...words, spokenKey].join(' ') };
  }
  const mods = [p.ctrl && 'Ctrl', p.alt && 'Alt', p.shift && 'Shift', p.meta && (platform === 'windows' ? 'Win' : 'Super')].filter(Boolean);
  const key = PC_KEYS[p.key] ?? p.key;
  return { text: [...mods, key].join('+'), spoken: [...mods, SPOKEN_KEYS[p.key] ?? p.key].join(' ') };
}

// heldText shows the modifiers held while a shortcut is being recorded.
export function heldText(held, platform) {
  if (platform === 'mac') return [held.ctrl && '⌃', held.alt && '⌥', held.shift && '⇧', held.meta && '⌘'].filter(Boolean).join('');
  return [held.ctrl && 'Ctrl', held.alt && 'Alt', held.shift && 'Shift', held.meta && (platform === 'windows' ? 'Win' : 'Super')]
    .filter(Boolean).map((m) => `${m}+`).join('');
}

// ariaKeys is a chord as aria-keyshortcuts writes it.
export function ariaKeys(chord, platform) {
  const p = toPhysical(chord, platform);
  const key = p.key === 'Space' ? 'Space' : p.key;
  return [p.ctrl && 'Control', p.alt && 'Alt', p.shift && 'Shift', p.meta && 'Meta', key].filter(Boolean).join('+');
}
