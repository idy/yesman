// A worktree's changes in the files pane: a tree of the changed files,
// opened a folder at a time, and the diff of the file chosen. The tree can
// show folders alone instead: a folder chosen then lists its changed files,
// to see what changed without reading every diff. The Go side (info.go)
// measures the changes once and lists them a folder at a time, opening as
// many folders ahead as a small listing allows, so a large diff never
// arrives whole: without files when the tree shows folders alone, and a
// long folder a page at a time. A diff is read up to 256 KB unless asked
// for all of it, and drawn in blocks only as they scroll into view.

import { $, h, icon } from './dom.js';
import { escapeHTML } from './markdown.js';
import { t, tn, tNodes, formatNumber } from './i18n.js';

// Lines a block of the diff draws at once; blocks out of view stay empty.
const BLOCK = 200;

// Whether the tree shows folders alone, kept across launches.
const prefs = { folders: localStorage.getItem('yesman.changes.tree') === 'folders' };

const parentOf = (rel) => (rel.includes('/') ? rel.slice(0, rel.lastIndexOf('/')) : '');
// A file or folder that no longer has changes, after they were measured again.
const gone = (err) => /has no changes/.test(err?.message || String(err));

const STATUS = {
  added: ['A', 'Added'],
  untracked: ['U', 'New, not tracked by git'],
  modified: ['M', 'Modified'],
  deleted: ['D', 'Deleted'],
  renamed: ['R', 'Renamed'],
  copied: ['C', 'Copied'],
  typechange: ['T', 'Type changed'],
};

export const statusMark = (status) => {
  const [letter, label] = STATUS[status] ?? ['M', 'Modified'];
  return h('span', { class: 'change-mark', 'data-change': status, title: t(label), 'aria-label': t(label) }, letter);
};

// counts shows "+3 −1"; a binary file shows that it is one.
export function counts(n, extra = {}) {
  if (n.binary) return h('span', { class: 'change-stat', ...extra }, t('binary'));
  if (n.large) return h('span', { class: 'change-stat', ...extra }, t('large'));
  return h('span', { class: 'change-stat', ...extra },
    n.additions ? h('span', { class: 'change-add' }, `+${formatNumber(n.additions)}`) : null,
    n.deletions ? h('span', { class: 'change-del' }, `−${formatNumber(n.deletions)}`) : null);
}

// parsePatch reads a unified diff's hunks into lines: h for a hunk's
// header, + and - for added and removed lines, ' ' for context and x for
// "No newline at end of file".
export function parsePatch(patch) {
  const lines = [];
  let o = 0;
  let n = 0;
  const src = patch.split('\n');
  if (src.at(-1) === '') src.pop();
  for (const l of src) {
    if (l.startsWith('@@')) {
      const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(l);
      if (m) { o = Number(m[1]); n = Number(m[2]); }
      lines.push({ k: 'h', t: l });
    } else if (l[0] === '+') lines.push({ k: '+', n: n++, t: l.slice(1) });
    else if (l[0] === '-') lines.push({ k: '-', o: o++, t: l.slice(1) });
    else if (l[0] === '\\') lines.push({ k: 'x', t: l.slice(2) });
    else lines.push({ k: ' ', o: o++, n: n++, t: l.slice(1) });
  }
  return lines;
}

const KIND = { h: 'diff-hunk', '+': 'diff-add', '-': 'diff-del', ' ': 'diff-ctx', x: 'diff-meta' };

function linesHTML(lines, from, to) {
  let html = '';
  for (let i = from; i < to; i++) {
    const l = lines[i];
    const sign = l.k === 'h' || l.k === 'x' ? '' : l.k;
    html += `<div class="diff-line ${KIND[l.k]}"><span class="diff-num">${l.o ?? ''}</span><span class="diff-num">${l.n ?? ''}</span><span class="diff-text"><span class="diff-sign">${sign}</span>${escapeHTML(l.t)}</span></div>`;
  }
  return html;
}

