// The info panel, between the conversation and the files: the yesman shown
// at a glance. Its knowledge opens in the files pane, read as pages. Its
// worktrees show what each changed, or the pull request its branch is in,
// and open in the files pane, their changes or their files. Storage shows
// the disk its workspace takes, by part.
//
// Info (info.go) answers quickly; pull requests and storage take longer and
// arrive by themselves. Each is kept a while, and asked again when the
// workspace changes, an agent finishes a run, or the user refreshes.

import { $, h, icon } from './dom.js';
import { counts, formatSize } from './diff.js';
import { t, tn, formatNumber } from './i18n.js';
import { isTaskLog } from './task-log.js';
import { createWorktreeMenu, worktreeEntry } from './worktree-cleanup.js';

// How long each part is kept before the panel asks again by itself.
const FRESH = { info: 5000, pulls: 120000, usage: 60000 };

// createInfo returns the panel. host:
//   call(method, ...args), fail(err)
//   yesman()               the yesman whose agent is shown, or null
//   isOpen()               whether the panel shows
//   files()                what the files pane shows (files.shown())
//   openKnowledge(), openTaskLog(), openWorktree(dir, show), checkout(y)
//   openTasks(), tasksOpen(), tasksSummary(y)   the Tasks pane and its summary
//   needsYou(y)            what waits on the user from y: { count, error,
//                          refused, open (the Needs you pane shows) }
//   openNeedsYou(y)        open the Needs you pane, of y, beside its conversation
//   pane()                 the pane beside the conversation, as state.pane
//   openTopics()           open the Topics pane beside the conversation
//   openURL(url)
export function createInfo(host) {
  const data = new Map(); // yesman name -> what was read of it

  function stateOf(name) {
    let d = data.get(name);
    const hire = host.yesman()?.name === name ? host.yesman().hiredAt : undefined;
    if (!d || (hire && d.hire !== hire)) {
      d = { hire, info: null, pulls: null, usage: null, at: {}, busy: {}, error: {} };
      data.set(name, d);
    }
    return d;
  }

  // load asks for one part of a yesman: info, pulls or usage.
  async function load(name, part, refresh = false) {
    const d = stateOf(name);
    if (d.busy[part]) return;
    d.busy[part] = true;
    if (part !== 'info') render();
    try {
      if (part === 'info') d.info = await host.call('Info', name);
      else if (part === 'pulls') d.pulls = Object.fromEntries((await host.call('Pulls', name, refresh)).map((p) => [p.dir, p]));
      else d.usage = await host.call('Usage', name, refresh);
      d.error[part] = null;
    } catch (err) {
      d.error[part] = err?.message || String(err);
    } finally {
      d.busy[part] = false;
      d.at[part] = Date.now();
    }
    render();
  }

  // stale asks again for what is older than it should be.
  function stale(name) {
    const d = stateOf(name);
    for (const part of ['info', 'pulls', 'usage']) {
      if (!d.busy[part] && !(Date.now() - (d.at[part] ?? 0) < FRESH[part])) load(name, part);
    }
  }

  // ---------- Following the workspace ----------

  const timers = new Map();
  // soon asks again for parts of a yesman once changes settle.
  function soon(name, parts, wait = 1500) {
    const key = `${name}:${parts.join()}`;
    clearTimeout(timers.get(key));
    timers.set(key, setTimeout(() => {
      timers.delete(key);
      if (!data.has(name)) return;
      for (const part of parts) {
        if (shows(name)) load(name, part, part === 'pulls');
        else stateOf(name).at[part] = 0; // asked again when it next shows
      }
    }, wait));
  }

  const shows = (name) => host.isOpen() && host.yesman()?.name === name;

  // The worktrees changed: checked out, removed, moved.
  function onChanged() {
    for (const name of data.keys()) soon(name, ['info', 'pulls'], 300);
  }

  // Files of a workspace changed while the files pane watches it.
  function onFiles({ yesman }) {
    if (data.has(yesman)) soon(yesman, ['info']);
  }

  // An agent of the yesman used a tool, or finished a run: what it did may
  // have changed the worktrees, pushed a branch or opened a pull request.
  function onAgentEvent({ agent, data: e }) {
    const name = agent?.split('/')[0];
    if (!name || !data.has(name)) return;
    if (e.type === 'tool_execution_end') soon(name, ['info'], 2000);
    else if (e.type === 'agent_end' || e.type === 'agent_settled') soon(name, ['info', 'pulls', 'usage'], 1000);
  }

  // ---------- Rendering ----------

  function render() {
    const aside = $('#yesman-info');
    if (!aside) return;
    const y = host.isOpen() ? host.yesman() : null;
    aside.hidden = !y;
    aside.setAttribute('aria-label', t('Info'));
    document.documentElement.dataset.info = y ? 'open' : 'closed';
    if (!y) return;
    const d = stateOf(y.name);
    stale(y.name);
    const top = aside.scrollTop;
    // A redraw keeps focus on the entry it was on.
    const focused = aside.contains(document.activeElement) ? document.activeElement.closest('[data-info-entry]')?.dataset.infoEntry : null;
    aside.replaceChildren(
      entriesCard(y, d),
      storage(y, d),
      worktrees(y, d));
    aside.scrollTop = top;
    if (focused) aside.querySelector(`[data-info-entry="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
  }

  function section(title, actions, ...body) {
    return h('section', { class: 'info-section' },
      h('div', { class: 'info-heading' }, h('h2', null, title), actions),
      ...body);
  }

  function refreshButton(label, busy, onclick) {
    return h('button', { class: 'mds-icon-button mds-icon-button--compact info-refresh', type: 'button', title: label, 'aria-label': label, disabled: busy, 'aria-busy': busy ? 'true' : null, onclick },
      busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'));
  }

  // ---------- Entries ----------

  // One card lists what of the yesman opens elsewhere, a row each, in this
  // order: what it keeps, what it did, what waits on the user, and what the
  // user discussed with it. A new part adds its row to entries.
  function entries(y, d) {
    return [knowledgeEntry(y, d), workingLogsEntry(y), needsYouEntry(y), topicsEntry(y, d), tasksEntry(y)].filter(Boolean);
  }

  function entriesCard(y, d) {
    return h('section', { class: 'info-section info-section--entries' },
      h('ul', { class: 'info-entries' }, entries(y, d).map((e) => h('li', null,
        h('button', { class: 'info-link', type: 'button', 'data-info-entry': e.key, 'aria-current': e.current ? 'page' : null, title: e.title, onclick: e.open },
          h('span', { class: 'info-link-label' }, e.label),
          icon('chevron', 'mds-icon info-chevron'),
          h('span', { class: 'info-aside', 'data-tone': e.tone ?? null }, e.aside ?? ''))))));
  }

  function knowledgeEntry(y, d) {
    const k = d.info?.knowledge;
    const shown = host.files();
    return {
      key: 'knowledge', label: t('Knowledge'), title: t('Read the notes the agents keep, in the files pane'),
      current: shown?.yesman === y.name && shown.kind === 'knowledge',
      aside: !k ? '' : k.notes || k.index ? tn(k.notes, '{n} note', '{n} notes') : t('Empty'),
      open: () => host.openKnowledge(),
    };
  }

  // Working logs is the task log, .yesman/task-log.md, under the name the
  // page gives it.
  function workingLogsEntry(y) {
    const shown = host.files();
    return {
      key: 'working-logs', label: t('Working Logs'), title: t('Read task and PR history in the files pane'),
      current: shown?.yesman === y.name && isTaskLog(shown.path),
      open: () => host.openTaskLog(),
    };
  }

  // What waits on the user from this yesman, counted from what the Needs
  // you pane shows; it opens the pane.
  function needsYouEntry(y) {
    const { count, error, refused, open } = host.needsYou(y);
    return {
      key: 'needs-you', label: t('Needs You'), title: t('See what {name} needs from you', { name: y.displayName || y.name }),
      current: open,
      // A file that cannot be read is said, not counted as nothing; and so
      // is a lead that cannot hand over new requests.
      aside: error ? [count && tn(count, '{n} waiting', '{n} waiting'), t('Cannot be read')].filter(Boolean).join(' · ')
        : refused ? [count && tn(count, '{n} waiting', '{n} waiting'), t('New requests refused')].filter(Boolean).join(' · ')
        : count ? tn(count, '{n} waiting', '{n} waiting') : t('Nothing waiting'),
      tone: error || refused ? 'error' : count ? 'waiting' : null,
      open: () => host.openNeedsYou(y),
    };
  }

  // The topics worth finding again, counted from what the Info read of the
  // topics file; it opens the Topics pane.
  function topicsEntry(y, d) {
    const c = d.info?.topics;
    return {
      key: 'topics', label: t('Topics'), title: c?.error || t('Questions and discussions worth keeping: conclusions and open questions, beside the conversation'),
      current: host.pane() === 'topics',
      // A file that cannot be read is said, not counted as nothing.
      aside: !c ? '' : c.error ? t('Cannot be read') : c.open ? t('{n} open', { n: formatNumber(c.open) }) : c.resolved + c.archived ? t('Nothing open') : t('Empty'),
      tone: c?.error ? 'error' : null,
      open: () => host.openTopics(),
    };
  }

  // The topics changed: their count is read again.
  function onTopics(name) {
    if (data.has(name)) soon(name, ['info'], 1500);
  }

  // ---------- Tasks ----------

  // Tasks: the yesman's scheduled tasks, how many are on and when the next
  // one runs, and whether a run needs a look; it opens the Tasks pane.
  function tasksEntry(y) {
    const s = host.tasksSummary?.(y);
    if (!s) return null;
    // A file that cannot be read is said, not counted as nothing.
    const unread = !!s.error && s.total == null;
    let text;
    if (s.loading) text = t('Loading…');
    else if (unread) text = t('Cannot be read');
    else if (!s.total) text = t('None');
    else if (!s.on) text = t('All off');
    else text = s.next ? t('{n} on · next {when}', { n: formatNumber(s.on), when: nextWhen(s.next, s.zone) }) : t('{n} on', { n: formatNumber(s.on) });
    const look = s.attention ? tn(s.attention, '1 run needs a look', '{n} runs need a look') : '';
    const attention = look ? h('span', { class: 'info-attention', title: look }, icon('alert'), h('span', { class: 'mds-visually-hidden' }, look)) : null;
    return {
      key: 'tasks', label: t('Tasks'), title: unread ? s.error : t('Scheduled tasks: requests the lead gets at times you choose'),
      current: !!host.tasksOpen?.(),
      aside: [attention, text], tone: unread ? 'error' : null,
      open: () => host.openTasks(),
    };
  }

  // nextWhen says when the next run is, briefly: a time today, else a day.
  function nextWhen(iso, zone) {
    const d = new Date(iso);
    const now = new Date();
    const sameDay = d.toDateString() === now.toDateString();
    const opts = sameDay ? { hour: '2-digit', minute: '2-digit' } : { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' };
    return new Intl.DateTimeFormat(undefined, opts).format(d);
  }

  // ---------- Worktrees ----------

  const menu = createWorktreeMenu({ ...host, updated(y, removed) {
    const d = data.get(y.name);
    if (!d || d.hire !== y.hiredAt) return;
    const keep = w => !removed.some(r => r.dir === w.dir && r.cleanupId === w.cleanupId);
    if (d.info) d.info.worktrees = d.info.worktrees.filter(keep);
    const live = host.yesman();
    if (live?.name === y.name && live.hiredAt === y.hiredAt) {
      live.worktrees = (live.worktrees ?? []).filter(keep);
      host.removed(y, removed);
    }
    // Reads already in flight still own d. They cannot publish a pre-removal
    // list into the new state, even when rereading the current list fails.
    data.set(y.name, { ...d, at: {}, busy: {}, error: {} });
    render();
  } });

  function worktrees(y, d) {
    const list = d.info?.worktrees;
    const busy = d.busy.info || d.busy.pulls;
    const refresh = refreshButton(t('Refresh the worktrees and their pull requests'), busy, () => { load(y.name, 'info'); load(y.name, 'pulls', true); });
    let body;
    if (!d.info) {
      body = d.error.info ? h('p', { class: 'info-note danger' }, d.error.info) : h('p', { class: 'info-note' }, t('Loading…'));
    } else if (!list.length && !d.info.worktreesError) {
      body = h('div', { class: 'info-note' },
        h('span', null, t('No worktrees yet. The agents check out the repositories they need.')),
        h('button', { class: 'mds-link-button info-note-action', type: 'button', onclick: () => host.checkout(y) }, t('Check out a repository…')));
    } else {
      // Worktrees whose pull request is merged fold into one row at the end,
      // unless the files pane shows one.
      const shown = host.files();
      const merged = list.filter((w) => d.pulls?.[w.dir]?.pull?.state === 'merged'
        && !(shown?.yesman === y.name && shown.kind === 'worktree' && shown.dir === w.dir));
      const open = list.filter((w) => !merged.includes(w));
      body = [
        open.length ? h('ul', { class: 'info-list', role: 'list' }, open.map((w) => worktreeRow(y, d, w))) : null,
        merged.length ? mergedFold(y, d, merged) : null,
      ];
    }
    const heading = h('button', { class: 'worktree-heading', type: 'button', 'aria-haspopup': 'menu',
      'data-info-entry': worktreeEntry(null), onclick: e => menu(e, y), title: t('Worktree actions') }, t('Worktrees'));
    const card = section(heading, refresh, body);
    card.querySelector('.info-heading').oncontextmenu = e => menu(e, y);
    const error = d.error.info || d.info?.worktreesError;
    if (d.info && error) card.append(h('p', { class: 'info-note danger', role: 'status' }, t('Could not refresh Worktrees.'), ' ', error));
    return card;
  }

  // Whether the merged worktrees show, kept across launches.
  let mergedOpen = localStorage.getItem('yesman.info.merged') === 'open';

  function mergedFold(y, d, merged) {
    const toggle = h('button', {
      class: 'info-merged-toggle', type: 'button', 'aria-expanded': String(mergedOpen),
      title: mergedOpen ? t('Hide the worktrees whose pull requests are merged') : t('Show the worktrees whose pull requests are merged'),
      onclick: () => {
        mergedOpen = !mergedOpen;
        localStorage.setItem('yesman.info.merged', mergedOpen ? 'open' : 'closed');
        render();
      },
    }, icon('chevron', 'mds-icon info-chevron'), h('span', null, tn(merged.length, '{n} merged', '{n} merged')));
    return h('div', { class: 'info-merged' }, toggle,
      mergedOpen ? h('ul', { class: 'info-list', role: 'list' }, merged.map((w) => worktreeRow(y, d, w))) : null);
  }

  function worktreeRow(y, d, w) {
    const pull = d.pulls?.[w.dir]?.pull;
    const shown = host.files();
    const current = shown?.yesman === y.name && shown.kind === 'worktree' && shown.dir === w.dir;
    const open = () => host.openWorktree(w.dir, 'changes');
    const row = { class: 'info-row', 'data-current': current ? 'true' : null, oncontextmenu: e => menu(e, y, w) };
    const main = { class: 'info-row-main', type: 'button', 'data-info-entry': worktreeEntry(w), 'aria-haspopup': 'menu', onclick: open };
    const filesButton = h('button', { class: 'mds-icon-button mds-icon-button--compact info-row-action', type: 'button', title: t('Show its files'), 'aria-label': t('Show the files of {name}', { name: w.dir }), onclick: () => host.openWorktree(w.dir, 'files') }, icon('folder'));
    if (pull) {
      const checks = pull.checks;
      const tone = pull.state === 'merged' ? 'merged' : pull.state === 'closed' ? 'closed' : pull.draft ? 'draft' : 'open';
      return h('li', row,
        h('button', { ...main, title: `${pull.title}\n${pull.repo}#${pull.number} · ${w.dir}\n${t('Show its changes')}` },
          h('span', { class: 'info-row-icon', 'data-pull': tone }, icon(pull.state === 'merged' ? 'merge' : 'pullRequest')),
          h('span', { class: 'info-row-title' }, pull.title),
          h('span', { class: 'info-row-meta' },
            h('span', { class: 'info-pull-number' }, `#${pull.number}`),
            counts(pull),
            pullState(pull, tone),
            checks ? checksEl(checks) : null)),
        h('div', { class: 'info-row-actions' },
          filesButton,
          h('button', { class: 'mds-icon-button mds-icon-button--compact info-row-action', type: 'button', title: t('Open on GitHub'), 'aria-label': t('Open #{number} on GitHub', { number: pull.number }), onclick: () => host.openURL(pull.url) }, icon('external'))));
    }
    const c = w.changes;
    const meta = [];
    if (w.detached) meta.push(h('span', null, t('detached at {head}', { head: (w.head ?? '').slice(0, 7) })));
    else meta.push(h('span', { class: 'info-branch' }, w.branch));
    if (c) {
      if (c.files) meta.push(counts(c, { title: tn(c.files, '{n} file changed', '{n} files changed') }));
      else meta.push(h('span', { class: 'info-quiet' }, t('no changes')));
      if (c.uncommitted) meta.push(h('span', { class: 'info-dirty', title: t('Changes not committed yet') }, tn(c.uncommitted, '{n} uncommitted', '{n} uncommitted')));
    } else if (w.error) {
      meta.push(h('span', { class: 'danger', title: w.error }, t('can’t compare')));
    }
    const repo = w.repo.split('/').slice(-2).join('/');
    return h('li', row,
      h('button', { ...main, title: `${w.dir}\n${repo}${w.branch ? ` · ${w.branch}` : ''}${w.base ? `\n${t('Compared with {base}', { base: w.base })}` : ''}\n${t('Show its changes')}` },
        h('span', { class: 'info-row-icon' }, icon('branch')),
        h('span', { class: 'info-row-title' }, w.dir),
        h('span', { class: 'info-row-meta' }, meta)),
      h('div', { class: 'info-row-actions' }, filesButton));
  }

  function pullState(pull, tone) {
    const label = { merged: t('merged'), closed: t('closed'), draft: t('draft') }[tone];
    return label ? h('span', { class: 'info-pull-state', 'data-pull': tone }, label) : null;
  }

  // checksEl sums the CI checks of a pull request's head: all passed, how
  // many failed, or how many are done while some still run.
  function checksEl(c) {
    const n = (v) => formatNumber(v);
    const [tone, glyph, text, title] = c.failed
      ? ['failed', icon('x'), t('{failed}/{total} failed', { failed: n(c.failed), total: n(c.total) }), t('{failed} of {total} checks failed', { failed: n(c.failed), total: n(c.total) })]
      : c.pending
        ? ['pending', icon('record', 'mds-icon info-checks-dot'), t('{done}/{total} done', { done: n(c.passed + c.failed), total: n(c.total) }), t('{pending} of {total} checks still running', { pending: n(c.pending), total: n(c.total) })]
        : ['passed', icon('check'), tn(c.total, '{n} check', '{n} checks'), t('All {total} checks passed', { total: n(c.total) })];
    return h('span', { class: 'info-checks', 'data-checks': tone, title, 'aria-label': title }, glyph, text);
  }

  // ---------- Storage ----------

  const KINDS = {
    knowledge: 'Knowledge',
    sessions: 'Conversations',
    yesman: 'yesman’s own files',
    other: 'Other files',
  };

  // Storage folds into one row with the total, unless opened; open, it
  // lists the largest parts and sums the rest.
  const STORAGE_ROWS = 5;
  let storageOpen = localStorage.getItem('yesman.info.storage') === 'open';

  function storage(y, d) {
    const u = d.usage;
    // Worktrees take the accent in steps; the rest keep a tone of their own.
    let nth = 0;
    const all = (u?.parts ?? []).map((p) => ({ ...p, tone: p.kind === 'worktree' ? `worktree-${Math.min(nth++, 3)}` : p.kind }));
    const top = all.length > STORAGE_ROWS + 1 ? all.slice(0, STORAGE_ROWS) : all;
    const rest = all.slice(top.length);
    const parts = rest.length
      ? [...top, {
        kind: 'rest', tone: 'rest', name: tn(rest.length, '{n} more', '{n} more'), path: rest.map((p) => label(p)).join('\n'),
        bytes: rest.reduce((n, p) => n + p.bytes, 0), files: rest.reduce((n, p) => n + p.files, 0),
      }]
      : top;
    const whole = u?.bytes || 1;
    const meter = (cls) => h('div', { class: cls, role: 'img', 'aria-label': t('{size} in all', { size: formatSize(u.bytes) }) },
      parts.filter((p) => p.bytes > 0).map((p) => h('span', { 'data-tone': p.tone, style: `flex-grow: ${p.bytes / whole}`, title: `${label(p)}: ${formatSize(p.bytes)}` })));
    const toggle = h('button', {
      class: 'info-link info-fold', type: 'button', 'aria-expanded': String(storageOpen),
      title: storageOpen ? t('Fold the storage') : t('Show what takes the space'),
      onclick: () => {
        storageOpen = !storageOpen;
        localStorage.setItem('yesman.info.storage', storageOpen ? 'open' : 'closed');
        render();
      },
    },
    h('span', { class: 'info-link-label' }, t('Storage')),
    icon('chevron', 'mds-icon info-chevron'),
    h('span', { class: 'info-aside' },
      u ? formatSize(u.bytes) : d.error.usage ? h('span', { class: 'danger', title: d.error.usage }, t('can’t measure')) : t('Measuring…'),
      u && !storageOpen ? meter('info-meter info-meter--mini') : null));
    if (!storageOpen) return h('section', { class: 'info-section info-section--link' }, toggle);
    const refresh = refreshButton(t('Measure again'), d.busy.usage, () => load(y.name, 'usage', true));
    const head = h('div', { class: 'info-fold-head' }, toggle, refresh);
    if (!u) {
      return h('section', { class: 'info-section info-section--link' }, head, d.error.usage
        ? h('p', { class: 'info-note danger' }, d.error.usage)
        : h('p', { class: 'info-note' }, t('Measuring…')));
    }
    const rows = h('ul', { class: 'info-usage', role: 'list' }, parts.map((p) => h('li', { title: `${p.path}\n${tn(p.files, '{n} file', '{n} files')}` },
      h('i', { class: 'info-swatch', 'data-tone': p.tone, 'aria-hidden': 'true' }),
      h('span', { class: 'info-usage-label' }, label(p)),
      h('span', { class: 'info-usage-size' }, formatSize(p.bytes)))));
    return h('section', { class: 'info-section info-section--link' }, head,
      h('div', { class: 'info-fold-body' },
        h('p', { class: 'info-total' }, u.partial
          ? t('At least, in {files}', { files: tn(u.files, '{n} file', '{n} files') })
          : tn(u.files, '{n} file', '{n} files')),
        meter('info-meter'), rows,
        h('p', { class: 'info-foot' }, t('Git history stays in the shared repo pool and is not counted here.'))));
  }

  const label = (p) => (p.kind === 'worktree' || p.kind === 'rest' ? p.name : t(KINDS[p.kind] ?? p.kind));

  // worktrees lists a yesman's worktrees as the panel last read them, with
  // what each changed and its pull request; else as the sidebar has them.
  function listWorktrees(y) {
    const d = data.get(y.name);
    if (d?.info) return d.info.worktrees.map((w) => ({ ...w, pull: d.pulls?.[w.dir]?.pull ?? null }));
    return (y.worktrees ?? []).map((w) => ({ ...w, dir: (w.dir ?? '').replace(/\\/g, '/') }));
  }

  return { render, onChanged, onFiles, onAgentEvent, onTopics, worktrees: listWorktrees };
}
