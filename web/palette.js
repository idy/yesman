// The command palette: a searchable list of what the window can do, with
// each action's keys, opened by palette.open (⌘K, or Ctrl+K). It runs the
// actions shortcuts.js registers, with their own checks, and the items the
// app adds as it shows them: its yesmen and workers.
//
// It is a modal dialog: while it is open, its search field has the keys.
// ↑ and ↓ choose, Enter runs, Esc or the palette's own keys close it. An
// action that cannot run now is listed with why, and does not run.

import { h, icon } from './dom.js';
import { t } from './i18n.js';

// score says how well a query matches a text, higher better, or 0 for not
// at all: the whole text, its start, a word's start, anywhere, or the first
// letters of its words in order ("shf" for Show or hide the files).
export function score(query, text) {
  const q = query.trim().toLocaleLowerCase();
  if (!q) return 1;
  const s = text.toLocaleLowerCase();
  if (s === q) return 100;
  if (s.startsWith(q)) return 80;
  const at = s.indexOf(q);
  if (at > 0 && /[\s\p{P}]/u.test(s[at - 1])) return 60;
  if (at >= 0) return 40;
  let i = 0;
  for (const word of s.split(/[\s\p{P}]+/u)) if (word && word[0] === q[i]) i++;
  return i === q.length ? 10 : 0;
}

// rank orders entries by how well a query matches them, each word of it,
// in their label first; ties keep their order.
export function rank(entries, query) {
  const words = query.trim().split(/\s+/).filter(Boolean);
  if (!words.length) return entries;
  return entries.map((e, i) => {
    const fields = [e.label, e.group ?? '', ...(e.keywords ?? []), ...(e.keys ?? [])];
    let total = 0;
    for (const word of words) {
      const best = Math.max(score(word, e.label) * 2, ...fields.slice(1).map((f) => score(word, f)));
      if (!best) return null;
      total += best;
    }
    return { e, total, i };
  }).filter(Boolean).sort((a, b) => b.total - a.total || a.i - b.i).map((r) => r.e);
}

