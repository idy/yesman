// pi's current context estimate and cumulative session usage. Keep these
// separate: the tokens billed across requests are not the context window.
import { h } from './dom.js';

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });
const exact = new Intl.NumberFormat('en', { maximumFractionDigits: 0 });
const percentage = new Intl.NumberFormat('en', { maximumFractionDigits: 1 });
const number = (value) => typeof value === 'number' && Number.isFinite(value);
const count = (value) => number(value) ? exact.format(value) : '—';
const errorText = (err) => err?.message || err?.error || String(err);

function ring() {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('class', 'context-ring');
  svg.setAttribute('aria-hidden', 'true');
  for (const cls of ['context-ring-track', 'context-ring-fill']) {
    const circle = document.createElementNS(ns, 'circle');
    for (const [name, value] of Object.entries({ cx: 12, cy: 12, r: 9, fill: 'none', 'stroke-width': 2, pathLength: 100, class: cls })) circle.setAttribute(name, value);
    svg.append(circle);
  }
  return svg;
}

function usageOf(data) {
  const u = data?.contextUsage;
  const limit = number(u?.contextWindow) && u.contextWindow > 0 ? u.contextWindow : null;
  const used = limit !== null && number(u?.tokens) ? Math.max(0, u.tokens) : null;
  return { limit, used, percent: used === null ? null : used / limit * 100 };
}

const row = (label, value) => h('div', { class: 'context-row' }, h('dt', null, label), h('dd', null, value));

