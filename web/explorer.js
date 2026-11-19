// The resource explorer: what the panes beside the conversation that browse
// a yesman's resources share. Content is on the left, its navigation on the
// right: the files and Knowledge (files.js), the Working log, Topics
// (topics.js) and Needs You (app.js). This module draws no data of its own:
// each pane gives it nodes from its own trusted structure.
//
//   dragHandle      a vertical separator that sets a width
//   resizer         a pane's or a navigation column's width, kept per pane
//   explorerBody    content | separator | navigation, one column when narrow
//   treeView        a WAI-ARIA tree: expand, select, roving focus, keys
//   listView        an ordinary list of choices, with arrow keys
//   focusNode       focus a drawn row by its node
//   scrollMemory    where content was scrolled, per thing shown, bounded

import { $, h, icon } from './dom.js';
import { t } from './i18n.js';

// dragHandle makes el a vertical separator that sets a width as the
// pointer moves, or by 16px with the arrow keys; double-clicking resets it.
export function dragHandle(el, { widthAt, current, apply, reset }) {
  el.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    try {
      el.setPointerCapture(e.pointerId); // keeps the cursor while dragging
    } catch {
      // A pointer the page did not see go down; the window still follows it.
    }
    document.documentElement.classList.add('resizing');
    const move = (ev) => apply(widthAt(ev.clientX), false);
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', up);
      document.documentElement.classList.remove('resizing');
      apply(current(), true);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', up);
  });
  el.addEventListener('dblclick', reset);
  el.addEventListener('keydown', (e) => {
    const step = e.key === 'ArrowLeft' ? 16 : e.key === 'ArrowRight' ? -16 : 0;
    if (!step) return;
    e.preventDefault();
    apply(current() + step, true);
  });
}

// A width the user dragged to, in pixels, or 0 for the default.
const stored = (key) => Number(localStorage.getItem(`yesman.explorer.${key}`)) || 0;
function store(key, px) {
  if (px) localStorage.setItem(`yesman.explorer.${key}`, String(Math.round(px)));
  else localStorage.removeItem(`yesman.explorer.${key}`);
}

// The room a pane leaves the conversation beside it, as app.css's pane
// widths do, and the width below which an explorer shows one column
// (explorer.css), with the least its content and navigation keep.
export const CONVERSATION_MIN = 280;
export const TWO_COLUMNS = 600;
const CONTENT_MIN = 260;
const NAV_MIN = 180;

// The widest a pane may be now, beside the sidebar and the info panel.
function paneMax() {
  const side = ($('#side')?.getBoundingClientRect().width ?? 0) + ($('#yesman-info')?.getBoundingClientRect().width ?? 0);
  return Math.max(240, window.innerWidth - side - CONVERSATION_MIN);
}
const clampPane = (px, min) => Math.round(Math.min(Math.max(px, min), paneMax()));

// resizer returns the separators of pane aside, named key: one at its left
// edge for the pane's width, and one between content and navigation. Both
// keep what the user chose across launches, per pane, as a preference: the
// stylesheet fits it to the room there is now, as the window, the info
// panel or the pane changes (--explorer-pane-width, --explorer-nav-width),
// and it applies again once there is room.
export function resizer(key, { min = 320 } = {}) {
  const paneKey = `${key}.width`;
  const navKey = `${key}.nav`;
  const separator = (label) => h('div', { class: 'explorer-resize', role: 'separator', 'aria-orientation': 'vertical', 'aria-label': label, tabindex: '0', title: t('Drag to resize; double-click for the default width') });
  return {
    // apply sets the widths kept on aside, the pane, after it is drawn.
    apply(aside) {
      const w = stored(paneKey);
      if (w) aside.style.setProperty('--explorer-pane-width', `${Math.max(w, min)}px`);
      else aside.style.removeProperty('--explorer-pane-width');
      const n = stored(navKey);
      if (n) aside.style.setProperty('--explorer-nav-width', `${n}px`);
      else aside.style.removeProperty('--explorer-nav-width');
    },
    pane(aside) {
      const el = separator(t('Width of the pane'));
      el.classList.add('explorer-resize--pane');
      dragHandle(el, {
        widthAt: (x) => window.innerWidth - x,
        current: () => aside.getBoundingClientRect().width,
        apply: (px, done) => {
          const w = clampPane(px, min);
          aside.style.setProperty('--explorer-pane-width', `${w}px`);
          if (done) store(paneKey, w);
        },
        reset: () => { store(paneKey, 0); aside.style.removeProperty('--explorer-pane-width'); },
      });
      return el;
    },
    nav(aside) {
      const el = separator(t('Width of the navigation'));
      const room = () => ($('.explorer', aside) ?? aside).getBoundingClientRect().width;
      const clamp = (px) => Math.round(Math.max(NAV_MIN, Math.min(px, room() - CONTENT_MIN)));
      dragHandle(el, {
        widthAt: (x) => aside.getBoundingClientRect().right - x,
        current: () => $('.explorer-nav', aside)?.getBoundingClientRect().width ?? 260,
        apply: (px, done) => {
          const w = clamp(px);
          aside.style.setProperty('--explorer-nav-width', `${w}px`);
          if (done) store(navKey, w);
        },
        reset: () => { store(navKey, 0); aside.style.removeProperty('--explorer-nav-width'); },
      });
      return el;
    },
  };
}

