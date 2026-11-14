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
    return typeof value.name === 'string' && isTaskLog(value.path) && typeof value.anchor === 'string' ? value : null;
  } catch { return null; }
}

export function saveLogRoute(name, path, anchor = '', replace = false) {
  const value = { name, path, anchor };
  const hash = `#task-log=${encodeURIComponent(JSON.stringify(value))}`;
  if (location.hash !== hash) history[replace ? 'replaceState' : 'pushState'](null, '', hash);
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