// place says where focus was when the palette opened, and back(place) finds
// that place again when the control that had focus was drawn again since.
export function createPalette({ shortcuts, items = () => [], fail = () => {}, place = () => null, back = () => null }) {
  const platform = shortcuts.platform;
  let dialog = null;
  let input = null;
  let list = null;
  let status = null;
  let entries = [];
  let shown = [];
  let active = 0;
  let opener = null;
  let where = null;
  let seq = 0;

  function build() {
    dialog?.remove();
    input = h('input', {
      class: 'mds-input palette-input', type: 'text', role: 'combobox', 'aria-expanded': 'true', 'aria-controls': 'palette-list',
      'aria-autocomplete': 'list', autocomplete: 'off', spellcheck: 'false', 'aria-label': t('Search commands'),
      placeholder: t('Type a command, a yesman or a worker'),
      oninput: () => { active = 0; paint(); },
      onkeydown: onKey,
    });
    list = h('div', { class: 'palette-list', id: 'palette-list', role: 'listbox', 'aria-label': t('Commands') });
    status = h('p', { class: 'palette-status', role: 'status', 'aria-live': 'polite' });
    const enter = platform === 'mac' ? '↩' : 'Enter';
    const esc = platform === 'mac' ? '⎋' : 'Esc';
    dialog = h('dialog', { class: 'mds-dialog palette-dialog', 'aria-label': t('Commands') },
      h('label', { class: 'search palette-search' }, icon('search'), input),
      list, status,
      h('p', { class: 'palette-foot', 'aria-hidden': 'true' },
        h('span', null, h('kbd', { class: 'keycap' }, '↑'), h('kbd', { class: 'keycap' }, '↓'), ' ', t('to choose')),
        h('span', null, h('kbd', { class: 'keycap' }, enter), ' ', t('to run')),
        h('span', null, h('kbd', { class: 'keycap' }, esc), ' ', t('to close'))));
    dialog.addEventListener('close', onClose);
    dialog.addEventListener('pointerdown', (e) => { if (e.target === dialog) dialog.close('cancel'); });
    document.body.append(dialog);
  }

  // collect lists what the palette offers now, with whether each can run.
  function collect() {
    const ctx = { source: 'palette' };
    const out = [];
    for (const action of shortcuts.actions()) {
      if (action.palette === false) continue;
      const s = shortcuts.state(action, ctx);
      out.push({
        key: `action:${action.id}`, label: shortcuts.label(action), group: t(action.group ?? ''),
        keywords: (action.keywords ?? []).map((k) => t(k)), keys: shortcuts.keycaps(action.id).map((k) => k.text),
        spoken: shortcuts.keycaps(action.id).map((k) => k.spoken),
        enabled: s.enabled, reason: s.reason ? t(s.reason) : '', run: () => shortcuts.run(action.id, ctx),
      });
    }
    for (const item of items()) {
      let reason = '';
      try {
        const answer = item.when ? item.when() : true;
        if (answer !== true && answer !== undefined) reason = t(answer || 'Not available now.');
      } catch (err) {
        reason = err?.message || String(err);
      }
      out.push({ ...item, key: `item:${item.key}`, keys: [], spoken: [], enabled: !reason, reason,
        run: () => Promise.resolve().then(item.run).catch(fail) });
    }
    return out;
  }

  function optionEl(entry, i) {
    const id = `palette-option-${i}`;
    return h('div', {
      class: 'palette-option', id, role: 'option', 'aria-selected': String(i === active), 'aria-disabled': entry.enabled ? null : 'true',
      'data-key': entry.key, 'data-active': i === active ? 'true' : null,
      onpointermove: () => { if (active !== i) { active = i; mark(); } },
      onclick: () => { active = i; choose(); },
    },
    h('span', { class: 'palette-option-text' },
      h('span', { class: 'palette-option-label' }, entry.label),
      entry.enabled ? (entry.group ? h('span', { class: 'palette-option-group' }, entry.group) : null)
        : h('span', { class: 'palette-option-reason' }, entry.reason)),
    entry.keys.length ? h('span', { class: 'palette-option-keys', 'aria-label': entry.spoken.join(t(', or ')) },
      entry.keys.map((k) => h('kbd', { class: 'keycap', 'aria-hidden': 'true' }, k))) : null);
  }

  function paint() {
    if (!dialog?.open) return;
    const keep = shown[active]?.key;
    shown = rank(entries, input.value);
    if (keep && active !== 0) {
      const again = shown.findIndex((e) => e.key === keep);
      if (again >= 0) active = again;
    }
    active = Math.max(0, Math.min(active, shown.length - 1));
    list.replaceChildren(...shown.map(optionEl));
    if (!shown.length) list.append(h('p', { class: 'palette-empty' }, t('Nothing matches “{query}”.', { query: input.value.trim() })));
    status.textContent = '';
    mark();
  }

  // mark shows the active option without drawing the list again.
  function mark() {
    const options = [...list.querySelectorAll('[role="option"]')];
    options.forEach((el, i) => {
      el.setAttribute('aria-selected', String(i === active));
      el.toggleAttribute('data-active', i === active);
    });
    const el = options[active];
    if (el) {
      input.setAttribute('aria-activedescendant', el.id);
      el.scrollIntoView({ block: 'nearest' });
    } else {
      input.removeAttribute('aria-activedescendant');
    }
  }

  function move(by) {
    if (!shown.length) return;
    active = (active + by + shown.length) % shown.length;
    mark();
  }

  function onKey(e) {
    if (e.isComposing || e.keyCode === 229) return;
    // Its own keys close it, as they opened it.
    if (shortcuts.matches(e, 'palette.open')) {
      e.preventDefault();
      if (!e.repeat) dialog.close('cancel');
      return;
    }
    const plain = !e.metaKey && !e.ctrlKey && !e.altKey;
    if (e.key === 'ArrowDown' && plain) { e.preventDefault(); move(1); }
    else if (e.key === 'ArrowUp' && plain) { e.preventDefault(); move(-1); }
    else if (e.key === 'PageDown' && plain) { e.preventDefault(); move(Math.min(8, shown.length - 1 - active) || 0); }
    else if (e.key === 'PageUp' && plain) { e.preventDefault(); move(-Math.min(8, active) || 0); }
    else if (e.key === 'Enter' && plain && !e.shiftKey) { e.preventDefault(); if (!e.repeat) choose(); }
  }

  // choose runs the active option. The palette closes first, giving focus
  // back to where it was, so an action that moves focus, opens a dialog or
  // shows another agent keeps it.
  function choose() {
    const entry = shown[active];
    if (!entry || !dialog.open) return;
    if (!entry.enabled) {
      status.textContent = t('{action} cannot run now: {reason}', { action: entry.label, reason: entry.reason });
      return;
    }
    dialog.close('run');
    restore();
    entry.run();
  }

  // restore gives focus back to what had it when the palette opened, while
  // nothing else has taken it.
  function restore() {
    const now = document.activeElement;
    if (!now || now === document.body || dialog.contains(now)) {
      const target = opener?.isConnected ? opener : back(where);
      target?.focus({ preventScroll: true });
    }
    opener = null;
    where = null;
  }

  function onClose() {
    // The close event comes a task later: one that arrives once the palette
    // has opened again belongs to the earlier opening, and must not undo
    // where this one gives focus back to.
    if (dialog.open) return;
    // A run restored focus already; Esc and clicks outside restore it here,
    // unless something else has taken it since.
    if (dialog.returnValue !== 'run') restore();
  }

  function open() {
    if (!dialog) build();
    if (dialog.open || document.querySelector('dialog[open]')) return;
    opener = document.activeElement;
    where = place();
    seq++;
    entries = collect();
    input.value = '';
    active = 0;
    dialog.returnValue = '';
    dialog.showModal();
    input.focus();
    paint();
  }

  return {
    open,
    isOpen: () => !!dialog?.open,
    close: () => { if (dialog?.open) dialog.close('cancel'); },
    // refresh lists what can run again, as agents come and go, keeping the
    // search and the option chosen.
    refresh() {
      if (!dialog?.open) return;
      entries = collect();
      paint();
    },
    // relocalize draws it again in the page's language.
    relocalize() {
      const wasOpen = dialog?.open;
      if (wasOpen) dialog.close('cancel');
      build();
      if (wasOpen) open();
    },
    get seq() { return seq; },
  };
}
