// Settings › Grant. This is application OS access, never agent tool approval.
import { h, icon } from './dom.js';
import { t } from './i18n.js';

const PERMISSIONS = {
  bluetooth: ['Bluetooth', 'Allow Bluetooth device tasks you choose to run. Granting access does not scan or connect to devices.', 'device'],
  notifications: ['Notifications', 'Let yesman notify you when a lead needs you or finishes its work.', 'bell'],
  'full-disk': ['Full Disk Access', 'Access protected files when your work needs them.', 'folder'],
  microphone: ['Microphone', 'Allow audio tasks you choose to run. Granting access does not start recording.', 'record'],
  camera: ['Camera', 'Allow camera tasks you choose to run. Granting access does not capture images.', 'image'],
  accessibility: ['Accessibility', 'Allow desktop automation to inspect and operate application controls.', 'users'],
  screen: ['Screen Recording', 'Allow screen capture for desktop tasks you choose to run.', 'device'],
  input: ['Input Monitoring', 'Allow desktop tasks to monitor keyboard and mouse input when needed.', 'sliders'],
  automation: ['Automation', 'Review application control permissions for automation tasks.', 'gear'],
};
const STATES = {
  granted: 'Granted', 'not-determined': 'Not requested', denied: 'Denied', restricted: 'Restricted by the system',
  'not-granted': 'Not granted', limited: 'Limited', manual: 'Manual · cannot detect', system: 'System managed · cannot detect',
  'not-applicable': 'Not applicable', unavailable: 'Unavailable in this build', unknown: 'Unknown',
};
const DETAILS = {
  'full-disk-manual': 'macOS provides no reliable public query for all-file access. Add this copy of yesman under Privacy & Security › Full Disk Access, then restart if macOS asks.',
  'automation-manual': 'There is no global Automation grant. macOS asks separately for each target app when Apple Events are used. Review those decisions in Privacy & Security › Automation.',
  'bundle-required': 'Open the packaged yesman.app to query or request its permissions. A command-line or differently identified development build cannot report the installed app’s access.',
  'native-unavailable': 'This macOS build has no native permission bridge. Use the packaged release or build with CGO_ENABLED=1.',
  'usage-missing': 'This build is missing a required permission usage description. Use the packaged release.',
  'notification-alerts-off': 'Notification authorization exists, but visible alerts are disabled. Enable alerts in System Settings; Focus can also silence delivery.',
  'notification-provisional': 'Notifications are allowed quietly. Use System Settings to enable visible alerts.',
  'notification-sound-off': 'Alerts are allowed, but notification sounds are off. Focus and other system settings may still silence delivery.',
  'restart-may-be-needed': 'macOS reports access for this process, without a decision history. After changing access, restart yesman if macOS asks.',
  'boolean-status': 'macOS reports whether this process is trusted, without distinguishing a first request from a denial.',
  'windows-system': 'Windows manages this through device and desktop app settings. yesman cannot reliably query a per-app grant for this installed desktop application.',
  'windows-no-tcc': 'Windows has no equivalent macOS permission switch for this item. File access, desktop sessions and security policy still apply.',
  'linux-system': 'Access depends on your desktop environment, devices, session and portal or sandbox policy. There is no portable application-wide grant to query or request here.',
};
const OUTCOMES = {
  'settings-opened': 'System Settings opened. Make the change there, then return and refresh. Opening Settings does not grant access.',
  granted: 'The system reports access granted.', denied: 'Permission was denied. You can change it in System Settings.',
  restricted: 'The system restricts this permission. Check your device policy.',
  unchanged: 'Access is not confirmed. Finish any system prompt or change it in System Settings, then refresh. Restart if macOS asks.',
  'query-failed': 'The request finished, but its status could not be read. Refresh to try again.',
};
const errorText = (err) => String(err?.message || err?.error || err);