// createChanges keeps the changes of each worktree the pane shows. host:
//   call(method, ...args), fail(err)
//   render()          draws the pane again
//   openFile(rel)     opens a file of the workspace in the worktree's files
export function createChanges(host) {
  const states = new Map(); // yesman + worktree dir -> its changes

  function stateOf(p) {
    const key = `${p.name}\u0001${p.scope.dir}`;
    let s = states.get(key);
    if (!s) {
      s = {
        key, yesman: p.name, dir: p.scope.dir, view: null, error: null, loading: false,
        // Listings by folder: with their files, and of folders alone.
        folders: new Map(), dirs: new Map(), expanded: new Set(),
        // The file whose diff shows; else, while the tree shows folders
        // alone, the folder whose files show ('' for the top); else the
        // summary.
        selected: null, folder: null,
        diffs: new Map(), seq: 0,
      };
      states.set(key, s);
    }
    return s;
  }

  const treeMap = (s) => (prefs.folders ? s.dirs : s.folders);
  const shownFolder = (s) => (s.selected ? parentOf(s.selected) : s.folder);

  // ---------- Loading ----------

  // keep files a listing in map, with the folders it opened ahead; those
  // are added to expand when given, as for the first listing of a diff.
  function keep(map, path, nodes, more, expand) {
    map.set(path, { nodes, more });
    for (const n of nodes) {
      if (n.dir && n.children) {
        keep(map, n.path, n.children, n.more ?? 0, expand);
        expand?.add(n.path);
      }
    }
  }

  // fetchFolder lists a folder: for the tree, with the folders below it
  // that fit, or alone (flat), as its menu and its list of files want it.
  async function fetchFolder(s, path, { folders = false, flat = false } = {}) {
    const seq = s.seq;
    const map = folders ? s.dirs : s.folders;
    try {
      const v = await host.call('Changes', s.yesman, s.dir, { folder: path, folders, flat });
      if (seq === s.seq) keep(map, path, v.nodes, v.more ?? 0, null);
      return { nodes: v.nodes, more: v.more ?? 0 };
    } catch (err) {
      if (seq === s.seq) map.set(path, { error: err?.message || String(err) });
      throw err;
    }
  }

  const treeLoad = (s, path) => fetchFolder(s, path, { folders: prefs.folders }).catch(() => {});

  async function load(s, refresh) {
    const seq = ++s.seq;
    s.loading = true;
    s.stale = false;
    if (!s.view) host.render();
    try {
      const folders = prefs.folders;
      const v = await host.call('Changes', s.yesman, s.dir, { folder: '', refresh, folders });
      if (seq !== s.seq) return;
      const first = !s.view;
      s.view = v;
      s.error = null;
      const open = [...s.expanded].sort((a, b) => a.length - b.length);
      s.folders.clear();
      s.dirs.clear();
      const map = treeMap(s);
      keep(map, '', v.nodes, v.more ?? 0, first ? s.expanded : null);
      if (!first) {
        // Folders open before are listed again, if they still have changes.
        s.expanded = new Set();
        for (const path of open) {
          if (!map.has(path)) await treeLoad(s, path);
          if (seq !== s.seq) return;
          if (map.get(path)?.nodes) s.expanded.add(path);
        }
      }
      // What shows is read again, or let go once it has no changes: also
      // after a view that was let go, whose diffs belong to the old one.
      s.diffs.clear();
      if (s.selected) loadDiff(s, s.selected, false);
      else if (s.folder !== null) loadListing(s, s.folder);
    } catch (err) {
      if (seq !== s.seq) return;
      s.error = err?.message || String(err);
    } finally {
      if (seq === s.seq) s.loading = false;
    }
    host.render();
  }

  // restore lists what the tree shows when it changes from files to
  // folders alone or back: the top, and the folders open.
  async function restore(s) {
    const seq = s.seq;
    const map = treeMap(s);
    if (!map.get('')?.nodes) {
      map.set('', 'loading');
      renderTree(s);
      await treeLoad(s, '');
    }
    for (const path of [...s.expanded].sort((a, b) => a.length - b.length)) {
      if (seq !== s.seq || map !== treeMap(s)) return;
      if (!map.has(path)) await treeLoad(s, path);
    }
    renderTree(s);
  }

  async function toggle(s, path) {
    if (s.expanded.has(path)) {
      s.expanded.delete(path);
    } else {
      s.expanded.add(path);
      const map = treeMap(s);
      if (!map.has(path)) {
        map.set(path, 'loading');
        renderTree(s);
        await treeLoad(s, path);
      }
    }
    renderTree(s);
    $(`#changes-tree-list [data-path="${CSS.escape(path)}"]`)?.focus({ preventScroll: true });
  }

  async function loadDiff(s, path, all) {
    s.diffs.set(path, 'loading');
    if (s.selected === path) renderView(s);
    try {
      s.diffs.set(path, await host.call('Diff', s.yesman, s.dir, path, all));
    } catch (err) {
      if (gone(err) && s.selected === path) {
        s.diffs.delete(path);
        s.selected = null;
        renderTree(s);
        renderView(s);
        renderBar();
        return;
      }
      s.diffs.set(path, { error: err?.message || String(err) });
    }
    if (s.selected === path) renderView(s);
  }

  // loadListing lists a folder's own changed files and folders, for the
  // list that shows when the folder is chosen.
  function loadListing(s, path) {
    s.folders.set(path, 'loading');
    if (s.folder === path) renderView(s);
    fetchFolder(s, path, { flat: true }).then(() => {}, (err) => {
      if (gone(err) && s.folder === path) {
        s.folder = null;
        renderBar();
      }
    }).finally(() => {
      if (s.folder === path && !s.selected) renderView(s);
    });
  }

  // loadMore lists the next page of a long folder: of the tree as it shows
  // now (folders alone or with their files), or of a folder's list.
  async function loadMore(s, path, folders = false) {
    const f = (folders ? s.dirs : s.folders).get(path);
    if (!f?.nodes || f.busy) return;
    const seq = s.seq;
    f.busy = true;
    renderView(s);
    renderTree(s);
    try {
      const v = await host.call('Changes', s.yesman, s.dir, { folder: path, folders, flat: true, offset: f.nodes.length });
      if (seq === s.seq) {
        f.nodes = f.nodes.concat(v.nodes);
        f.more = v.more ?? 0;
      }
    } catch (err) {
      host.fail(err);
    } finally {
      f.busy = false;
    }
    renderView(s);
    renderTree(s);
  }

  function select(s, path) {
    s.selected = path;
    s.folder = parentOf(path);
    if (!s.diffs.has(path)) loadDiff(s, path, false);
    renderTree(s);
    renderView(s);
    renderBar();
  }

  // pickFolder shows a folder's changed files, while the tree shows
  // folders alone, and opens it. The folder already shown opens or closes.
  function pickFolder(s, path, hasDirs) {
    if (shownFolder(s) === path && !s.selected) {
      if (hasDirs) toggle(s, path);
      return;
    }
    s.folder = path;
    s.selected = null;
    if (path && hasDirs && !s.expanded.has(path)) toggle(s, path);
    if (!s.folders.get(path)?.nodes) loadListing(s, path);
    renderTree(s);
    renderView(s);
    renderBar();
  }

  function setFolders(s, on) {
    prefs.folders = on;
    localStorage.setItem('yesman.changes.tree', on ? 'folders' : 'files');
    if (!on && !s.selected) s.folder = null;
    host.render();
    restore(s);
  }

  function renderBar() {
    host.renderBar();
  }

  // reveal opens the folders of the tree down to a file or folder chosen
  // elsewhere, listing the ones not listed yet, one at a time.
  async function reveal(s, target, file) {
    const map = treeMap(s);
    let folder = '';
    for (let depth = 0; depth < 64; depth++) {
      let f = map.get(folder);
      if (!f?.nodes) {
        await treeLoad(s, folder);
        f = map.get(folder);
      }
      const n = f?.nodes?.find((x) => x.dir && (target === x.path || target.startsWith(`${x.path}/`)));
      // A folder shows its row; with folders alone, so does a file's.
      if (!n || n.path === target || (file && prefs.folders && n.path === parentOf(target))) break;
      s.expanded.add(n.path);
      folder = n.path;
    }
    renderTree(s);
    $('#changes-tree-list [aria-selected="true"]')?.scrollIntoView({ block: 'nearest' });
  }

  // cached returns a folder of the changes already listed, list lists it
  // alone: its changed folders and files, as the path's menus show them.
  function cached(p, folder) {
    const f = stateOf(p).folders.get(folder);
    return f?.nodes ? { entries: f.nodes, more: f.more ?? 0 } : null;
  }

  async function list(p, folder) {
    const v = await fetchFolder(stateOf(p), folder, { flat: true });
    return { entries: v.nodes, more: v.more };
  }

  // nodeAt finds a folder in the listings of the folders above it.
  function nodeAt(s, path) {
    for (const map of [s.dirs, s.folders]) {
      for (const f of map.values()) {
        const n = f?.nodes?.find((x) => x.path === path);
        if (n) return n;
      }
    }
    return null;
  }

  // ---------- Following changes ----------

  const timers = new Map();
  // onFiles takes the files event: changes under a worktree shown measure
  // it again once they settle.
  function onFiles(yesman, paths, shown) {
    for (const s of states.values()) {
      if (s.yesman !== yesman || !s.view) continue;
      const prefix = `${s.dir}/`;
      if (!paths.some((rel) => rel === s.dir || rel.startsWith(prefix))) continue;
      clearTimeout(timers.get(s.key));
      timers.set(s.key, setTimeout(() => {
        timers.delete(s.key);
        if (shown(s.yesman, s.dir)) load(s, true);
        else s.stale = true; // measured again when next shown
      }, 1200));
    }
  }

  // ---------- Rendering ----------

  // ensure starts measuring a worktree's changes the first time they show,
  // and lists what the tree shows if it changed mode meanwhile.
  function ensure(p) {
    const s = stateOf(p);
    if (!s.view && !s.loading && !s.error) load(s, true);
    else if (s.stale && !s.loading) load(s, true);
    else if (s.view && !treeMap(s).has('')) restore(s);
    return s;
  }

  // invalidate says a yesman's worktrees may have changed while nothing
  // watched them: each is measured again when it next shows.
  function invalidate(yesman) {
    for (const s of states.values()) if (s.yesman === yesman && s.view) s.stale = true;
  }

  function tree(p) {
    const s = stateOf(p);
    return h('nav', { class: 'files-tree changes-tree', 'aria-label': t('Changed files') },
      h('div', { class: 'files-tree-list', id: 'changes-tree-list', role: 'tree', 'aria-label': t('Changed files'), 'data-changes': s.key, onkeydown: (e) => treeKey(e, s) }));
  }

  function row(s, n, depth) {
    const open = n.dir && s.expanded.has(n.path);
    const title = n.dir
      ? `${n.path}\n${tn(n.files, '{n} file changed', '{n} files changed')}`
      : `${n.oldPath ? `${n.oldPath} → ` : ''}${n.path}${n.uncommitted ? `\n${t('Not committed yet')}` : ''}`;
    return h('button', {
      class: 'tree-row change-row', type: 'button', role: 'treeitem', 'data-path': n.path, 'data-dir': n.dir ? 'true' : null,
      'aria-level': String(depth + 1), 'aria-expanded': n.dir ? String(open) : null, 'aria-selected': String(!n.dir && n.path === s.selected),
      style: `--depth: ${depth}`, title,
      onclick: () => (n.dir ? toggle(s, n.path) : select(s, n.path)),
    },
    n.dir ? icon('chevron', 'mds-icon tree-chevron') : statusMark(n.status),
    h('span', { class: 'tree-name' }, n.name, !n.dir && n.uncommitted ? icon('record', 'mds-icon change-dirty') : null),
    counts(n));
  }

  // dirRow is a folder of the tree of folders alone: choosing it lists its
  // files; its arrow, when it has folders, opens it.
  function dirRow(s, n, depth) {
    const open = s.expanded.has(n.path);
    const twist = n.dirs
      ? h('span', { class: 'tree-twist', onclick: (e) => { e.stopPropagation(); toggle(s, n.path); } }, icon('chevron', 'mds-icon tree-chevron'))
      : icon('folder', 'mds-icon tree-icon');
    return h('button', {
      class: 'tree-row change-row', type: 'button', role: 'treeitem', 'data-path': n.path, 'data-dir': 'true', 'data-leaf': n.dirs ? null : 'true',
      'aria-level': String(depth + 1), 'aria-expanded': n.dirs ? String(open) : null, 'aria-selected': String(shownFolder(s) === n.path),
      style: `--depth: ${depth}`, title: `${n.path}\n${tn(n.files, '{n} file changed', '{n} files changed')}`,
      onclick: () => pickFolder(s, n.path, n.dirs > 0),
    }, twist, h('span', { class: 'tree-name' }, n.name), counts(n));
  }

  function renderTree(s) {
    const list = $('#changes-tree-list');
    if (!list || list.dataset.changes !== s.key) return;
    const top = list.scrollTop;
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.path : null;
    const rows = [];
    const walk = (map, path, depth, make) => {
      const f = map.get(path);
      if (f === 'loading' || f === undefined) {
        rows.push(h('p', { class: 'tree-note', style: `--depth: ${depth}` }, t('Loading…')));
        return;
      }
      if (f.error) {
        rows.push(h('p', { class: 'tree-note danger', style: `--depth: ${depth}` }, f.error));
        return;
      }
      for (const n of f.nodes) {
        if (!n.dir && make === dirRow) continue;
        rows.push(make(s, n, depth));
        if (n.dir && s.expanded.has(n.path)) walk(map, n.path, depth + 1, make);
      }
      if (f.more) {
        rows.push(h('p', { class: 'tree-note', style: `--depth: ${depth}` },
          h('button', { class: 'mds-link-button tree-more', type: 'button', disabled: f.busy, onclick: () => loadMore(s, path, map === s.dirs) },
            f.busy ? t('Loading…') : t('Show {n} more', { n: formatNumber(Math.min(f.more, 1000)) })),
          ' ', tn(f.more, '{n} not shown yet', '{n} not shown yet')));
      }
    };
    if (s.error && !s.view) {
      rows.push(h('p', { class: 'tree-note danger' }, s.error));
    } else if (!s.view) {
      rows.push(h('p', { class: 'tree-note' }, t('Comparing…')));
    } else if (!s.view.stats.files) {
      rows.push(h('p', { class: 'tree-note' }, t('No changes.')));
    } else if (prefs.folders) {
      // The worktree's own row lists the files at its top.
      rows.push(h('button', {
        class: 'tree-row change-row', type: 'button', role: 'treeitem', 'data-path': '', 'data-dir': 'true', 'data-leaf': 'true',
        'aria-level': '1', 'aria-selected': String(shownFolder(s) === ''), style: '--depth: 0', title: s.dir,
        onclick: () => pickFolder(s, '', false),
      }, icon('branch', 'mds-icon tree-icon'), h('span', { class: 'tree-name' }, s.dir), counts(s.view.stats)));
      walk(s.dirs, '', 1, dirRow);
    } else {
      walk(s.folders, '', 0, row);
    }
    list.replaceChildren(...rows);
    list.scrollTop = top;
    const els = [...list.querySelectorAll('[data-path]')];
    const stop = els.find((r) => r.getAttribute('aria-selected') === 'true') ?? els[0];
    for (const r of els) r.tabIndex = r === stop ? 0 : -1;
    if (focused != null) $(`#changes-tree-list [data-path="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
  }

  function treeKey(e, s) {
    const el = e.target.closest('[data-path]');
    if (!el) return;
    const rows = [...$('#changes-tree-list').querySelectorAll('[data-path]')];
    const at = rows.indexOf(el);
    const path = el.dataset.path;
    const dir = el.dataset.dir === 'true' && !el.dataset.leaf;
    const go = (to) => { if (to) { e.preventDefault(); to.focus(); } };
    switch (e.key) {
      case 'ArrowDown': go(rows[at + 1]); break;
      case 'ArrowUp': go(rows[at - 1]); break;
      case 'Home': go(rows[0]); break;
      case 'End': go(rows.at(-1)); break;
      case 'ArrowRight':
        if (dir && !s.expanded.has(path)) { e.preventDefault(); toggle(s, path); } else if (dir) go(rows[at + 1]);
        break;
      case 'ArrowLeft': {
        if (dir && s.expanded.has(path)) { e.preventDefault(); toggle(s, path); break; }
        // The folder a row is in is the nearest row above it at a lesser depth.
        const level = Number(el.getAttribute('aria-level'));
        for (let i = at - 1; i >= 0; i--) if (Number(rows[i].getAttribute('aria-level')) < level) { go(rows[i]); break; }
        break;
      }
    }
  }

  function actions(p) {
    const s = stateOf(p);
    const btn = (name, label, onclick, extra = {}) => h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick, ...extra }, icon(name));
    return [
      btn('folder', prefs.folders ? t('Show files in the tree') : t('Show only folders in the tree, and a folder’s files when it is chosen'),
        () => setFolders(s, !prefs.folders), { 'aria-pressed': String(prefs.folders) }),
      btn('collapse', t('Collapse all folders'), () => { s.expanded.clear(); renderTree(s); }),
      btn('reload', t('Compare again'), () => load(s, true), { disabled: s.loading, 'aria-busy': s.loading ? 'true' : null }),
    ];
  }

  // view fills the pane's preview: the chosen file's diff, a folder's
  // changed files, or a summary.
  function view(p, section) {
    const s = stateOf(p);
    section.dataset.changes = s.key;
    renderView(s, section);
    renderTree(s);
  }

  let observer = null;

  function renderView(s, section = $('#files-view')) {
    if (!section || section.dataset.changes !== s.key) return;
    observer?.disconnect();
    observer = null;
    section.classList.remove('files-view--editor');
    const listing = !s.selected && prefs.folders && s.folder !== null;
    const shown = section.dataset.diff;
    section.dataset.diff = s.selected ?? (listing ? `folder:${s.folder}` : '');
    const top = section.scrollTop;
    if (!s.selected) {
      section.replaceChildren(listing ? listingEl(s, s.folder) : summary(s));
      // A folder's list keeps its place as more of it arrives.
      section.scrollTop = shown === section.dataset.diff ? top : 0;
      return;
    }
    if (shown !== section.dataset.diff) section.scrollTop = 0;
    const d = s.diffs.get(s.selected);
    if (!d || d === 'loading') {
      section.replaceChildren(h('p', { class: 'files-note' }, t('Loading…')));
      return;
    }
    if (d.error) {
      section.replaceChildren(h('div', { class: 'files-note' }, h('strong', null, t('This diff can’t be shown')), h('span', null, d.error)));
      return;
    }
    section.replaceChildren(diffEl(s, d, section));
  }

  // listingEl lists a folder's changed files, and the folders in it with
  // changes, for review without the diffs.
  function listingEl(s, path) {
    const f = s.folders.get(path);
    const stats = path ? nodeAt(s, path) : s.view?.stats;
    const head = h('div', { class: 'diff-head' },
      icon(path ? 'folder' : 'branch', 'mds-icon diff-head-icon'),
      h('span', { class: 'diff-what', title: path ? `${s.dir}/${path}` : s.dir }, stats ? tn(stats.files, '{n} file changed', '{n} files changed') : ''),
      stats ? counts(stats) : null);
    if (!f || f === 'loading') return h('div', { class: 'diff' }, head, h('p', { class: 'files-note' }, t('Loading…')));
    if (f.error) return h('div', { class: 'diff' }, head, h('p', { class: 'files-note danger' }, f.error));
    const rows = f.nodes.map((n) => (n.dir
      ? h('button', { class: 'changes-list-row', type: 'button', 'data-path': n.path, title: n.path,
        onclick: () => { pickFolder(s, n.path, n.dirs > 0); reveal(s, n.path, false); } },
      icon('folder', 'mds-icon changes-list-icon'),
      h('span', { class: 'changes-list-name' }, n.name, h('span', { class: 'changes-list-note' }, tn(n.files, '{n} file', '{n} files'))),
      counts(n))
      : h('button', { class: 'changes-list-row', type: 'button', 'data-path': n.path,
        title: `${n.oldPath ? `${n.oldPath} → ` : ''}${n.path}${n.uncommitted ? `\n${t('Not committed yet')}` : ''}`,
        onclick: () => select(s, n.path) },
      statusMark(n.status),
      h('span', { class: 'changes-list-name' }, n.name,
        n.uncommitted ? icon('record', 'mds-icon change-dirty') : null,
        n.oldPath ? h('span', { class: 'changes-list-note' }, t('from {path}', { path: n.oldPath })) : null),
      counts(n))));
    if (!rows.length) rows.push(h('p', { class: 'files-note' }, t('No changes here.')));
    const list = h('div', { class: 'changes-list', role: 'list', onkeydown: (e) => {
      const els = [...e.currentTarget.querySelectorAll('.changes-list-row')];
      const i = els.indexOf(document.activeElement);
      const to = e.key === 'ArrowDown' ? els[i + 1] : e.key === 'ArrowUp' ? els[i - 1] : null;
      if (to) { e.preventDefault(); to.focus(); }
    } }, rows);
    const more = f.more
      ? h('div', { class: 'changes-list-more' },
        h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: f.busy, onclick: () => loadMore(s, path) },
          f.busy ? t('Loading…') : t('Show {n} more', { n: formatNumber(Math.min(f.more, 1000)) })),
        h('span', null, tn(f.more, '{n} not shown yet', '{n} not shown yet')))
      : null;
    return h('div', { class: 'diff' }, head, list, more);
  }

  function summary(s) {
    const v = s.view;
    if (!v) {
      return h('div', { class: 'files-note' }, s.error
        ? [h('strong', null, t('The changes can’t be shown')), h('span', null, s.error)]
        : h('span', null, t('Comparing…')));
    }
    const short = (sha) => sha?.slice(0, 7) ?? '';
    const rows = [
      [t('Compared with'), v.base === 'HEAD' ? t('The last commit') : v.base],
      v.base === 'HEAD' ? null : [t('Since'), h('span', { class: 'mds-mono' }, short(v.mergeBase))],
      v.base === 'HEAD' ? null : [t('Commits'), t('{ahead} ahead, {behind} behind', { ahead: formatNumber(v.ahead), behind: formatNumber(v.behind) })],
      [t('Files'), tn(v.stats.files, '{n} file changed', '{n} files changed')],
      [t('Lines'), counts(v.stats)],
      [t('Not committed'), tn(v.stats.uncommitted, '{n} file', '{n} files')],
    ].filter(Boolean);
    const what = v.base === 'HEAD' ? t('Edits since the last commit.') : t('The commits of this branch since it left {base}, and the edits not committed yet.', { base: v.base });
    const next = prefs.folders ? t('Choose a folder to list its changed files.') : t('Choose a file to see its diff.');
    return h('div', { class: 'changes-summary' },
      h('dl', { class: 'context-rows' }, rows.map(([k, val]) => h('div', { class: 'context-row' }, h('dt', null, k), h('dd', null, val)))),
      h('p', { class: 'changes-hint' }, v.stats.files
        ? `${what} ${next}`
        : t('Nothing changed since {base}.', { base: v.base === 'HEAD' ? t('the last commit') : v.base })));
  }

  function diffEl(s, d, section) {
    const notes = [];
    if (d.binary) notes.push(t('A binary file: there is no text to compare.'));
    else if (d.large) notes.push(t('This new file is over 4 MB, too large to show here.'));
    const lines = d.patch ? parsePatch(d.patch) : [];
    if (!lines.length && !notes.length) {
      notes.push(d.status === 'renamed' || d.status === 'copied' ? t('Moved without changes to its text.') : t('No changes to its text.'));
    }
    const rel = `${s.dir}/${d.path}`;
    const head = h('div', { class: 'diff-head' },
      statusMark(d.status),
      // The bar above shows the path; this says what happened to it.
      h('span', { class: 'diff-what', title: rel }, d.oldPath
        ? tNodes('{status} from {path}', { status: t(STATUS[d.status]?.[1] ?? 'Renamed'), path: h('code', null, d.oldPath) })
        : t(STATUS[d.status]?.[1] ?? 'Modified')),
      counts(d),
      d.status === 'deleted' ? null : h('button', { class: 'mds-button mds-button--small mds-button--subtle', type: 'button', onclick: () => host.openFile(rel) }, t('Open file')));
    const parts = [head];
    if (d.truncated) {
      parts.push(h('div', { class: 'files-notice diff-cut' },
        h('span', null, t('Showing the first {shown} of this {size} diff.', { shown: formatSize(d.patch.length), size: formatSize(d.size) })),
        d.size <= 16 * 1024 * 1024 || !d.size
          ? h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => loadDiff(s, d.path, true) }, t('Show the whole diff'))
          : null));
    }
    for (const note of notes) parts.push(h('p', { class: 'files-note' }, note));
    if (lines.length) {
      const digits = String(lines.reduce((m, l) => Math.max(m, l.o ?? 0, l.n ?? 0), 0)).length;
      const body = h('div', { class: 'diff-body', style: `--diff-digits: ${Math.max(digits, 2)}ch` });
      const blocks = [];
      for (let from = 0; from < lines.length; from += BLOCK) {
        const to = Math.min(from + BLOCK, lines.length);
        const block = h('div', { class: 'diff-block', style: `--diff-lines: ${to - from}`, 'data-from': String(from), 'data-to': String(to) });
        blocks.push(block);
      }
      body.append(...blocks);
      parts.push(body);
      // Fill the first blocks now and the rest as they near the view.
      for (const b of blocks.slice(0, 3)) fill(b, lines);
      if (blocks.length > 3) {
        observer = new IntersectionObserver((entries) => {
          for (const e of entries) {
            if (e.isIntersecting && !e.target.dataset.filled) {
              fill(e.target, lines);
              observer?.unobserve(e.target);
            }
          }
        }, { root: section, rootMargin: '1600px 0px' });
        for (const b of blocks.slice(3)) observer.observe(b);
      }
    }
    return h('div', { class: 'diff' }, parts);
  }

  function fill(block, lines) {
    block.dataset.filled = 'true';
    block.innerHTML = linesHTML(lines, Number(block.dataset.from), Number(block.dataset.to));
  }

  return {
    ensure, invalidate, tree, view, actions, onFiles, cached, list,
    // What shows: the file whose diff shows, else the folder whose files
    // show, as path; '' or null for the top or the summary.
    shown: (p) => {
      const s = stateOf(p);
      return { path: s.selected ?? (prefs.folders ? s.folder : null), file: Boolean(s.selected) };
    },
    select: (p, path) => { const s = stateOf(p); select(s, path); reveal(s, path, true); },
    clear: (p) => { const s = stateOf(p); s.selected = null; s.folder = null; renderTree(s); renderView(s); renderBar(); },
    totals: (p) => stateOf(p).view?.stats ?? null,
  };
}

export function formatSize(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