export function createContextViewer({ call, keyOf }) {
  const cache = new Map();
  const svg = ring();
  const fill = svg.querySelector('.context-ring-fill');
  const button = h('button', { class: 'context-toggle', type: 'button', 'aria-label': 'Context window', 'aria-haspopup': 'dialog', 'aria-expanded': 'false', 'aria-controls': 'context-panel' }, svg);
  const value = h('span', { class: 'context-value', role: 'status' });
  const body = h('div', { class: 'context-body' });
  const dismiss = h('button', { class: 'mds-link-button', type: 'button', onclick: () => close(true) }, 'Close');
  const panel = h('section', { class: 'context-panel', id: 'context-panel', role: 'dialog', 'aria-label': 'Context window', tabindex: -1, hidden: true },
    h('div', { class: 'context-heading' }, h('h2', null, 'Context window'), dismiss), value, body);
  const topLayer = typeof panel.showPopover === 'function';
  if (topLayer) panel.setAttribute('popover', 'manual');
  let current = null;
  let listeners = null;
  let timer = null;

  function place() {
    if (panel.hidden || !button.isConnected) return;
    const anchor = button.getBoundingClientRect();
    const box = panel.getBoundingClientRect();
    panel.style.left = `${Math.max(12, Math.min(anchor.right - box.width, innerWidth - box.width - 12))}px`;
    panel.style.top = `${Math.max(12, anchor.top - box.height - 8)}px`;
  }

  function close(refocus = false) {
    if (!panel.hidden && topLayer && panel.matches(':popover-open')) panel.hidePopover();
    panel.hidden = true;
    button.setAttribute('aria-expanded', 'false');
    listeners?.abort();
    listeners = null;
    if (refocus && button.isConnected) button.focus({ preventScroll: true });
  }

  function paint() {
    const entry = current && cache.get(current.key);
    const data = entry?.data;
    const { used, limit, percent } = usageOf(data);
    const known = used !== null;
    fill.setAttribute('stroke-dasharray', `${known ? Math.min(100, percent) : 0} 100`);
    button.setAttribute('aria-busy', String(!!entry?.loading));
    const label = known
      ? `Context window: ${count(used)} of ${count(limit)} tokens (${percentage.format(percent)}%)${entry?.error ? '; refresh failed, showing last reported usage' : ''}`
      : entry?.error ? 'Context usage unavailable' : entry?.loading ? 'Loading context usage' : 'Context usage not yet reported';
    button.title = label;
    button.setAttribute('aria-label', label);
    if (panel.hidden) return;
    value.textContent = known ? `${compact.format(used)} / ${compact.format(limit)} (${percentage.format(percent)}%)`
      : limit ? `— / ${compact.format(limit)}` : 'Usage unavailable';
    const expanded = body.querySelector('details')?.open ?? false;
    const focused = body.contains(document.activeElement) ? document.activeElement : null;
    body.setAttribute('aria-busy', String(!!entry?.loading));
    body.replaceChildren();
    if (entry?.error) body.append(h('div', { class: 'context-error', role: 'alert' }, h('p', null, errorText(entry.error)),
      h('button', { class: 'mds-link-button', type: 'button', disabled: entry.loading, onclick: () => load(current) }, 'Try again')));
    if (!data) {
      if (!entry?.error) body.append(h('p', { class: 'context-note' }, 'Loading context usage…'));
      if (focused) dismiss.focus({ preventScroll: true });
      place();
      return;
    }
    const meter = h('div', {
      class: 'context-meter', role: known ? 'meter' : null, 'aria-label': known ? 'Estimated context used' : null,
      'aria-valuemin': known ? 0 : null, 'aria-valuemax': known ? limit : null, 'aria-valuenow': known ? Math.min(used, limit) : null,
      'aria-valuetext': known ? `${count(used)} of ${count(limit)} tokens, ${percentage.format(percent)}%` : null,
      title: known ? label : 'pi has not reported the current context size',
    }, h('span', { style: `width: ${known ? Math.min(100, percent) : 0}%` }));
    body.append(meter,
      h('dl', { class: 'context-rows' }, row('Used context', known ? count(used) : 'Not yet reported'), row('Free space', known ? count(Math.max(0, limit - used)) : '—')),
      h('h3', { class: 'context-section' }, 'Session totals'),
      h('dl', { class: 'context-rows' },
        row('Input tokens', count(data.tokens?.input)), row('Output tokens', count(data.tokens?.output)),
        row('Cache read', count(data.tokens?.cacheRead)), row('Cache write', count(data.tokens?.cacheWrite)), row('Total tokens', count(data.tokens?.total))),
      h('details', { class: 'context-details', open: expanded }, h('summary', null, 'Messages and tools'),
        h('dl', { class: 'context-rows' }, row('User messages', count(data.userMessages)), row('Assistant messages', count(data.assistantMessages)),
          row('Tool calls', count(data.toolCalls)), row('Tool results', count(data.toolResults)))),
      h('p', { class: 'context-note' }, known
        ? 'Context usage is estimated by pi. Session totals count usage across requests.'
        : 'pi has not reported the current context size. Session totals count usage across requests.'));
    if (focused) (focused.tagName === 'SUMMARY' ? body.querySelector('summary') : dismiss).focus({ preventScroll: true });
    place();
  }

  async function load(target) {
    const entry = target && cache.get(target.key);
    if (!entry) return;
    if (entry.loading) { entry.again = true; return; }
    entry.loading = true;
    entry.again = false;
    const generation = entry.generation;
    if (current?.key === target.key) paint();
    try {
      const data = await call('Context', target.id);
      if (cache.get(target.key) === entry && generation === entry.generation) {
        entry.data = data;
        entry.error = null;
        entry.updated = Date.now();
      }
    } catch (err) {
      if (cache.get(target.key) === entry && generation === entry.generation) entry.error = err;
    } finally {
      entry.loading = false;
      if (cache.get(target.key) === entry && current?.key === target.key) {
        paint();
        if (entry.again) { entry.again = false; load(current); }
      }
    }
  }

  function open() {
    if (!current) return;
    if (!panel.hidden) { close(); return; }
    clearTimeout(timer);
    document.body.append(panel);
    panel.hidden = false;
    if (topLayer) panel.showPopover();
    button.setAttribute('aria-expanded', 'true');
    paint();
    dismiss.focus({ preventScroll: true });
    listeners = new AbortController();
    const options = { signal: listeners.signal };
    document.addEventListener('pointerdown', (e) => {
      if (!panel.contains(e.target) && !button.contains(e.target)) close();
    }, { ...options, capture: true });
    document.addEventListener('focusin', (e) => {
      if (!panel.contains(e.target) && !button.contains(e.target)) close();
    }, options);
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(true); }
    }, options);
    document.addEventListener('scroll', (e) => { if (!panel.contains(e.target)) place(); }, { ...options, capture: true });
    window.addEventListener('resize', place, options);
    load(current);
  }
  button.addEventListener('click', open);
  panel.addEventListener('toggle', place, true);

  return {
    mount(a) {
      const key = keyOf(a);
      const switched = current?.key !== key;
      if (switched) { close(); clearTimeout(timer); }
      current = { key, id: a.id };
      let entry = cache.get(key);
      if (!entry) { entry = { data: null, error: null, loading: false, again: false, generation: 0, updated: 0 }; cache.set(key, entry); }
      const model = `${a.provider}/${a.model}`;
      const modelChanged = entry.model !== model;
      if (modelChanged) { entry.model = model; entry.generation++; entry.data = null; entry.error = null; }
      paint();
      if (switched || modelChanged || Date.now() - entry.updated > 1000 && !entry.error) load(current);
      if (!panel.hidden) requestAnimationFrame(place);
      return button;
    },
    refresh(a) {
      if (!a || current?.key !== keyOf(a) || !button.isConnected) return;
      clearTimeout(timer);
      const target = current;
      timer = setTimeout(() => { if (current?.key === target.key) load(current); }, 200);
    },
    close,
    prune(keys) {
      for (const key of cache.keys()) if (!keys.has(key)) cache.delete(key);
      if (current && !keys.has(current.key)) { close(); clearTimeout(timer); current = null; }
    },
  };
}
