// The Tasks pane: a yesman's scheduled tasks (internal/schedule). Each is a
// request for its lead that comes due once, every day, every week or every
// so often, while the app runs. The pane lists them with when each runs
// next and how its last run went; a task opens to its instructions and its
// history, each run with its board task. The Go side owns the schedule: this
// page only shows it and asks for changes.

import { $, h, icon } from './dom.js';
import { t, tn, formatNumber, locale } from './i18n.js';
import { openDialog } from './mds/js/magpie.js';

const RUN_STATUS = {
  sending: 'Sending…',
  sent: 'Sent to the lead',
  queued: 'Queued for the lead',
  failed: 'Not sent',
  unknown: 'Delivery unknown',
  missed: 'Missed',
  skipped: 'Skipped',
  damaged: 'Unreadable record',
  doing: 'In progress',
  blocked: 'Blocked',
  done: 'Done',
  dropped: 'Dropped',
  removed: 'Board task removed',
};
const TONE = { done: 'good', doing: 'accent', sent: 'accent', queued: 'accent', sending: 'accent', blocked: 'warn', missed: 'warn', skipped: 'quiet', failed: 'error', unknown: 'error', damaged: 'error', dropped: 'quiet', removed: 'quiet' };
const KIND = { scheduled: 'Scheduled', manual: 'Run now', catchup: 'Catch-up', missed: 'Missed', skipped: 'Skipped' };

export const localZone = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
};

let zones = null;
function timeZones() {
  if (!zones) {
    try {
      zones = Intl.supportedValuesOf('timeZone');
    } catch {
      zones = [];
    }
    if (!zones.includes('UTC')) zones = ['UTC', ...zones];
  }
  return zones;
}

