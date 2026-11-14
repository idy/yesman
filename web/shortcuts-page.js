// Settings › Shortcuts: every action's keys, found by name or keys, each
// recorded, removed or reset to its defaults, and all reset at once.
// Changes save at once, in settings.json through the app (shortcuts.go);
// only keys that differ from the defaults are kept.
//
// Recording takes the next keys pressed in the recorder, before anything
// else on the page sees them, so the keys recorded do not also run their
// action. Esc cancels it, as does leaving it. Keys this platform keeps, a
// key alone and the keys an input method or AltGr types with are refused,
// with why; keys another action has are offered to it instead.

import { h, icon } from './dom.js';
import { t } from './i18n.js';
import { parseBinding, readKey, bindingId, firesInText, mayBeAltGr, keycap, heldText } from './keys.js';
import { MAX_BINDINGS } from './shortcuts.js';

// Keys the file editor (CodeMirror) takes while it has focus: a shortcut on
// them runs only elsewhere.
const EDITOR_KEYS = {
  all: ['Mod+/', 'Mod+[', 'Mod+]', 'Mod+Alt+ArrowDown', 'Mod+Alt+ArrowUp', 'Mod+Alt+\\', 'Mod+Alt+G', 'Mod+Shift+L', 'Mod+D', 'Mod+F',
    'Mod+G', 'Mod+Shift+G', 'Mod+I', 'Mod+U', 'Mod+Shift+\\', 'Mod+Shift+K', 'Mod+Home', 'Mod+End', 'Alt+ArrowUp', 'Alt+ArrowDown',
    'Alt+Shift+ArrowUp', 'Alt+Shift+ArrowDown', 'Ctrl+Alt+[', 'Ctrl+Alt+]', 'F3', 'Shift+F3'],
  mac: ['Ctrl+A', 'Ctrl+B', 'Ctrl+D', 'Ctrl+E', 'Ctrl+F', 'Ctrl+H', 'Ctrl+K', 'Ctrl+L', 'Ctrl+N', 'Ctrl+O', 'Ctrl+P', 'Ctrl+T', 'Ctrl+V',
    'Ctrl+Alt+H', 'Mod+Shift+U', 'Alt+Shift+M', 'Mod+Alt+[', 'Mod+Alt+]'],
  other: ['Alt+A', 'Alt+L', 'Alt+U', 'Ctrl+M', 'Ctrl+Alt+H'],
};

const SYSTEM = { mac: 'macOS and yesman’s menus use these keys.', windows: 'Windows uses these keys.', linux: 'The desktop uses these keys.' };
const MODIFIER = {
  mac: 'Add ⌘, ⌥ or ⌃: a key alone or with ⇧ types text.',
  windows: 'Add Ctrl or Alt: a key alone or with Shift types text.',
  linux: 'Add Ctrl or Alt: a key alone or with Shift types text.',
};
const CARET = {
  mac: 'Use two of ⌘, ⌥ and ⌃ with this key: one moves the caret or deletes text in fields.',
  windows: 'This moves the caret or deletes text in fields. Ctrl or Alt with Page Up or Page Down can be shortcuts.',
  linux: 'This moves the caret or deletes text in fields. Ctrl or Alt with Page Up or Page Down can be shortcuts.',
};