// explorerBody lays content out on the left and its navigation on the right.
// When the pane is narrow, one shows at a time: focus says which ('content'
// or 'nav'), and the content's back button, shown only then, goes to the
// navigation.
export function explorerBody({ name, content, nav, focus = 'content', handle = null, label }) {
  return h('div', { class: 'explorer', 'data-explorer': name, 'data-focus': focus },
    h('section', { class: 'explorer-content', 'aria-label': label ?? null }, content),
    // The separator sits on the navigation's left border.
    h('nav', { class: 'explorer-nav', 'aria-label': t('Navigation') }, handle, nav));
}

// narrowBack is the content's way back to the navigation in a narrow pane.
export const narrowBack = (label, onclick, key = 'back') => h('button', { class: 'mds-link-button explorer-back', type: 'button', 'data-key': key, onclick },
  icon('arrowLeft'), label);

// treeView draws nodes as a tree. A node is
//   { id, label, meta?, icon?, marker?, title?, children?: nodes, lazy?: bool, current?: bool, attrs? }
// A leaf can put its marker in the same column as the branch chevron.
// A node with children (or lazy) expands; selecting a node calls select(node).
// expanded is a Set of ids the caller keeps; toggle(node, open) changes it.
// Single click selects, the chevron or Left/Right expand and collapse.
// The row focused is not the row selected: one row takes Tab, the one last
// focused (active, which the caller keeps from onActive) while it is drawn,
// else the selected one, else the first; focus moving in the tree moves it.
export function treeView({ id, label, nodes, expanded, selected, select, toggle, empty, active = null, onActive = null }) {
  const root = h('div', { class: 'explorer-tree', id, role: 'tree', 'aria-label': label });
  const rows = [];
  const walk = (list, depth, parent) => {
    const group = depth ? h('div', { role: 'group' }) : root;
    list.forEach((node, i) => {
      const branch = Boolean(node.children?.length || node.lazy);
      const open = branch && expanded.has(node.id);
      const row = h('div', {
        class: node.cls ? `explorer-row ${node.cls}` : 'explorer-row', role: 'treeitem', tabindex: '-1', 'data-node': node.id, 'data-kind': node.kind ?? null,
        'aria-level': String(depth + 1), 'aria-setsize': String(list.length), 'aria-posinset': String(i + 1),
        'aria-expanded': branch ? String(open) : null, 'aria-selected': node.selectable === false ? null : String(node.id === selected),
        'aria-current': node.current ? 'true' : null, style: `--depth: ${depth}`, ...(node.attrs ?? {}),
      },
      // The visible line; a branch's group of children follows it inside
      // the treeitem, as WAI-ARIA's tree pattern nests them.
      h('div', { class: 'explorer-line', title: node.title ?? null },
        branch ? h('span', { class: 'explorer-marker', 'aria-hidden': 'true', 'data-toggle': 'true' }, icon('chevron', 'mds-icon explorer-chevron'))
          : h('span', { class: 'explorer-marker', 'aria-hidden': 'true' }, node.marker ? icon(node.marker, 'mds-icon explorer-marker-icon') : null),
        node.icon ? icon(node.icon, 'mds-icon explorer-icon') : null,
        h('span', { class: 'explorer-label' }, node.label),
        node.meta != null && node.meta !== '' ? h('span', { class: 'explorer-meta' }, node.meta) : null));
      rows.push({ row, node, parent, branch, open });
      group.append(row);
      if (open && node.children?.length) {
        const sub = walk(node.children, depth + 1, node);
        row.append(sub);
      }
    });
    return group;
  };
  walk(nodes, 0, null);
  if (!nodes.length && empty) root.append(h('p', { class: 'explorer-note', role: 'presentation' }, empty));
  const stop = rows.find((r) => r.node.id === active) ?? rows.find((r) => r.node.id === selected) ?? rows[0];
  if (stop) stop.row.tabIndex = 0;
  const byRow = new Map(rows.map((r) => [r.row, r]));
  root.addEventListener('focusin', (e) => {
    const r = byRow.get(e.target);
    if (!r) return;
    rove(root, r.row);
    onActive?.(r.node.id);
  });
  const activate = (r, how) => {
    if (r.node.selectable === false) { if (r.branch) toggle(r.node, !r.open); return; }
    select(r.node, how);
  };
  root.addEventListener('click', (e) => {
    const rowEl = e.target.closest('.explorer-row');
    const r = rowEl && byRow.get(rowEl);
    if (!r || !root.contains(rowEl)) return;
    e.stopPropagation();
    if (r.branch && (e.target.closest('[data-toggle]') || r.node.selectable === false)) toggle(r.node, !r.open);
    else activate(r, 'click');
  });
  root.addEventListener('keydown', (e) => {
    const rowEl = e.target.closest?.('.explorer-row');
    const r = rowEl && byRow.get(rowEl);
    if (!r) return;
    // Every drawn row is visible: a collapsed branch draws no children.
    const visible = rows;
    const at = visible.indexOf(r);
    const go = (x) => { if (x) { e.preventDefault(); e.stopPropagation(); x.row.focus(); } };
    switch (e.key) {
      case 'ArrowDown': go(visible[at + 1]); break;
      case 'ArrowUp': go(visible[at - 1]); break;
      case 'Home': go(visible[0]); break;
      case 'End': go(visible.at(-1)); break;
      case 'ArrowRight':
        if (r.branch && !r.open) { e.preventDefault(); toggle(r.node, true); } else if (r.branch) go(visible[at + 1]);
        break;
      case 'ArrowLeft':
        if (r.branch && r.open) { e.preventDefault(); toggle(r.node, false); } else if (r.parent) go(rows.find((x) => x.node === r.parent));
        break;
      case 'Enter': case ' ':
        e.preventDefault();
        activate(r, 'key');
        break;
    }
  });
  return root;
}

