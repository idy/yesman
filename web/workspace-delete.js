import { h, openModal } from './dom.js';
import { t } from './i18n.js';

// One opening owns its target, buttons and async replies. Only the explicit
// button click commits. A new hire or another archive of the name must get
// its own confirmation from the Store.
export function workspaceDeletion({ call, errorText, fail, refresh, toast, section, row }) {
  let opening = null;
  let stored = null;
  let loading = false;
  let loadSequence = 0;
  let listError = '';

  const storageSelector = '#stored-workspaces button[data-storage-key]';
  function storageFocus(button = document.activeElement) {
    if (!button?.matches(storageSelector)) return null;
    const key = button.dataset.storageKey;
    const index = [...document.querySelectorAll(storageSelector)].indexOf(button);
    return () => {
      const items = [...document.querySelectorAll(storageSelector)].filter(el => !el.disabled);
      const next = items.find(el => el.dataset.storageKey === key) ?? items[Math.min(index, items.length - 1)];
      next?.focus({ preventScroll: true });
    };
  }

  function ask(workspace, record = null, restore = storageFocus()) {
    if (opening || (!workspace && !record)) return;
    const name = record?.label || workspace?.displayName || workspace?.name;
    const title = record?.pending ? t('Finish deleting “{name}”?', { name })
      : record?.archived ? t('Delete the archive of “{name}”?', { name }) : t('Delete “{name}”?', { name });
    const location = record ? `${record.archived ? 'archive' : 'yesmen'}/${record.entry}` : `yesmen/${workspace.name}`;
    const body = record?.pending ? t('This permanently removes the remaining files from the interrupted deletion.')
      : record?.archived ? t('This permanently deletes this archived workspace, including its conversations, tasks, Knowledge, Working Logs and files.')
        : t('Its agents stop. Its conversations, tasks, Knowledge, Working Logs and workspace files are permanently deleted.');
    const status = h('p', { class: 'deletion-status', role: 'status', tabindex: '-1' });
    const cancel = h('button', { type: 'button', class: 'mds-button', autofocus: true, onclick: () => dialog.close('cancel') }, t('Cancel'));
    const ok = h('button', { type: 'button', class: 'mds-button delete-permanently', disabled: !record, onclick: commit }, t('Delete permanently'));
    const dialog = h('dialog', { id: 'delete-workspace-dialog', class: 'mds-dialog workspace-deletion', 'aria-labelledby': 'deletion-title', 'aria-describedby': 'deletion-body deletion-location' },
      h('h2', { class: 'mds-heading', id: 'deletion-title' }, title),
      h('p', { class: 'path', id: 'deletion-location' }, location),
      h('p', { id: 'deletion-body' }, body),
      h('p', { class: 'dialog-hint' }, t('The shared repo pool, its branches and tags, and other workspaces are kept.')),
      status, h('div', { class: 'mds-dialog-actions' }, cancel, ok));
    let busy = false;
    let target = record;
    opening = dialog;
    document.body.append(dialog);
    dialog.oncancel = (e) => { if (busy) e.preventDefault(); };
    const closed = openModal(dialog);
    cancel.focus();
    if (!record) {
      status.textContent = t('Checking workspace…');
      call('PrepareWorkspaceDeletion', workspace.name, workspace.hiredAt ?? null).then((result) => {
        if (!dialog.open) return;
        if (result.label !== name) throw Error(t('This workspace was renamed. Reopen its menu.'));
        target = result;
        status.textContent = '';
        ok.disabled = false;
      }).catch(showError);
    }
    return closed.finally(() => {
      opening = null;
      dialog.remove();
      if (document.activeElement === document.body && !document.querySelector('dialog[open]')) restore?.();
    });

    function showError(err) {
      if (!dialog.open) return;
      status.setAttribute('role', 'alert');
      status.dataset.tone = 'error';
      status.textContent = errorText(err);
      ok.disabled = true;
      cancel.disabled = busy = false;
      cancel.textContent = t('Close');
      cancel.focus();
    }
    async function commit() {
      if (!target || busy || ok.disabled || !dialog.open) return;
      busy = cancel.disabled = ok.disabled = true;
      status.textContent = t('Deleting workspace…');
      status.focus();
      try {
        await call('DeleteWorkspace', target);
      } catch (err) {
        showError(err);
        if (stored !== null) loadStored(true);
        return;
      }
      if (stored !== null) await loadStored(true);
      if (!record) await refresh().catch(fail);
      dialog.close('deleted');
      toast(t('Deleted “{name}”.', { name }));
    }
  }

  function storedSection() {
    const action = (label, fn) => h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: fn }, label);
    const show = action(t(stored === null ? 'Show workspaces' : 'Refresh'), () => loadStored());
    show.dataset.storageKey = 'list';
    show.setAttribute('aria-disabled', String(loading));
    const rows = [row(t('Archived and interrupted deletions'), h('p', null, t('Archived workspaces are kept until you delete them.')), show)];
    if (loading || listError || stored?.length === 0) rows.push(h('p', { class: 'stored-note', role: 'status' },
      loading ? t('Loading…') : listError || t('No archived workspaces or interrupted deletions.')));
    for (const r of stored ?? []) {
      const label = r.label || r.entry;
      const del = action(t(r.pending ? 'Finish deletion…' : 'Delete…'), e => ask(null, r, storageFocus(e.currentTarget)));
      del.dataset.storageKey = `${r.archived}/${r.entry}/${r.instance}`;
      del.disabled = !!r.problem;
      del.setAttribute('aria-label', t('Delete {record}…', { record: r.entry }));
      rows.push(row(label, h('div', null, h('p', { class: 'path' }, `${r.archived ? 'archive' : 'yesmen'}/${r.entry}`), r.problem && h('p', { class: 'danger' }, r.problem)), del));
    }
    const el = section(t('Stored workspaces'), null, ...rows);
    el.id = 'stored-workspaces';
    return el;
  }
  async function loadStored(afterDeletion = false) {
    if (loading && !afterDeletion) return;
    const sequence = ++loadSequence;
    const redraw = () => {
      const restore = storageFocus();
      document.getElementById('stored-workspaces')?.replaceWith(storedSection());
      if (document.activeElement === document.body) restore?.();
    };
    loading = true;
    redraw();
    try {
      const records = await call('StoredWorkspaces');
      if (sequence === loadSequence) { stored = records; listError = ''; }
    } catch (err) {
      if (sequence === loadSequence) { stored = null; listError = errorText(err); }
    } finally {
      if (sequence === loadSequence) { loading = false; redraw(); }
    }
  }
  return { ask, storedSection, storageFocus };
}
