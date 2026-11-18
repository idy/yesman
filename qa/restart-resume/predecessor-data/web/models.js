// A roomy model chooser using Magpie's select surfaces and keyboard pattern.
import { h, icon } from './dom.js';
import { t, formatNumber } from './i18n.js';

let serial = 0;
let activeMenu = null;

export function createModelPicker({ models, provider, model, disabled, onSelect }) {
  const id = `model-menu-${++serial}`;
  const selected = models.findIndex((m) => m.id === model && m.provider === provider);
  const choice = models[selected];
  const trigger = h('button', {
    class: 'mds-select mds-select--text mds-select-trigger mds-select--font-caret composer-model', type: 'button', role: 'combobox',
    'aria-label': t('Model'), 'aria-haspopup': 'listbox', 'aria-expanded': 'false', 'aria-controls': id,
    disabled: disabled || !models.length, title: `${provider}/${model}`,
  }, choice?.name || model || t('No models available'), icon('caret-down', 'mds-icon mds-select-caret'));
  const menu = h('div', { class: 'mds-select-menu model-menu', id, role: 'listbox', 'aria-label': t('Model'), hidden: true });
  const topLayer = typeof menu.showPopover === 'function';
  if (topLayer) menu.setAttribute('popover', 'manual');
  const controller = new AbortController();
  const listen = (node, type, fn, options = {}) => node.addEventListener(type, fn, { ...options, signal: controller.signal });
  const grouped = new Set(models.map((m) => m.provider)).size > 1;
  const groups = new Map();
  const rows = models.map((m, index) => {
    let parent = menu;
    if (grouped) {
      if (!groups.has(m.provider)) {
        const group = h('div', { role: 'group', 'aria-label': m.provider }, h('div', { class: 'mds-select-group', 'aria-hidden': true }, m.provider));
        groups.set(m.provider, group);
        menu.append(group);
      }
      parent = groups.get(m.provider);
    }
    const label = m.name || m.id;
    const context = m.contextWindow > 0 ? t('{size} context window', { size: formatNumber(m.contextWindow, { notation: 'compact', maximumFractionDigits: 1 }) }) : null;
    const row = h('div', {
      class: 'mds-select-option model-option', id: `${id}-${index}`, role: 'option', 'data-index': index,
      'aria-selected': String(index === selected), title: context ? `${label}\n${context}` : label,
    }, h('span', { class: 'model-option-copy' }, h('span', { class: 'model-option-name' }, label),
      context ? h('span', { class: 'model-option-context' }, context) : null));
    parent.append(row);
    return row;
  });
  let active = selected >= 0 ? selected : 0;
  let typed = '';
  let typedAt = 0;
  let opened = null;

  function close() {
    if (menu.hidden) return;
    if (topLayer && menu.matches(':popover-open')) menu.hidePopover();
    menu.hidden = true;
    trigger.setAttribute('aria-expanded', 'false');
    trigger.removeAttribute('aria-activedescendant');
    opened?.abort();
    opened = null;
    if (activeMenu === api) activeMenu = null;
    typed = '';
  }

  function paint() {
    for (let i = 0; i < rows.length; i++) rows[i].toggleAttribute('data-active', i === active);
    const row = rows[active];
    if (!row) return;
    trigger.setAttribute('aria-activedescendant', row.id);
    if (row.offsetTop < menu.scrollTop) menu.scrollTop = row.offsetTop;
    else if (row.offsetTop + row.offsetHeight > menu.scrollTop + menu.clientHeight) menu.scrollTop = row.offsetTop + row.offsetHeight - menu.clientHeight;
  }

  function place() {
    if (menu.hidden) return;
    const rect = trigger.getBoundingClientRect();
    menu.style.width = `${Math.min(520, innerWidth - 24)}px`;
    const above = Math.max(0, rect.top - 20), below = Math.max(0, innerHeight - rect.bottom - 20);
    const down = below >= Math.min(menu.scrollHeight, 640) || below > above;
    menu.style.maxHeight = `${Math.min(640, down ? below : above)}px`;
    const height = menu.getBoundingClientRect().height;
    menu.style.left = `${Math.max(12, Math.min(rect.right - menu.getBoundingClientRect().width, innerWidth - menu.getBoundingClientRect().width - 12))}px`;
    menu.style.top = `${down ? rect.bottom + 8 : Math.max(12, rect.top - height - 8)}px`;
  }

  function open() {
    if (trigger.disabled || !menu.hidden) return;
    activeMenu?.close();
    activeMenu = api;
    document.body.append(menu);
    menu.hidden = false;
    if (topLayer) menu.showPopover();
    trigger.setAttribute('aria-expanded', 'true');
    active = selected >= 0 ? selected : 0;
    trigger.focus({ preventScroll: true });
    place();
    paint();
    opened = new AbortController();
    const options = { signal: opened.signal };
    document.addEventListener('pointerdown', (e) => { if (!trigger.contains(e.target) && !menu.contains(e.target)) close(); }, { ...options, capture: true });
    document.addEventListener('focusin', (e) => { if (!trigger.contains(e.target) && !menu.contains(e.target)) close(); }, options);
    document.addEventListener('scroll', (e) => { if (!menu.contains(e.target)) place(); }, { ...options, capture: true });
    window.addEventListener('resize', place, options);
  }

  function choose(index) {
    const m = models[index];
    if (!m) return;
    close();
    trigger.focus({ preventScroll: true });
    if (index !== selected) onSelect(m);
  }

  listen(trigger, 'click', () => menu.hidden ? open() : close());
  listen(trigger, 'keydown', (e) => {
    if (trigger.disabled || e.isComposing || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === 'Escape') { if (!menu.hidden) { e.preventDefault(); e.stopPropagation(); close(); } return; }
    if (e.key === 'Tab') { close(); return; }
    if (['Enter', ' ', 'ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
      e.preventDefault();
      if (e.key === 'Enter' || e.key === ' ') { if (menu.hidden) open(); else choose(active); return; }
      const wasOpen = !menu.hidden;
      open();
      if (e.key === 'Home') active = 0;
      else if (e.key === 'End') active = models.length - 1;
      else if (wasOpen) active = Math.max(0, Math.min(models.length - 1, active + (e.key === 'ArrowDown' ? 1 : -1)));
      paint();
      return;
    }
    if (e.key.length !== 1) return;
    e.preventDefault();
    open();
    const now = Date.now();
    typed = now - typedAt < 700 ? typed + e.key.toLowerCase() : e.key.toLowerCase();
    typedAt = now;
    const search = [...typed].every((c) => c === typed[0]) ? typed[0] : typed;
    const choices = models.map((_, i) => i);
    const order = [...choices.slice(active + 1), ...choices.slice(0, active + 1)];
    if (search.length > 1) order.unshift(active);
    const match = order.find((i) => (models[i].name || models[i].id).toLowerCase().startsWith(search));
    if (match !== undefined) { active = match; paint(); }
  });
  listen(menu, 'pointerdown', (e) => { if (e.pointerType === 'mouse') e.preventDefault(); });
  listen(menu, 'pointermove', (e) => {
    const row = e.target.closest('[role=option]');
    if (row) { active = Number(row.dataset.index); paint(); }
  });
  listen(menu, 'click', (e) => {
    const row = e.target.closest('[role=option]');
    if (row) choose(Number(row.dataset.index));
  });
  const api = { trigger, close, destroy() { close(); controller.abort(); menu.remove(); } };
  return api;
}
