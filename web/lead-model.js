// Edit a workspace lead through the same idle-agent model and effort calls
// as its composer. Open explicitly: browsing Settings need not start a lead.
import { $, h, openModal } from './dom.js';
import { t } from './i18n.js';

export async function editLeadModel({ lead, current, accept, call, effortName, restore }) {
  const dialog = $('#crew-dialog');
  if (dialog.open || !current()) return;
  const title = h('h2', { class: 'mds-heading', id: 'lead-model-title' }, t('Model for {name}', { name: lead.label }));
  const form = h('div', { class: 'lead-model-editor' });
  const status = h('p', { class: 'mds-hint', role: 'status' }, t('Reading pi’s models…'));
  const done = h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close() }, t('Done'));
  form.append(title, status, h('div', { class: 'mds-dialog-actions' }, done));
  dialog.replaceChildren(form);
  dialog.setAttribute('aria-labelledby', title.id);
  const opened = openModal(dialog);
  const active = () => dialog.open && form.parentNode === dialog;
  let models, info;
  const model = h('select', { class: 'mds-select', onchange: () => {
    const m = models[Number(model.value)];
    if (m) void update({ provider: m.provider, model: m.id });
  } });
  const effort = h('select', { class: 'mds-select', onchange: () => update({ effort: effort.value }) });
  const field = (label, input) => h('label', { class: 'field' }, h('span', null, t(label)), input);
  const fields = h('div', { class: 'crew-pick' }, field('Model', model), field('Effort', effort));
  function paint() {
    model.value = String(models.findIndex((m) => m.provider === info.provider && m.id === info.model));
    const levels = info.effortLevels ?? [];
    effort.replaceChildren(...levels.map((l) => h('option', { value: l, selected: l === info.effort }, effortName(l))));
    const idle = info.status === 'idle';
    model.disabled = !idle || !models.length || !current();
    effort.disabled = !idle || levels.length < 2 || !current();
    status.textContent = idle ? t('Changes apply to this lead’s conversation.') : t('Wait for this lead’s run to finish before changing its model or effort.');
  }
  const change = (expected, next) => call('LeadModels', lead.yesman, lead.since, expected, next);
  async function update(next) {
    if (!current() || !active() || done.disabled) return;
    const focused = document.activeElement;
    model.disabled = effort.disabled = done.disabled = true;
    dialog.oncancel = (e) => e.preventDefault();
    try {
      const result = await change({ provider: info.provider, model: info.model, effort: info.effort }, next);
      if (!active() || !current() || result.agent.since !== lead.since) return;
      info = result.agent;
      accept(info);
      if (active()) paint();
    } catch (err) {
      if (active()) { paint(); status.textContent = String(err?.message || err); }
    } finally {
      done.disabled = false;
      if (form.parentNode === dialog) dialog.oncancel = null;
      if (active() && [document.body, dialog].includes(document.activeElement)) focused?.focus();
    }
  }
  void change({}, {}).then((v) => {
    if (!active() || !current() || v.agent.since !== lead.since) return;
    models = v.models; info = v.agent;
    accept(info);
    model.append(...models.map((m, i) => h('option', { value: i }, `${m.provider}/${m.id}`)));
    status.before(fields);
    paint();
    if (document.activeElement === done) model.focus();
  }).catch((err) => { if (active()) status.textContent = String(err?.message || err); });
  await opened;
  if (form.parentNode === dialog) dialog.removeAttribute('aria-labelledby');
  if (document.activeElement === document.body) restore();
}
