// The files pane: a yesman's workspace as a tree, and previews of its
// files in tabs, with code highlighted, Markdown rendered and images shown.
// The Go side lists and reads files (files.go) and reports changes while
// the pane watches a workspace.

import { $, h, icon } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { languageOf } from './code.js';
import { createEditor } from './editor.js';

// A folder shows this many entries; Go to File finds the rest.
const MAX_TREE_ROWS = 2000;

// Settings that apply to every workspace, kept across launches.
const prefs = {
  tree: localStorage.getItem('yesman.files.tree') !== 'hidden',
  wrap: localStorage.getItem('yesman.files.wrap') !== 'off',
  autoSave: localStorage.getItem('yesman.files.autosave') !== 'off',
};

// Widths the user dragged to, in pixels, or 0 for the default: the pane
// with a file open, the pane with the tree alone, and the tree beside a file.
const widths = Object.fromEntries(['file', 'tree', 'list'].map((k) => [k, Number(localStorage.getItem(`yesman.files.width.${k}`)) || 0]));

function saveWidth(key, px) {
  widths[key] = px;
  if (px) localStorage.setItem(`yesman.files.width.${key}`, String(Math.round(px)));
  else localStorage.removeItem(`yesman.files.width.${key}`);
}

// dragHandle makes el a vertical separator that sets a width as the
// pointer moves, or by 16px with the arrow keys; double-clicking resets it.
function dragHandle(el, { widthAt, current, apply, reset }) {
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

// createFiles returns the pane. host connects it to the page:
//   call(method, ...args)   a Yesman service method
//   fail(err)               shows an error
//   yesman()                the yesman whose agent is shown, or null
//   isOpen(), setOpen(on)   whether the pane shows
//   checkout(y), dismiss(name)
//   confirm(title, body, ok)  asks before something is lost
export function createFiles(host) {
  const panes = new Map(); // yesman name -> its pane's state
  let watching = null;
  let wide = false;

  function pane(y) {
    let p = panes.get(y.name);
    if (!p) {
      p = { name: y.name, path: y.path, tabs: [], active: null, expanded: new Set(), dirs: new Map(), views: new Map(), mode: new Map(), query: '', results: null };
      panes.set(y.name, p);
    }
    p.path = y.path;
    return p;
  }

  const parentOf = (rel) => (rel.includes('/') ? rel.slice(0, rel.lastIndexOf('/')) : '.');
  const baseOf = (rel) => rel.slice(rel.lastIndexOf('/') + 1);
  const absOf = (p, rel) => `${p.path}/${rel}`;

  // ---------- Loading ----------

  async function loadDir(p, rel) {
    if (!p.dirs.has(rel)) p.dirs.set(rel, 'loading');
    try {
      p.dirs.set(rel, await host.call('ListFiles', p.name, rel));
    } catch (err) {
      p.dirs.set(rel, { error: err?.message || String(err) });
    }
    if (current() === p) renderTree();
  }

  async function loadView(p, rel) {
    if (!p.views.has(rel)) p.views.set(rel, 'loading');
    try {
      const v = await host.call('ReadFile', p.name, rel);
      const tab = tabOf(p, rel);
      if (tab && v.kind === 'text' && !editor?.has(keyOf(p, rel))) {
        tab.base = v.hash;
        tab.readOnly = Boolean(v.truncated);
      }
      p.views.set(rel, v);
    } catch (err) {
      p.views.set(rel, { error: err?.message || String(err) });
    }
    if (current() === p && p.active === rel) renderView();
  }

  function current() {
    const y = host.isOpen() && host.yesman();
    return y ? pane(y) : null;
  }

  // ---------- Editing ----------

  // Every text file shows in one editor, made the first time it is needed.
  let editor = null;
  let editorLoading = null;
  const keyOf = (p, rel) => `${p.name}\n${rel}`;
  const tabOf = (p, rel) => p?.tabs.find((t) => t.path === rel);

  function ensureEditor() {
    editorLoading ??= createEditor({ onChange: edited, onSave: (k) => { const [p, rel] = byKey(k); if (p) save(p, rel); } })
      .then((ed) => { editor = ed; ed.setWrap(prefs.wrap); return ed; });
    return editorLoading;
  }

  function byKey(k) {
    const at = k.indexOf('\n');
    return [panes.get(k.slice(0, at)), k.slice(at + 1)];
  }

  // edited follows a change the user made in a file: its tab stays, shows
  // that it is unsaved, and auto save saves it once typing pauses.
  function edited(k) {
    const [p, rel] = byKey(k);
    const tab = tabOf(p, rel);
    if (!tab) return;
    tab.pinned = true;
    if (!tab.dirty) {
      tab.dirty = true;
      if (current() === p) renderHead();
    }
    if (prefs.autoSave) scheduleSave(p, rel);
  }

  const timers = new Map();
  function scheduleSave(p, rel) {
    const k = keyOf(p, rel);
    clearTimeout(timers.get(k));
    timers.set(k, setTimeout(() => { timers.delete(k); save(p, rel); }, 800));
  }

  // save writes a file's text, unless it changed on disk since it was read:
  // then the file keeps both until the user reloads or overwrites it.
  async function save(p, rel, force = false) {
    const tab = tabOf(p, rel);
    const k = keyOf(p, rel);
    if (!tab || tab.readOnly || !editor?.has(k)) return false;
    if (!force && (!tab.dirty || tab.conflict)) return !tab.dirty;
    clearTimeout(timers.get(k));
    const text = editor.text(k);
    try {
      const v = await host.call('WriteFile', p.name, rel, text, force ? '' : (tab.base ?? ''));
      tab.base = v.hash;
      tab.conflict = null;
      tab.dirty = editor.text(k) !== text;
      p.views.set(rel, v);
      return true;
    } catch (err) {
      if (/changed on disk/.test(err?.message || String(err))) tab.conflict = 'changed';
      else host.fail(err);
      return false;
    } finally {
      if (current() === p) {
        renderHead();
        if (p.active === rel) renderBanner();
      }
    }
  }

  // refresh reads an open file again after it changed on disk.
  async function refresh(p, rel) {
    const tab = tabOf(p, rel);
    if (!tab) return;
    const k = keyOf(p, rel);
    let v;
    try {
      v = await host.call('ReadFile', p.name, rel);
    } catch (err) {
      if (tab.dirty) tab.conflict = 'deleted';
      else p.views.set(rel, { error: err?.message || String(err) });
      if (current() === p && p.active === rel) (tab.dirty ? renderBanner : renderView)();
      return;
    }
    if (v.kind !== 'text' || !editor?.has(k)) {
      p.views.set(rel, v);
      if (current() === p && p.active === rel) renderView();
      return;
    }
    if (v.hash && v.hash === tab.base) return; // this pane's own save
    if (tab.dirty) {
      tab.conflict = 'changed';
      if (current() === p && p.active === rel) renderBanner();
      return;
    }
    editor.replace(k, v.text);
    tab.base = v.hash;
    tab.readOnly = Boolean(v.truncated);
    tab.conflict = null;
    p.views.set(rel, v);
    if (current() === p && p.active === rel && languageOf(rel) === 'markdown') renderView();
  }

  // reload throws away unsaved changes for what is on disk.
  async function reload(p, rel) {
    const tab = tabOf(p, rel);
    if (!tab) return;
    tab.dirty = false;
    tab.conflict = null;
    tab.base = null;
    await refresh(p, rel);
    if (current() === p) renderHead();
    renderBanner();
  }

  // ---------- Opening files ----------

  // open shows a file in a tab: a preview tab, in italics, that the next
  // file replaces, unless pin keeps it.
  function open(rel, pin = false) {
    const y = host.yesman();
    if (!y) return;
    const p = pane(y);
    const at = p.tabs.findIndex((t) => t.path === rel);
    if (at >= 0) {
      if (pin) p.tabs[at].pinned = true;
    } else {
      const preview = p.tabs.findIndex((t) => !t.pinned && !t.dirty);
      const tab = { path: rel, pinned: pin, dirty: false, base: null, conflict: null, readOnly: false };
      if (preview >= 0) {
        const gone = p.tabs[preview].path;
        p.views.delete(gone);
        editor?.forget(keyOf(p, gone));
        p.tabs[preview] = tab;
      } else {
        p.tabs.push(tab);
      }
    }
    p.active = rel;
    // Show where it is in the tree.
    for (let dir = parentOf(rel); dir !== '.'; dir = parentOf(dir)) p.expanded.add(dir);
    if (!host.isOpen()) host.setOpen(true);
    loadView(p, rel);
    for (const dir of p.expanded) if (!p.dirs.has(dir)) loadDir(p, dir);
    render();
  }

  async function closeTab(p, rel) {
    const tab = tabOf(p, rel);
    if (!tab) return;
    if (tab.dirty) {
      if (prefs.autoSave && !tab.conflict) await save(p, rel);
      if (tab.dirty && !(await host.confirm(`Close ${baseOf(rel)} without saving?`, 'Your changes to it are not saved and will be lost.', 'Close Without Saving'))) return;
    }
    const at = p.tabs.indexOf(tab);
    if (at < 0) return;
    clearTimeout(timers.get(keyOf(p, rel)));
    editor?.forget(keyOf(p, rel));
    p.tabs.splice(at, 1);
    p.views.delete(rel);
    if (p.active === rel) p.active = p.tabs[Math.min(at, p.tabs.length - 1)]?.path ?? null;
    render();
  }

  // openPath opens an absolute path from a link, if it is in the workspace
  // shown, and reports whether it did.
  function openPath(abs) {
    const y = host.yesman();
    if (!y || !abs.startsWith(`${y.path}/`)) return false;
    const rel = abs.slice(y.path.length + 1).replace(/\/+$/, '');
    if (!rel) return false;
    open(rel, true);
    return true;
  }

  // ---------- Watching ----------

  function watch(name) {
    if (watching === name) return;
    watching = name;
    host.call('WatchFiles', name ?? '').catch(() => {}); // without a watch, reopening refreshes
  }

  // onChange takes the "files" event: it reloads what changed and is shown.
  function onChange({ yesman, paths }) {
    const p = panes.get(yesman);
    if (!p) return;
    const dirs = new Set();
    for (const rel of paths) {
      if (rel === '.') dirs.add('.');
      dirs.add(parentOf(rel));
      if (p.dirs.has(rel)) dirs.add(rel);
      if (tabOf(p, rel)) refresh(p, rel);
    }
    for (const dir of dirs) if (p.dirs.has(dir)) loadDir(p, dir);
  }

  // ---------- Rendering ----------

  function render() {
    const aside = $('#info');
    const p = current();
    aside.hidden = !p;
    document.documentElement.dataset.files = p ? (wide && p.active ? 'wide' : 'open') : 'closed';
    if (!p) {
      watch(null);
      return;
    }
    watch(p.name);
    if (!p.dirs.has('.')) loadDir(p, '.');
    const keep = scrolls();
    aside.toggleAttribute('data-has-file', Boolean(p.active));
    const mode = p.active ? 'file' : 'tree';
    const fixed = wide && p.active;
    aside.style.width = widths[mode] && !fixed ? `${clampPane(widths[mode], mode)}px` : '';
    if (widths.list) aside.style.setProperty('--tree-width', `${clampList(widths.list)}px`);
    else aside.style.removeProperty('--tree-width');
    aside.replaceChildren(head(p), bar(p), h('div', { class: 'files-body', 'data-tree': String(prefs.tree || !p.active) },
      p.active ? h('section', { class: 'files-view', id: 'files-view', 'aria-label': baseOf(p.active) }) : null,
      prefs.tree || !p.active ? tree(p) : null));
    if (!fixed) aside.append(paneHandle(mode));
    if (p.active && prefs.tree) $('.files-tree', aside).append(listHandle());
    renderView();
    renderTree();
    keep();
  }

  // scrolls keeps where the view and tree were scrolled, and the focused
  // row, across a render.
  function scrolls() {
    const viewPath = $('#files-view')?.dataset.path;
    const view = $('#files-view')?.scrollTop ?? 0;
    const list = $('#files-tree-list')?.scrollTop ?? 0;
    const focused = document.activeElement?.closest?.('#info [data-path], #info input')?.dataset?.path ?? document.activeElement?.id;
    return () => {
      const v = $('#files-view');
      if (v && v.dataset.path === viewPath) v.scrollTop = view;
      const l = $('#files-tree-list');
      if (l) l.scrollTop = list;
      if (!focused) return;
      const again = document.getElementById(focused) ?? $(`#info [data-path="${CSS.escape(focused)}"]`);
      again?.focus({ preventScroll: true });
    };
  }

  // ---------- Resizing ----------

  // The pane keeps at least 320px for the conversation beside it.
  function clampPane(px, mode) {
    const side = $('#side')?.getBoundingClientRect().width ?? 0;
    const max = Math.max(280, window.innerWidth - side - 320);
    return Math.round(Math.min(Math.max(px, mode === 'file' ? 420 : 220), max));
  }

  function clampList(px) {
    const pane = $('#info')?.getBoundingClientRect().width ?? 600;
    return Math.round(Math.min(Math.max(px, 160), pane - 240));
  }

  function paneHandle(mode) {
    const el = h('div', { class: 'files-resize', role: 'separator', 'aria-orientation': 'vertical', 'aria-label': 'Width of the files', tabindex: '0', title: 'Drag to resize; double-click for the default width' });
    const aside = () => $('#info');
    dragHandle(el, {
      widthAt: (x) => window.innerWidth - x,
      current: () => aside().getBoundingClientRect().width,
      apply: (px, done) => {
        const w = clampPane(px, mode);
        aside().style.width = `${w}px`;
        if (done) saveWidth(mode, w);
      },
      reset: () => { saveWidth(mode, 0); aside().style.width = ''; },
    });
    return el;
  }

  function listHandle() {
    const el = h('div', { class: 'files-resize', role: 'separator', 'aria-orientation': 'vertical', 'aria-label': 'Width of the file tree', tabindex: '0', title: 'Drag to resize; double-click for the default width' });
    const aside = () => $('#info');
    dragHandle(el, {
      widthAt: (x) => aside().getBoundingClientRect().right - x,
      current: () => $('.files-tree', aside()).getBoundingClientRect().width,
      apply: (px, done) => {
        const w = clampList(px);
        aside().style.setProperty('--tree-width', `${w}px`);
        if (done) saveWidth('list', w);
      },
      reset: () => { saveWidth('list', 0); aside().style.removeProperty('--tree-width'); },
    });
    return el;
  }

  function iconButton(name, label, onclick, extra = {}) {
    return h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick, ...extra }, icon(name));
  }

  function head(p) {
    const tabs = p.tabs.map((t) => h('div', { class: 'files-tab', role: 'tab', 'aria-selected': String(t.path === p.active), 'data-pinned': t.pinned ? 'true' : null, 'data-dirty': t.dirty ? 'true' : null, title: t.dirty ? `${t.path} (not saved)` : t.path },
      h('button', { class: 'files-tab-name', type: 'button', onclick: () => { p.active = t.path; if (!p.views.has(t.path)) loadView(p, t.path); render(); }, ondblclick: () => { t.pinned = true; render(); } }, baseOf(t.path)),
      iconButton('x', `Close ${baseOf(t.path)}`, () => closeTab(p, t.path))));
    return h('div', { class: 'files-head' },
      tabs.length ? h('div', { class: 'files-tabs', role: 'tablist', 'aria-label': 'Open files' }, tabs) : h('strong', { class: 'files-title' }, 'Files'),
      h('div', { class: 'files-head-actions' },
        p.active ? iconButton(wide ? 'restore' : 'expand', wide ? 'Show the conversation again' : 'Widen the preview', () => { wide = !wide; render(); }) : null,
        iconButton('x', 'Close the files', () => host.setOpen(false))));
  }

  function renderHead() {
    const p = current();
    const old = $('#info .files-head');
    if (p && old) old.replaceWith(head(p));
  }

  function bar(p) {
    const isMarkdown = p.active && languageOf(p.active) === 'markdown';
    const mode = p.mode.get(p.active) ?? 'preview';
    return h('div', { class: 'files-bar' },
      h('span', { class: 'files-crumb', title: p.active ? absOf(p, p.active) : p.path }, p.active ?? p.name),
      h('div', { class: 'files-bar-actions' },
        isMarkdown ? h('button', { class: 'mds-button mds-button--small mds-button--subtle', type: 'button', onclick: () => { p.mode.set(p.active, mode === 'preview' ? 'source' : 'preview'); render(); } }, mode === 'preview' ? 'Source' : 'Preview') : null,
        iconButton('collapse', 'Collapse all folders', () => { p.expanded.clear(); renderTree(); }, { disabled: !p.expanded.size }),
        p.active ? iconButton('tree', prefs.tree ? 'Hide the file tree' : 'Show the file tree', toggleTree, { 'aria-pressed': String(prefs.tree) }) : null,
        iconButton('more', 'More', (e) => more(e, p))));
  }

  async function more(e, p) {
    const r = e.currentTarget.getBoundingClientRect();
    try {
      const action = await host.call('FilesMenu', Math.round(r.left), Math.round(r.bottom + 4), prefs.tree, prefs.wrap, prefs.autoSave, p.expanded.size > 0);
      const y = host.yesman();
      switch (action) {
        case 'find': focusFind(); break;
        case 'tree': toggleTree(); break;
        case 'collapse': p.expanded.clear(); renderTree(); break;
        case 'wrap': setPref('wrap', !prefs.wrap); editor?.setWrap(prefs.wrap); break;
        case 'autosave':
          setPref('autoSave', !prefs.autoSave);
          if (prefs.autoSave) for (const t of p.tabs) if (t.dirty) save(p, t.path);
          break;
        case 'knowledge': open('.yesman/knowledge/INDEX.md', true); break;
        case 'checkout': if (y) host.checkout(y); break;
        case 'reveal': host.call('Reveal', p.path).catch(host.fail); break;
        case 'dismiss': host.dismiss(p.name); break;
      }
    } catch (err) {
      host.fail(err);
    }
  }

  function setPref(key, on) {
    prefs[key] = on;
    localStorage.setItem(`yesman.files.${key.toLowerCase()}`, key === 'tree' ? (on ? 'shown' : 'hidden') : (on ? 'on' : 'off'));
  }

  function toggleTree() {
    setPref('tree', !prefs.tree);
    render();
  }

  // ---------- Tree ----------

  function tree(p) {
    const input = h('input', { class: 'mds-input', id: 'files-find', type: 'search', placeholder: 'Go to a file', 'aria-label': 'Go to a file', spellcheck: 'false', autocomplete: 'off', value: p.query,
      oninput: (e) => find(p, e.currentTarget.value),
      onkeydown: (e) => {
        if (e.key === 'Enter' && p.results?.length) open(p.results[0], true);
        else if (e.key === 'Escape' && p.query) { e.preventDefault(); find(p, ''); e.currentTarget.value = ''; }
        else if (e.key === 'ArrowDown') { e.preventDefault(); $('#files-tree-list [data-path]')?.focus(); }
      } });
    return h('nav', { class: 'files-tree', 'aria-label': 'Files' },
      h('label', { class: 'search files-search' }, icon('search'), input),
      h('div', { class: 'files-tree-list', id: 'files-tree-list', role: 'tree', 'aria-label': p.name, onkeydown: (e) => treeKey(e, p) }));
  }

  let findSeq = 0;
  async function find(p, query) {
    p.query = query;
    const seq = ++findSeq;
    if (!query.trim()) {
      p.results = null;
      renderTree();
      return;
    }
    try {
      const results = await host.call('FindFiles', p.name, query);
      if (seq !== findSeq) return;
      p.results = results;
    } catch (err) {
      p.results = [];
      host.fail(err);
    }
    renderTree();
  }

  function fileIcon(rel) {
    const lang = languageOf(rel);
    if (/\.(png|jpe?g|gif|webp|svg|ico|bmp)$/i.test(rel)) return 'image';
    if (lang === 'markdown') return 'fileText';
    if (lang !== 'plaintext') return 'fileCode';
    return 'file';
  }

  function row(p, entry, depth) {
    const open = entry.dir && p.expanded.has(entry.path);
    return h('button', {
      class: 'tree-row', type: 'button', role: 'treeitem', 'data-path': entry.path, 'data-dir': entry.dir ? 'true' : null,
      'aria-level': String(depth + 1), 'aria-expanded': entry.dir ? String(open) : null, 'aria-selected': String(entry.path === p.active),
      style: `--depth: ${depth}`, title: entry.path,
      onclick: () => (entry.dir ? toggleDir(p, entry.path) : open_(entry.path)),
      ondblclick: () => { if (!entry.dir) open_(entry.path, true); },
      oncontextmenu: (e) => fileMenu(e, p, entry),
    },
    entry.dir ? icon('chevron', 'mds-icon tree-chevron') : icon(fileIcon(entry.path), 'mds-icon tree-icon'),
    h('span', { class: 'tree-name' }, entry.name));
  }

  const open_ = (rel, pin) => open(rel, pin);

  function toggleDir(p, rel) {
    if (p.expanded.has(rel)) p.expanded.delete(rel);
    else {
      p.expanded.add(rel);
      if (!Array.isArray(p.dirs.get(rel))) loadDir(p, rel);
    }
    renderTree();
    $(`#files-tree-list [data-path="${CSS.escape(rel)}"]`)?.focus({ preventScroll: true });
    $('#info .files-bar-actions [aria-label="Collapse all folders"]')?.toggleAttribute('disabled', !p.expanded.size);
  }

  async function fileMenu(e, p, entry) {
    e.preventDefault();
    try {
      const action = await host.call('FileMenu', p.name, entry.path, Boolean(entry.dir), Math.round(e.clientX), Math.round(e.clientY));
      if (action === 'open') open(entry.path, true);
    } catch (err) {
      host.fail(err);
    }
  }

  function renderTree() {
    const list = $('#files-tree-list');
    const p = current();
    if (!list || !p) return;
    const top = list.scrollTop;
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.path : null;
    const rows = [];
    if (p.results) {
      for (const rel of p.results) {
        rows.push(h('button', { class: 'tree-row tree-result', type: 'button', role: 'treeitem', 'data-path': rel, 'aria-selected': String(rel === p.active), title: rel, style: '--depth: 0',
          onclick: () => open(rel), ondblclick: () => open(rel, true), oncontextmenu: (e) => fileMenu(e, p, { path: rel }) },
          icon(fileIcon(rel), 'mds-icon tree-icon'),
          h('span', { class: 'tree-name' }, baseOf(rel), h('span', { class: 'tree-dir' }, parentOf(rel) === '.' ? '' : parentOf(rel)))));
      }
      if (!rows.length) rows.push(h('p', { class: 'tree-note' }, `No file matches “${p.query.trim()}”.`));
    } else {
      const walk = (dir, depth) => {
        const entries = p.dirs.get(dir);
        if (entries === 'loading' || entries === undefined) {
          if (depth === 0) rows.push(h('p', { class: 'tree-note' }, 'Loading…'));
          return;
        }
        if (entries.error) {
          rows.push(h('p', { class: 'tree-note danger', style: `--depth: ${depth}` }, entries.error));
          return;
        }
        if (!entries.length && depth === 0) rows.push(h('p', { class: 'tree-note' }, 'The workspace is empty. Agents check repositories out into it.'));
        for (const e of entries.slice(0, MAX_TREE_ROWS)) {
          rows.push(row(p, e, depth));
          if (e.dir && p.expanded.has(e.path)) walk(e.path, depth + 1);
        }
        if (entries.length > MAX_TREE_ROWS) {
          rows.push(h('p', { class: 'tree-note', style: `--depth: ${depth}` }, `${(entries.length - MAX_TREE_ROWS).toLocaleString()} more. Go to a file to find them.`));
        }
      };
      walk('.', 0);
    }
    list.replaceChildren(...rows);
    list.scrollTop = top;
    if (focused) $(`#files-tree-list [data-path="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
    // Only one row takes Tab; the arrows move between them.
    const rowsEls = [...list.querySelectorAll('[data-path]')];
    const tabStop = rowsEls.find((r) => r.getAttribute('aria-selected') === 'true') ?? rowsEls[0];
    for (const r of rowsEls) r.tabIndex = r === tabStop ? 0 : -1;
  }

  function treeKey(e, p) {
    const rowEl = e.target.closest('[data-path]');
    if (!rowEl) return;
    const rows = [...$('#files-tree-list').querySelectorAll('[data-path]')];
    const at = rows.indexOf(rowEl);
    const rel = rowEl.dataset.path;
    const dir = rowEl.dataset.dir === 'true';
    const go = (el) => { if (el) { e.preventDefault(); el.focus(); } };
    switch (e.key) {
      case 'ArrowDown': go(rows[at + 1]); break;
      case 'ArrowUp': go(rows[at - 1] ?? $('#files-find')); break;
      case 'Home': go(rows[0]); break;
      case 'End': go(rows.at(-1)); break;
      case 'ArrowRight':
        if (dir && !p.expanded.has(rel)) { e.preventDefault(); toggleDir(p, rel); } else if (dir) go(rows[at + 1]);
        break;
      case 'ArrowLeft':
        if (dir && p.expanded.has(rel)) { e.preventDefault(); toggleDir(p, rel); } else if (parentOf(rel) !== '.') go($(`#files-tree-list [data-path="${CSS.escape(parentOf(rel))}"]`));
        break;
    }
  }

  // ---------- Preview ----------

  function renderView() {
    const section = $('#files-view');
    const p = current();
    if (!section || !p?.active) return;
    const rel = p.active;
    const v = p.views.get(rel);
    section.dataset.fresh = section.dataset.path === rel ? '' : 'true';
    section.dataset.path = rel;
    const top = section.dataset.fresh ? 0 : section.scrollTop;
    if (!v || v === 'loading') {
      section.replaceChildren(h('p', { class: 'files-note' }, 'Loading…'));
      return;
    }
    if (v.error) {
      section.replaceChildren(h('div', { class: 'files-note' },
        h('strong', null, /no such file|not exist/i.test(v.error) ? 'This file is gone' : 'This file can’t be shown'),
        h('span', null, v.error)));
      return;
    }
    const size = formatSize(v.size);
    if (v.kind === 'image') {
      const img = h('img', { src: v.dataUrl, alt: baseOf(rel) });
      const caption = h('span', null, size);
      img.addEventListener('load', () => { caption.textContent = `${img.naturalWidth} × ${img.naturalHeight} · ${size}`; }, { once: true });
      section.replaceChildren(h('figure', { class: 'files-image' }, img, h('figcaption', null, caption)));
      return;
    }
    if (v.kind === 'binary') {
      section.replaceChildren(h('div', { class: 'files-note' },
        h('strong', null, 'This file isn’t text'),
        h('span', null, `${size}. Open it with another app to see it.`),
        h('div', null, h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => host.call('Reveal', absOf(p, rel)).catch(host.fail) }, icon('folder'), 'Reveal'))));
      return;
    }
    const tab = tabOf(p, rel);
    const k = keyOf(p, rel);
    const notice = v.truncated ? h('p', { class: 'files-notice' }, `Showing the first ${formatSize(new Blob([v.text]).size)} of this ${size} file. Large files open read-only.`) : null;
    const banner = h('div', { id: 'files-banner' });
    const lang = languageOf(rel);
    const rendered = lang === 'markdown' && (p.mode.get(rel) ?? 'preview') === 'preview';
    section.classList.toggle('files-view--editor', !rendered);
    if (rendered) {
      const article = h('article', { class: 'md files-md', html: renderMarkdown(editor?.has(k) ? editor.text(k) : v.text) });
      // Links to other files of the workspace open them here.
      article.addEventListener('click', (e) => {
        const href = e.target.closest('a[href]')?.getAttribute('href');
        if (!href || href.startsWith('#') || /^[a-z][a-z0-9+.-]*:/i.test(href) || e.target.closest('a').dataset.path) return;
        const target = resolve(parentOf(rel), href.split('#')[0]);
        if (target) open(target, true);
      });
      section.replaceChildren(...[banner, notice, article].filter(Boolean));
      renderBanner();
      if (!section.dataset.fresh) section.scrollTop = top;
      return;
    }
    const slot = h('div', { class: 'files-editor', id: 'files-editor' }, h('p', { class: 'files-note' }, 'Loading…'));
    section.replaceChildren(...[banner, notice, slot].filter(Boolean));
    renderBanner();
    ensureEditor().then((ed) => {
      if (!slot.isConnected || current() !== p || p.active !== rel) return;
      slot.replaceChildren(ed.dom);
      ed.show(k, { text: v.text, lang, readOnly: Boolean(tab?.readOnly || v.truncated) });
    }, (err) => slot.replaceChildren(h('p', { class: 'files-note' }, `The editor did not load: ${err?.message || err}`)));
  }

  // renderBanner says when the file shown changed on disk under unsaved
  // changes, with the choice of which to keep.
  function renderBanner() {
    const slot = $('#files-banner');
    const p = current();
    const tab = p && tabOf(p, p.active);
    if (!slot) return;
    if (!tab?.conflict) {
      slot.replaceChildren();
      slot.className = '';
      slot.removeAttribute('role');
      return;
    }
    const gone = tab.conflict === 'deleted';
    slot.className = 'files-conflict';
    slot.setAttribute('role', 'alert');
    slot.replaceChildren(
      h('span', null, gone ? 'This file was deleted on disk. Your changes are only here.' : 'This file changed on disk since you opened it, and you have unsaved changes.'),
      h('div', { class: 'files-conflict-actions' },
        gone
          ? h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => closeTab(p, p.active) }, 'Close')
          : h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => reload(p, p.active) }, 'Use the File on Disk'),
        h('button', { class: 'mds-button mds-button--small mds-button--primary', type: 'button', onclick: () => save(p, p.active, true) }, gone ? 'Save Again' : 'Keep Mine')));
  }

  // resolve joins a relative link to the directory it is in, or returns
  // null when it climbs out of the workspace.
  function resolve(dir, href) {
    const parts = dir === '.' ? [] : dir.split('/');
    for (const part of decodeURI(href).split('/')) {
      if (part === '..') { if (!parts.length) return null; parts.pop(); } else if (part && part !== '.') parts.push(part);
    }
    return parts.join('/') || null;
  }

  function formatSize(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
    return `${(n / 1024 / 1024).toFixed(1)} MB`;
  }

  // ---------- Keys ----------

  function focusFind() {
    if (!host.yesman()) return;
    if (!host.isOpen()) host.setOpen(true);
    if (!prefs.tree) setPref('tree', true);
    render();
    const input = $('#files-find');
    input?.focus();
    input?.select();
  }

  // onKey takes the page's shortcuts for the pane, and reports whether it
  // used the key.
  function onKey(e) {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 's' && !e.defaultPrevented) {
      const p = current();
      if (!p?.active) return false;
      e.preventDefault();
      save(p, p.active);
      return true;
    }
    if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'p') {
      e.preventDefault();
      focusFind();
      return true;
    }
    if (mod && e.shiftKey && e.key.toLowerCase() === 'y' && current()) {
      e.preventDefault();
      toggleTree();
      return true;
    }
    return false;
  }

  return { render, open, openPath, onChange, onKey };
}
