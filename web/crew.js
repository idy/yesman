// Settings › yesman: what new yesmen run as, and the crew: the models,
// each at one effort, that yesman runs agents as. Show the same scores the
// lead weighs when choosing a model for a worker, in their original units.
import { $, h, icon } from './dom.js';
import { openDialog } from './mds/js/magpie.js';
import { t, tNodes, locale, formatNumber } from './i18n.js';

const EFFORTS = { off: 'Off', minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' };
const effortName = (e) => (e ? t(EFFORTS[e] ?? e) : t('Default effort'));
// pi names a model with where it is served: "Kimi K3.1 Pro · China Magpie".
const shortName = (name, id) => (name ?? '').split(' · ')[0] || id;
const optionName = (m) => `${shortName(m.modelName, m.model)} · ${effortName(m.effort)}`;
const errorText = (err) => String(err?.message || err?.error || err);

// The figures the user may give for an option, as Artificial Analysis
// measures them; intelligence alone is needed.
const FIELDS = [
  { key: 'intelligence', label: 'Intelligence', unit: null, max: 100, required: true },
  { key: 'speed', label: 'Speed', unit: 'tok/s' },
  { key: 'coding', label: 'Coding', unit: '%', max: 100 },
  { key: 'costPerTask', label: 'Cost per task', unit: 'USD' },
  { key: 'minutesPerTask', label: 'Time per task', unit: 'min' },
];

export function createCrew({ call, fail, shown, rerender, openPage, preferenceRow }) {
  let view = null; // Yesman.Crew
  let loading = null; // the load under way
  let refreshing = false;
  let busy = false; // a change is being saved
  let revision = 0; // a saved change makes an earlier read obsolete

  function load(refresh = false) {
    if (loading && !refresh) return loading;
    refreshing = refresh;
    const readRevision = revision;
    const p = call(refresh ? 'RefreshCrew' : 'Crew').then((v) => {
      if (loading === p && revision === readRevision) view = v;
    }, fail).finally(() => {
      if (loading === p) { loading = null; refreshing = false; }
      if (shown()) rerender();
    });
    loading = p;
    if (refresh && shown()) rerender();
    return p;
  }

  // Invalidate reads both before and after saving: a pending catalog refresh
  // must not restore a removed option or an earlier estimate.
  async function save(method, ...args) {
    revision++;
    const next = await call(method, ...args);
    revision++;
    view = next;
  }

  async function change(method, ...args) {
    busy = true;
    if (shown()) rerender();
    try {
      await save(method, ...args);
      return true;
    } catch (err) {
      fail(err);
      return false;
    } finally {
      busy = false;
      if (shown()) rerender();
    }
  }

  const yesmanMember = () => view?.members.find((m) => m.key === view.yesman && !m.missing);

  // describe says, beside a form that hires, what the new yesman runs as.
  function describe(el) {
    if (!view) { el.replaceChildren(); return; }
    const settings = (label) => h('button', { class: 'mds-link-button inline-link', type: 'button', onclick: () => { el.closest('dialog')?.close('cancel'); openPage(); } }, label);
    const m = yesmanMember();
    el.replaceChildren(...(m
      ? tNodes('Its lead runs as {model}. {change}', { model: h('strong', null, optionName(m)), change: settings(t('Change')) })
      : tNodes('Choose what new yesmen run as in {settings} first.', { settings: settings(t('Settings › yesman')) })));
  }

  // ---------- Adding an option, and the user's own scores ----------

  function scoreFields(score) {
    const inputs = FIELDS.map((f) => h('input', {
      class: 'mds-input', type: 'number', name: f.key, min: 0, max: f.max, step: 'any', inputmode: 'decimal',
      value: score?.[f.key] ?? null, required: f.required, 'aria-describedby': f.key === 'intelligence' ? 'crew-intelligence-hint' : null,
    }));
    const el = h('div', { class: 'crew-scores' }, FIELDS.map((f, i) => h('label', { class: 'field' },
      h('span', null, t(f.label), f.unit ? h('span', { class: 'mds-optional' }, f.unit) : null), inputs[i])),
    h('p', { class: 'mds-hint', id: 'crew-intelligence-hint' },
      t('Intelligence is on Artificial Analysis’s Intelligence Index, where the smartest model it scored is at {top}. Cost and time are per task of that index.',
        { top: formatNumber(view.top, { maximumFractionDigits: 1 }) })));
    return {
      el,
      // Hidden fields are disabled too, so that a value left in them, out
      // of range, does not hold back the form with nothing to show why.
      need(on) {
        el.hidden = !on;
        for (const input of inputs) input.disabled = !on;
      },
      read() {
        const out = {};
        FIELDS.forEach((f, i) => { if (inputs[i].value !== '') out[f.key] = Number(inputs[i].value); });
        return out;
      },
    };
  }

  // openOption adds an option to the crew, or, given a member, changes the
  // user's own scores for it.
  async function openOption(member) {
    const dialog = $('#crew-dialog');
    const scores = scoreFields(member?.score);
    const error = h('p', { class: 'danger', role: 'alert', hidden: true });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, member ? t('Save') : t('Add'));
    const lead = h('p', { class: 'dialog-hint' });
    let pick = null; // the model and effort chosen, when adding
    const body = [];
    if (member) {
      lead.textContent = t('Artificial Analysis has no record of {model}. Give your own estimate, so the lead can weigh it against the rest.', { model: optionName(member) });
      scores.need(true);
    } else {
      const inCrew = new Set(view.members.map((m) => m.key));
      const keyOf = (m, level) => `${m.provider}/${m.id}:${level}`;
      const providers = new Set(view.models.map((m) => m.provider));
      const model = h('select', { class: 'mds-select', name: 'model', required: true, 'aria-label': t('Model') });
      const groups = new Map();
      view.models.forEach((m, i) => {
        let parent = model;
        if (providers.size > 1) {
          if (!groups.has(m.provider)) model.append(groups.set(m.provider, h('optgroup', { label: m.provider })).get(m.provider));
          parent = groups.get(m.provider);
        }
        parent.append(h('option', { value: i, title: m.key }, shortName(m.name, m.id)));
      });
      const effort = h('select', { class: 'mds-select', name: 'effort', required: true, 'aria-label': t('Effort') });
      const paintScores = () => {
        const m = view.models[model.value];
        const need = !!effort.value && !m.scored.includes(effort.value);
        lead.textContent = need
          ? t('Artificial Analysis has no record of this model at this effort. Give your own estimate, so the lead can weigh it against the rest.')
          : t('Its lead weighs Artificial Analysis’s scores for it when it chooses a model for a worker.');
        scores.need(need);
        submit.disabled = !effort.value;
        pick = { m, effort: effort.value };
      };
      const paintEfforts = () => {
        const m = view.models[model.value];
        effort.replaceChildren(...m.levels.map((level) => {
          const taken = inCrew.has(keyOf(m, level));
          return h('option', { value: level, disabled: taken }, taken ? t('{effort}, added already', { effort: effortName(level) }) : effortName(level));
        }));
        effort.value = m.levels.find((level) => !inCrew.has(keyOf(m, level))) ?? '';
        paintScores();
      };
      model.addEventListener('change', paintEfforts);
      effort.addEventListener('change', paintScores);
      // Start at the first model with an effort not yet in the crew.
      const first = view.models.findIndex((m) => m.levels.some((level) => !inCrew.has(keyOf(m, level))));
      model.value = String(Math.max(0, first));
      body.push(h('div', { class: 'crew-pick' },
        h('label', { class: 'field' }, h('span', null, t('Model')), model),
        h('label', { class: 'field' }, h('span', null, t('Effort')), effort)));
      paintEfforts();
    }
    const form = h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        submit.disabled = true;
        error.hidden = true;
        try {
          const score = scores.el.hidden ? null : scores.read();
          if (member) await save('SetCrewScore', member.key, score);
          else await save('AddCrewOption', pick.m.provider, pick.m.id, pick.effort, score);
          dialog.close('ok');
          if (shown()) rerender();
        } catch (err) {
          error.textContent = errorText(err);
          error.hidden = false;
          submit.disabled = false;
        }
      },
    },
    h('h2', { class: 'mds-heading' }, member ? t('Your scores') : t('Add a model')),
    lead, ...body, scores.el, error,
    h('div', { class: 'mds-dialog-actions' },
      h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close('cancel') }, t('Cancel')), submit));
    dialog.replaceChildren(form);
    const answer = openDialog(dialog);
    form.querySelector('select, input')?.focus();
    return answer;
  }

  // ---------- The page ----------

  function memberScores(m) {
    if (!m.score) return null;
    return h('dl', { class: 'crew-metrics', role: 'group', 'aria-label': t('Scores for {model}', { model: optionName(m) }) }, FIELDS.map((f) => {
      const value = m.score[f.key];
      const available = typeof value === 'number' && Number.isFinite(value);
      const formatted = available ? `${formatNumber(value, { maximumSignificantDigits: 4 })}${f.unit ? ` ${f.unit}` : ''}` : null;
      // The source records a zero, but carries no confirmation of actual zero elapsed time.
      const sourceZeroTime = m.source === 'aa' && f.key === 'minutesPerTask' && value === 0;
      return h('div', null, h('dt', null, t(f.label)), h('dd', null, available
        ? sourceZeroTime ? t('{value} (source record; actual time unverified)', { value: formatted }) : formatted
        : [h('span', { 'aria-hidden': 'true' }, '—'), h('span', { class: 'mds-visually-hidden' }, t('Not available'))]));
    }));
  }

  function memberRow(m) {
    let status;
    if (m.source === 'aa') status = t('Scored by Artificial Analysis');
    else if (m.source === 'user') status = t('Your estimate');
    else status = h('span', { class: 'danger' }, t('No scores: give yours'));
    const name = optionName(m);
    return h('div', { class: 'card-row crew-row' },
      h('div', { class: 'card-row-text' }, h('strong', { title: m.key }, name),
        h('span', null, `${m.provider}/${m.model} · `, status,
          m.source === 'aa' && m.score && !m.score.effort ? ` · ${t('Effort not specified by source')}` : null,
          m.missing ? h('span', { class: 'danger' }, ` · ${t('pi no longer has this model at this effort')}`) : null)),
      m.key === view.yesman ? h('span', { class: 'mds-pill crew-default' }, t('New yesmen')) : h('span'),
      h('div', { class: 'card-row-actions' },
        m.source !== 'aa' ? h('button', { class: 'mds-icon-button', type: 'button', disabled: busy, title: t('Your scores'), 'aria-label': t('Your scores for {model}', { model: name }), onclick: () => openOption(m) }, icon('sliders')) : null,
        h('button', { class: 'mds-icon-button', type: 'button', disabled: busy, title: t('Remove'), 'aria-label': t('Remove {model}', { model: name }), onclick: () => change('RemoveCrewOption', m.key) }, icon('trash'))),
      memberScores(m));
  }

  function renderPage(inner) {
    if (!view) {
      inner.append(h('p', { class: 'toolbar-lead' }, t('Reading pi’s models…')));
      return;
    }
    const usable = view.members.filter((m) => !m.missing);
    const yesman = h('select', { class: 'mds-select', 'aria-label': t('New yesmen run as'), disabled: busy || !usable.length, onchange: () => change('SetYesmanModel', yesman.value) },
      usable.map((m) => h('option', { value: m.key, title: m.key }, optionName(m))));
    if (!yesmanMember()) yesman.prepend(h('option', { value: '', disabled: true }, usable.length ? t('Choose a model') : t('Add a model first')));
    yesman.value = yesmanMember()?.key ?? '';
    const date = new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeZone: 'UTC' }).format(new Date(`${view.date}T00:00:00Z`));
    const source = h('button', { class: 'mds-link-button inline-link', type: 'button', onclick: () => call('OpenURL', view.source).catch(fail) }, 'Artificial Analysis');
    const add = h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: busy || refreshing || !view.models.length, onclick: () => openOption() }, icon('plus'), t('Add a model'));
    const refresh = h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: busy || refreshing,
      title: t('pi reads its models when it starts: read them again after changing pi’s configuration.'), onclick: () => load(true) },
    refreshing ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'), t('Read pi’s models again'));
    inner.append(h('div', { class: 'crew-page' },
      view.error ? h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' },
        h('div', null, h('strong', null, t('Could not read pi’s models')), h('p', null, view.error))) : null,
      h('section', { class: 'settings-section' },
        h('div', { class: 'mds-section-heading' }, h('h2', null, t('New yesmen'))),
        h('div', { class: 'mds-group' }, preferenceRow(t('Model'),
          h('p', null, t('A new yesman’s lead runs as this, and so do its workers unless the lead chooses another. You can change an agent’s model while you talk to it.')), yesman))),
      h('section', { class: 'settings-section' },
        h('div', { class: 'mds-section-heading' }, h('h2', null, t('Models')),
          h('p', null, t('The models, each at one effort, that agents run as. The lead chooses one for each worker by how smart, fast and costly it is for the task.'))),
        h('div', { class: 'mds-toolbar crew-toolbar' }, h('div', { class: 'mds-toolbar-actions' }, refresh, add)),
        h('div', { class: 'card-list', 'aria-busy': String(busy) }, view.members.length ? view.members.map(memberRow)
          : h('div', { class: 'card-empty' }, h('strong', null, t('No models yet')), h('span', null, t('Add the models agents may run as. Until there is one, no yesman can be hired and no worker started.'))))),
      h('p', { class: 'crew-source' }, tNodes('Scores from {source}, {date}, found by each model’s name.', { source, date }), ' ',
        t('Intelligence is Artificial Analysis’s Intelligence Index; coding is the mean of its coding evaluations. Cost and time are per task of that index. Missing figures are shown as —.')),
    ));
  }

  return { load, describe, renderPage, view: () => view };
}
