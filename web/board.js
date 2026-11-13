// The board pane: a yesman's task list, which its agents keep with the
// yesman_board tool (internal/yesmen/board.go). It is the plan, not what
// the agents are doing now: open tasks show in the agents' order, finished
// ones fold away at the end, and a row changes its mark, never its place,
// when its status changes.

import { $, h, icon } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { t, formatNumber } from './i18n.js';

const LABEL = { todo: 'To do', doing: 'In progress', blocked: 'Blocked', done: 'Done', dropped: 'Dropped' };
const isOpen = (t) => t.status !== 'done' && t.status !== 'dropped';

function mark(status) {
  switch (status) {
    case 'done': return icon('check', 'mds-icon board-mark');
    case 'blocked': return icon('alert', 'mds-icon board-mark');
    case 'dropped': return icon('minimize', 'mds-icon board-mark');
    default: return h('i', { class: 'board-mark board-dot', 'aria-hidden': 'true' });
  }
}

function updated(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return t('updated just now');
  if (s < 3600) return t('updated {n} min ago', { n: formatNumber(Math.round(s / 60)) });
  if (s < 86400) return t('updated {n} h ago', { n: formatNumber(Math.round(s / 3600)) });
  return t('updated {n} d ago', { n: formatNumber(Math.round(s / 86400)) });
}

// createBoard returns the pane. host connects it to the page:
//   call(method, ...args)   a Yesman service method
//   yesman()                the yesman whose agent is shown, or null
//   isOpen(), setOpen(on)   whether the pane shows
//   agent(id)               an agent, to link a task's owner to it
//   openAgent(id)           shows an agent's conversation
export function createBoard(host) {
  const boards = new Map(); // yesman name -> { tasks, error }
  const expanded = new Set(); // `${yesman}/${task id}`
  const loading = new Map(); // yesman name -> its load, and whether another is due
  let doneOpen = false;

  function current() {
    return (host.isOpen() && host.yesman()) || null;
  }

  // load reads a board; a change while it loads reads it again after.
  async function load(name) {
    const running = loading.get(name);
    if (running) {
      running.again = true;
      return;
    }
    const entry = { again: false };
    loading.set(name, entry);
    try {
      const b = await host.call('Board', name);
      boards.set(name, { tasks: b?.tasks ?? [], error: null });
    } catch (err) {
      boards.set(name, { tasks: boards.get(name)?.tasks ?? null, error: err?.message || String(err) });
    }
    loading.delete(name);
    if (entry.again) return load(name);
    if (current()?.name === name) render();
  }

  function render() {
    const aside = $('#board');
    const y = current();
    aside.hidden = !y;
    aside.setAttribute('aria-label', t('Board'));
    document.documentElement.dataset.board = y ? 'open' : 'closed';
    if (!y) return;
    const b = boards.get(y.name);
    if (!b && !loading.has(y.name)) load(y.name);
    const tasks = b?.tasks ?? [];
    const open = tasks.filter(isOpen);
    const closed = tasks.filter((t) => !isOpen(t));
    const scroll = $('.board-body', aside)?.scrollTop ?? 0;
    const focused = document.activeElement?.closest?.('#board [data-key]')?.dataset.key;

    const count = b ? (open.length ? t('{n} open', { n: formatNumber(open.length) }) : '') : '';
    const body = h('div', { class: 'board-body' });
    if (!b) {
      body.append(h('p', { class: 'board-empty' }, t('Loading…')));
    } else if (b.error && !b.tasks) {
      body.append(h('p', { class: 'board-empty', 'data-tone': 'error' }, b.error));
    } else if (!tasks.length) {
      body.append(h('div', { class: 'board-empty' },
        h('strong', null, t('No tasks yet')),
        h('p', null, t('{name} adds tasks here as it plans, and its workers keep them up to date.', { name: y.name }))));
    } else {
      body.append(open.length
        ? h('ol', { class: 'board-list', 'aria-label': t('Open tasks') }, open.map((task) => row(y, task)))
        : h('p', { class: 'board-empty' }, t('Nothing open.')));
      if (closed.length) {
        const done = h('details', { class: 'board-done', open: doneOpen },
          h('summary', null, icon('chevron', 'mds-icon board-fold'), t('Done {n}', { n: formatNumber(closed.length) })),
          h('ol', { class: 'board-list' }, closed.map((task) => row(y, task))));
        done.addEventListener('toggle', () => { doneOpen = done.open; });
        body.append(done);
      }
    }

    aside.replaceChildren(
      h('div', { class: 'files-head drag' },
        h('strong', { class: 'files-title' }, t('Board')),
        count ? h('span', { class: 'board-count' }, count) : null,
        h('div', { class: 'files-head-actions' },
          h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Close the board'), 'aria-label': t('Close the board'), onclick: () => host.setOpen(false) }, icon('x')))),
      body);
    body.scrollTop = scroll;
    if (focused) $(`#board [data-key="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
  }

  function row(y, task) {
    const key = `${y.name}/${task.id}`;
    const isExpanded = expanded.has(key);
    const status = LABEL[task.status] ? t(LABEL[task.status]) : task.status;
    const owner = task.owner ? host.agent(`${y.name}/${task.owner}`) : null;
    const ownerEl = !task.owner ? null
      : owner
        ? h('button', { class: 'board-owner', type: 'button', title: t('{name}: open its conversation', { name: task.owner }), onclick: () => host.openAgent(owner.id) }, owner.number ? `a${owner.number}` : task.owner)
        : h('span', { class: 'board-owner', 'data-gone': 'true', title: t('{name} is not one of {yesman}\'s agents now', { name: task.owner, yesman: y.name }) }, task.owner);
    const inline = task.status === 'blocked' && task.note && !isExpanded ? h('p', { class: 'board-inline' }, task.note.split('\n')[0]) : null;
    const details = isExpanded
      ? h('div', { class: 'board-details' },
        task.detail ? h('div', { class: 'md', html: renderMarkdown(task.detail) }) : null,
        task.note ? h('div', { class: 'board-note' }, h('span', { class: 'board-label' }, task.status === 'blocked' ? t('Blocked') : t('Note')), h('div', { class: 'md', html: renderMarkdown(task.note) })) : null,
        h('p', { class: 'board-meta' }, `${task.id} · ${status} · ${updated(task.updatedAt)}`))
      : null;
    return h('li', { class: 'board-task', 'data-status': task.status },
      h('button', {
        class: 'board-toggle', type: 'button', 'data-key': key, 'aria-expanded': String(isExpanded),
        title: `${status}: ${task.title}`,
        onclick: () => {
          if (expanded.has(key)) expanded.delete(key);
          else expanded.add(key);
          render();
        },
      }, mark(task.status), h('span', { class: 'board-title' }, task.title)),
      ownerEl, inline, details);
  }

  // onChange follows a board the agents changed.
  function onChange(name) {
    if (boards.has(name) || current()?.name === name) load(name);
  }

  return { render, onChange };
}
