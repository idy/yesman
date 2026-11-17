// Task history paths and URL state. This module holds no document content.
export const TASK_LOG = '.yesman/task-log.md';
export const TASK_LOG_DIR = '.yesman/task-log';
const platformPath = (path) => typeof navigator !== 'undefined' && /Win/i.test(navigator.platform) ? path?.toLowerCase() : path;
export const isTaskLog = (path) => {
  const value = platformPath(path);
  if (value === TASK_LOG) return true;
  if (typeof value !== 'string' || !value.startsWith(`${TASK_LOG_DIR}/`) || !/\.md$/i.test(value)) return false;
  return value.slice(TASK_LOG_DIR.length + 1).split('/').every((part) => part && part !== '.' && part !== '..' && !/[\\\u0000]/.test(part));
};
export const legacyLog = (path) => /^\.yesman\/task-log\/legacy-[0-9a-f]{64}-(?:original|\d{4})\.md$/.test(platformPath(path));
export const logBase = (path) => legacyLog(path) ? '.yesman' : path.slice(0, path.lastIndexOf('/'));

export function documentLink(dir, href) {
  try {
    const [file, ...fragment] = href.split('#');
    const parts = dir === '.' ? [] : dir.split('/');
    const decoded = decodeURIComponent(file);
    if (/^[a-z][a-z0-9+.-]*:|^[\\/~]|[\u0000-\u001f]/i.test(decoded)) return null;
    for (const part of decoded.replace(/\\/g, '/').split('/')) {
      if (part === '..') { if (!parts.length) return null; parts.pop(); }
      else if (part && part !== '.') parts.push(part);
    }
    return { path: parts.join('/'), anchor: decodeURIComponent(fragment.join('#')) };
  } catch { return null; }
}

export function logRoute() {
  try {
    if (!location.hash.startsWith('#task-log=')) return null;
    const value = JSON.parse(decodeURIComponent(location.hash.slice(10)));
    if (!(typeof value.name === 'string' && isTaskLog(value.path) && typeof value.anchor === 'string')) return null;
    const saved = history.state?.workingLog;
    const scroll = saved?.name === value.name && saved.path === value.path && saved.anchor === value.anchor && Number.isFinite(saved.scroll) && saved.scroll >= 0 ? saved.scroll : undefined;
    return { ...value, scroll };
  } catch { return null; }
}

export function saveLogRoute(name, path, anchor = '', replace = false, scroll = 0) {
  const value = { name, path, anchor };
  const hash = `#task-log=${encodeURIComponent(JSON.stringify(value))}`;
  const state = { ...history.state, workingLog: { ...value, scroll } };
  history[replace || location.hash === hash ? 'replaceState' : 'pushState'](state, '', hash);
}

export function saveLogScroll(name, path, anchor, scroll) {
 const route=logRoute();
 if (route?.name===name&&route.path===path&&route.anchor===anchor) {
  history.replaceState({ ...history.state, workingLog: { name,path,anchor,scroll } },'');
 }
}

// IDs are derived from text, never interpreted as HTML. Duplicate headings
// follow GitHub's common -1/-2 convention. Legacy custom anchors remain in
// the complete unmodified original; unknown anchors show a visible fallback.
export function headingAnchors(article) {
  const used = new Set();
  for (const el of article.querySelectorAll('h1,h2,h3,h4,h5,h6')) {
    const base = el.textContent.trim().toLowerCase().replace(/[^\p{L}\p{N}_\s-]/gu, '').replace(/\s/g, '-');
    let id = base;
    for (let n = 1; used.has(id); n++) id = `${base}-${n}`;
    used.add(id); el.id = id; el.tabIndex = -1;
  }
}

// ---------- Records by day ----------

// A record's date is a real calendar day written in the log itself: the
// TOC line's leading YYYY-MM-DD, else the YYYY-MM-DD- prefix of its file
// name. File times are never used: copying or restoring a log changes them.
export function validDay(text) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(text ?? '');
  if (!m) return null;
  const [y, mo, d] = m.slice(1).map(Number);
  const at = new Date(Date.UTC(y, mo - 1, d));
  return y >= 1970 && at.getUTCFullYear() === y && at.getUTCMonth() === mo - 1 && at.getUTCDate() === d ? text : null;
}

const fileDay = (path) => validDay(/^(\d{4}-\d{2}-\d{2})-/.exec(path.slice(path.lastIndexOf('/') + 1))?.[1]);
const LINK = /\[((?:[^\[\]]|\[[^\]]*\])*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)/g;
const plain = (text) => text.replace(/[`*_~]/g, '').replace(/\[([^\]]*)\]\([^)]*\)/g, '$1').trim();

// recordsByDay organizes the Working log's records for navigation, from the
// TOC's text (or null) and the entries listed in its record folder (or
// null). Records come from the TOC's links, in its order, then from flat
// files the TOC does not link, by name. Each record is
// { path, anchor, title, day, linked }. Days run newest first; within a
// day, records keep that order. Records with no valid date, such as legacy
// originals, slices and catalogs, are kept apart in undated, never dropped
// or given a guessed date.
export function recordsByDay(toc, entries) {
  const records = [];
  const seen = new Set();
  const add = (record) => {
    const key = platformPath(record.path);
    if (seen.has(key)) return;
    seen.add(key);
    records.push(record);
  };
  for (const line of String(toc ?? '').split(/\r?\n/)) {
    const lineDay = validDay(/^\s*(?:[-*+]|\d+[.)])?\s*(\d{4}-\d{2}-\d{2})(?![\d-])/.exec(line)?.[1]);
    for (const m of line.matchAll(LINK)) {
      const target = documentLink('.yesman', m[2]);
      if (!target?.path || !isTaskLog(target.path) || platformPath(target.path) === TASK_LOG) continue;
      add({ path: target.path, anchor: target.anchor, title: plain(m[1]) || target.path.slice(target.path.lastIndexOf('/') + 1), day: lineDay ?? fileDay(target.path), linked: true });
    }
  }
  const files = (Array.isArray(entries) ? entries : [])
    .filter((e) => e && !e.dir && typeof e.path === 'string' && isTaskLog(e.path) && e.path.lastIndexOf('/') === TASK_LOG_DIR.length)
    .sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  for (const e of files) add({ path: e.path, anchor: '', title: e.path.slice(TASK_LOG_DIR.length + 1).replace(/\.md$/i, ''), day: fileDay(e.path), linked: false });
  const byDay = new Map();
  const undated = [];
  for (const r of records) {
    if (!r.day) { undated.push(r); continue; }
    if (!byDay.has(r.day)) byDay.set(r.day, []);
    byDay.get(r.day).push(r);
  }
  const days = [...byDay.keys()].sort().reverse().map((day) => ({ day, records: byDay.get(day) }));
  return { days, undated };
}
