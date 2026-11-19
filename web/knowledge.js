// Knowledge's root INDEX is a catalog. It supplies labels and order, while
// listings supply the actual hierarchy. A link never creates a row or causes
// another catalog or note to be read.
import { renderDocument } from './markdown.js';
import { documentLink } from './task-log.js';

export const KNOWLEDGE = '.yesman/knowledge';
export const KNOWLEDGE_INDEX = `${KNOWLEDGE}/INDEX.md`;
const MAX_CATALOG_LENGTH = 128 * 1024;
const MAX_LINKS = 2000;
const MAX_DEPTH = 16;
const markdown = /\.(?:md|markdown)$/i;
const safeName = (name) => name && name !== '.' && name !== '..' && !/[\\/\u0000-\u001f]/.test(name);
const nativeKey = (path) => /Win/i.test(navigator.platform) ? path.toLowerCase() : path;

export function knowledgeCatalog(text) {
  if (!text || text.length > MAX_CATALOG_LENGTH) return null;
  const template = document.createElement('template');
  template.innerHTML = renderDocument(text);
  const order = new Map();
  const links = new Map();
  const anchors = template.content.querySelectorAll('a[href]:not([data-path]):not([data-external])');
  for (let rank = 0; rank < Math.min(anchors.length, MAX_LINKS); rank++) {
    const link = anchors[rank];
    const href = link.getAttribute('href');
    if (href.startsWith('#')) continue;
    const file = documentLink('.', href);
    const target = documentLink(KNOWLEDGE, href);
    if (!file?.path || !target?.path.startsWith(`${KNOWLEDGE}/`) || nativeKey(target.path) === nativeKey(KNOWLEDGE_INDEX)) continue;
    const parts = target.path.slice(KNOWLEDGE.length + 1).split('/');
    if (parts.length > MAX_DEPTH || !parts.every(safeName)) continue;
    const key = nativeKey(target.path);
    if (!links.has(key)) links.set(key, { title: link.textContent.trim().slice(0, 256), anchor: target.anchor });
    let path = KNOWLEDGE;
    for (const part of parts) {
      path += `/${part}`;
      const prefix = nativeKey(path);
      if (!order.has(prefix)) order.set(prefix, rank);
    }
  }
  return { order, links };
}

export function knowledgeEntries(dir, entries, catalog, depth) {
  const seen = new Set();
  const rows = [];
  for (const entry of entries) {
    if (!entry || typeof entry.name !== 'string' || !safeName(entry.name) || entry.path !== `${dir}/${entry.name}` || entry.special) continue;
    if (entry.dir ? depth >= MAX_DEPTH - 1 : !markdown.test(entry.name)) continue;
    const key = nativeKey(entry.path);
    if (seen.has(key)) continue;
    seen.add(key);
    const index = dir === KNOWLEDGE && key === nativeKey(KNOWLEDGE_INDEX);
    const link = catalog?.links.get(key);
    rows.push({ entry: { ...entry, name: link?.title || entry.name, anchor: entry.dir ? '' : link?.anchor },
      rank: index ? -1 : catalog?.order.get(key) ?? Infinity });
  }
  // Stable sort keeps unlisted entries in the service's directory order.
  return rows.sort((a, b) => a.rank - b.rank).map(({ entry }) => entry);
}
