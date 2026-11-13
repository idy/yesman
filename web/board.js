// The board pane: a yesman's task list, which its agents keep with the
// yesman_board tool (internal/yesmen/board.go). It is the plan, not what
// the agents are doing now: open tasks show in the agents' order, finished
// ones fold away at the end, and a row changes its mark, never its place,
// when its status changes.

import { $, h, icon } from './dom.js';
import { renderMarkdown } from './markdown.js';

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
  if (s < 60) return 'updated just now';
  if (s < 3600) return `updated ${Math.round(s / 60)} min ago`;
  if (s < 86400) return `updated ${Math.round(s / 3600)} h ago`;
  return `updated ${Math.round(s / 86400)} d ago`;
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
    document.documentElement.dataset.board = y ? 'open' : 'closed';
    if (!y) return;
    const b = boards.get(y.name);
    if (!b && !loading.has(y.name)) load(y.name);
    const tasks = b?.tasks ?? [];
    const open = tasks.filter(isOpen);
    const closed = tasks.filter((t) => !isOpen(t));
    const scroll = $('.board-body', aside)?.scrollTop ?? 0;
    const focused = document.activeElement?.closest?.('#board [data-key]')?.dataset.key;

    const count = b ? (open.length ? `${open.length} open` : '') : '';
    const body = h('div', { class: 'board-body' });
    if (!b) {
      body.append(h('p', { class: 'board-empty' }, 'Loading…'));
    } else if (b.error && !b.tasks) {
      body.append(h('p', { class: 'board-empty', 'data-tone': 'error' }, b.error));
    } else if (!tasks.length) {
      body.append(h('div', { class: 'board-empty' },
        h('strong', null, 'No tasks yet'),
        h('p', null, `${y.name} adds tasks here as it plans, and its workers keep them up to date.`)));
    } else {
      body.append(open.length
        ? h('ol', { class: 'board-list', 'aria-label': 'Open tasks' }, open.map((t) => row(y, t)))
        : h('p', { class: 'board-empty' }, 'Nothing open.'));
      if (closed.length) {
        const done = h('details', { class: 'board-done', open: doneOpen },
          h('summary', null, icon('chevron', 'mds-icon board-fold'), `Done ${closed.length}`),
          h('ol', { class: 'board-list' }, closed.map((t) => row(y, t))));
        done.addEventListener('toggle', () => { doneOpen = done.open; });
        body.append(done);
      }
    }

    aside.replaceChildren(
      h('div', { class: 'files-head drag' },
        h('strong', { class: 'files-title' }, 'Board'),
        count ? h('span', { class: 'board-count' }, count) : null,
        h('div', { class: 'files-head-actions' },
          h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Close the board', 'aria-label': 'Close the board', onclick: () => host.setOpen(false) }, icon('x')))),
      body);
    body.scrollTop = scroll;
    if (focused) $(`#board [data-key="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
  }

  function row(y, t) {
    const key = `${y.name}/${t.id}`;
    const isExpanded = expanded.has(key);
    const owner = t.owner ? host.agent(`${y.name}/${t.owner}`) : null;
    const ownerEl = !t.owner ? null
      : owner
        ? h('button', { class: 'board-owner', type: 'button', title: `${t.owner}: open its conversation`, onclick: () => host.openAgent(owner.id) }, owner.number ? `a${owner.number}` : t.owner)
        : h('span', { class: 'board-owner', 'data-gone': 'true', title: `${t.owner} is not one of ${y.name}'s agents now` }, t.owner);
    const inline = t.status === 'blocked' && t.note && !isExpanded ? h('p', { class: 'board-inline' }, t.note.split('\n')[0]) : null;
    const details = isExpanded
      ? h('div', { class: 'board-details' },
        t.detail ? h('div', { class: 'md', html: renderMarkdown(t.detail) }) : null,
        t.note ? h('div', { class: 'board-note' }, h('span', { class: 'board-label' }, t.status === 'blocked' ? 'Blocked' : 'Note'), h('div', { class: 'md', html: renderMarkdown(t.note) })) : null,
        h('p', { class: 'board-meta' }, `${t.id} · ${LABEL[t.status] ?? t.status} · ${updated(t.updatedAt)}`))
      : null;
    return h('li', { class: 'board-task', 'data-status': t.status },
      h('button', {
        class: 'board-toggle', type: 'button', 'data-key': key, 'aria-expanded': String(isExpanded),
        title: `${LABEL[t.status] ?? t.status}: ${t.title}`,
        onclick: () => {
          if (expanded.has(key)) expanded.delete(key);
          else expanded.add(key);
          render();
        },
      }, mark(t.status), h('span', { class: 'board-title' }, t.title)),
      ownerEl, inline, details);
  }

  // onChange follows a board the agents changed.
  function onChange(name) {
    if (boards.has(name) || current()?.name === name) load(name);
  }

  return { render, onChange };
}
