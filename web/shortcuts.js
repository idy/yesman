// Shortcuts: the actions the window can take, by a stable ID, and the keys
// that take them. The command palette, Settings › Shortcuts and the keys
// all run the same action, with its own check of whether it can run now.
//
// An action:
//
//   {
//     id: 'pane.files',                 stable; settings keep keys by it
//     label: 'Show or hide the files',  English; said in the page's language,
//     params: { n: 1 },                 with {names} filled in
//     group: 'Panels',                  where Settings and the palette list it
//     keys: ['Mod+Shift+E'],            default keys, or { mac: [...], other: [...] }
//     keywords: ['explorer'],           more words the palette finds it by
//     when(ctx) { return true },        or why it cannot run now, in English
//     run(ctx) { … },                   ctx: { source: 'key' | 'palette', target }
//     repeat: false,                    whether holding the keys repeats it
//     palette: true,                    whether the palette lists it
//     context: undefined,               see below
//   }
//
// Dispatch runs after the page's own handlers, on the document's keydown.
// A key a field, the editor, the composer or a menu used (defaultPrevented)
// stays theirs, as does every key while an input method composes, a modal
// dialog (the palette among them) or a menu is open, or a shortcut is being
// recorded. In text fields and the editor only shortcuts with Mod work
// (shortcuts with ⌥ or ⌃ on a Mac type or edit there). Only a key an
// action takes is prevented; a key whose action cannot run now goes on as
// if it had none.
//
// A context action takes keys only where its context says, and before the
// actions that take the same keys everywhere:
//
//   context: { id: 'composer', label: 'In the message box',
//              test: (target) => target.matches('.composer textarea'),
//              bare: ['ArrowUp'] }       keys it may take without a modifier
//
// Nothing here takes a bare key such as ArrowUp in a text field unless a
// context action names it.

import {
  parseBinding, formatBinding, toPhysical, toPortable, physicalId, readKey, bindingProblem, firesInText, keycap, ariaKeys,
} from './keys.js';

export const MAX_BINDINGS = 4;

