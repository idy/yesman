// User-directed cleanup from the Info panel. A menu, its dialog and the
// eventual receipt all belong to the hire and row that opened them.
import { $, h, openModal } from './dom.js';
import { t, tn } from './i18n.js';
import { taskStatusLabel } from './board.js';

export function worktreeEntry(w) {
  return w ? `worktree:${w.dir}:${w.cleanupId || ''}` : 'worktrees';
}

export function createWorktreeMenu(host) {
  let opening = null;
  const current = y => host.isOpen() && host.yesman()?.name === y.name && host.yesman()?.hiredAt === y.hiredAt;
  const entry = w => $(`#yesman-info [data-info-entry="${CSS.escape(worktreeEntry(w))}"]`);

  async function open(event, y, w = null) {
    event.preventDefault();
    if ((opening && (!opening.dialog || opening.dialog.open)) || !current(y)) return;
    const request = opening = {};
    const opener = entry(w);
    const box = opener?.getBoundingClientRect();
    const pointer = event.clientX || event.clientY;
    const x = Math.round(pointer ? event.clientX : box?.left || 0);
    const top = Math.round(pointer ? event.clientY : box?.bottom || 0);
    opener?.focus({ preventScroll: true });
    try {
      const action = await host.call('WorktreeMenu', !!w, x, top);
      if (!current(y)) return;
      if (w && action === 'reveal') await host.call('Reveal', w.path);
      if (action === (w ? 'remove' : 'cleanup')) await review(y, w, request);
    } catch (err) {
      host.fail(err);
    } finally {
      if (opening === request) {
        opening = null;
        if (current(y) && document.activeElement === document.body) (entry(w) || entry(null))?.focus({ preventScroll: true });
      }
    }
  }

  async function review(y, selected, request) {
    const heading = h('h2', { class: 'mds-heading', id: 'worktree-cleanup-title' }, selected ? t('Remove worktree?') : t('Clean up worktrees'));
    const hint = h('p', { class: 'dialog-hint', id: 'worktree-cleanup-hint', role: 'status' }, t('Checking worktrees…'));
    const listing = h('div', { class: 'cleanup-listing' });
    const close = h('button', { class: 'mds-button', type: 'button', autofocus: true, 'data-close': true }, t('Cancel'));
    const confirm = h('button', { class: 'mds-button mds-button--primary', type: 'button', hidden: true, 'data-confirm': true });
    const dialog = h('dialog', { class: 'mds-dialog worktree-cleanup', 'aria-labelledby': heading.id, 'aria-describedby': hint.id },
      heading, hint, listing, h('div', { class: 'mds-dialog-actions' }, close, confirm));
    request.dialog = dialog;
    document.body.append(dialog);
    let token = '', committing = false;
    const closed = openModal(dialog);
    close.onclick = () => dialog.close();
    dialog.oncancel = event => { if (committing) event.preventDefault(); };
    const cancelToken = id => { if (id) host.call('CancelWorktreeCleanup', id).catch(host.fail); };

    function end(title, note) {
      heading.textContent = title;
      hint.textContent = note;
      confirm.hidden = true;
      close.disabled = false;
      close.textContent = t('Close');
      close.focus({ preventScroll: true });
    }
    async function preview() {
      try {
        const result = await host.call('PreviewWorktreeCleanup', y.name, y.hiredAt, selected, host.unsaved(y));
        if (!dialog.open || !current(y)) { cancelToken(result.token); dialog.close(); return; }
        token = result.token;
        const ready = result.rows.filter(row => !row.reason);
        const kept = result.rows.filter(row => row.reason);
        if (ready.length) {
          hint.textContent = t('Git saves each commit in the shared repository and keeps your branches and tags.');
          listing.append(rowList(ready));
          if (selected && ready[0]?.keepWarning) {
            const warning = h('p', { id: 'worktree-cleanup-warning', class: 'cleanup-warning', role: 'status' }, t(ready[0].keepWarning));
            listing.append(warning);
            confirm.setAttribute('aria-describedby', warning.id);
          }
          confirm.textContent = selected ? t('Remove worktree') : tn(ready.length, 'Remove 1 worktree', 'Remove {n} worktrees');
          confirm.hidden = false;
        } else {
          end(selected ? t('Worktree kept') : t('No worktrees to remove'), kept.length ? t('These directories will stay in the workspace.') : t('This workspace has no registered worktrees.'));
        }
        if (kept.length) {
          const keptList = rowList(kept);
          listing.append(ready.length ? h('details', { class: 'cleanup-kept' }, h('summary', null, tn(kept.length, 'Keep 1 worktree', 'Keep {n} worktrees')), keptList) : keptList);
        }
      } catch (err) {
        if (dialog.open) end(t('Could not check worktrees'), t(err?.message || String(err)));
      }
    }
    confirm.onclick = async () => {
      if (committing || !token || !dialog.open) return;
      if (!current(y)) { dialog.close(); return; }
      committing = true;
      const id = token;
      token = '';
      confirm.disabled = close.disabled = true;
      hint.textContent = t('Checking again and removing directories…');
      try {
        const results = await host.call('ConfirmWorktreeCleanup', y.name, y.hiredAt, id, host.unsaved(y));
        const removed = results.filter(row => row.removed);
        host.updated(y, removed);
        const issues = results.filter(row => row.reason);
        if (!issues.length) {
          host.toast(tn(removed.length, 'Removed 1 worktree.', 'Removed {n} worktrees.'));
          dialog.close();
        } else {
          listing.replaceChildren(rowList(issues));
          const saved = removed.filter(row => !row.reason);
          if (saved.length) listing.append(h('details', { class: 'cleanup-kept' }, h('summary', null, t('Removed directories')), rowList(saved)));
          end(t('Cleanup results'), tn(removed.length, 'Removed 1 worktree.', 'Removed {n} worktrees.'));
        }
      } catch (err) {
        // A lost answer is not evidence of success or failure. Read again,
        // keep the receipt visible and require a fresh preview for any retry.
        host.updated(y, []);
        listing.replaceChildren();
        end(t('Could not confirm the result'), t(err?.message || String(err)));
      } finally {
        committing = false;
      }
    };
    void preview();
    await closed;
    cancelToken(token);
    dialog.remove();
  }

  return open;
}

function rowList(rows) {
  return h('ul', { class: 'cleanup-rows', role: 'list' }, rows.map(row => {
    const task = [row.task, taskStatusLabel(row.taskStatus)].filter(Boolean).join(' · ');
    const detail = [row.detail !== row.task ? row.detail : '', task].filter(Boolean).join(' · ');
    return h('li', null,
      h('div', { class: 'cleanup-path', dir: 'auto' }, row.path.endsWith(row.dir) && row.dir
        ? [h('span', { class: 'cleanup-parent' }, row.path.slice(0, -row.dir.length)), h('span', null, row.dir)] : row.path),
      !row.reason ? h('div', { class: 'cleanup-source' }, row.branch ? `${row.branch} · ` : '', h('code', { title: row.head }, row.head)) : null,
      row.reason ? h('div', { class: 'cleanup-reason' }, t(row.reason), detail ? ` ${detail}` : '') : null);
  }));
}
