// Settings → GitHub App. The bridge returns public identity/status only; PEM
// import uses a native chooser and registration exchanges credentials in Go.
import { h, icon } from './dom.js';
import { t } from './i18n.js';

const errorText = (e) => String(e?.message || e?.error || e);
export function createGitHubAppPage({ call, shown, rerender, settingsSection, preferenceRow }) {
  const s = { view: null, error: '', busy: false, dirty: false, repositories: '', workspaces: [], owner: '', name: 'yesman agent', appID: '' };
  let reads = 0;
  function accept(v) {
    s.view = v;
    if (!s.dirty) {
      s.repositories = (v.config.repositories ?? []).join('\n');
      s.workspaces = [...(v.config.workspaces ?? [])];
    }
    if (!s.appID && v.app) s.appID = String(v.app.id);
  }
  async function load() {
    const seq = ++reads;
    try { const v = await call('GitHubApp'); if (seq === reads) accept(v); }
    catch (err) { if (seq === reads) s.error = errorText(err); }
    if (seq === reads && shown()) rerender();
  }
  async function action(method, args = [], saved = false) {
    if (s.busy) return;
    s.busy = true; s.error = ''; ++reads;
    if (shown()) rerender();
    try { await call(method, ...args); if (saved) s.dirty = false; }
    catch (err) { s.error = errorText(err); }
    finally {
      // Invalidate every read begun before the mutation settled. The fresh
      // snapshot also handles native-dialog cancellation and key replacement.
      ++reads; s.busy = false; await load();
    }
  }
  function markDirty() {
    s.dirty = true;
    for (const button of document.querySelectorAll('#github-app-page [data-requires-saved]')) button.disabled = true;
    const status = document.querySelector('#github-app-scope-status');
    if (status) status.textContent = t('Unsaved changes');
  }
  const button = (label, method, args = [], disabled = false, saved = false) => h('button', {
    type: 'button', class: 'mds-button mds-button--small', disabled: s.busy || disabled,
    'data-requires-saved': method === 'TestGitHubApp' || method === 'EnableGitHubApp' && args[0] === true,
    onclick: () => action(method, typeof args === 'function' ? args() : args, saved),
  }, t(label));
  const field = (label, props) => h('label', { class: 'github-app-field' }, h('span', null, t(label)), h('input', { class: 'mds-input', disabled: s.busy, ...props }));
  const row = (label, hint, control) => preferenceRow(t(label), h('p', null, t(hint)), control);
  function render(inner) {
    const v = s.view;
    const page = h('div', { class: 'github-app-page', id: 'github-app-page', 'aria-busy': String(s.busy) });
    inner.append(page);
    page.append(h('div', { class: 'mds-toolbar' },
      h('p', { class: 'toolbar-lead' }, t('Create agent pull requests with your own GitHub App name and avatar.')),
      h('button', { type: 'button', class: 'mds-icon-button', 'aria-label': t('Refresh'), onclick: load }, icon('reload'))));
    if (s.error || v?.error) page.append(h('p', { class: 'danger', role: 'alert' }, t(s.error || v.error)));
    if (v && !v.routingAvailable) page.append(h('p', { class: 'danger', role: 'alert' }, t('GitHub App command routing is unavailable. Install GitHub CLI or repair the private runtime wrapper and restart. You can disable App identity to use the existing credentials.')));
    if (!v) { page.append(h('p', null, t('Reading…'))); return; }
    page.append(settingsSection(t('PR identity'), null,
      row(v.config.enabled ? 'App identity enabled' : 'Personal credentials',
        'Only gh pr create in the selected workspaces uses the App. Git push, other gh commands, merging and releases keep their existing credentials.',
        button(v.config.enabled ? 'Disable App identity' : 'Enable App identity', 'EnableGitHubApp', [!v.config.enabled], !v.config.enabled && (!v.verified || !v.unlocked || !v.routingAvailable || s.dirty || !v.config.workspaces?.length || !v.config.repositories?.length))),
      row('Credential priority',
        'Enabling explicitly overrides GH_TOKEN and GITHUB_TOKEN for PR creation in this scope. Locked keys and repositories outside the list fail without using a personal token.',
        h('span', { class: 'mds-hint' }, t('No global login changes')))));
    if (!v.configured || !v.unlocked) {
      let password = '';
      const control = h('form', { class: 'github-app-unlock', onsubmit: (e) => {
        e.preventDefault(); const entered = password; password = ''; action('PrepareGitHubApp', [entered]);
      } },
        field('Vault password', { type: 'password', autocomplete: 'off', value: '', oninput: (e) => { password = e.target.value; } }),
        h('button', { class: 'mds-button mds-button--small', type: 'submit', disabled: s.busy }, t(v.configured ? 'Unlock' : 'Set up key storage')));
      page.append(settingsSection(t('Private key storage'), null,
        row(v.configured ? 'Key storage locked' : 'Set up key storage', v.keychain
          ? 'Leave the password empty to use the system credential store. App keys stay in a separate encrypted vault and never enter agent environments.'
          : 'Choose a password of at least 8 characters. Enter it again after restarting to unlock the separate encrypted App vault.', control)));
      return;
    }
    page.append(settingsSection(t('Private key storage'), null,
      row(v.app ? `${v.app.name} (${v.app.slug}[bot])` : 'No App key imported',
        'The private key is encrypted locally. It is not included in configuration exports or sent to agents.',
        h('div', { class: 'row-end' }, v.app ? h('span', { class: 'mds-hint', 'aria-label': t('Private key stored') }, '••••••••') : null,
          button('Lock', 'LockGitHubApp'))),
      ...(v.app ? [row('App ID', String(v.app.id), h('button', { type: 'button', class: 'mds-link-button', onclick: () => call('OpenURL', `${v.app.html_url}/installations/new`).catch((e) => { s.error = errorText(e); if (shown()) rerender(); }) }, t('Manage installation')))] : []),
      row('Import an existing App key', 'Enter its App ID, then choose the PEM file in the native file dialog. The key never passes through this page.',
        h('div', { class: 'github-app-import' }, field('App ID', { inputmode: 'numeric', value: s.appID, oninput: (e) => { s.appID = e.target.value; } }), button('Choose key file…', 'ImportGitHubAppKey', () => [s.appID])))));
    const setup = v.setup ?? {};
    page.append(settingsSection(t('Register an App'), t('Use a personal GitHub account. Registration requests pull requests write, contents read and metadata read, with webhooks disabled.'),
      row('App avatar', 'Save the existing yesman logo, then upload it in the GitHub App’s Display information settings.', button('Save App logo', 'ExportGitHubAppLogo')),
      h('div', { class: 'github-app-register' },
        field('GitHub owner', { value: s.owner, autocomplete: 'off', oninput: (e) => { s.owner = e.target.value; } }),
        field('App name', { value: s.name, oninput: (e) => { s.name = e.target.value; } }),
        button('Start registration', 'RegisterGitHubApp', () => [s.owner, s.name], !!setup.url)),
      ...(setup.url ? [row('Registration open', 'Open this local page and finish GitHub login and confirmation. Then install using Only select repositories. The link expires after 30 minutes.',
        h('div', { class: 'row-end' }, h('button', { type: 'button', class: 'mds-button mds-button--small', onclick: () => call('OpenURL', setup.url).catch((e) => { s.error = errorText(e); if (shown()) rerender(); }) }, t('Open registration')),
          button('Cancel registration', 'CancelGitHubAppRegistration')))] : []),
      ...(setup.error ? [h('p', { class: 'danger', role: 'alert' }, t(setup.error))] : []),
      ...(setup.complete ? [h('p', { class: 'mds-hint', role: 'status' }, t('App saved. Complete its repository installation, then save and test the scope below.'))] : [])));
    const repoField = h('textarea', { class: 'mds-textarea', rows: 3, 'aria-label': t('Repositories, one owner/repo per line'), disabled: s.busy,
      oninput: (e) => { s.repositories = e.target.value; markDirty(); } }, s.repositories);
    const choices = [...(v.workspaces ?? [])];
    for (const name of s.workspaces) if (!choices.some((w) => w.name === name)) choices.push({ name, label: `${name} (${t('Unavailable')})` });
    const workspaceList = h('div', { class: 'github-app-workspaces' }, choices.map((w) => h('label', null,
      h('input', { type: 'checkbox', value: w.name, checked: s.workspaces.includes(w.name), disabled: s.busy, onchange: (e) => {
        s.workspaces = e.target.checked ? [...new Set([...s.workspaces, w.name])] : s.workspaces.filter((x) => x !== w.name); markDirty();
      } }), w.label)));
    page.append(settingsSection(t('Selected scope'), t('Choose repositories and workspaces explicitly. Saving a changed scope disables App identity until you test and enable it again.'),
      row('Repositories', 'One owner/repo per line. The App must be installed on each selected repository.', repoField),
      row('Workspaces', 'Apply to the next PR creation by agents in these workspaces, including workers already running. Other workspaces keep their credentials.', workspaceList),
      h('div', { class: 'github-app-actions' }, button('Save scope', 'ConfigureGitHubApp', () => [{ enabled: false, repositories: s.repositories.split(/\s+/).filter(Boolean), workspaces: s.workspaces }], false, true),
        button('Test connection', 'TestGitHubApp', [], !v.app || s.dirty),
        h('span', { class: 'mds-hint', id: 'github-app-scope-status', role: 'status' }, t(s.dirty ? 'Unsaved changes' : v.verified ? 'Connection verified' : 'Test before enabling'))),
      ...(v.connections ?? []).map((c) => h('p', { class: 'mds-hint' }, `${c.repository} · ${c.app.slug}[bot] · ${t('Installation ID')} ${c.installation_id}`))));
  }
  return { load, render };
}
