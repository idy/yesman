// The files pane: a yesman's workspace as a tree, and previews of its
// files in tabs, with code highlighted, Markdown rendered and images shown.
// The Go side lists and reads files (files.go) and reports changes while
// the pane watches a workspace.
//
// The pane shows the whole workspace, or one part of it: the knowledge,
// read as rendered pages, or a worktree, its files or its changes against
// the branch it started from (diff.js). Each part keeps its own tabs.

import { $, h, icon } from './dom.js';
import { renderDocument } from './markdown.js';
import { languageOf } from './code.js';
import { createEditor } from './editor.js';
import { createChanges, statusMark, counts } from './diff.js';
import { t, tn, formatNumber } from './i18n.js';

// A folder's menu shows this many entries.
const MENU_ROWS = 500;

// The knowledge notes of a workspace, and the note that lists them.
const KNOWLEDGE = '.yesman/knowledge';
const KNOWLEDGE_INDEX = `${KNOWLEDGE}/INDEX.md`;

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
//   shown()                   after the pane draws, or what it shows changes
export function createFiles(host) {
  const panes = new Map(); // yesman name and part shown -> that pane's state
  const scopes = new Map(); // yesman name -> the part of its workspace shown
  let watching = null;
  let wide = false;

  // A scope is the part of a workspace the pane shows: all of it, the
  // knowledge, or a worktree by its directory.
  const WORKSPACE = { kind: 'workspace' };
  const scopeOf = (y) => scopes.get(y.name) ?? WORKSPACE;
  const scopeKey = (sc) => (sc.kind === 'worktree' ? `worktree:${sc.dir}` : sc.kind);
  const rootOf = (sc) => (sc.kind === 'knowledge' ? KNOWLEDGE : sc.kind === 'worktree' ? sc.dir : '.');
  const within = (rel, root) => root === '.' || rel === root || rel.startsWith(`${root}/`);
  // Knowledge reads as pages: its files open rendered, and read-only.
  const locked = (p) => p.scope.kind === 'knowledge';
  // A worktree's pane shows its files, or its changes.
  const isChanges = (p) => p?.scope.kind === 'worktree' && p.show === 'changes';

  function pane(y, scope = scopeOf(y)) {
    const key = `${y.name}\u0001${scopeKey(scope)}`;
    let p = panes.get(key);
    if (!p) {
      p = { key, name: y.name, path: y.path, scope, root: rootOf(scope), show: 'files', tabs: [], active: null, expanded: new Set(), dirs: new Map(), views: new Map(), mode: new Map(), query: '', results: null };
      panes.set(key, p);
    }
    p.path = y.path;
    return p;
  }

  const panesOf = (name) => [...panes.values()].filter((p) => p.name === name);

  const changes = createChanges({
    call: (...args) => host.call(...args),
    fail: (err) => host.fail(err),
    render: () => render(),
    renderBar: () => renderBar(),
    openFile: (rel) => open(rel, true),
  });

  const parentOf = (rel) => (rel.includes('/') ? rel.slice(0, rel.lastIndexOf('/')) : '.');
  const baseOf = (rel) => rel.slice(rel.lastIndexOf('/') + 1);
  // Paths from Go use the system's separator: backslashes on Windows.
  const slashed = (path) => path.replace(/\\/g, '/');
  const absOf = (p, rel) => {
    const sep = p.path.includes('\\') ? '\\' : '/';
    return p.path.replace(/[\\/]+$/, '') + sep + rel.split('/').join(sep);
  };

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
        tab.readOnly = Boolean(v.truncated) || locked(p);
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
  const keyOf = (p, rel) => `${p.key}\u0000${rel}`;
  const tabOf = (p, rel) => p?.tabs.find((t) => t.path === rel);

  function ensureEditor() {
    editorLoading ??= createEditor({ onChange: edited, onSave: (k) => { const [p, rel] = byKey(k); if (p) save(p, rel); } })
      .then((ed) => { editor = ed; ed.setWrap(prefs.wrap); return ed; });
    return editorLoading;
  }

  function byKey(k) {
    const at = k.indexOf('\u0000');
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
    timers.set(k, setTimeout(() => {
      timers.delete(k);
      if (prefs.autoSave) save(p, rel);
    }, 800));
  }

  // save writes a file's text, unless it changed on disk since it was read:
  // then the file keeps both until the user reloads or overwrites it. The
  // saves of a file run one after another, each from the hash the one
  // before wrote, so that two never race.
  function save(p, rel, force = false) {
    const tab = tabOf(p, rel);
    if (!tab) return Promise.resolve(false);
    const run = (tab.saving ?? Promise.resolve()).then(() => write(p, rel, tab, force));
    tab.saving = run.catch(() => {});
    return run;
  }

  async function write(p, rel, tab, force) {
    const k = keyOf(p, rel);
    if (tabOf(p, rel) !== tab || tab.readOnly || !editor?.has(k)) return false;
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
    // A save in progress changes the file and the hash it is compared with.
    await tab.saving;
    if (tabOf(p, rel) !== tab) return;
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
    tab.conflict = null;
    p.views.set(rel, v);
    // A file that grew past the limit can only be read now, or the other
    // way round.
    const readOnly = Boolean(v.truncated) || locked(p);
    const changed = tab.readOnly !== readOnly;
    if (changed) {
      tab.readOnly = readOnly;
      editor.setReadOnly(k, readOnly);
    }
    if (current() === p && p.active === rel && (changed || languageOf(rel) === 'markdown')) renderView();
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
  // file replaces, unless pin keeps it. A file outside the part of the
  // workspace shown opens with all of it.
  function open(rel, pin = false) {
    const y = host.yesman();
    if (!y) return;
    let p = pane(y);
    if (!within(rel, p.root)) {
      scopes.delete(y.name);
      p = pane(y);
    }
    p.show = 'files';
    const at = p.tabs.findIndex((t) => t.path === rel);
    if (at >= 0) {
      if (pin) p.tabs[at].pinned = true;
    } else {
      const preview = p.tabs.findIndex((t) => !t.pinned && !t.dirty);
      const tab = { path: rel, pinned: pin, dirty: false, base: null, conflict: null, readOnly: locked(p) };
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
    for (let dir = parentOf(rel); dir !== '.' && dir !== p.root; dir = parentOf(dir)) p.expanded.add(dir);
    if (!host.isOpen()) host.setOpen(true);
    // A file the editor has open may have changed while nothing watched
    // it: refresh reconciles it, keeping unsaved changes as a conflict.
    if (editor?.has(keyOf(p, rel))) refresh(p, rel);
    else loadView(p, rel);
    for (const dir of p.expanded) if (!p.dirs.has(dir)) loadDir(p, dir);
    render();
  }

  async function closeTab(p, rel) {
    const tab = tabOf(p, rel);
    if (!tab) return;
    if (tab.dirty) {
      if (prefs.autoSave && !tab.conflict) await save(p, rel);
      if (tab.dirty && !(await host.confirm(t('Close {file} without saving?', { file: baseOf(rel) }), t('Your changes to it are not saved and will be lost.'), t('Close Without Saving')))) return;
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
    if (!y) return false;
    const root = `${slashed(y.path).replace(/\/+$/, '')}/`;
    const path = slashed(abs);
    // Windows paths match regardless of case.
    const inside = /^[A-Za-z]:\//.test(root) ? path.toLowerCase().startsWith(root.toLowerCase()) : path.startsWith(root);
    if (!inside) return false;
    const rel = path.slice(root.length).replace(/\/+$/, '');
    if (!rel) return false;
    open(rel, true);
    return true;
  }

  // ---------- Parts of the workspace ----------

  // setScope shows a part of the shown yesman's workspace, null for all.
  function setScope(scope, show) {
    const y = host.yesman();
    if (!y) return null;
    if (scope && scope.kind !== 'workspace') scopes.set(y.name, scope);
    else scopes.delete(y.name);
    const p = pane(y);
    if (show) p.show = show;
    if (!host.isOpen()) host.setOpen(true);
    else render();
    return p;
  }

  // openKnowledge shows the knowledge, with its index open.
  async function openKnowledge() {
    const p = setScope({ kind: 'knowledge' });
    if (!p || p.tabs.length) return;
    if (!Array.isArray(p.dirs.get(p.root))) await loadDir(p, p.root);
    const entries = p.dirs.get(p.root);
    if (!p.tabs.length && Array.isArray(entries) && entries.some((e) => e.path === KNOWLEDGE_INDEX)) open(KNOWLEDGE_INDEX, true);
  }

  // openWorktree shows a worktree: its changes, or its files.
  function openWorktree(dir, show = 'changes') {
    setScope({ kind: 'worktree', dir }, show);
  }

  // shown says what the pane shows: null when closed, else the part of the
  // workspace and, for a worktree, whether its files or its changes.
  function shown() {
    const p = current();
    return p ? { yesman: p.name, kind: p.scope.kind, dir: p.scope.dir ?? null, show: p.scope.kind === 'worktree' ? p.show : null } : null;
  }

  // ---------- Watching ----------

  function watch(name) {
    if (watching === name) return;
    watching = name;
    host.call('WatchFiles', name ?? '').catch(() => {});
    // Nothing was watched while the pane was closed or showed another
    // workspace: read again what it shows, as a change would.
    if (!name) return;
    for (const p of panesOf(name)) {
      for (const dir of p.dirs.keys()) loadDir(p, dir);
      for (const t of p.tabs) refresh(p, t.path);
    }
    changes.invalidate(name);
  }

  // onChange takes the "files" event: it reloads what changed and is shown.
  function onChange({ yesman, paths }) {
    for (const p of panesOf(yesman)) {
      const dirs = new Set();
      for (const rel of paths) {
        if (rel === '.') dirs.add('.');
        dirs.add(parentOf(rel));
        if (p.dirs.has(rel)) dirs.add(rel);
        if (tabOf(p, rel)) refresh(p, rel);
      }
      for (const dir of dirs) if (p.dirs.has(dir)) loadDir(p, dir);
    }
    changes.onFiles(yesman, paths, (name, dir) => {
      const p = current();
      return Boolean(p && p.name === name && isChanges(p) && p.scope.dir === dir);
    });
  }

  // ---------- Rendering ----------

  function render() {
    const aside = $('#files');
    const p = current();
    aside.hidden = !p;
    const diff = isChanges(p);
    const hasView = Boolean(p && (p.active || diff));
    document.documentElement.dataset.files = p ? (wide && hasView ? 'wide' : 'open') : 'closed';
    if (!p) {
      watch(null);
      closeMenu();
      host.shown?.();
      return;
    }
    watch(p.name);
    closeMenu();
    if (diff) changes.ensure(p);
    else if (!p.dirs.has(p.root)) loadDir(p, p.root);
    const keep = scrolls();
    aside.toggleAttribute('data-has-file', hasView);
    const mode = hasView ? 'file' : 'tree';
    const fixed = wide && hasView;
    aside.style.width = widths[mode] && !fixed ? `${clampPane(widths[mode], mode)}px` : '';
    if (widths.list) aside.style.setProperty('--tree-width', `${clampList(widths.list)}px`);
    else aside.style.removeProperty('--tree-width');
    const showTree = diff || prefs.tree || !p.active;
    aside.replaceChildren(...[head(p), bar(p), h('div', { class: 'files-body', 'data-tree': String(showTree) },
      hasView ? h('section', { class: 'files-view', id: 'files-view', 'aria-label': diff ? t('Changes') : baseOf(p.active) }) : null,
      showTree ? (diff ? changes.tree(p) : tree(p)) : null)].filter(Boolean));
    if (!fixed) aside.append(paneHandle(mode));
    if (hasView && showTree) $('.files-tree', aside).append(listHandle());
    if (diff) {
      changes.view(p, $('#files-view'));
    } else {
      renderView();
      renderTree();
    }
    keep();
    host.shown?.();
  }

  // scrolls keeps where the view and tree were scrolled, and the focused
  // row, across a render.
  function scrolls() {
    const viewPath = $('#files-view')?.dataset.path;
    const view = $('#files-view')?.scrollTop ?? 0;
    const list = $('#files-tree-list')?.scrollTop ?? 0;
    const focused = document.activeElement?.closest?.('#files [data-path], #files input')?.dataset?.path ?? document.activeElement?.id;
    return () => {
      const v = $('#files-view');
      if (v && v.dataset.path === viewPath) v.scrollTop = view;
      const l = $('#files-tree-list');
      if (l) l.scrollTop = list;
      if (!focused) return;
      const again = document.getElementById(focused) ?? $(`#files [data-path="${CSS.escape(focused)}"]`);
      again?.focus({ preventScroll: true });
    };
  }

  // ---------- Resizing ----------

  // The pane keeps at least 320px for the conversation beside it.
  function clampPane(px, mode) {
    const side = ($('#side')?.getBoundingClientRect().width ?? 0) + ($('#yesman-info')?.getBoundingClientRect().width ?? 0);
    const max = Math.max(280, window.innerWidth - side - 320);
    return Math.round(Math.min(Math.max(px, mode === 'file' ? 420 : 220), max));
  }

  function clampList(px) {
    const pane = $('#files')?.getBoundingClientRect().width ?? 600;
    return Math.round(Math.min(Math.max(px, 160), pane - 240));
  }

  function paneHandle(mode) {
    const el = h('div', { class: 'files-resize', role: 'separator', 'aria-orientation': 'vertical', 'aria-label': t('Width of the files'), tabindex: '0', title: t('Drag to resize; double-click for the default width') });
    const aside = () => $('#files');
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
    const el = h('div', { class: 'files-resize', role: 'separator', 'aria-orientation': 'vertical', 'aria-label': t('Width of the file tree'), tabindex: '0', title: t('Drag to resize; double-click for the default width') });
    const aside = () => $('#files');
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
    const diff = isChanges(p);
    const title = diff ? t('Changes') : p.scope.kind === 'knowledge' ? t('Knowledge') : t('Files');
    const tabs = (diff ? [] : p.tabs).map((tab) => h('div', { class: 'files-tab', role: 'tab', 'aria-selected': String(tab.path === p.active), 'data-pinned': tab.pinned ? 'true' : null, 'data-dirty': tab.dirty ? 'true' : null, title: tab.dirty ? t('{path} (not saved)', { path: tab.path }) : tab.path },
      h('button', { class: 'files-tab-name', type: 'button', onclick: () => { p.active = tab.path; if (!p.views.has(tab.path)) loadView(p, tab.path); render(); }, ondblclick: () => { tab.pinned = true; render(); } }, baseOf(tab.path)),
      iconButton('x', t('Close {file}', { file: baseOf(tab.path) }), () => closeTab(p, tab.path))));
    return h('div', { class: 'files-head drag' },
      tabs.length ? h('div', { class: 'files-tabs', role: 'tablist', 'aria-label': t('Open files') }, tabs) : h('strong', { class: 'files-title' }, title),
      h('div', { class: 'files-head-actions' },
        p.active || diff ? iconButton(wide ? 'restore' : 'expand', wide ? t('Show the conversation again') : t('Widen the preview'), () => { wide = !wide; render(); }) : null,
        iconButton('x', t('Close the files'), () => host.setOpen(false))));
  }

  function renderHead() {
    const p = current();
    const old = $('#files .files-head');
    if (p && old) old.replaceWith(head(p));
  }

  // ---------- The bar ----------

  // bar is one row: the part of the workspace shown, which switches to
  // another, then the path of what is open, a segment per folder, then what
  // can be done, and for a worktree the choice of its files or its changes.
  function bar(p) {
    const tab = (show, label) => h('button', { type: 'button', role: 'tab', 'aria-selected': String(p.show === show),
      onclick: () => { if (p.show !== show) { p.show = show; render(); } } }, label);
    const tabs = p.scope.kind === 'worktree'
      ? h('div', { class: 'mds-tabs files-scope-tabs', role: 'tablist', 'aria-label': p.scope.dir }, tab('files', t('Files')), tab('changes', t('Changes')))
      : null;
    if (isChanges(p)) {
      return h('div', { class: 'files-bar' }, changeCrumbs(p), h('div', { class: 'files-bar-actions' }, tabs, changes.actions(p)));
    }
    const isMarkdown = p.active && languageOf(p.active) === 'markdown' && !locked(p);
    const mode = p.mode.get(p.active) ?? 'preview';
    return h('div', { class: 'files-bar' },
      crumbs(p),
      h('div', { class: 'files-bar-actions' },
        tabs,
        isMarkdown ? h('button', { class: 'mds-button mds-button--small mds-button--subtle', type: 'button', onclick: () => { p.mode.set(p.active, mode === 'preview' ? 'source' : 'preview'); render(); } }, mode === 'preview' ? t('Source') : t('Preview')) : null,
        iconButton('collapse', t('Collapse all folders'), () => { p.expanded.clear(); renderTree(); }, { disabled: !p.expanded.size, 'data-action': 'collapse' }),
        p.active ? iconButton('tree', prefs.tree ? t('Hide the file tree') : t('Show the file tree'), toggleTree, { 'aria-pressed': String(prefs.tree) }) : null,
        iconButton('more', t('More'), (e) => more(e, p))));
  }

  function renderBar() {
    const p = current();
    const old = $('#files .files-bar');
    if (!p || !old) return;
    closeMenu();
    old.replaceWith(bar(p));
  }

  // ---------- Path ----------

  const sep = () => h('span', { class: 'files-crumb-sep', 'aria-hidden': 'true' }, '/');
  const crumb = (label, last, onopen) => h('button', {
    class: 'files-crumb', type: 'button', 'aria-haspopup': 'menu', 'aria-expanded': 'false', 'aria-current': last ? 'page' : null,
    onclick: (e) => onopen(e.currentTarget),
  }, label);
  const scopeLabel = (p) => (p.scope.kind === 'worktree' ? p.scope.dir : p.scope.kind === 'knowledge' ? t('Knowledge') : p.name);

  // scopeCrumb names the part of the workspace shown, and opens the menu
  // that shows another: all of it, the knowledge, or another worktree.
  function scopeCrumb(p) {
    const kind = p.scope.kind;
    return h('button', {
      class: 'files-crumb files-crumb--scope', type: 'button', 'aria-haspopup': 'menu', 'aria-expanded': 'false',
      'aria-current': (isChanges(p) ? changes.shown(p).path : p.active) ? null : 'page', title: t('Show all files, the knowledge or another worktree'),
      onclick: (e) => scopeMenu(e.currentTarget, p),
    }, icon(kind === 'worktree' ? 'branch' : kind === 'knowledge' ? 'book' : 'folder', 'mds-icon files-crumb-icon'), h('span', null, scopeLabel(p)));
  }

  // crumbs shows where the file open is, a segment per folder below the
  // part shown: each opens a menu of its folder, and the file's own segment
  // one of the folder it is in.
  function crumbs(p) {
    const base = p.root === '.' ? '' : `${p.root}/`;
    const parts = p.active ? p.active.slice(base.length).split('/') : [];
    const nav = h('nav', { class: 'files-crumbs', 'aria-label': t('Path'), title: p.active ? absOf(p, p.active) : absOf(p, p.root) }, scopeCrumb(p));
    const source = filesSource(p);
    parts.forEach((part, i) => {
      const last = i === parts.length - 1;
      const dir = last ? parentOf(p.active) : base + parts.slice(0, i + 1).join('/');
      nav.append(sep(), crumb(part, last, (anchor) => folderMenu(anchor, source, dir, p.active)));
    });
    return nav;
  }

  // changeCrumbs shows the changed file whose diff shows, or the folder
  // whose files show, its folders opening menus of what changed in them;
  // or, with neither, how much the worktree changed.
  function changeCrumbs(p) {
    const { path: rel, file } = changes.shown(p);
    const nav = h('nav', { class: 'files-crumbs', 'aria-label': t('Path'), title: rel ? `${p.scope.dir}/${rel}` : p.scope.dir }, scopeCrumb(p));
    if (!rel) {
      const total = changes.totals(p);
      if (total) nav.append(h('span', { class: 'changes-total' }, tn(total.files, '{n} file', '{n} files'), counts(total)));
      return nav;
    }
    const parts = rel.split('/');
    const source = changesSource(p);
    parts.forEach((part, i) => {
      const last = i === parts.length - 1;
      // A file's segment opens the folder it is in; a folder's, itself.
      const dir = last && file ? source.parentOf(rel) : parts.slice(0, i + 1).join('/');
      nav.append(sep(), crumb(part, last, (anchor) => folderMenu(anchor, source, dir, rel)));
    });
    return nav;
  }

  // What a folder's menu lists: the files of a folder, or what changed in
  // it, which the Go side keeps in memory and lists a folder at a time.
  const chevron = () => icon('chevron', 'mds-icon files-menu-chevron');

  function filesSource(p) {
    return {
      root: p.root,
      rootName: scopeLabel(p),
      parentOf,
      cached: (folder) => { const e = p.dirs.get(folder); return Array.isArray(e) ? { entries: e, more: 0 } : null; },
      list: async (folder) => {
        const entries = await host.call('ListFiles', p.name, folder);
        p.dirs.set(folder, entries);
        return { entries, more: 0 };
      },
      icon: (e) => (e.dir ? chevron() : icon(fileIcon(e.path), 'mds-icon files-menu-icon')),
      aside: () => null,
      current: () => p.active,
      open: (e) => { closeMenu(); open(e.path, true); },
      empty: () => t('This folder is empty.'),
      moreNote: (n) => t('{n} more. Go to a file to find them.', { n: formatNumber(n) }),
    };
  }

  function changesSource(p) {
    return {
      root: '',
      rootName: p.scope.dir,
      parentOf: (rel) => (rel.includes('/') ? rel.slice(0, rel.lastIndexOf('/')) : ''),
      cached: (folder) => changes.cached(p, folder),
      list: (folder) => changes.list(p, folder),
      icon: (e) => (e.dir ? chevron() : statusMark(e.status)),
      aside: (e) => counts(e),
      current: () => changes.shown(p).path,
      open: (e) => { closeMenu(); changes.select(p, e.path); },
      empty: () => t('No changes here.'),
      moreNote: (n) => tn(n, '{n} more not shown', '{n} more not shown'),
    };
  }

  let menu = null; // the open menu: { el, anchor, off }

  function closeMenu(refocus = false) {
    if (!menu) return;
    const { el, anchor, off } = menu;
    menu = null;
    off();
    el.remove();
    anchor.setAttribute('aria-expanded', 'false');
    if (refocus && anchor.isConnected) anchor.focus();
  }

  // menuFrame opens a menu under anchor, as Magpie's select menu, or closes
  // it when it is open already. keys takes the keys it does not use itself.
  function menuFrame(anchor, label, keys) {
    const again = menu?.anchor === anchor;
    closeMenu();
    if (again) return null; // a second click closes it
    // The menu takes focus itself while it has no item to focus: loading,
    // failed or empty, so that its keys still work.
    const el = h('div', { class: 'files-menu', role: 'menu', tabindex: '-1', 'aria-label': label });
    const away = (e) => { if (!el.contains(e.target) && !anchor.contains(e.target)) closeMenu(); };
    const resize = () => closeMenu();
    document.addEventListener('pointerdown', away, true);
    window.addEventListener('resize', resize);
    menu = { el, anchor, off: () => { document.removeEventListener('pointerdown', away, true); window.removeEventListener('resize', resize); } };
    anchor.setAttribute('aria-expanded', 'true');
    document.body.append(el);
    const place = () => {
      const r = anchor.getBoundingClientRect();
      el.style.left = `${Math.round(Math.max(8, Math.min(r.left - 4, window.innerWidth - el.offsetWidth - 8)))}px`;
      el.style.top = `${Math.round(r.bottom + 4)}px`;
    };
    el.addEventListener('keydown', (e) => {
      const items = [...el.querySelectorAll('.files-menu-item')];
      const i = items.indexOf(document.activeElement);
      const go = (n) => { e.preventDefault(); items[(n + items.length) % items.length]?.focus(); };
      if (e.key === 'ArrowDown') go(i + 1);
      else if (e.key === 'ArrowUp') go(i - 1);
      else if (e.key === 'Home') go(0);
      else if (e.key === 'End') go(items.length - 1);
      else if (e.key === 'Escape') { e.preventDefault(); closeMenu(true); }
      else if (e.key === 'Tab') closeMenu();
      else keys?.(e);
    });
    return {
      el,
      isOpen: () => menu?.el === el,
      note(text, danger = false) {
        el.replaceChildren(h('p', { class: danger ? 'files-menu-note danger' : 'files-menu-note' }, text));
        place();
        el.focus({ preventScroll: true });
      },
      // fill shows items, focusing the one marked current, else the first.
      fill(items) {
        el.replaceChildren(...items);
        place();
        const focus = el.querySelector('[aria-current="true"]') ?? el.querySelector('.files-menu-item') ?? el;
        focus.focus({ preventScroll: true });
        if (focus !== el) focus.scrollIntoView({ block: 'nearest' });
      },
    };
  }

  // folderMenu lists a folder under anchor, from source. Folders open in
  // it, a level down; files open. current is marked, or the folder on the
  // way to it.
  async function folderMenu(anchor, source, dir, current) {
    let at = dir;
    let showing = 0; // the newest listing: an older one that answers late is dropped
    const nameOf = (folder) => (folder === source.root ? source.rootName : folder);
    const frame = menuFrame(anchor, nameOf(dir), (e) => {
      if (e.key === 'ArrowRight' && document.activeElement?.querySelector('.files-menu-chevron')) { e.preventDefault(); document.activeElement.click(); }
      else if (e.key === 'ArrowLeft' && at !== dir && at !== source.root) { e.preventDefault(); show(source.parentOf(at), at); }
    });
    if (!frame) return;
    const show = async (folder, mark) => {
      const seq = ++showing;
      at = folder;
      frame.el.setAttribute('aria-label', nameOf(folder));
      let listed = source.cached(folder);
      if (!listed) {
        frame.note(t('Loading…'));
        try {
          listed = await source.list(folder);
        } catch (err) {
          if (frame.isOpen() && seq === showing) frame.note(err?.message || String(err), true);
          return;
        }
        if (!frame.isOpen() || seq !== showing) return;
      }
      const { entries, more } = listed;
      const items = [];
      if (folder !== source.root && folder !== dir) {
        const up = source.parentOf(folder);
        items.push(h('button', { class: 'files-menu-item files-menu-up', type: 'button', role: 'menuitem', onclick: () => show(up, folder) },
          icon('chevron', 'mds-icon files-menu-back'), h('span', null, up === source.root ? source.rootName : baseOf(up))));
      }
      // The folder it came up from, or the file open or the folder it is
      // in, is marked.
      const marked = mark ?? source.current();
      const onPath = (e) => Boolean(marked) && (e.path === marked || (e.dir && marked.startsWith(`${e.path}/`)));
      for (const e of entries.slice(0, MENU_ROWS)) {
        const aside = source.aside(e);
        items.push(h('button', {
          class: aside ? 'files-menu-item files-menu-item--aside' : 'files-menu-item', type: 'button', role: 'menuitem', 'data-path': e.path,
          'aria-current': onPath(e) ? 'true' : null, title: e.path,
          onclick: () => (e.dir ? show(e.path, null) : source.open(e)),
        }, source.icon(e), h('span', null, e.name), aside));
      }
      if (!entries.length) items.push(h('p', { class: 'files-menu-note' }, source.empty()));
      const hidden = Math.max(0, entries.length - MENU_ROWS) + (more ?? 0);
      if (hidden) items.push(h('p', { class: 'files-menu-note' }, source.moreNote(hidden)));
      frame.fill(items);
    };
    await show(dir, current);
  }

  // scopeMenu shows the parts of the workspace the pane can show: all of
  // it, the knowledge, and each worktree, with what it changed or its pull
  // request as the info panel last read them. Choosing the part shown goes
  // to its top: a worktree's summary of changes.
  function scopeMenu(anchor, p) {
    const y = host.yesman();
    const frame = y && menuFrame(anchor, t('Show'));
    if (!frame) return;
    const here = scopeKey(p.scope);
    const item = (scope, glyph, label, aside, title) => {
      const mine = scopeKey(scope) === here;
      return h('button', {
        class: 'files-menu-item files-menu-item--aside', type: 'button', role: 'menuitem', 'aria-current': mine ? 'true' : null, title,
        onclick: () => {
          closeMenu();
          if (mine) {
            if (isChanges(p)) changes.clear(p);
            return;
          }
          // From one worktree to another, the same view: files or changes.
          setScope(scope, p.scope.kind === 'worktree' && scope.kind === 'worktree' ? p.show : undefined);
        },
      }, icon(glyph), h('span', null, label), aside ?? h('span'));
    };
    const items = [
      item(WORKSPACE, 'folder', y.name, h('span', { class: 'files-menu-aside' }, t('All files')), y.path),
      item({ kind: 'knowledge' }, 'book', t('Knowledge'), null, KNOWLEDGE),
    ];
    const wts = host.worktrees?.(y) ?? [];
    if (wts.length) items.push(h('p', { class: 'files-menu-heading', role: 'presentation' }, t('Worktrees')));
    for (const w of wts) {
      const pull = w.pull;
      const aside = pull ? h('span', { class: 'files-menu-aside' }, `#${pull.number}`) : w.changes?.files ? counts(w.changes) : null;
      items.push(item({ kind: 'worktree', dir: w.dir }, pull ? (pull.state === 'merged' ? 'merge' : 'pullRequest') : 'branch', w.dir, aside,
        [w.dir, pull ? pull.title : w.branch].filter(Boolean).join('\n')));
    }
    frame.fill(items);
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
          if (prefs.autoSave) {
            for (const t of p.tabs) if (t.dirty) save(p, t.path);
          } else {
            for (const timer of timers.values()) clearTimeout(timer);
            timers.clear();
          }
          break;
        case 'knowledge': openKnowledge(); break;
        case 'checkout': if (y) host.checkout(y); break;
        case 'reveal': host.call('Reveal', p.root === '.' ? p.path : absOf(p, p.root)).catch(host.fail); break;
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
    const input = h('input', { class: 'mds-input', id: 'files-find', type: 'search', placeholder: t('Go to a file'), 'aria-label': t('Go to a file'), spellcheck: 'false', autocomplete: 'off', value: p.query,
      oninput: (e) => find(p, e.currentTarget.value),
      onkeydown: (e) => {
        if (e.key === 'Enter' && p.results?.length) open(p.results[0], true);
        else if (e.key === 'Escape' && p.query) { e.preventDefault(); find(p, ''); e.currentTarget.value = ''; }
        else if (e.key === 'ArrowDown') { e.preventDefault(); $('#files-tree-list [data-path]')?.focus(); }
      } });
    return h('nav', { class: 'files-tree', 'aria-label': t('Files') },
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
      const results = await host.call('FindFiles', p.name, query, p.root);
      if (seq !== findSeq) return;
      p.results = results;
    } catch (err) {
      if (seq !== findSeq) return; // a newer query answers
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
    $('#files .files-bar-actions [data-action="collapse"]')?.toggleAttribute('disabled', !p.expanded.size);
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
      if (!rows.length) rows.push(h('p', { class: 'tree-note' }, t('No file matches “{query}”.', { query: p.query.trim() })));
    } else {
      const walk = (dir, depth) => {
        const entries = p.dirs.get(dir);
        if (entries === 'loading' || entries === undefined) {
          if (depth === 0) rows.push(h('p', { class: 'tree-note' }, t('Loading…')));
          return;
        }
        if (entries.error) {
          // Knowledge that has no folder yet is only empty.
          if (depth === 0 && locked(p) && /no such file|not exist|cannot find/i.test(entries.error)) rows.push(h('p', { class: 'tree-note' }, emptyNote(p)));
          else rows.push(h('p', { class: 'tree-note danger', style: `--depth: ${depth}` }, entries.error));
          return;
        }
        if (!entries.length && depth === 0) rows.push(h('p', { class: 'tree-note' }, emptyNote(p)));
        for (const e of entries.slice(0, MAX_TREE_ROWS)) {
          rows.push(row(p, e, depth));
          if (e.dir && p.expanded.has(e.path)) walk(e.path, depth + 1);
        }
        if (entries.length > MAX_TREE_ROWS) {
          rows.push(h('p', { class: 'tree-note', style: `--depth: ${depth}` }, t('{n} more. Go to a file to find them.', { n: formatNumber(entries.length - MAX_TREE_ROWS) })));
        }
      };
      walk(p.root, 0);
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
    if (!section || !p?.active || isChanges(p)) return;
    const rel = p.active;
    const v = p.views.get(rel);
    section.dataset.fresh = section.dataset.path === rel ? '' : 'true';
    section.dataset.path = rel;
    const top = section.dataset.fresh ? 0 : section.scrollTop;
    if (!v || v === 'loading') {
      section.replaceChildren(h('p', { class: 'files-note' }, t('Loading…')));
      return;
    }
    if (v.error) {
      section.replaceChildren(h('div', { class: 'files-note' },
        h('strong', null, /no such file|not exist/i.test(v.error) ? t('This file is gone') : t('This file can’t be shown')),
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
        h('strong', null, t('This file isn’t text')),
        h('span', null, t('{size}. Open it with another app to see it.', { size })),
        h('div', null, h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => host.call('Reveal', absOf(p, rel)).catch(host.fail) }, icon('folder'), t('Reveal')))));
      return;
    }
    const tab = tabOf(p, rel);
    const k = keyOf(p, rel);
    const notice = v.truncated ? h('p', { class: 'files-notice' }, t('Showing the first {shown} of this {size} file. Large files open read-only.', { shown: formatSize(new Blob([v.text]).size), size })) : null;
    const banner = h('div', { id: 'files-banner' });
    const lang = languageOf(rel);
    const rendered = lang === 'markdown' && (locked(p) || (p.mode.get(rel) ?? 'preview') === 'preview');
    section.classList.toggle('files-view--editor', !rendered);
    if (rendered) {
      const article = h('article', { class: 'md files-md', html: renderDocument(editor?.has(k) ? editor.text(k) : v.text) });
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
    const slot = h('div', { class: 'files-editor', id: 'files-editor' }, h('p', { class: 'files-note' }, t('Loading…')));
    section.replaceChildren(...[banner, notice, slot].filter(Boolean));
    renderBanner();
    ensureEditor().then((ed) => {
      if (!slot.isConnected || current() !== p || p.active !== rel) return;
      slot.replaceChildren(ed.dom);
      ed.show(k, { text: v.text, lang, readOnly: Boolean(tab?.readOnly || v.truncated || locked(p)) });
    }, (err) => slot.replaceChildren(h('p', { class: 'files-note' }, t('The editor did not load: {error}', { error: err?.message || err }))));
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
      h('span', null, gone ? t('This file was deleted on disk. Your changes are only here.') : t('This file changed on disk since you opened it, and you have unsaved changes.')),
      h('div', { class: 'files-conflict-actions' },
        gone
          ? h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => closeTab(p, p.active) }, t('Close'))
          : h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => reload(p, p.active) }, t('Use the File on Disk')),
        h('button', { class: 'mds-button mds-button--small mds-button--primary', type: 'button', onclick: () => save(p, p.active, true) }, gone ? t('Save Again') : t('Keep Mine'))));
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
    const p = pane(host.yesman());
    p.show = 'files';
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

  function emptyNote(p) {
    if (p.scope.kind === 'knowledge') return t('No notes yet. The agents write down here what they learn.');
    if (p.scope.kind === 'worktree') return t('This worktree is empty.');
    return t('The workspace is empty. Agents check repositories out into it.');
  }

  return { render, open, openPath, openKnowledge, openWorktree, shown, onChange, onKey };
}
