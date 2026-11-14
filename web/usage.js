// Settings › Model usage: what agents actually used, read from their pi
// session logs, for each model at each effort. These are measurements of
// this computer's agents, kept apart from the Artificial Analysis benchmark
// scores in Settings › yesman.
import { $, h, icon } from './dom.js';
import { t, tn, tNodes, locale, formatNumber } from './i18n.js';
import { effortName } from './crew.js';

const errorText = (err) => String(err?.message || err?.error || err);
// pi names a model with where it is served: "Kimi K3.1 Pro · China Magpie".
const shortName = (name) => (name ?? '').split(' · ')[0];
const EFFORT_ORDER = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];

const AGENTS = [['workers', 'Workers'], ['leads', 'Leads'], ['all', 'Workers and leads']];
const PERIODS = [['all', 'All kept history'], ['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days']];

// The table's columns: who used a model, what they did with it and how
// fast it answered first, then the tokens in detail, which scroll into view
// in a narrow window. Numbers sort largest first; a missing speed last.
const COLUMNS = [
  { key: 'model', label: 'Model', text: true },
  { key: 'effort', label: 'Effort', text: true },
  { key: 'workers', label: 'Workers', when: (q) => q.agents !== 'leads' },
  { key: 'leads', label: 'Leads', when: (q) => q.agents !== 'workers' },
  { key: 'tasks', label: 'Tasks done' },
  { key: 'speed', label: 'Output speed', unit: 'tok/s' },
  { key: 'total', label: 'Total tokens' },
  { key: 'responses', label: 'Responses' },
  { key: 'input', label: 'Input' },
  { key: 'output', label: 'Output' },
  { key: 'cacheRead', label: 'Cache read' },
  { key: 'cacheWrite', label: 'Cache write' },
];

const sameQuery = (a, b) => !!a && !!b && a.yesman === b.yesman && a.agents === b.agents && a.period === b.period;