export function createShortcutsPage({ shortcuts, confirm, shown, rerender, settingsSection }) {
  const platform = shortcuts.platform;
  const editorKeys = new Set([...EDITOR_KEYS.all, ...(platform === 'mac' ? EDITOR_KEYS.mac : EDITOR_KEYS.other)].map((b) => bindingId(b, platform)));
  let query = '';
  let rec = null; // { id, held, error, conflict: { binding, others }, saving }
  let errors = new Map(); // action ID -> the last change's error
  let resetting = false;
  let onRecordKey = null;

  const cap = (binding) => keycap(parseBinding(binding), platform);

  function problemText(problem, chord) {
    switch (problem) {
      case 'meta': return platform === 'windows' ? t('The Windows key belongs to the system.') : t('The Super key belongs to the system.');
      case 'key': return t('Enter, Space, Tab and Esc keep their own jobs.');
      case 'modifier': return t(MODIFIER[platform]);
      case 'caret': return t(CARET[platform]);
      case 'system': return t(SYSTEM[platform]);
      case 'editing': return t('These keys copy, paste, cut, undo or select all.');
      case 'builtin': return t('{keys} saves the open file.', { keys: keycap(chord, platform).text });
      default: return t('These keys cannot be a shortcut.');
    }
  }


  const sameList = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);

  // change saves actions' new keys, or their defaults when they are.
  // compute works the keys out from those kept when the change's turn to
  // save comes, after the changes before it have saved or failed: a key
  // removed while another removal saves is not brought back by it.
  async function change(compute) {
    let ids = [];
    try {
      await shortcuts.update(() => {
        const next = {};
        for (const [id, list] of Object.entries(compute())) {
          const value = list && sameList(list, shortcuts.defaultsOf(id)) ? null : list;
          // Leave out what would not change.
          if (value === null ? !shortcuts.isCustom(id) : shortcuts.isCustom(id) && sameList(value, current(id))) continue;
          next[id] = value;
        }
        ids = Object.keys(next);
        for (const id of ids) errors.delete(id);
        return next;
      });
      return true;
    } catch (err) {
      for (const id of ids) errors.set(id, err?.message || String(err));
      if (shown()) renderList();
      return false;
    }
  }

  const current = (id) => shortcuts.bindingsOf(id).map((b) => b.binding);

  // ---------- Recording ----------

  function startRecording(id) {
    stopRecording();
    rec = { id, held: '', error: '', conflict: null, saving: false };
    shortcuts.recording = true;
    onRecordKey = (e) => record(e);
    window.addEventListener('keydown', onRecordKey, true);
    window.addEventListener('keyup', onRecordKey, true);
    window.addEventListener('blur', onBlur);
    document.addEventListener('focusin', onFocusIn);
    document.addEventListener('pointerdown', onPointerDown, true);
    renderList();
    document.getElementById('shortcut-recorder')?.focus();
  }

  function stopRecording(refocus = false) {
    if (!rec) return;
    const id = rec.id;
    rec = null;
    shortcuts.recording = false;
    window.removeEventListener('keydown', onRecordKey, true);
    window.removeEventListener('keyup', onRecordKey, true);
    window.removeEventListener('blur', onBlur);
    document.removeEventListener('focusin', onFocusIn);
    document.removeEventListener('pointerdown', onPointerDown, true);
    onRecordKey = null;
    if (shown()) {
      renderList();
      if (refocus) document.querySelector(`[data-action="${CSS.escape(id)}"] .shortcut-add`)?.focus();
    }
  }

  // Modifiers held while the window loses focus are let go of there.
  function onBlur() {
    if (!rec) return;
    rec.held = '';
    paintRecorder();
  }

  const inRecording = (el) => !!el?.closest?.('.shortcut-recording');

  function onFocusIn(e) {
    if (rec && !inRecording(e.target)) stopRecording();
  }

  function onPointerDown(e) {
    if (rec && !inRecording(e.target)) stopRecording();
  }

  function record(e) {
    if (!rec) return;
    const plain = !e.metaKey && !e.ctrlKey && !e.altKey;
    if (e.target?.id !== 'shortcut-recorder') {
      // Esc cancels from anywhere in the recording, its conflict's buttons
      // too; other keys there do as they would (Enter chooses).
      if (e.type === 'keydown' && e.key === 'Escape' && plain && !e.shiftKey && !e.isComposing && inRecording(e.target)) {
        e.preventDefault();
        e.stopImmediatePropagation();
        if (!e.repeat && !rec.saving) stopRecording(true);
      }
      return;
    }
    if (e.type === 'keydown' && e.key === 'Tab' && plain) { stopRecording(); return; } // focus moves on
    // The keys are the recorder's: nothing else on the page takes them.
    e.preventDefault();
    e.stopImmediatePropagation();
    const read = readKey(e, platform);
    if (e.type === 'keyup') {
      rec.held = read.held ? heldText(read.held, platform) : '';
      paintRecorder();
      return;
    }
    if (e.repeat || rec.saving) return;
    if (e.key === 'Escape' && plain && !e.shiftKey) { stopRecording(true); return; }
    switch (read.kind) {
      case 'ime': rec.error = t('Finish typing with the input method first.'); break;
      case 'modifier': rec.held = heldText(read.held, platform); rec.error = ''; break;
      case 'altgraph': rec.error = t('AltGr types a character here. Choose keys without it.'); break;
      case 'unknown': rec.error = t('yesman cannot tell which key that is. Try another.'); break;
      case 'key': accept(shortcuts.toPortable(read.phys)); return;
    }
    paintRecorder();
  }

  async function accept(binding) {
    const action = shortcuts.action(rec.id);
    const chord = parseBinding(binding);
    rec.held = '';
    rec.conflict = null;
    const problem = shortcuts.problemOf(action.id, chord);
    if (problem) {
      rec.error = t('{keys}: {problem}', { keys: cap(binding).text, problem: problemText(problem, chord) });
      paintRecorder();
      return;
    }
    const mine = current(rec.id);
    if (mine.some((b) => bindingId(b, platform) === bindingId(binding, platform))) {
      rec.error = t('{keys} is one of its shortcuts already.', { keys: cap(binding).text });
      paintRecorder();
      return;
    }
    const others = shortcuts.conflicts(rec.id, binding);
    if (others.length) {
      rec.error = '';
      rec.conflict = { binding, others };
      renderList();
      document.querySelector('.shortcut-conflict .mds-button--primary')?.focus();
      return;
    }
    await save(binding, []);
  }

  // save adds binding to the action recorded for, taking it from others.
  async function save(binding, others) {
    const r = rec;
    r.saving = true;
    const id = bindingId(binding, platform);
    const same = (b) => bindingId(b, platform) === id;
    const ok = await change(() => {
      const mine = current(r.id);
      const changes = { [r.id]: mine.some(same) ? mine : [...mine, binding] };
      for (const other of others) changes[other.id] = current(other.id).filter((b) => !same(b));
      return changes;
    });
    if (rec !== r) return;
    r.saving = false;
    if (ok) stopRecording(true);
    else {
      r.error = errors.get(r.id) ?? '';
      paintRecorder();
    }
  }

  function paintRecorder() {
    const el = document.getElementById('shortcut-recorder');
    if (!el || !rec) return;
    el.querySelector('.shortcut-recorder-keys').textContent = rec.held || t('Press keys…');
    const error = document.getElementById('shortcut-recorder-error');
    if (error) {
      error.textContent = rec.error;
      error.hidden = !rec.error;
    }
  }

  // ---------- Drawing ----------

  function chip(action, b) {
    const k = b.chord ? keycap(b.chord, platform) : { text: b.binding, spoken: b.binding };
    const notes = [];
    if (b.problem) notes.push(problemText(b.problem, b.chord));
    return h('span', { class: 'shortcut-chip', 'data-unusable': b.problem ? 'true' : null, title: notes.join('\n') || null },
      h('kbd', { class: 'keycap', 'aria-label': k.spoken }, k.text),
      h('button', {
        class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: !!rec,
        title: t('Remove {keys}', { keys: k.text }), 'aria-label': t('Remove {keys} from {action}', { keys: k.spoken, action: shortcuts.label(action) }),
        onclick: () => change(() => ({ [action.id]: current(action.id).filter((x) => x !== b.binding) })),
      }, icon('x')));
  }

  // notes say what limits an action's keys.
  function notes(action, list) {
    const out = [];
    if (action.context) out.push(t('Only {where}.', { where: t(action.context.label) }));
    for (const b of list) {
      if (b.problem) {
        out.push(t('{keys} cannot work here: {problem}', { keys: b.binding, problem: problemText(b.problem, b.chord) }));
        continue;
      }
      const k = keycap(b.chord, platform).text;
      const pid = bindingId(b.binding, platform);
      for (const other of shortcuts.conflicts(action.id, b.binding)) {
        const first = shortcuts.winner(b.binding);
        out.push(t('{keys} is also a shortcut of “{other}”; “{first}” runs.', { keys: k, other: shortcuts.label(other), first: shortcuts.label(first) }));
      }
      if (!action.context && !firesInText(b.chord, platform)) out.push(t('{keys} does not work while typing in a text field.', { keys: k }));
      if (mayBeAltGr(b.chord, platform)) out.push(t('On keyboards with AltGr, {keys} may type a character instead.', { keys: k }));
      if (editorKeys.has(pid)) out.push(t('In the file editor, {keys} edits the file instead.', { keys: k }));
    }
    return out;
  }

  function row(action) {
    const list = shortcuts.bindingsOf(action.id);
    const custom = shortcuts.isCustom(action.id);
    const recording = rec?.id === action.id;
    const error = errors.get(action.id);
    const label = shortcuts.label(action);
    const controls = h('div', { class: 'shortcut-keys' },
      list.length ? list.map((b) => chip(action, b)) : h('span', { class: 'shortcut-none' }, t('None')));
    if (recording) {
      controls.append(h('button', {
        class: 'shortcut-recorder', id: 'shortcut-recorder', type: 'button', 'aria-describedby': 'shortcut-recorder-hint shortcut-recorder-error',
        'aria-label': t('Record a shortcut for {action}', { action: label }),
      }, h('span', { class: 'shortcut-recorder-keys', 'aria-live': 'polite' }, rec.held || t('Press keys…'))));
    } else {
      controls.append(h('button', {
        class: 'mds-icon-button mds-icon-button--compact shortcut-add', type: 'button', disabled: list.length >= MAX_BINDINGS || !!rec,
        title: list.length >= MAX_BINDINGS ? t('An action has at most {n} shortcuts.', { n: MAX_BINDINGS }) : t('Add a shortcut'),
        'aria-label': t('Add a shortcut for {action}', { action: label }),
        onclick: () => startRecording(action.id),
      }, icon('plus')));
    }
    if (custom) {
      controls.append(h('button', {
        class: 'mds-link-button shortcut-reset', type: 'button', disabled: !!rec,
        'aria-label': t('Reset {action} to its default shortcuts', { action: label }),
        onclick: () => change(() => ({ [action.id]: null })),
      }, t('Reset')));
    }
    const info = notes(action, list);
    const defaults = shortcuts.defaultsOf(action.id);
    if (custom) {
      info.unshift(defaults.length
        ? t('Changed. Default: {keys}.', { keys: defaults.map((d) => cap(d).text).join(t(', ')) })
        : t('Changed. It has no shortcut by default.'));
    }
    const conflict = recording && rec.conflict;
    return h('div', { class: `mds-row shortcut-row${recording ? ' shortcut-recording' : ''}`, 'data-action': action.id, 'data-custom': custom ? 'true' : null },
      h('div', { class: 'mds-row-label' }, h('h3', null, label),
        info.length ? h('ul', { class: 'shortcut-notes' }, info.map((n) => h('li', null, n))) : null,
        recording ? h('p', { class: 'shortcut-hint', id: 'shortcut-recorder-hint' },
          platform === 'mac' ? t('Press the keys, or ⎋ to cancel.') : t('Press the keys, or Esc to cancel.')) : null,
        recording ? h('p', { class: 'danger', id: 'shortcut-recorder-error', role: 'alert', hidden: !rec.error }, rec.error) : null,
        conflict ? h('div', { class: 'shortcut-conflict', role: 'alert' },
          h('p', null, t('{keys} runs “{other}” now.', { keys: cap(conflict.binding).text, other: conflict.others.map((o) => shortcuts.label(o)).join(t(', ')) })),
          h('div', { class: 'storage-actions' },
            h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-conflict': 'other', onclick: () => { rec.conflict = null; renderList(); document.getElementById('shortcut-recorder')?.focus(); } }, t('Choose other keys')),
            h('button', { class: 'mds-button mds-button--small mds-button--primary', type: 'button', 'data-conflict': 'use', onclick: () => save(conflict.binding, conflict.others) },
              t('Use for “{action}”', { action: label })))) : null,
        error ? h('p', { class: 'danger', role: 'alert' }, error) : null),
      h('div', { class: 'mds-row-control' }, controls));
  }

  // Search folds case the same way whatever the system's language: the
  // page is in English or Chinese, and Turkish rules would not find “import”.
  function matchesQuery(action) {
    const q = query.trim().toLowerCase();
    if (!q) return true;
    const texts = [shortcuts.label(action), t(action.group ?? ''), action.id, ...(action.keywords ?? []).map((k) => t(k)),
      ...shortcuts.bindingsOf(action.id).flatMap((b) => (b.chord ? [b.binding, keycap(b.chord, platform).text, keycap(b.chord, platform).spoken] : [b.binding]))];
    return q.split(/\s+/).every((word) => texts.some((s) => s.toLowerCase().includes(word)));
  }

  // recordingFocus says which of the recording's controls has the focus:
  // the recorder, or a button of its conflict.
  function recordingFocus() {
    const active = document.activeElement;
    if (!rec || !inRecording(active)) return null;
    return active.dataset?.conflict ? `.shortcut-conflict [data-conflict="${active.dataset.conflict}"]` : '#shortcut-recorder';
  }

  // renderList draws the list again, as a change saves or the keys load.
  // While recording, the focus stays on the same control of the recording
  // (else the recorder), so its keys and Esc still reach it.
  function renderList() {
    const el = document.getElementById('shortcut-list');
    if (!el) return;
    const keep = recordingFocus();
    const groups = new Map();
    for (const action of shortcuts.actions()) {
      if (!matchesQuery(action) && rec?.id !== action.id) continue;
      if (!groups.has(action.group)) groups.set(action.group, []);
      groups.get(action.group).push(action);
    }
    const sections = [...groups].map(([group, actions]) => settingsSection(t(group), null, ...actions.map(row)));
    el.replaceChildren(...(sections.length ? sections
      : [h('div', { class: 'card-empty' }, h('span', null, t('No action matches “{query}”.', { query: query.trim() })))]));
    if (keep) (el.querySelector(keep) ?? document.getElementById('shortcut-recorder'))?.focus();
    paintRecorder();
    paintResetAll();
  }

  const customCount = () => shortcuts.actions().filter((a) => shortcuts.isCustom(a.id)).length;

  // Reset all is there while any action has keys of its own, as they are
  // saved, loaded or reset, and not while recording or resetting.
  function paintResetAll() {
    const button = document.getElementById('shortcut-reset-all');
    if (button) button.disabled = !customCount() || resetting || !!rec;
  }

  function builtInSection() {
    const keys = (...list) => h('span', { class: 'shortcut-keys' }, list.map((b) => {
      const k = cap(b);
      return h('kbd', { class: 'keycap', 'aria-label': k.spoken }, k.text);
    }));
    const line = (what, ...list) => h('div', { class: 'mds-row shortcut-row' },
      h('div', { class: 'mds-row-label' }, h('h3', null, what)), h('div', { class: 'mds-row-control' }, keys(...list)));
    return settingsSection(t('Built-in keys'), t('These keys keep their jobs where they work and cannot be changed.'),
      line(t('Send a message, or steer the run under way'), 'Enter'),
      line(t('New line in the message box'), 'Shift+Enter'),
      line(t('Save the open file'), 'Mod+S'),
      line(t('Close a dialog, menu or the command palette'), 'Escape'),
      line(t('Open the menu of a sidebar entry'), 'Shift+F10'),
      h('div', { class: 'mds-row shortcut-row shortcut-reserved' }, h('p', null, platform === 'mac'
        ? t('macOS and yesman’s menus keep keys such as ⌘Q, ⌘W, ⌘H, ⌘M and ⌘R, and copy, paste, cut, undo and select all keep theirs, so these cannot be shortcuts. Keys a keyboard tool or the system takes before yesman cannot work as shortcuts either.')
        : t('Windows keeps keys such as Alt+F4 and the Windows key, and copy, paste, cut, undo and select all keep theirs, so these cannot be shortcuts. Keys a keyboard tool or the system takes before yesman cannot work as shortcuts either.'))));
  }

  function platformNote() {
    if (platform === 'mac') return t('Keys show as on a Mac. A shortcut with ⌘ uses Ctrl on Windows and Linux; ⌃ is the Control key.');
    if (platform === 'windows') return t('Keys show as on Windows. A shortcut with Ctrl uses ⌘ on a Mac; the Windows key belongs to the system.');
    return t('Keys show as on Linux. A shortcut with Ctrl uses ⌘ on a Mac; the Super key belongs to the system.');
  }

  function render(inner) {
    const search = h('label', { class: 'search' }, icon('search'),
      h('input', { class: 'mds-input', id: 'shortcut-search', type: 'search', placeholder: t('Find an action or keys'), 'aria-label': t('Find an action or keys'),
        spellcheck: 'false', autocomplete: 'off', value: query,
        oninput: (e) => { query = e.currentTarget.value; renderList(); } }));
    const resetAll = h('button', { class: 'mds-button mds-button--small', type: 'button', id: 'shortcut-reset-all', disabled: !customCount() || resetting || !!rec,
      onclick: async () => {
        if (!(await confirm(t('Reset all shortcuts?'), tnCount(customCount()), t('Reset all')))) return;
        resetting = true;
        errors.delete('*');
        rerender();
        try {
          await shortcuts.resetAll();
          errors = new Map();
        } catch (err) {
          errors.set('*', err?.message || String(err));
        } finally {
          resetting = false;
          if (shown()) { rerender(); document.getElementById('shortcut-reset-all')?.focus(); }
        }
      } }, t('Reset all'));
    inner.append(
      h('div', { class: 'mds-toolbar' },
        h('div', { class: 'toolbar-lead' }, h('p', null, t('The keys of yesman’s actions, inside its window. The command palette lists them all with their keys.')),
          h('p', null, platformNote()),
          errors.get('*') ? h('p', { class: 'danger', role: 'alert' }, errors.get('*')) : null),
        h('div', { class: 'mds-toolbar-actions' }, search, resetAll)),
      h('div', { id: 'shortcut-list', class: 'shortcut-list' }),
      builtInSection());
    renderList();
  }

  const tnCount = (n) => (n === 1 ? t('1 action has keys of its own. Every action gets its default keys again.')
    : t('{n} actions have keys of their own. Every action gets its default keys again.', { n }));

  return {
    render,
    renderList,
    async load() {
      await shortcuts.load();
      if (shown()) renderList();
    },
    // leave stops recording when the page is left.
    leave() { stopRecording(); },
    get recording() { return rec?.id ?? null; },
  };
}
