// Worker dispatch preferences in Settings › yesman. Edit one pool at a time,
// retaining the opening's revision and draft while the underlying page refreshes.
import { $, h, openModal } from './dom.js';
import { t } from './i18n.js';

export function workerPools({ view, busy, save, load, fail, shown, preferenceRow, optionName }) {
  if (!view.pools) return null;
  const byKey = new Map(view.members.map((m) => [m.key, m]));
  const rows = view.pools.map((pool) => {
    const keys = pool.members ?? [];
    const usable = keys.map((k) => byKey.get(k)).filter((m) => m && !m.missing);
    const summary = usable.length ? usable.map(optionName).join(', ')
      : t(pool.id === 'default' ? 'Uses the current Yes Man’s model and effort' : 'Uses the Default pool');
    const edit = h('button', { type: 'button', class: 'mds-button mds-button--small', disabled: busy,
      'data-pool': pool.id, 'aria-label': t('Edit {label}', { label: t(pool.name) }), onclick: () => editPool(pool) }, t('Edit'));
    return preferenceRow(t(pool.name), h('p', null, summary), edit);
  });
  return h('section', { class: 'settings-section worker-pools' },
    h('div', { class: 'mds-section-heading' }, h('h2', null, t('Worker routing')),
      h('p', null, t('The lead matches each task’s main deliverable to a label, then compares the models in its pool. Unmatched tasks use Default.'))),
    h('div', { class: 'mds-group' }, rows));

  async function editPool(pool) {
    const dialog = $('#crew-dialog');
    if (busy || dialog.open) return;
    const name = t(pool.name);
    const guide = t(pool.guidance);
    const input = h('textarea', { class: 'mds-textarea', name: 'instructions', rows: 3, maxlength: 2000, placeholder: guide, 'aria-describedby': 'pool-guide' });
    input.value = pool.instructions || guide;
    const selected = new Set(pool.members ?? []);
    const choices = view.members.map((m) => {
      const box = h('input', { type: 'checkbox', value: m.key, checked: selected.has(m.key), disabled: m.missing && !selected.has(m.key) });
      return h('label', { class: 'mds-check pool-member' }, box,
        h('span', null, optionName(m), h('small', null, `${m.provider}/${m.model}`, m.missing ? ` · ${t('Unavailable')}` : '')));
    });
    const fields = h('fieldset', { class: 'pool-fields' }, h('label', { class: 'field' }, t('Dispatch instructions'), input),
      h('p', { class: 'mds-hint', id: 'pool-guide' }, t('Leave blank to use the built-in instructions. Up to 1,000 characters.')),
      h('fieldset', { class: 'pool-options' }, h('legend', null, t('Models and efforts')),
        choices.length ? choices : h('p', { class: 'mds-hint' }, t('Add a model under Models to include it here.'))));
    const error = h('p', { class: 'danger', role: 'alert', hidden: true });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, t('Save'));
    const form = h('form', { onsubmit: async (event) => {
      event.preventDefault();
      if (submit.disabled) return;
      const members = Array.from(fields.querySelectorAll('input:checked'), (el) => el.value);
      let message;
      if ([...input.value].length > 1000) message = t('Use at most 1,000 characters of plain text for dispatch instructions.');
      else if (members.length > 16) message = t('Choose up to 16 model and effort combinations per pool.');
      if (message) { error.textContent = message; error.hidden = false; return; }
      const focused = document.activeElement;
      fields.disabled = submit.disabled = true;
      error.hidden = true;
      try {
        if (await save('SetWorkerPool', pool.id, pool.revision, { instructions: input.value === guide ? '' : input.value, members }) && form.parentNode === dialog && dialog.open) dialog.close('ok');
      } catch (err) {
        if (form.parentNode === dialog && dialog.open) { error.textContent = String(err?.message || err); error.hidden = false; }
        else fail(err);
        await load(); // reopening after a conflict must see the winner
      } finally {
        fields.disabled = submit.disabled = false;
        if (form.parentNode === dialog && dialog.open && [document.body, dialog].includes(document.activeElement)) focused?.focus();
        restoreFocus();
      }
    } }, h('h2', { class: 'mds-heading', id: 'pool-title' }, name), fields, error,
    h('div', { class: 'mds-dialog-actions' }, h('button', { type: 'button', class: 'mds-button', onclick: () => dialog.close('cancel') }, t('Cancel')), submit));
    function restoreFocus() {
      // WebKit can finish restoring native dialog focus on the next frame.
      // The original Edit button may have been replaced by the save response.
      requestAnimationFrame(() => {
        if (dialog.open || form.parentNode !== dialog || !shown()) return;
        const active = document.activeElement;
        if (!active || active === document.body || dialog.contains(active)) $(`[data-pool="${pool.id}"]`)?.focus();
      });
    }
    // openOption shares this dialog. Scope its accessible name to this opening.
    dialog.setAttribute('aria-labelledby', 'pool-title');
    dialog.replaceChildren(form);
    const answer = openModal(dialog);
    input.focus();
    await answer;
    if (form.parentNode === dialog) dialog.removeAttribute('aria-labelledby');
    restoreFocus();
  }
}
