import { h, openModal } from './dom.js';
import { t } from './i18n.js';

// One opening owns its target, buttons and async replies. Only the explicit
// button click commits. A new hire or another archive of the name must get
// its own confirmation from the Store.
export function workspaceDeletion({ call, errorText, onLiveDeleted, toast, section, row }) {
  let opening = null;
  let stored = null;
  let loading = false;
  let loadSequence = 0;
  let listError = '';

  const storageSelector = '#stored-workspaces button[data-storage-key]';
  const recordKey = r => `${r.archived}/${r.entry}/${r.instance}`;
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
    const ok = h('button', { type: 'button', class: 'mds-button delete-permanently', disabled: !record, onclick: () => commit(false) }, t('Delete permanently'));
    const review = h('button', { type: 'button', class: 'mds-button force-delete-action', hidden: true, onclick: armForce }, t('Force Delete…'));
    const force = h('button', { type: 'button', class: 'mds-button force-delete-action', hidden: true, onclick: () => commit(true) }, t('Force Delete'));
    const refresh = h('button', { type: 'button', class: 'mds-button', hidden: true, onclick: () => checkForce(true) }, t('Check again'));
    const warning = h('div', { id: 'deletion-checkouts', class: 'deletion-checkouts', role: 'status', hidden: true });
    const dialog = h('dialog', { id: 'delete-workspace-dialog', class: 'mds-dialog workspace-deletion', 'aria-labelledby': 'deletion-title', 'aria-describedby': 'deletion-body deletion-location' },
      h('h2', { class: 'mds-heading', id: 'deletion-title' }, title),
      h('p', { class: 'path', id: 'deletion-location' }, location),
      h('p', { id: 'deletion-body' }, body),
      h('p', { class: 'dialog-hint' }, t('The shared repo pool, its branches and tags, and other workspaces are kept.')),
      warning, status, h('div', { class: 'mds-dialog-actions' }, cancel, refresh, review, force, ok));
    let busy = false;
    let target = record;
    let token = '';
    let checked = 0;
    let checking = false;
    let armed = false;
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
        void checkForce();
      }).catch(showError);
    } else if (record.pending && !record.archived) {
      void checkForce();
    }
    return closed.finally(() => {
      checked++;
      forgetToken(token);
      opening = null;
      dialog.remove();
      if (document.activeElement === document.body && !document.querySelector('dialog[open]')) restore?.();
    });

    function forgetToken(value) {
      if (value) void call('CancelWorkspaceForceDeletion', value).catch(() => {});
    }

    async function checkForce(retry = false) {
      if (!dialog.open || !target || target.archived || checking) return;
      const sequence = ++checked;
      checking = true;
      forgetToken(token);
      token = '';
      armed = false;
      ok.hidden = false;
      dialog.setAttribute('aria-describedby', 'deletion-body deletion-location');
      force.hidden = review.hidden = refresh.hidden = true;
      warning.hidden = false;
      warning.textContent = t('Checking for local Git checkouts…');
      if (retry) {
        status.textContent = '';
        cancel.textContent = t('Cancel');
        cancel.focus();
      }
      try {
        const result = await call('CheckWorkspaceForceDeletion', target);
        if (!dialog.open || sequence !== checked) {
          forgetToken(result.token);
          return;
        }
        token = result.token || '';
        const entries = result.entries ?? [];
        warning.replaceChildren(...[
          entries.length ? h('p', null, t('Git checkouts in this workspace')) : null,
          entries.length ? h('ul', { class: 'deletion-checkout-paths' }, entries.map(e =>
            h('li', { class: 'path' }, e.path === '.' ? location : `${location}/${e.path}`))) : null,
          token ? h('p', { class: 'danger', id: 'force-deletion-risk' }, t('Force Delete permanently removes these local checkouts and the rest of this workspace. Unsaved changes, untracked files, and Git commits or branches stored only here can be lost.')) : null,
          result.problem ? h('p', { class: 'danger' }, t('Force Delete is unavailable.'), ' ', result.problem) : null,
        ].filter(Boolean));
        warning.hidden = !warning.hasChildNodes();
        review.hidden = !token;
        if (token) ok.disabled = true;
        else if (retry && !result.problem && !entries.length) ok.disabled = false;
        refresh.hidden = !result.problem && !status.textContent;
      } catch (err) {
        if (!dialog.open || sequence !== checked) return;
        warning.textContent = t('Could not check local Git checkouts.') + ' ' + errorText(err);
        refresh.hidden = false;
      } finally {
        if (sequence === checked) checking = false;
      }
    }

    function armForce() {
      if (busy || !token || !dialog.open) return;
      armed = true;
      ok.hidden = review.hidden = true;
      force.hidden = false;
      dialog.setAttribute('aria-describedby', 'deletion-body deletion-location force-deletion-risk deletion-checkouts');
      cancel.textContent = t('Cancel');
      cancel.focus();
    }

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
    async function commit(forced) {
      if (!target || busy || !dialog.open || (forced ? !armed || !token : ok.disabled)) return;
      const confirmation = token;
      if (forced) token = '';
      busy = cancel.disabled = ok.disabled = review.disabled = force.disabled = refresh.disabled = true;
      status.textContent = t('Deleting workspace…');
      status.focus();
      try {
        if (forced) await call('ForceDeleteWorkspace', target, confirmation);
        else await call('DeleteWorkspace', target);
      } catch (err) {
        showError(err);
        review.disabled = force.disabled = refresh.disabled = false;
        if (forced) {
          armed = false;
          force.hidden = true;
          ok.hidden = false;
          refresh.hidden = false;
        } else if (!record?.archived && !token) {
          refresh.hidden = false;
          void checkForce();
        }
        if (stored !== null) loadStored(true);
        return;
      }
      dialog.close('deleted');
      toast(t('Deleted “{name}”.', { name }));
      if (record && stored) stored = stored.filter(r => recordKey(r) !== recordKey(record));
      if (!record) onLiveDeleted(workspace);
      // Keep the list current independently of the completed confirmation. A
      // visible list can show and focus the surviving rows even during this read.
      if (stored !== null || loading) void loadStored(true);
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
      del.dataset.storageKey = recordKey(r);
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
      if (sequence === loadSequence) listError = errorText(err);
    } finally {
      if (sequence === loadSequence) { loading = false; redraw(); }
    }
  }
  return { ask, storedSection, storageFocus };
}