export function createUsagePage({ call, shown, openScores }) {
  const query = { yesman: '', agents: 'workers', period: 'all' };
  let view = null; // Yesman.ModelUsage for view.query
  let error = ''; // why the last read failed
  let loading = false;
  let request = 0; // only the latest read is shown
  let sort = { key: 'total', desc: true };

  function load() {
    const id = ++request;
    const asked = { ...query };
    loading = true;
    error = '';
    redraw();
    call('ModelUsage', asked).then((v) => {
      if (id === request) view = v;
    }, (err) => {
      if (id === request) error = errorText(err);
    }).finally(() => {
      if (id !== request) return;
      loading = false;
      redraw();
    });
  }

  function choose(key, value) {
    if (query[key] === value) return;
    query[key] = value;
    load();
  }

  // ---------- Formatting ----------

  const number = (n) => formatNumber(n ?? 0);
  const notRecorded = (label = t('Not recorded')) => [h('span', { 'aria-hidden': 'true' }, '—'), h('span', { class: 'mds-visually-hidden' }, label)];
  const percent = (part, whole) => formatNumber(whole > 0 ? part / whole : 0, { style: 'percent', maximumFractionDigits: 0 });
  const when = (iso) => new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));

  function modelName(r) {
    if (!r.model) return { name: t('Model not recorded'), note: t('Compaction and tool usage') };
    const name = shortName(view.names?.[`${r.provider}/${r.model}`]);
    return name ? { name, id: `${r.provider}/${r.model}` } : { name: r.model, id: r.provider };
  }

  function value(r, key) {
    switch (key) {
      case 'model': return modelName(r).name.toLocaleLowerCase(locale());
      case 'effort': return r.effort ? EFFORT_ORDER.indexOf(r.effort) : 99;
      case 'total': return r.total;
      case 'speed': return r.speed;
      default: return r[key];
    }
  }

  function sorted(rows) {
    const { key, desc } = sort;
    return rows.slice().sort((a, b) => {
      const x = value(a, key), y = value(b, key);
      if (x == null || y == null) return x == null ? (y == null ? 0 : 1) : -1; // missing last, either way
      const c = typeof x === 'string' ? x.localeCompare(y, locale()) : x - y;
      return (desc ? -c : c) || b.total - a.total;
    });
  }

  // A text column starts in alphabetical order, a number largest first.
  function sortBy(key) {
    const text = COLUMNS.find((c) => c.key === key)?.text;
    sort = sort.key === key ? { key, desc: !sort.desc } : { key, desc: !text };
    redraw(key);
  }

  // ---------- The table ----------

  function speedCell(r) {
    if (r.speed == null) return notRecorded(t('Not measured'));
    const shown = formatNumber(r.speed, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
    const partial = r.output > 0 && r.timedOutput < r.output;
    return [h('span', null, shown), partial ? h('span', { class: 'usage-sub' }, t('{share} of output timed', { share: percent(r.timedOutput, r.output) })) : null];
  }

  function cells(r, columns) {
    return columns.slice(2).map((c) => {
      if (c.key === 'speed') return h('td', null, speedCell(r));
      return h('td', null, number(c.key === 'total' ? r.total : r[c.key]));
    });
  }

  function table(columns) {
    const head = h('tr', null, columns.map((c) => {
      const active = sort.key === c.key;
      return h('th', { scope: 'col', 'aria-sort': active ? (sort.desc ? 'descending' : 'ascending') : null, class: c.text ? 'usage-text' : null },
        h('button', { class: 'usage-sort', type: 'button', 'data-sort': c.key, onclick: () => sortBy(c.key) },
          h('span', null, t(c.label), c.unit ? h('span', { class: 'usage-unit' }, c.unit) : null),
          active ? icon(sort.desc ? 'caret-down' : 'up', 'mds-icon usage-sort-icon') : null));
    }));
    const body = sorted(view.rows).map((r) => {
      const m = modelName(r);
      return h('tr', null,
        h('th', { scope: 'row', class: 'usage-model' },
          h('span', { class: 'usage-model-name' }, m.name),
          m.id ? h('span', { class: 'usage-model-id mds-mono' }, m.id) : h('span', { class: 'usage-model-id' }, m.note),
          r.responseModels?.length ? h('span', { class: 'usage-model-id' }, t('Answered as {models}', { models: r.responseModels.join(', ') })) : null),
        h('td', { class: 'usage-text' }, r.effort ? effortName(r.effort) : notRecorded()),
        cells(r, columns));
    });
    const total = { ...view.total, speed: null };
    const foot = h('tr', null,
      h('th', { scope: 'row', class: 'usage-model' }, h('span', { class: 'usage-model-name' }, t('Total'))),
      h('td', { class: 'usage-text' }),
      columns.slice(2).map((c) => {
        if (c.key === 'speed') return h('td', null, notRecorded(t('Speeds of different models are not combined')));
        const n = c.key === 'total' ? total.total : total[c.key];
        const overlap = c.key === 'workers' && view.overlap > 0;
        return h('td', null, number(n), overlap ? h('span', { class: 'usage-sub' }, t('each once')) : null);
      }));
    return h('div', { class: 'usage-scroll', role: 'region', tabindex: '0', 'aria-labelledby': 'usage-caption' },
      h('table', { class: 'usage-table' },
        h('caption', { id: 'usage-caption', class: 'mds-visually-hidden' }, t('Model usage by model and effort')),
        h('thead', null, head), h('tbody', null, body), h('tfoot', null, foot)));
  }

  // ---------- What the figures cover ----------

  function summary() {
    const v = view;
    const item = (label, n) => h('div', null, h('dt', null, label), h('dd', null, number(n)));
    return h('dl', { class: 'usage-summary', 'aria-label': t('Totals') },
      query.agents !== 'leads' ? item(t('Workers'), v.total.workers) : null,
      query.agents !== 'workers' ? item(t('Leads'), v.total.leads) : null,
      item(t('Tasks done'), v.total.tasks),
      item(t('Responses'), v.total.responses),
      item(t('Total tokens'), v.total.total));
  }

  function notes() {
    const v = view;
    const c = v.coverage;
    const tasks = v.tasks;
    const out = [];
    if (c.errors?.length) {
      out.push(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null,
        h('strong', null, t('Some session logs could not be read')),
        h('p', null, t('What could be read is counted; the rest is left out.')),
        h('ul', { class: 'usage-errors' }, c.errors.map((e) => h('li', null, e))))));
    }
    const lines = [];
    if (c.records > 0) {
      lines.push(t('From {from} to {to}, in the session logs of {workspaces}.', {
        from: when(c.from), to: when(c.to), workspaces: tn(c.workspaces, '1 yesman', '{n} yesmen'),
      }));
    }
    if (c.unreadable + c.oversized > 0) lines.push(tn(c.unreadable + c.oversized, '1 log line could not be read and is not counted.', '{n} log lines could not be read and are not counted.'));
    if (v.overlap > 0) lines.push(tn(v.overlap, '1 worker used more than one model or effort: it counts in each of their rows, and once in the total.', '{n} workers used more than one model or effort: each counts in each of their rows, and once in the total.'));
    const unattributed = tasks.done - tasks.attributed;
    if (unattributed > 0) {
      const reasons = [
        [tasks.severalModels, 'its owner used several models, and when it was done is not recorded'],
        [tasks.noResponses, 'its owner has no response to go by'],
        [tasks.unowned, 'no owner'],
        [tasks.unknownOwner, 'owned by an agent that is not known'],
        [tasks.otherRole, query.agents === 'leads' ? 'owned by a worker' : 'owned by a lead'],
      ].filter(([n]) => n > 0).map(([n, why]) => t('{reason}: {n}', { reason: t(why), n: number(n) }));
      lines.push(tn(unattributed, '1 done task is not counted for a model ({reasons}).', '{n} done tasks are not counted for a model ({reasons}).', { reasons: reasons.join(t('; ')) }));
    }
    if (tasks.timeUnknown > 0) lines.push(tn(tasks.timeUnknown, '1 task was done before yesman recorded when tasks are done: it counts only in All kept history.', '{n} tasks were done before yesman recorded when tasks are done: they count only in All kept history.'));
    if (c.noEffort > 0) lines.push(tn(c.noEffort, '1 response has no recorded effort.', '{n} responses have no recorded effort.'));
    if (lines.length) out.push(h('ul', { class: 'usage-notes' }, lines.map((l) => h('li', null, l))));
    return out;
  }

  function method() {
    const points = [
      'Each response counts under the provider, the full model ID and the effort pi recorded for that request, so switching models in a conversation splits its usage. A response without a recorded effort is shown as Not recorded, never as the effort chosen now.',
      'Workers are the distinct workers with usage of a model. Waking a worker again is the same worker; one hired again under a name is another. A worker that used several models is in each of their rows and once in the total. Runs are not counted: session logs do not mark where a run starts.',
      'Tasks done are board tasks marked done now, for the model their owner was using when the task was done. For tasks done before yesman recorded that, a task counts only when its owner used a single model and effort. Reopened and dropped tasks do not count, and a worker finishing a run does not finish a task.',
      'Tokens are pi’s usage of every response, including failed and stopped ones, and of cache warming, compaction and tools. Input does not include cache reads and writes; the total is pi’s own, which adds them; output includes reasoning. Costs are not shown: pi estimates them from its catalog’s prices, not from your bills.',
      'Output speed is the output tokens of completed responses divided by the time they took, from sending the request to the last token: it includes waiting for the first token and reasoning, not tools or idle time. Responses count by their size, not as an average of speeds. Several agents at once do not add up to it.',
      'Only the current yesmen’s logs are read: dismissed yesmen and deleted logs are not included, and what pi did not record is not reconstructed.',
    ];
    return h('details', { class: 'usage-method' },
      h('summary', null, t('How these are counted')),
      h('ul', null, points.map((p) => h('li', null, t(p)))));
  }

  // ---------- The page ----------

  function results() {
    const el = h('div', { id: 'usage-results', class: 'usage-results', 'aria-busy': loading ? 'true' : 'false' });
    const current = sameQuery(view?.query, query) ? view : null;
    if (error) {
      el.append(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null,
        h('strong', null, t('Could not read model usage')), h('p', null, error),
        h('button', { class: 'mds-button mds-button--small usage-retry', type: 'button', onclick: load }, icon('reload'), t('Try again')))));
    }
    if (!current) {
      if (loading) el.append(h('p', { class: 'toolbar-lead usage-loading', role: 'status' }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }), t('Reading session logs…')));
      return el;
    }
    if (!current.rows.length) {
      el.append(h('div', { class: 'card-empty usage-empty' },
        h('strong', null, t('No model usage recorded')),
        h('span', null, query.period === 'all' ? t('Agents’ responses show here once they answer.') : t('No agent answered in this period.'))));
      el.append(...notes());
      return el;
    }
    el.append(summary(), table(COLUMNS.filter((c) => !c.when || c.when(query))), ...notes());
    return el;
  }

  function filters() {
    const select = (key, label, choices) => h('label', { class: 'usage-filter' }, h('span', null, label),
      h('select', { class: 'mds-select', 'data-filter': key, onchange: (e) => choose(key, e.currentTarget.value) },
        choices.map(([v, text]) => h('option', { value: v, selected: query[key] === v }, text))));
    const yesmen = [['', t('All yesmen')], ...(view?.yesmen ?? []).map((y) => [y.name, y.label])];
    if (query.yesman && !yesmen.some(([v]) => v === query.yesman)) yesmen.push([query.yesman, query.yesman]);
    const read = view?.readAt ? t('Read at {time}', { time: new Intl.DateTimeFormat(locale(), { timeStyle: 'short' }).format(new Date(view.readAt)) }) : '';
    return h('div', { class: 'mds-toolbar usage-toolbar', id: 'usage-filters' },
      h('div', { class: 'usage-filters' },
        select('yesman', t('yesman'), yesmen),
        select('agents', t('Agents'), AGENTS.map(([v, l]) => [v, t(l)])),
        select('period', t('Period'), PERIODS.map(([v, l]) => [v, t(l)]))),
      h('div', { class: 'mds-toolbar-actions' },
        h('span', { class: 'usage-read' }, read),
        h('button', { class: 'mds-button mds-button--small', type: 'button', id: 'usage-refresh', disabled: loading, 'aria-busy': loading ? 'true' : null, onclick: load },
          loading ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'), t('Refresh'))));
  }

  // redraw replaces the results and filters in place, keeping focus on the
  // control that had it.
  function redraw(focusSort) {
    if (!shown()) return;
    const old = $('#usage-results');
    if (!old) return;
    const active = document.activeElement;
    const sortKey = focusSort ?? (active?.closest?.('#usage-results') ? active.dataset.sort : undefined);
    const filterKey = active?.closest?.('#usage-filters') ? (active.dataset.filter ?? active.id) : null;
    $('#usage-filters')?.replaceWith(filters());
    old.replaceWith(results());
    if (sortKey) document.querySelector(`#usage-results [data-sort="${sortKey}"]`)?.focus();
    else if (filterKey) document.querySelector(`#usage-filters [data-filter="${filterKey}"], #usage-filters #${filterKey}`)?.focus();
  }

  function render(inner) {
    inner.classList.add('usage-page-inner');
    const scores = h('button', { class: 'mds-link-button inline-link', type: 'button', onclick: openScores }, t('Settings › yesman'));
    inner.append(h('section', { class: 'settings-section usage-page' },
      h('div', { class: 'mds-section-heading' }, h('h2', null, t('Model usage')),
        h('p', null, t('What this computer’s agents actually used, read from their session logs: for each model at each effort, the agents that used it, the tasks done with it, tokens and measured output speed.'), ' ',
          tNodes('Benchmark scores from Artificial Analysis are in {settings}; they are not measured here.', { settings: scores }))),
      filters(), results(), method()));
  }

  return { load, render, view: () => view };
}