export function createGrants({ call, shown }) {
  let root = null;
  let view = null;
  let epoch = 0;
  let serial = 0;
  let reading = null;
  let refreshAgain = false; // one read after an action invalidates an in-flight snapshot
  let pending = null; // retained across tab switches until the system call settles
  let error = '';
  let feedback = null;

  function leave() {
    if (!root) return;
    epoch++; serial++;
    root = null; reading = null; refreshAgain = false; view = null; error = ''; feedback = null;
  }
  const active = (e) => e === epoch && shown() && root?.isConnected;

  // Repaint only this page; preserve a live focused control, never move focus
  // back from navigation, another control or another app after an async call.
  function paint() {
    if (!root || !shown()) return;
    const focus = root.contains(document.activeElement) ? document.activeElement.dataset.grantFocus : null;
    const refresh = h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-grant-focus': 'refresh', 'aria-disabled': String(!!reading), onclick: refreshStatus },
      reading ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'), t('Refresh'));
    root.replaceChildren(...[
      h('div', { class: 'grant-toolbar' }, h('h2', { class: 'mds-heading' }, t('Grant')), refresh),
      h('p', { class: 'toolbar-lead' }, t('Choose the access your work needs. Permissions apply to this copy of yesman; independent terminals, runtimes and helper apps may need their own grants.')),
      error ? h('p', { class: 'danger', role: 'alert' }, t('Could not read permissions. Refresh to try again.'), ' ', error) : null,
      !view && (reading || pending) ? h('p', { role: 'status', class: 'toolbar-lead' }, t(pending ? 'Waiting for the system…' : 'Reading permissions…')) : null,
      view ? h('div', { class: 'mds-group grant-list', 'aria-label': t('Application permissions') }, view.items.map(row)) : null].filter(Boolean));
    if (focus) {
      const replacement = root.querySelector(`[data-grant-focus="${CSS.escape(focus)}"]`) ||
        root.querySelector(`[data-grant-focus="${CSS.escape(focus.replace(/-request$/, '-settings'))}"]`) || refresh;
      replacement.focus({ preventScroll: true });
    }
  }
  function row(item) {
    const spec = PERMISSIONS[item.id];
    if (!spec) return null;
    const [name, description, glyph] = spec;
    const busy = pending?.id === item.id || item.busy;
    const note = feedback?.id === item.id ? feedback : null;
    const nativePending = view?.items.some(value => value.busy);
    const blocked = (action) => !!pending || !!reading || (action === 'request' && nativePending);
    const button = (action, label) => h('button', {
      class: 'mds-button mds-button--small', type: 'button', 'aria-disabled': String(blocked(action)),
      'data-grant-action': action, 'data-grant-focus': `${item.id}-${action}`, 'aria-label': t('{action}: {permission}', { action: t(label), permission: t(name) }),
      onclick: () => { if (!blocked(action)) act(item.id, action); },
    }, icon(action === 'settings' ? 'external' : 'plus'), t(label));
    return h('section', { class: 'mds-row grant-row', 'data-permission': item.id, 'aria-labelledby': `grant-${item.id}` },
      h('div', { class: 'grant-label' }, h('h3', { id: `grant-${item.id}` }, icon(glyph), t(name)), h('p', null, t(description)),
        item.detail && DETAILS[item.detail] ? h('p', { class: 'grant-detail' }, t(DETAILS[item.detail])) : null),
      h('div', { class: 'grant-controls' },
        h('span', { class: 'grant-state', 'data-state': item.error ? 'unknown' : item.state }, t(STATES[item.error ? 'unknown' : item.state] || STATES.unknown)),
        busy ? h('span', { class: 'grant-pending', role: 'status' }, t('Waiting for the system…')) : null,
        h('div', { class: 'grant-actions' }, item.request ? button('request', 'Request') : null, item.settings ? button('settings', 'Open Settings') : null)),
      item.error ? h('p', { class: 'grant-feedback danger', role: 'alert' }, t('Could not read status.'), ' ', item.error) : null,
      note ? h('p', { class: `grant-feedback${note.error ? ' danger' : ''}`, role: note.error ? 'alert' : 'status' }, note.text) : null);
  }
  async function refreshStatus() {
    // Status reads are independent of a pending OS action. Coalesce repeated
    // refresh/focus events; an action completion queues one genuinely new read.
    if (!shown() || !root || reading) return;
    const read = { epoch, serial };
    reading = read; error = '';
    paint();
    const current = () => reading === read && active(read.epoch) && read.serial === serial;
    try {
      const next = await call('Grants');
      if (current()) view = next;
    } catch (err) {
      if (current()) { error = errorText(err); view = null; }
    } finally {
      // A departed visit's read must not clear the new visit's loading state.
      if (reading === read) {
        reading = null;
        const again = refreshAgain;
        refreshAgain = false;
        if (active(read.epoch)) { paint(); if (again) refreshStatus(); }
      }
    }
  }
  async function act(id, action) {
    if (pending || reading || !shown()) return;
    const e = epoch;
    const operation = { id, action };
    pending = operation; feedback = null; serial++;
    paint();
    try {
      const result = await call('GrantAction', id, action);
      if (active(e)) {
        if (view) view.items = view.items.map(item => item.id === id ? result.item : item);
        feedback = { id, text: t(OUTCOMES[result.outcome] || OUTCOMES.unchanged) };
      }
    } catch (err) {
      if (active(e)) feedback = { id, error: true, text: t('Permission action failed. Refresh the status before trying again.' ) + ' ' + errorText(err) };
    } finally {
      if (pending === operation) pending = null;
      // Success, failure and timeout can all change native status/busy. Retire
      // any earlier snapshot, including a read started on a newer page visit.
      serial++;
      if (shown() && root) {
        refreshAgain = !!reading;
        paint();
        if (!reading) refreshStatus();
      }
    }
  }
  function render(inner) {
    root = h('div', { class: 'grant-page' });
    inner.append(root);
    paint();
  }
  const onReturn = () => { if (shown() && document.visibilityState !== 'hidden') refreshStatus(); };
  window.addEventListener('focus', onReturn);
  document.addEventListener('visibilitychange', onReturn);
  window.addEventListener('pagehide', leave);
  return { render, load: refreshStatus, leave };
}