// rove makes row the one row of tree root that takes Tab.
function rove(root, row) {
  for (const r of root.querySelectorAll('.explorer-row[tabindex="0"]')) if (r !== row) r.tabIndex = -1;
  row.tabIndex = 0;
}

// focusNode puts focus on the row of node id in tree el, if it is drawn; it
// becomes the row that takes Tab.
export function focusNode(el, id) {
  const row = el && [...el.querySelectorAll('.explorer-row')].find((r) => r.dataset.node === id);
  if (row) {
    rove(el.closest('[role="tree"]') ?? el, row);
    row.focus({ preventScroll: true });
    row.scrollIntoView({ block: 'nearest' });
  }
  return Boolean(row);
}

// listView draws an ordinary list of choices: each item a button with
// aria-current on the selected one; Up/Down/Home/End move between them.
// An optional onActive lets the caller preserve a single tab stop on redraw.
export function listView({ label, items, cls = '', onActive }) {
  const list = h('ul', { class: `explorer-list ${cls}`.trim(), 'aria-label': label }, items);
  if (onActive) list.addEventListener('focusin', (e) => {
    if (!e.target.matches('li > button')) return;
    for (const button of list.querySelectorAll(':scope > li > button[tabindex="0"]')) button.tabIndex = -1;
    e.target.tabIndex = 0;
    onActive(e.target);
  });
  list.addEventListener('keydown', (e) => {
    const buttons = [...list.querySelectorAll(':scope > li > button')];
    const at = buttons.indexOf(e.target);
    if (at < 0) return;
    const go = (n) => { e.preventDefault(); buttons[Math.max(0, Math.min(buttons.length - 1, n))]?.focus(); };
    if (e.key === 'ArrowDown') go(at + 1);
    else if (e.key === 'ArrowUp') go(at - 1);
    else if (e.key === 'Home') go(0);
    else if (e.key === 'End') go(buttons.length - 1);
  });
  return list;
}

// scrollMemory keeps where content was scrolled for each thing shown, the
// newest 256, so that returning to it restores its place.
export function scrollMemory(limit = 256) {
  const at = new Map();
  return {
    save(key, top) {
      if (key == null) return;
      at.delete(key);
      at.set(key, top);
      while (at.size > limit) at.delete(at.keys().next().value);
    },
    get: (key) => at.get(key) ?? 0,
    has: (key) => at.has(key),
  };
}