// builtIn are keys with a job of their own here (⌘S saves the open file),
// which no action can take.
export function createShortcuts({ platform, call, fail = () => {}, t = (s) => s, builtIn = [] }) {
  const builtInIds = new Set(builtIn.map((b) => physicalId(toPhysical(parseBinding(b), platform))));
  const actions = new Map(); // id -> action, in the order registered
  let overrides = {}; // id -> [binding], as Settings › Shortcuts keeps them
  let index = new Map(); // physical id -> [{ action, binding, custom }], best first
  let ready = false;
  let recording = false;
  let composing = false;
  let saving = Promise.resolve();
  let saveSeq = 0;
  const listeners = new Set();

  const defaultsOf = (action) => {
    const keys = action.keys ?? [];
    return Array.isArray(keys) ? keys : (keys[platform] ?? keys.other ?? []);
  };

  function register(...list) {
    for (const action of list.flat()) {
      if (!/^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$/.test(action.id) || action.id.length > 64) throw new Error(`Bad action ID: ${action.id}`);
      if (actions.has(action.id)) throw new Error(`Action registered twice: ${action.id}`);
      actions.set(action.id, action);
    }
    rebuild();
  }

  // bindingsOf lists an action's keys: its own, or its defaults. Each is
  // { binding, chord, problem }; a kept binding this platform cannot use
  // has its problem and takes no key.
  function bindingsOf(id) {
    const action = actions.get(id);
    if (!action) return [];
    const custom = Object.hasOwn(overrides, id);
    const list = custom ? overrides[id] : defaultsOf(action);
    return list.map((binding) => {
      const chord = parseBinding(binding);
      return { binding: chord ? formatBinding(chord) : binding, chord, custom, problem: problemOf(action, chord) };
    });
  }

  // problemOf says why a chord cannot be one of an action's keys here, or
  // null; see keys.js's bindingProblem, and "builtin".
  function problemOf(action, chord) {
    const problem = bindingProblem(chord, platform, { bare: action.context?.bare });
    if (problem) return problem;
    return builtInIds.has(physicalId(toPhysical(chord, platform))) ? 'builtin' : null;
  }

  const isCustom = (id) => Object.hasOwn(overrides, id);

  // labelOf says an action's label in the page's language; params, or a
  // function giving them, fill it in.
  const labelOf = (action) => t(action.label, typeof action.params === 'function' ? action.params() : action.params);

  function rebuild() {
    const next = new Map();
    let order = 0;
    for (const action of actions.values()) {
      for (const b of bindingsOf(action.id)) {
        if (b.problem) continue;
        const pid = physicalId(toPhysical(b.chord, platform));
        if (!next.has(pid)) next.set(pid, []);
        next.get(pid).push({ action, binding: b.binding, custom: b.custom, order: order++ });
      }
    }
    // A context action first; then keys the user chose over defaults; then
    // the order actions were registered in.
    for (const list of next.values()) list.sort((a, b) => (!!b.action.context - !!a.action.context) || (b.custom - a.custom) || (a.order - b.order));
    index = next;
    for (const fn of listeners) fn();
  }

  function setOverrides(next) {
    overrides = {};
    for (const [id, list] of Object.entries(next ?? {})) {
      if (Array.isArray(list)) overrides[id] = list.filter((b) => typeof b === 'string');
    }
    rebuild();
  }

  // ---------- What an action can do now ----------

  function state(action, ctx = {}) {
    let answer = true;
    try {
      answer = action.when ? action.when(ctx) : true;
    } catch (err) {
      answer = err?.message || String(err);
    }
    return answer === true || answer === undefined ? { enabled: true } : { enabled: false, reason: answer || 'Not available now.' };
  }

  async function run(id, ctx = {}) {
    const action = actions.get(id);
    if (!action) return false;
    const s = state(action, ctx);
    if (!s.enabled) return false;
    try {
      await action.run(ctx);
    } catch (err) {
      fail(err);
    }
    return true;
  }

  // ---------- Keys ----------

  const layerOpen = () => !!document.querySelector('dialog[open]');

  function inMenu(target) {
    return !!target?.closest?.('[role="menu"], [role="listbox"], [role="combobox"][aria-expanded="true"]');
  }

  function isEditable(target) {
    if (!target || target.nodeType !== 1) return false;
    if (target.isContentEditable || target.closest('.cm-editor')) return true;
    if (target.tagName === 'TEXTAREA' || target.tagName === 'SELECT') return true;
    if (target.tagName !== 'INPUT') return false;
    return !['button', 'checkbox', 'radio', 'range', 'color', 'file', 'image', 'reset', 'submit'].includes(target.type);
  }

  // match finds the action a keydown takes, the first that may take it
  // where it was pressed, or null. It does not check whether it can run.
  function match(e) {
    const read = readKey(e, platform);
    if (read.kind !== 'key') return null;
    const candidates = index.get(physicalId(read.phys));
    if (!candidates) return null;
    const target = e.target;
    const editable = isEditable(target);
    for (const c of candidates) {
      const context = c.action.context;
      if (context) {
        if (context.test(target)) return c;
        continue;
      }
      if (editable && !firesInText(parseBinding(c.binding), platform)) continue;
      return c;
    }
    return null;
  }

  // matches says whether a keydown is one of an action's keys, wherever it
  // was pressed: the palette closes on its own keys.
  function matches(e, id) {
    const read = readKey(e, platform);
    if (read.kind !== 'key') return false;
    return (index.get(physicalId(read.phys)) ?? []).some((c) => c.action.id === id);
  }

  function onKeyDown(e) {
    if (!ready || recording || composing || e.defaultPrevented) return;
    if (layerOpen() || inMenu(e.target)) return;
    const hit = match(e);
    if (!hit) return;
    const ctx = { source: 'key', target: e.target, event: e };
    if (!state(hit.action, ctx).enabled) return;
    e.preventDefault();
    e.stopPropagation();
    if (e.repeat && !hit.action.repeat) return;
    run(hit.action.id, ctx);
  }

  // listen takes the document's keys, after the handlers that come before
  // it, and follows input methods composing.
  function listen(doc = document) {
    doc.addEventListener('compositionstart', () => { composing = true; }, true);
    doc.addEventListener('compositionend', () => { composing = false; }, true);
    doc.addEventListener('keydown', onKeyDown);
    // A window losing focus mid-composition never reports its end.
    globalThis.addEventListener?.('blur', () => { composing = false; });
  }

  // ---------- Showing keys ----------

  const usable = (id) => bindingsOf(id).filter((b) => !b.problem);

  // keycaps shows an action's usable keys.
  const keycaps = (id) => usable(id).map((b) => keycap(b.chord, platform));

  // hint is a control's title with its first key: "Show the files (⇧⌘E)".
  function hint(label, id) {
    const first = keycaps(id)[0];
    return first ? `${label} (${first.text})` : label;
  }

  const aria = (id) => usable(id).map((b) => ariaKeys(b.chord, platform)).join(' ') || null;

  // primary is an action's first usable key, as it is kept, for a native
  // menu to show.
  const primary = (id) => usable(id)[0]?.binding ?? '';

  // conflicts lists the other actions a binding's keys run, in the same
  // places: everywhere, or the same context.
  function conflicts(id, binding) {
    const chord = parseBinding(binding);
    if (!chord) return [];
    const pid = physicalId(toPhysical(chord, platform));
    const mine = actions.get(id)?.context?.id ?? null;
    return (index.get(pid) ?? []).filter((c) => c.action.id !== id && (c.action.context?.id ?? null) === mine).map((c) => c.action);
  }

  // winner is the action a binding's keys run, when more than one has them.
  function winner(binding) {
    const chord = parseBinding(binding);
    return chord ? (index.get(physicalId(toPhysical(chord, platform)))?.[0]?.action ?? null) : null;
  }

  // ---------- Keeping keys ----------

  async function load() {
    try {
      setOverrides(await call('Shortcuts'));
    } finally {
      ready = true;
    }
  }

  // update changes actions' keys together: a list of bindings, [] for none,
  // or null for the defaults. Changes save one after another; the newest
  // answer wins.
  function update(changes) {
    const seq = ++saveSeq;
    const next = saving.catch(() => {}).then(() => call('SetShortcuts', changes)).then((saved) => {
      if (seq === saveSeq) setOverrides(saved);
      return saved;
    });
    saving = next;
    return next;
  }

  function resetAll() {
    const seq = ++saveSeq;
    const next = saving.catch(() => {}).then(() => call('ResetShortcuts')).then((saved) => {
      if (seq === saveSeq) setOverrides(saved);
      return saved;
    });
    saving = next;
    return next;
  }

  return {
    platform,
    register,
    actions: () => [...actions.values()],
    action: (id) => actions.get(id),
    defaultsOf: (id) => (actions.has(id) ? defaultsOf(actions.get(id)) : []),
    bindingsOf,
    isCustom,
    overrides: () => structuredClone(overrides),
    setOverrides,
    state,
    run,
    match,
    matches,
    onKeyDown,
    listen,
    isEditable,
    keycaps,
    hint,
    aria,
    primary,
    conflicts,
    winner,
    load,
    update,
    resetAll,
    toPortable: (phys) => formatBinding(toPortable(phys, platform)),
    format: formatBinding,
    problemOf: (id, chord) => (actions.has(id) ? problemOf(actions.get(id), chord) : 'invalid'),
    get ready() { return ready; },
    get recording() { return recording; },
    set recording(on) { recording = !!on; },
    onChange(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    label: labelOf,
  };
}