// A random request ID: one per click, kept for a retry of the same click.
function requestID() {
  const b = new Uint8Array(8);
  crypto.getRandomValues(b);
  return [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
}

function formatter(zone, opts) {
  try {
    return new Intl.DateTimeFormat(locale(), { ...opts, timeZone: zone });
  } catch {
    return new Intl.DateTimeFormat(locale(), opts);
  }
}

// when shows an instant as a date and time in a zone, naming the zone when
// it is not this computer's.
export function when(iso, zone) {
  if (!iso) return '';
  const d = new Date(iso);
  const local = localZone();
  const z = zone || local;
  const opts = { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' };
  if (z !== local) opts.timeZoneName = 'short';
  return formatter(z, opts).format(d);
}

function weekdayName(n, width = 'short') {
  // 2023-01-01 was a Sunday.
  return new Intl.DateTimeFormat(locale(), { weekday: width, timeZone: 'UTC' }).format(new Date(Date.UTC(2023, 0, 1 + n)));
}

function list(items) {
  try {
    return new Intl.ListFormat(locale(), { style: 'short', type: 'conjunction' }).format(items);
  } catch {
    return items.join(', ');
  }
}

// describe says when a plan runs, in words.
export function describe(s) {
  switch (s?.kind) {
    case 'once': return t('Once, on {date} at {time}', { date: s.date, time: s.time });
    case 'daily': return t('Every day at {time}', { time: s.time });
    case 'weekly':
      if ((s.days ?? []).length === 7) return t('Every day at {time}', { time: s.time });
      return t('Every {days} at {time}', { days: list((s.days ?? []).map((d) => weekdayName(d))), time: s.time });
    case 'interval': {
      const m = s.every ?? 0;
      if (m % 1440 === 0) return tn(m / 1440, 'Every day', 'Every {n} days');
      if (m % 60 === 0) return tn(m / 60, 'Every hour', 'Every {n} hours');
      return tn(m, 'Every minute', 'Every {n} minutes');
    }
    default: return t('On an unknown schedule');
  }
}

// createSchedules returns the pane. host connects it to the page:
//   call(method, ...args), fail(err)
//   yesman()               the yesman whose agent is shown, or null
//   isOpen(), setOpen(on)  whether the pane shows
//   openBoardTask(id)      shows a board task in the board pane
//   openLead()             shows the yesman's lead
//   openTaskLog()          opens the Working log in the files pane
//   confirm(title, body, ok)
//   changed()              the summary changed (the info panel shows it)
export function createSchedules(host) {
  const views = new Map(); // yesman name -> { view, error }
  const loading = new Map(); // yesman name -> { again }
  const expanded = new Set(); // `${yesman}/${plan}`
  const running = new Map(); // `${yesman}/${plan}` -> request ID of a Run now in flight
  const retryIDs = new Map(); // `${yesman}/${plan}` -> request ID to reuse after an uncertain failure
  const busy = new Set(); // keys of actions under way, such as `${yesman}/${run}:retry`
  let archivedOpen = false;
  let reveal = null; // `${yesman}/${plan}` to scroll to once shown

  const current = () => (host.isOpen() && host.yesman()) || null;

  async function load(name) {
    const running = loading.get(name);
    if (running) {
      running.again = true;
      return;
    }
    const entry = { again: false };
    loading.set(name, entry);
    try {
      const view = await host.call('Schedules', name);
      views.set(name, { view, error: null });
    } catch (err) {
      views.set(name, { view: views.get(name)?.view ?? null, error: err?.message || String(err) });
    }
    loading.delete(name);
    if (entry.again) return load(name);
    host.changed();
    if (current()?.name === name) render();
  }

  // ---------- Summary, for the info panel ----------

  function summary(y) {
    const v = views.get(y.name);
    if (!v && !loading.has(y.name)) load(y.name);
    if (!v?.view) return { loading: !v, error: v?.error ?? null };
    const plans = v.view.plans.filter((p) => p.state !== 'archived');
    const on = plans.filter((p) => p.state === 'on');
    let next = null;
    let zone = null;
    for (const p of on) {
      if (p.next && (!next || p.next < next)) {
        next = p.next;
        zone = p.timezone;
      }
    }
    const attention = plans.reduce((n, p) => n + p.runs.filter((r) => r.canCatchUp || r.status === 'failed' || r.status === 'unknown').length, 0);
    return { total: plans.length, on: on.length, next, zone, attention, owner: v.view.owner, error: v.view.error || v.error };
  }

  // ---------- Rendering ----------

  function render() {
    const aside = $('#schedules');
    if (!aside) return;
    const y = current();
    aside.hidden = !y;
    aside.setAttribute('aria-label', t('Tasks'));
    document.documentElement.dataset.tasks = y ? 'open' : 'closed';
    if (!y) return;
    const v = views.get(y.name);
    if (!v && !loading.has(y.name)) load(y.name);
    const view = v?.view;
    const scroll = $('.sched-body', aside)?.scrollTop ?? 0;
    const focused = document.activeElement?.closest?.('#schedules [data-key]')?.dataset.key;

    const plans = view?.plans ?? [];
    const active = plans.filter((p) => p.state !== 'archived');
    const archived = plans.filter((p) => p.state === 'archived');
    const on = active.filter((p) => p.state === 'on').length;
    const readOnly = view && !view.owner;
    const body = h('div', { class: 'sched-body' });
    if (!view) {
      body.append(v?.error ? h('p', { class: 'board-empty', 'data-tone': 'error', role: 'alert' }, v.error) : h('p', { class: 'board-empty' }, t('Loading…')));
    } else {
      if (readOnly) body.append(h('p', { class: 'sched-notice', role: 'status' }, icon('lock'), h('span', null, t('Another yesman app is running these tasks, so they are read-only here.'))));
      if (view.error) body.append(h('p', { class: 'sched-notice', 'data-tone': 'error', role: 'alert' }, icon('alert'), h('span', null, view.error)));
      if (v.error) body.append(h('p', { class: 'sched-notice', 'data-tone': 'error', role: 'alert' }, icon('alert'), h('span', null, v.error)));
      if (!active.length) {
        body.append(h('div', { class: 'board-empty' },
          h('strong', null, t('No scheduled tasks yet')),
          h('p', null, t('A scheduled task asks {name} to do something at a time you choose: once, every day, every week or every few hours. It runs while yesman is open, and each run gets its own board task.', { name: y.displayName || y.name })),
          !readOnly && !view.error ? h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => openEditor(y, null) }, icon('plus'), h('span', null, t('New task…'))) : null));
      } else {
        body.append(h('ol', { class: 'sched-list', 'aria-label': t('Scheduled task list') }, active.map((p) => planRow(y, view, p))));
      }
      if (archived.length) {
        const fold = h('details', { class: 'board-done', open: archivedOpen },
          h('summary', null, icon('chevron', 'mds-icon board-fold'), t('Archived {n}', { n: formatNumber(archived.length) })),
          h('ol', { class: 'sched-list' }, archived.map((p) => planRow(y, view, p))));
        fold.addEventListener('toggle', () => { archivedOpen = fold.open; });
        body.append(fold);
      }
      body.append(h('p', { class: 'sched-foot' }, t('Tasks run only while yesman is open and the computer is awake. A time missed while it was closed or asleep is shown here, and you can run it then.')));
    }
    const canAdd = view && view.owner && !view.error;
    aside.replaceChildren(
      h('div', { class: 'files-head drag' },
        h('div', { class: 'sched-heading' },
          h('strong', { class: 'files-title' }, t('Tasks')),
          h('span', { class: 'sched-subtitle' }, t('Scheduled tasks'))),
        on ? h('span', { class: 'board-count' }, tn(on, '{n} on', '{n} on')) : null,
        h('div', { class: 'files-head-actions' },
          canAdd ? h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('New task…'), 'aria-label': t('New task…'), 'data-key': 'new', onclick: () => openEditor(y, null) }, icon('plus')) : null,
          h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Close the tasks'), 'aria-label': t('Close the tasks'), onclick: () => host.setOpen(false) }, icon('x')))),
      body);
    body.scrollTop = scroll;
    if (reveal) {
      const el = $(`#schedules [data-plan="${CSS.escape(reveal)}"]`);
      if (el) {
        el.scrollIntoView({ block: 'nearest' });
        el.querySelector('.sched-toggle')?.focus({ preventScroll: true });
        reveal = null;
      }
    } else if (focused) {
      $(`#schedules [data-key="${CSS.escape(focused)}"]`)?.focus({ preventScroll: true });
    }
  }

  function lastRun(p) {
    return p.runs.find((r) => r.kind !== 'skipped') ?? p.runs[0] ?? null;
  }

  function statusChip(r, short = false) {
    const label = RUN_STATUS[r.status] ? t(RUN_STATUS[r.status]) : r.status;
    const text = r.status === 'missed' && r.count > 1 ? t('{label} ×{n}', { label, n: formatNumber(r.count) }) : label;
    return h('span', { class: 'sched-chip', 'data-tone': TONE[r.status] ?? 'quiet', title: short ? text : null }, text);
  }

  function nextText(p) {
    switch (p.state) {
      case 'off': return t('Off');
      case 'finished': return t('Finished');
      case 'archived': return t('Archived');
      case 'invalid': return t('Can’t run');
    }
    return p.next ? t('Next: {when}', { when: when(p.next, p.timezone) }) : t('Nothing more to run');
  }

  function planRow(y, view, p) {
    const key = `${y.name}/${p.id}`;
    const isExpanded = expanded.has(key);
    const last = lastRun(p);
    const writable = view.owner && !view.error && p.state !== 'archived';
    const zoneNote = p.timezone !== localZone() ? ` · ${p.timezone}` : '';
    const toggle = h('button', {
      class: 'sched-toggle', type: 'button', 'data-key': `${key}:open`, 'aria-expanded': String(isExpanded),
      onclick: () => {
        if (expanded.has(key)) expanded.delete(key);
        else expanded.add(key);
        render();
      },
    },
    icon('clock', 'mds-icon sched-mark'),
    h('span', { class: 'sched-title' }, p.title),
    h('span', { class: 'sched-when' }, describe(p.schedule) + zoneNote),
    h('span', { class: 'sched-next', 'data-state': p.state }, nextText(p)));
    const actions = [];
    if (writable) {
      const sw = h('input', {
        type: 'checkbox', role: 'switch', checked: p.enabled, 'data-key': `${key}:switch`,
        'aria-label': p.enabled ? t('On: {title}. Turn off', { title: p.title }) : t('Off: {title}. Turn on', { title: p.title }),
        disabled: busy.has(`${key}:switch`),
        onchange: (e) => setEnabled(y, p, e.target.checked, e.target),
      });
      actions.push(h('label', { class: 'mds-switch', title: p.enabled ? t('On: turn it off to stop its runs') : t('Off: turn it on to run it at its times') }, sw, h('span', { class: 'mds-switch-track' })));
      const inFlight = running.has(key) || p.sending;
      actions.push(h('button', {
        class: 'mds-icon-button mds-icon-button--compact', type: 'button', 'data-key': `${key}:run`,
        title: inFlight ? t('Sending to the lead…') : t('Run now'), 'aria-label': t('Run {title} now', { title: p.title }),
        disabled: inFlight, 'aria-busy': inFlight ? 'true' : null,
        onclick: () => runNow(y, p),
      }, inFlight ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('play')));
    }
    const row = h('li', { class: 'sched-plan', 'data-state': p.state, 'data-plan': key },
      h('div', { class: 'sched-row' }, toggle,
        h('div', { class: 'sched-row-side' }, last ? statusChip(last, true) : null, actions)));
    if (p.problem && p.state !== 'archived') row.append(h('p', { class: 'sched-problem', role: 'status' }, p.problem));
    if (isExpanded) row.append(details(y, view, p, writable));
    return row;
  }

  function details(y, view, p, writable) {
    const key = `${y.name}/${p.id}`;
    const meta = [p.id, describe(p.schedule), p.timezone];
    if (p.createdBy === 'lead') meta.push(t('Drafted by the lead'));
    return h('div', { class: 'sched-details' },
      h('div', { class: 'sched-instructions' }, h('span', { class: 'board-label' }, t('Instructions')), h('p', null, p.instructions)),
      h('p', { class: 'board-meta' }, meta.join(' · ')),
      p.createdBy === 'lead' && !p.enabled && writable ? h('p', { class: 'sched-hint' }, t('Your lead drafted this task when you asked. It runs only once you turn it on.')) : null,
      writable ? h('div', { class: 'sched-actions' },
        h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': `${key}:edit`, onclick: () => openEditor(y, p) }, icon('pencil'), h('span', null, t('Edit…'))),
        h('button', { class: 'mds-button mds-button--small mds-button--subtle', type: 'button', 'data-key': `${key}:archive`, onclick: () => archive(y, p) }, icon('archive'), h('span', null, t('Archive…'))),
        h('button', { class: 'mds-button mds-button--small mds-button--subtle', type: 'button', onclick: () => host.openTaskLog() }, icon('book'), h('span', null, t('Task log')))) : null,
      history(y, view, p, writable));
  }

  function history(y, view, p, writable) {
    if (!p.runs.length) return h('p', { class: 'sched-empty' }, t('No runs yet.'));
    return h('div', { class: 'sched-history' },
      h('span', { class: 'board-label' }, t('History')),
      h('ol', { class: 'sched-runs', 'aria-label': t('Runs of {title}', { title: p.title }) }, p.runs.map((r) => runRow(y, view, p, r, writable))),
      p.runs.length >= view.limits.history ? h('p', { class: 'sched-hint' }, t('The last {n} runs are kept.', { n: formatNumber(view.limits.history) })) : null);
  }

  function runRow(y, view, p, r, writable) {
    const key = `${y.name}/${r.id}`;
    const at = r.kind === 'missed'
      ? (r.count > 1 ? t('{first} – {last}', { first: when(r.first, p.timezone), last: when(r.last, p.timezone) }) : when(r.first, p.timezone))
      : when(r.at, p.timezone);
    const action = (name, label, iconName, fn) => h('button', {
      class: 'mds-link-button sched-run-action', type: 'button', 'data-key': `${key}:${name}`,
      disabled: busy.has(`${key}:${name}`), onclick: fn,
    }, icon(iconName), h('span', null, label));
    const acts = [];
    if (writable && view.owner) {
      if (r.canCatchUp) acts.push(action('catchup', t('Run it now'), 'play', () => catchUp(y, p, r)));
      if (r.canRetry) acts.push(action('retry', t('Send again'), 'reload', () => retry(y, r)));
      if (r.canCheck && r.status === 'unknown') acts.push(action('check', t('Check again'), 'reload', () => check(y, r)));
      if (r.canRunAgain) acts.push(action('again', t('Run again as a new run'), 'play', () => runNow(y, p, true)));
    }
    if (r.board && r.task) {
      acts.push(h('button', { class: 'board-owner', type: 'button', title: t('Board task {id}: {title}', { id: r.board, title: r.task.title }), onclick: () => host.openBoardTask(r.board) }, r.board));
    } else if (r.board) {
      acts.push(h('span', { class: 'board-owner', 'data-gone': 'true', title: t('Its board task {id} was removed', { id: r.board }) }, r.board));
    }
    if (r.kind !== 'missed' && r.kind !== 'skipped' && r.attempts?.length) {
      acts.push(h('button', { class: 'mds-link-button sched-run-action', type: 'button', onclick: () => host.openLead() }, icon('arrowLeft'), h('span', null, t('Conversation'))));
    }
    return h('li', { class: 'sched-run', 'data-status': r.status },
      h('div', { class: 'sched-run-line' },
        h('span', { class: 'sched-run-time' }, at),
        h('span', { class: 'sched-run-kind' }, t(KIND[r.kind] ?? r.kind)),
        statusChip(r)),
      r.reason ? h('p', { class: 'sched-run-reason' }, reasonText(r)) : null,
      r.outcome?.note && (r.status === 'done' || r.status === 'blocked' || r.status === 'dropped') ? h('p', { class: 'sched-run-reason' }, r.outcome.note) : null,
      acts.length ? h('div', { class: 'sched-run-actions' }, acts) : null);
  }

  // Reasons come from the Go side in English; the common ones read in the
  // user's language.
  function reasonText(r) {
    if (r.kind === 'missed') return r.reason.startsWith('yesman stopped') ? t('yesman stopped before this run started.') : t('yesman was not running, or the computer was asleep, when it was due.');
    if (r.kind === 'skipped') return r.prev && r.reason.includes('board task') ? t('Skipped: its last run (board task {id}) was still open.', { id: r.reason.match(/board task (t\d+)/)?.[1] ?? '' }) : t('Skipped: its last run was still open.');
    return r.reason;
  }

  // ---------- Actions ----------

  async function setEnabled(y, p, on, input) {
    const key = `${y.name}/${p.id}`;
    busy.add(`${key}:switch`);
    input.disabled = true;
    try {
      const res = await host.call('SetScheduleEnabled', y.name, p.id, on, p.revision);
      if (res.error) host.fail(res.error);
    } catch (err) {
      host.fail(err);
    } finally {
      busy.delete(`${key}:switch`);
      await load(y.name);
    }
  }

  async function runNow(y, p, again = false) {
    const key = `${y.name}/${p.id}`;
    if (running.has(key)) return;
    if (p.open && !again) {
      const prev = p.runs.find((r) => r.id === p.open);
      const ok = await host.confirm(t('Run {title} again?', { title: p.title }),
        prev?.board ? t('Its last run, board task {id}, is still open. Running it now sends the lead another request with a board task of its own.', { id: prev.board }) : t('Its last run is still open. Running it now sends the lead another request with a board task of its own.'),
        t('Run now'));
      if (!ok) return;
    } else if (again) {
      const ok = await host.confirm(t('Run {title} as a new run?', { title: p.title }),
        t('yesman cannot tell whether the earlier run reached the lead. A new run sends the request again with a board task of its own, so the lead may get it twice.'),
        t('Run again'));
      if (!ok) return;
    }
    const id = retryIDs.get(key) ?? requestID();
    running.set(key, id);
    render();
    try {
      await host.call('RunSchedule', y.name, p.id, id, '');
      retryIDs.delete(key);
    } catch (err) {
      // The same click may have gone through: trying again reuses its ID.
      retryIDs.set(key, id);
      host.fail(err);
    } finally {
      running.delete(key);
      await load(y.name);
    }
  }

  async function catchUp(y, p, r) {
    const key = `${y.name}/${r.id}:catchup`;
    if (busy.has(key)) return;
    busy.add(key);
    render();
    try {
      await host.call('RunSchedule', y.name, p.id, retryIDs.get(key) ?? requestID(), r.id);
    } catch (err) {
      host.fail(err);
    } finally {
      busy.delete(key);
      await load(y.name);
    }
  }

  async function retry(y, r) {
    const key = `${y.name}/${r.id}:retry`;
    if (busy.has(key)) return;
    busy.add(key);
    render();
    try {
      await host.call('RetryScheduleRun', y.name, r.id);
    } catch (err) {
      host.fail(err);
    } finally {
      busy.delete(key);
      await load(y.name);
    }
  }

  async function check(y, r) {
    const key = `${y.name}/${r.id}:check`;
    if (busy.has(key)) return;
    busy.add(key);
    render();
    try {
      await host.call('CheckScheduleRun', y.name, r.id);
    } catch (err) {
      host.fail(err);
    } finally {
      busy.delete(key);
      await load(y.name);
    }
  }

  async function archive(y, p) {
    if (!(await host.confirm(t('Archive {title}?', { title: p.title }), t('It will not run again. Its history stays, under Archived. A run already on its way goes on.'), t('Archive')))) return;
    try {
      await host.call('ArchiveSchedule', y.name, p.id, p.revision);
    } catch (err) {
      host.fail(err);
    }
    await load(y.name);
  }

  // ---------- The editor ----------

  let session = 0; // the dialog open now; late answers of an older one are ignored

  function openEditor(y, p) {
    const dialog = $('#schedule-dialog');
    const mine = ++session;
    const view = views.get(y.name)?.view;
    const limits = view?.limits ?? { title: 120, instructions: 4000, minEvery: 15, maxEvery: 10080 };
    const s = p?.schedule ?? { kind: 'daily', time: '09:00' };
    const today = new Date();
    const pad = (n) => String(n).padStart(2, '0');
    const isoDay = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    let saving = false;

    const field = (label, control, hint, name) => h('label', { class: 'field', 'data-field': name },
      h('span', null, label), control, hint ? h('span', { class: 'dialog-hint' }, hint) : null);
    const title = h('input', { class: 'mds-input', name: 'title', required: true, maxlength: String(limits.title), autocomplete: 'off', value: p?.title ?? '' });
    const instructions = h('textarea', { class: 'mds-textarea', name: 'instructions', required: true, rows: '6', maxlength: String(limits.instructions) });
    instructions.value = p?.instructions ?? '';
    const kind = h('select', { class: 'mds-select', name: 'kind' },
      [['once', t('Once')], ['daily', t('Every day')], ['weekly', t('Every week')], ['interval', t('Every few minutes, hours or days')]].map(([v, l]) => h('option', { value: v, selected: s.kind === v }, l)));
    const date = h('input', { class: 'mds-input', type: 'date', name: 'date', value: s.date ?? isoDay(new Date(today.getTime() + 86400000)) });
    const time = h('input', { class: 'mds-input', type: 'time', name: 'time', value: s.time ?? '09:00' });
    const days = h('div', { class: 'sched-days', role: 'group', 'aria-label': t('Days of the week') },
      [1, 2, 3, 4, 5, 6, 0].map((d) => h('label', { class: 'mds-check' },
        h('input', { type: 'checkbox', value: String(d), checked: (s.days ?? [1, 2, 3, 4, 5]).includes(d) }), weekdayName(d))));
    const minutes = s.every ?? 60;
    const unitOf = minutes % 1440 === 0 ? 1440 : minutes % 60 === 0 ? 60 : 1;
    const every = h('input', { class: 'mds-input', type: 'number', name: 'every', min: '1', step: '1', value: String(minutes / unitOf) });
    const unit = h('select', { class: 'mds-select', name: 'unit', 'aria-label': t('Unit') },
      [[1, t('minutes')], [60, t('hours')], [1440, t('days')]].map(([v, l]) => h('option', { value: String(v), selected: unitOf === v }, l)));
    const zone = h('input', { class: 'mds-input', name: 'timezone', list: 'schedule-zones', autocomplete: 'off', spellcheck: 'false', value: p?.timezone ?? localZone() });
    const zoneList = h('datalist', { id: 'schedule-zones' }, timeZones().map((z) => h('option', { value: z })));
    const enabled = h('input', { type: 'checkbox', name: 'enabled', checked: p ? p.enabled : true });
    const error = h('p', { class: 'dialog-hint sched-error', role: 'alert', hidden: true });
    const latest = h('button', { class: 'mds-link-button', type: 'button', hidden: true }, t('Use the latest version'));
    const cancel = h('button', { class: 'mds-button', type: 'button' }, t('Cancel'));
    const save = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, p ? t('Save') : t('Create'));

    const dateField = field(t('Date'), date, null, 'date');
    const timeField = field(t('Time'), time, null, 'time');
    const weekRow = h('div', { class: 'sched-form-days', 'data-field': 'days' }, h('span', null, t('On')), days);
    const intervalRow = h('div', { class: 'sched-form-row', 'data-field': 'every' },
      h('label', { class: 'field' }, h('span', null, t('Every')), h('span', { class: 'sched-every' }, every, unit)));
    const whenFields = h('div', { class: 'sched-when-fields' });
    const row = (...els) => h('div', { class: 'sched-form-row' }, ...els);
    const showKind = () => {
      const k = kind.value;
      whenFields.replaceChildren(...(k === 'once' ? [row(dateField, timeField)] : k === 'weekly' ? [weekRow, row(timeField)] : k === 'interval' ? [intervalRow] : [row(timeField)]));
    };
    kind.onchange = () => { clearError(); showKind(); };

    const form = h('form', { class: 'sched-form', novalidate: true },
      h('h2', { class: 'mds-heading', id: 'schedule-dialog-title' }, p ? t('Edit task') : t('New scheduled task')),
      field(t('Title'), title, null, 'title'),
      field(t('Instructions'), instructions, t('What your yesman is asked to do each time. Its lead handles it like a message from you, with the same permissions; nothing extra is allowed.'), 'instructions'),
      field(t('Repeat'), kind, null, 'kind'),
      whenFields,
      field(t('Time zone'), zone, t('Times are in this time zone. This computer’s is {zone}.', { zone: localZone() }), 'timezone'),
      zoneList,
      h('label', { class: 'mds-check sched-enabled' }, enabled, t('Turn it on: run it at these times')),
      error, latest,
      h('div', { class: 'mds-dialog-actions' }, cancel, save));
    showKind();

    function clearError() {
      error.hidden = true;
      latest.hidden = true;
      for (const el of form.querySelectorAll('[aria-invalid]')) el.removeAttribute('aria-invalid');
    }
    function showError(message, name) {
      error.textContent = message;
      error.hidden = false;
      const box = name ? form.querySelector(`[data-field="${CSS.escape(name)}"]`) : null;
      const control = box?.querySelector('input, select, textarea');
      if (control) {
        control.setAttribute('aria-invalid', 'true');
        control.setAttribute('aria-describedby', 'schedule-error');
        control.focus();
      }
    }
    error.id = 'schedule-error';
    for (const el of [title, instructions, date, time, every, zone]) el.addEventListener('input', clearError);

    function input() {
      const k = kind.value;
      const schedule = { kind: k };
      if (k === 'once') Object.assign(schedule, { date: date.value, time: time.value });
      if (k === 'daily') schedule.time = time.value;
      if (k === 'weekly') Object.assign(schedule, { time: time.value, days: [...days.querySelectorAll('input:checked')].map((c) => Number(c.value)) });
      if (k === 'interval') schedule.every = Math.round(Number(every.value) * Number(unit.value));
      return { id: p?.id ?? '', revision: p?.revision ?? 0, title: title.value, instructions: instructions.value, schedule, timezone: zone.value.trim(), enabled: enabled.checked };
    }

    // The page checks what it can; Go checks everything again.
    function validate(inp) {
      if (!inp.title.trim()) return [t('Give the task a title.'), 'title'];
      if (!inp.instructions.trim()) return [t('Say what yesman should do.'), 'instructions'];
      if (inp.schedule.kind === 'once' && !inp.schedule.date) return [t('Choose a date.'), 'date'];
      if (inp.schedule.kind !== 'interval' && !inp.schedule.time) return [t('Choose a time.'), 'time'];
      if (inp.schedule.kind === 'weekly' && !inp.schedule.days.length) return [t('Choose at least one day of the week.'), 'days'];
      if (inp.schedule.kind === 'interval' && !(inp.schedule.every >= limits.minEvery && inp.schedule.every <= limits.maxEvery)) return [t('Run it every 15 minutes to every 7 days.'), 'every'];
      if (!inp.timezone) return [t('Choose a time zone such as Asia/Shanghai or America/New_York.'), 'timezone'];
      return null;
    }

    let latestPlan = null;
    latest.onclick = async () => {
      if (!latestPlan) return;
      close('reload');
      openEditor(y, latestPlan);
    };

    form.onsubmit = async (e) => {
      e.preventDefault();
      if (saving) return;
      const inp = input();
      const bad = validate(inp);
      if (bad) {
        showError(...bad);
        return;
      }
      saving = true;
      for (const el of [save, cancel]) el.disabled = true;
      form.setAttribute('aria-busy', 'true');
      try {
        const res = await host.call('SaveSchedule', y.name, inp);
        if (mine !== session) return;
        if (res.plan) {
          expanded.add(`${y.name}/${res.plan.id}`);
          reveal = `${y.name}/${res.plan.id}`;
          close('save');
          load(y.name);
          return;
        }
        if (res.conflict) {
          await load(y.name);
          latestPlan = views.get(y.name)?.view?.plans.find((x) => x.id === p?.id) ?? null;
          showError(t('This task was changed since you opened it. Your text is kept here; use the latest version to start again from it.'));
          latest.hidden = !latestPlan;
          return;
        }
        showError(translateError(res.error), res.field);
      } catch (err) {
        if (mine !== session) return;
        showError(err?.message || String(err));
      } finally {
        if (mine === session) {
          saving = false;
          for (const el of [save, cancel]) el.disabled = false;
          form.removeAttribute('aria-busy');
        }
      }
    };

    function close(value) {
      if (dialog.open) dialog.close(value);
    }
    cancel.onclick = () => close('cancel');
    dialog.oncancel = (e) => { if (saving) e.preventDefault(); };
    dialog.replaceChildren(form);
    const done = openDialog(dialog);
    title.focus();
    done.then(() => {
      if (mine === session) {
        dialog.oncancel = null;
        dialog.replaceChildren();
      }
    });
  }

  // translateError shows the Go side's field messages in the user's language.
  function translateError(message) {
    return t(message ?? '');
  }

  // ---------- Following changes ----------

  // onChange follows a yesman whose tasks or runs changed.
  function onChange(name) {
    if (views.has(name) || current()?.name === name) load(name);
  }

  // A board change can change a run's status.
  function onBoard(name) {
    if (views.has(name)) load(name);
  }

  // open shows the pane, and a plan when given.
  function open(name, plan) {
    if (plan) {
      expanded.add(`${name}/${plan}`);
      reveal = `${name}/${plan}`;
    }
    host.setOpen(true);
    load(name);
  }

  return { render, onChange, onBoard, open, summary };
}
