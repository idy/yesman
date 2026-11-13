// Settings › Secrets and Settings › Variables: the environment variables
// every agent's commands get. A page edits its entries as a form or as
// YAML, which the app converts between, and saves them whole. Secrets are
// kept encrypted and open with a password; agents never read their values.
import { $, h, icon } from './dom.js';
import { initTabs, openDialog, toast } from './mds/js/magpie.js';
import { t, tn } from './i18n.js';
import { createEditor } from './editor.js';

const errorText = (err) => String(err?.message || err?.error || err);
const fileName = (path) => path.split(/[\\/]/).pop();
const dirName = (path) => path.slice(0, Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\')));

// What each page says, in English: t says it in the page's language.
const WORDS = {
  secrets: {
    lead: 'Secrets are environment variables every agent’s commands get, such as API keys and tokens. They are kept encrypted with sops and age, and open with your password. Agents read a secret’s name and description with yesman_env, never its value: output shows it as [secret NAME].',
    add: 'Add secret',
    empty: 'No secrets yet',
    emptyHint: 'Add the keys and tokens your agents need, with what each is for.',
    describe: 'What it is for: the account, the tool that reads it…',
    saved: 'Saved. Agents’ next commands get the change.',
  },
  variables: {
    lead: 'Variables are environment variables every agent’s commands get, such as an account name, a region or an endpoint. They are kept in plain text, and agents read their values and descriptions with yesman_env. Keep keys and tokens in Secrets.',
    add: 'Add variable',
    empty: 'No variables yet',
    emptyHint: 'Add the settings your agents need, with what each is for.',
    describe: 'What it is for, and who reads it…',
    saved: 'Saved. Agents’ next commands get the change.',
  },
};

// One editor serves both pages, which never show at once.
const editorPages = {};
let sharedEditor = null;
const editorFor = () => (sharedEditor ??= createEditor({
  onChange: (k) => editorPages[k.split('\n')[0]]?.edited(),
  onSave: (k) => editorPages[k.split('\n')[0]]?.save(),
}));

export function createEnvPage(kind, { call, fail, confirm, shown, rerender, preferenceRow, settingsSection, revealButton }) {
  const secret = kind === 'secrets';
  const w = WORDS[kind];
  const key = `${kind}\n${kind}.yaml`;
  const prefix = `env-${kind}`;
  const id = (what, row) => `${prefix}-${what}${row == null ? '' : `-${row}`}`;
  const s = {
    view: null, // Yesman.Secrets or Yesman.Variables
    mode: 'form', // form or yaml
    rows: [], // the form's: { id, name, value, description }
    text: '', // the YAML, as the editor starts with it
    saved: '[]', // the entries saved, as JSON, to tell changes
    savedText: '',
    error: '',
    busy: false,
    shown: new Set(), // the ids of rows whose value shows
    focused: '', // the locked state whose password field was focused
  };
  let nextId = 0;
  let editor = null;
  let loads = 0;

  const mounted = () => shown() && $(`#${prefix}-page`);
  const strip = (rows) => rows.map(({ name, value, description }) => ({ name, value, description }));
  const currentText = () => (editor?.has(key) ? editor.text(key) : s.text);
  const dirty = () => (s.mode === 'yaml' ? currentText() !== s.savedText : JSON.stringify(strip(s.rows)) !== s.saved);
  const count = (n) => (secret ? tn(n, '{n} secret', '{n} secrets') : tn(n, '{n} variable', '{n} variables'));

  // accept shows what the app has, unless edits not saved would be lost.
  function accept(view, { keep = false } = {}) {
    const editing = keep && s.view && dirty() && view.unlocked !== false;
    s.view = view;
    if (editing) return;
    s.rows = (view.entries ?? []).map((e) => ({ id: nextId++, ...e }));
    s.saved = JSON.stringify(strip(s.rows));
    s.text = s.savedText = view.text ?? '';
    s.shown.clear();
    editor?.forget(key);
  }

  async function load() {
    const seq = ++loads;
    let view;
    try {
      view = await call(secret ? 'Secrets' : 'Variables');
    } catch (err) {
      if (seq === loads) fail(err);
      return;
    }
    if (seq !== loads) return;
    accept(view, { keep: true });
    if (view.error && !s.error) s.error = view.error;
    if (shown()) rerender();
  }

  // ---------- Page ----------

  function render(inner) {
    const v = s.view;
    if (!v) {
      inner.append(h('p', { class: 'toolbar-lead' }, t('Reading…')));
      return;
    }
    const page = h('div', { class: 'env-page', id: `${prefix}-page` }, h('p', { class: 'toolbar-lead' }, t(w.lead)));
    inner.append(page);
    if (secret && !v.configured) {
      page.append(setupSection());
      focusOnce('setup', id('setup-password'));
      return;
    }
    if (secret && !v.unlocked) {
      page.append(...unlockSection().filter(Boolean));
      focusOnce('unlock', id('unlock-password'));
      return;
    }
    s.focused = '';
    const tab = (mode, label) => h('button', { type: 'button', role: 'tab', id: id('tab', mode), 'aria-controls': id('panel', mode), 'aria-selected': String(s.mode === mode) }, label);
    const tabs = h('div', { class: 'mds-tabs', role: 'tablist', 'aria-label': t('Edit as') }, tab('form', t('Form')), tab('yaml', 'YAML'));
    page.append(
      h('div', { class: 'mds-toolbar' }, tabs, h('div', { class: 'mds-toolbar-actions', id: id('actions') }, actions())),
      h('p', { class: 'danger env-error', id: id('error'), role: 'alert', hidden: !s.error }, s.error),
      h('div', { class: 'env-panel', id: id('panel', 'form'), role: 'tabpanel', 'aria-labelledby': id('tab', 'form') },
        h('div', { class: 'card-list env-list', id: id('list'), role: 'list', 'aria-label': secret ? t('Secrets') : t('Variables') }, rowEls()),
        h('div', null, h('button', { class: 'mds-link-button env-add', type: 'button', onclick: addRow }, icon('plus'), t(w.add)))),
      h('div', { class: 'env-panel', id: id('panel', 'yaml'), role: 'tabpanel', 'aria-labelledby': id('tab', 'yaml') },
        h('div', { class: 'env-editor', id: id('editor') }),
        h('p', { class: 'mds-hint' }, t('Each name has a value and a description. Comments are not kept. Cmd/Ctrl+S saves.'))),
      ...(secret ? secretSections(v) : variableSections(v)),
    );
    initTabs(tabs, { onSelect: (tabEl) => switchTo(tabEl.id === id('tab', 'yaml') ? 'yaml' : 'form') });
    if (s.mode === 'yaml') showEditor();
  }

  // focusOnce puts the caret in a password field when its form first
  // shows, not each time the page draws again.
  function focusOnce(state, elementId) {
    if (s.focused === state) return;
    s.focused = state;
    requestAnimationFrame(() => document.getElementById(elementId)?.focus());
  }

  function actions() {
    const changed = dirty();
    const button = (label, props) => h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: s.busy, ...props }, label);
    return [
      h('span', { class: 'env-status', id: id('status') }, changed ? t('Unsaved changes') : count(JSON.parse(s.saved).length)),
      changed ? button(t('Revert'), { onclick: revert }) : null,
      button(s.busy ? t('Saving…') : t('Save'), { class: 'mds-button mds-button--small mds-button--primary', id: id('save'), disabled: s.busy || !changed, onclick: save }),
      secret ? button([icon('lock'), t('Lock')], { title: t('Forget the key: commands get no secrets until you unlock them again'), onclick: lock }) : null,
    ];
  }

  function paintActions() {
    $(`#${id('actions')}`)?.replaceChildren(...actions().filter(Boolean));
  }

  function paintError() {
    const el = $(`#${id('error')}`);
    if (!el) return;
    el.textContent = s.error;
    el.hidden = !s.error;
  }

  function edited() {
    paintActions();
  }

  // ---------- Form ----------

  function rowEls() {
    if (!s.rows.length) {
      return h('div', { class: 'card-empty' }, h('strong', null, t(w.empty)), h('span', null, t(w.emptyHint)));
    }
    return s.rows.map(rowEl);
  }

  function paintList() {
    $(`#${id('list')}`)?.replaceChildren(...[rowEls()].flat());
  }

  // area is a text box that grows with its lines, up to eight.
  function area({ elementId, cls, text, label, placeholder, onchange }) {
    const lines = (v) => Math.min(8, Math.max(1, v.split('\n').length));
    const el = h('textarea', { class: `mds-textarea ${cls}`, id: elementId, rows: lines(text), placeholder, 'aria-label': label,
      spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off',
      oninput: (e) => { e.currentTarget.rows = lines(e.currentTarget.value); onchange(e.currentTarget.value); } });
    el.value = text;
    return el;
  }

  function rowEl(r) {
    const hidden = secret && !s.shown.has(r.id);
    const label = r.name || t('this entry');
    const name = h('input', { class: 'mds-input mds-mono env-name', id: id('name', r.id), value: r.name, placeholder: 'NAME', 'aria-label': t('Name'),
      spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off',
      oninput: (e) => { r.name = e.currentTarget.value; edited(); } });
    const value = area({ elementId: id('value', r.id), cls: `mds-mono env-value${hidden ? ' env-hidden' : ''}`, text: r.value,
      label: t('Value of {name}', { name: label }), placeholder: t('Value'), onchange: (v) => { r.value = v; edited(); } });
    const description = area({ elementId: id('description', r.id), cls: 'env-description', text: r.description,
      label: t('Description of {name}', { name: label }), placeholder: t(w.describe), onchange: (v) => { r.description = v; edited(); } });
    const iconButton = (glyph, title, onclick) => h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title, 'aria-label': title, onclick }, icon(glyph));
    return h('div', { class: 'env-row', role: 'listitem' },
      name, value,
      h('div', { class: 'env-row-actions' },
        secret ? h('button', { class: 'mds-button mds-button--small mds-button--subtle env-show', type: 'button', 'aria-pressed': String(!hidden),
          'aria-controls': id('value', r.id), onclick: () => toggleShown(r) }, hidden ? t('Show') : t('Hide')) : null,
        iconButton('copy', t('Copy the value of {name}', { name: label }), () => call('CopyValue', r.value).then(() => toast(t('Copied.')), fail)),
        iconButton('trash', t('Remove {name}', { name: label }), () => removeRow(r))),
      description);
  }

  function toggleShown(r) {
    if (s.shown.has(r.id)) s.shown.delete(r.id);
    else s.shown.add(r.id);
    const hidden = !s.shown.has(r.id);
    $(`#${id('value', r.id)}`)?.classList.toggle('env-hidden', hidden);
    const button = document.querySelector(`[aria-controls="${id('value', r.id)}"]`);
    if (button) {
      button.textContent = hidden ? t('Show') : t('Hide');
      button.setAttribute('aria-pressed', String(!hidden));
    }
  }

  function addRow() {
    const r = { id: nextId++, name: '', value: '', description: '' };
    s.rows.push(r);
    s.shown.add(r.id); // a value you type in shows
    paintList();
    paintActions();
    $(`#${id('name', r.id)}`)?.focus();
  }

  function removeRow(r) {
    const at = s.rows.indexOf(r);
    s.rows.splice(at, 1);
    paintList();
    paintActions();
    const next = s.rows[Math.min(at, s.rows.length - 1)];
    (next ? $(`#${id('name', next.id)}`) : $(`#${prefix}-page .env-add`))?.focus();
  }

  // ---------- YAML ----------

  async function showEditor() {
    editor ??= await editorFor();
    editorPages[kind] = { edited, save };
    const box = $(`#${id('editor')}`);
    if (!box || s.mode !== 'yaml') return;
    box.replaceChildren(editor.dom);
    editor.show(key, { text: s.text, lang: 'yaml', readOnly: false });
  }

  // switchTo converts the draft to the other mode; a mistake in the YAML
  // keeps it there.
  async function switchTo(mode) {
    if (mode === s.mode) return true;
    if (s.busy) return false; // a save is on its way
    try {
      if (mode === 'yaml') {
        s.text = await call('FormatEntries', kind, strip(s.rows));
        editor?.forget(key);
      } else {
        const text = currentText();
        const entries = await call('ParseEntries', kind, text);
        const old = new Map(s.rows.map((r) => [r.name, r.id]));
        s.rows = entries.map((e) => ({ id: old.get(e.name) ?? nextId++, ...e }));
        s.text = text;
      }
    } catch (err) {
      s.error = errorText(err);
      paintError();
      return false;
    }
    s.mode = mode;
    s.error = '';
    paintError();
    if (mode === 'yaml') await showEditor();
    else paintList();
    paintActions();
    return true;
  }

  // ---------- Saving ----------

  async function save() {
    if (s.busy || !dirty()) return;
    s.busy = true;
    s.error = '';
    paintActions();
    paintError();
    // What is sent: edits made while it saves stay, and show unsaved.
    const submitted = s.mode === 'yaml' ? currentText() : null;
    try {
      const view = submitted !== null
        ? await call(secret ? 'SaveSecretsText' : 'SaveVariablesText', submitted)
        : await call(secret ? 'SaveSecrets' : 'SaveVariables', strip(s.rows));
      s.view = view;
      s.saved = JSON.stringify(view.entries);
      s.savedText = view.text;
      if (submitted !== null && currentText() === submitted) {
        // The editor shows the file as kept, without the edits' comments.
        s.text = view.text;
        editor?.replace(key, view.text);
        const old = new Map(s.rows.map((r) => [r.name, r.id]));
        s.rows = view.entries.map((e) => ({ id: old.get(e.name) ?? nextId++, ...e }));
      }
      toast(t(w.saved));
    } catch (err) {
      s.error = errorText(err);
    } finally {
      s.busy = false;
      if (mounted()) {
        paintActions();
        paintError();
      }
    }
  }

  function revert() {
    accept(s.view);
    s.error = '';
    rerender();
  }

  async function lock() {
    if (dirty() && !(await confirm(t('Lock without saving?'), t('Your changes to the secrets are lost.'), t('Lock')))) return;
    try {
      accept(await call('LockSecrets'));
      s.error = '';
    } catch (err) {
      fail(err);
    }
    rerender();
  }

  // ---------- Locked ----------

  function passwordInput(elementId, autocomplete) {
    return h('input', { class: 'mds-input', type: 'password', id: elementId, required: true, autocomplete, spellcheck: 'false' });
  }

  function setupSection() {
    const password = passwordInput(id('setup-password'), 'new-password');
    const again = passwordInput(id('setup-again'), 'new-password');
    const error = h('p', { class: 'danger', role: 'alert', id: id('setup-error'), hidden: true });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, t('Set up secrets'));
    const form = h('form', { class: 'env-lock', onsubmit: async (e) => {
      e.preventDefault();
      error.hidden = true;
      if (password.value !== again.value) {
        error.textContent = t('The two passwords differ.');
        error.hidden = false;
        again.focus();
        return;
      }
      submit.disabled = true;
      try {
        accept(await call('SetUpSecrets', password.value));
        rerender();
      } catch (err) {
        error.textContent = errorText(err);
        error.hidden = false;
        password.focus();
      } finally {
        submit.disabled = false;
      }
    } },
    h('label', { class: 'field' }, h('span', null, t('Password')), password),
    h('label', { class: 'field' }, h('span', null, t('Password again')), again),
    error,
    h('div', null, submit));
    return settingsSection(t('Set a password'),
      t('The password opens your secrets each time yesman starts. yesman keeps it nowhere and cannot recover it, so keep it in your password manager.'), form);
  }

  function unlockSection() {
    const v = s.view;
    const password = passwordInput(id('unlock-password'), 'current-password');
    const error = h('p', { class: 'danger', role: 'alert', id: id('unlock-error'), hidden: true });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, t('Unlock'));
    const form = h('form', { class: 'env-lock', onsubmit: async (e) => {
      e.preventDefault();
      submit.disabled = true;
      error.hidden = true;
      try {
        accept(await call('UnlockSecrets', password.value));
        s.error = '';
        rerender();
      } catch (err) {
        error.textContent = errorText(err);
        error.hidden = false;
        password.select();
      } finally {
        submit.disabled = false;
      }
    } },
    h('label', { class: 'field', for: id('unlock-password') }, t('Password')),
    h('div', { class: 'storage-input' }, password, submit),
    error);
    const names = v.names ?? [];
    const hint = names.length
      ? tn(names.length, '1 secret is locked: {names}. Commands run without it until you unlock it.', '{n} secrets are locked: {names}. Commands run without them until you unlock them.', { names: names.join(', ') })
      : t('No secrets yet. Unlock to add some.');
    return [
      settingsSection(t('Unlock'), hint, form),
      v.error ? h('p', { class: 'danger' }, v.error) : null,
      settingsSection(t('Forgot the password?'), null,
        preferenceRow(t('Start over'), h('p', null, t('A forgotten password cannot be recovered. To start over, quit yesman and move this folder away; your secrets stay in it, encrypted.')),
          revealButton(dirName(v.file), t('Secrets')))),
    ];
  }

  // ---------- Back up ----------

  const smallButton = (label, onclick) => h('button', { class: 'mds-button mds-button--small', type: 'button', onclick }, label);

  function secretSections(v) {
    return [
      settingsSection(t('Back up'), t('Keep a copy of your secrets, or carry them to another computer.'),
        preferenceRow(t('Export encrypted'), h('p', null, t('A YAML file sealed with a password of its own. yesman imports it, and age -d opens it.')), smallButton(t('Export…'), exportSealed)),
        preferenceRow(t('Export as plain text'), h('p', null, t('A YAML file that anyone who reads it can use. Delete it once you are done with it.')), smallButton(t('Export…'), exportPlain)),
        preferenceRow(t('Import'), h('p', null, t('A file exported here, encrypted or not. You see what it changes, then merge it or replace with it.')), smallButton(t('Import…'), importFile))),
      settingsSection(t('Password and key'), null,
        preferenceRow(t('Password'), h('p', null, t('The secrets stay as they are; the new password opens them from then on.')), smallButton(t('Change…'), changePassword)),
        preferenceRow(t('Key for sops'), h('p', null, t('The age key that opens {file}, to edit it with sops: SOPS_AGE_KEY_FILE=key.txt sops edit {file}. Anyone with the key and the file reads your secrets.', { file: fileName(v.file) })),
          h('div', { class: 'storage-actions' }, revealButton(dirName(v.file), t('Secrets')), smallButton(t('Export key…'), exportKey)))),
    ];
  }

  function variableSections(v) {
    return [
      settingsSection(t('Back up'), t('Keep a copy of your variables, or carry them to another computer.'),
        preferenceRow(t('Export'), h('p', null, t('The variables as a YAML file.')), smallButton(t('Export…'), exportVariables)),
        preferenceRow(t('Import'), h('p', null, t('A file exported here. You see what it changes, then merge it or replace with it.')), smallButton(t('Import…'), importFile)),
        preferenceRow(t('File'), h('p', { class: 'path', title: v.file }, v.file), revealButton(v.file, t('the variables file')))),
    ];
  }

  // passwords asks for passwords in a dialog: run(values) does the work,
  // and its error shows there. It resolves to run's result, or undefined
  // when cancelled.
  function passwords({ title, hint, fields, ok, run }) {
    const dialog = $('#env-dialog');
    const error = h('p', { class: 'danger env-dialog-error', role: 'alert', hidden: true });
    const inputs = fields.map((f) => h('input', { class: 'mds-input', type: 'password', name: f.name, required: true, autocomplete: f.autocomplete ?? 'off' }));
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, ok);
    let result;
    dialog.replaceChildren(h('form', { onsubmit: async (e) => {
      e.preventDefault();
      const values = Object.fromEntries(fields.map((f, i) => [f.name, inputs[i].value]));
      submit.disabled = true;
      error.hidden = true;
      try {
        result = await run(values);
        dialog.close('ok');
      } catch (err) {
        error.textContent = errorText(err);
        error.hidden = false;
        inputs[0].select();
      } finally {
        submit.disabled = false;
      }
    } },
    h('h2', { class: 'mds-heading', id: 'env-dialog-title' }, title),
    hint ? h('p', { class: 'dialog-hint' }, hint) : null,
    fields.map((f, i) => h('label', { class: 'field' }, h('span', null, f.label), inputs[i])),
    error,
    h('div', { class: 'mds-dialog-actions' },
      h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close('cancel') }, t('Cancel')), submit)));
    const closed = openDialog(dialog);
    inputs[0].focus();
    return closed.then((answer) => {
      for (const input of inputs) input.value = '';
      return answer === 'ok' ? result : undefined;
    });
  }

  const same = (a, b) => {
    if (a !== b) throw new Error(t('The two passwords differ.'));
  };

  async function changePassword() {
    const done = await passwords({
      title: t('Change the password'),
      fields: [
        { name: 'current', label: t('Current password'), autocomplete: 'current-password' },
        { name: 'password', label: t('New password'), autocomplete: 'new-password' },
        { name: 'again', label: t('New password again'), autocomplete: 'new-password' },
      ],
      ok: t('Change password'),
      run: async (v) => {
        same(v.password, v.again);
        await call('ChangeSecretsPassword', v.current, v.password);
        return true;
      },
    });
    if (done) toast(t('The password is changed.'));
  }

  function exported(result) {
    if (!result.path) return;
    toast(t('Exported {what} to {path}.', { what: count(result.count), path: result.path }), { duration: 6000 });
  }

  async function exportSealed() {
    if (dirty()) toast(t('Changes not saved are left out.'));
    const password = await passwords({
      title: t('Export encrypted'),
      hint: t('Choose a password for the file. Importing it asks for it, and so does age -d.'),
      fields: [
        { name: 'password', label: t('Password for the file'), autocomplete: 'new-password' },
        { name: 'again', label: t('Password again'), autocomplete: 'new-password' },
      ],
      ok: t('Choose where…'),
      run: async (v) => {
        same(v.password, v.again);
        if ([...v.password].length < 8) throw new Error(t('The password should have at least 8 characters.'));
        return v.password;
      },
    });
    if (!password) return;
    try {
      exported(await call('ExportSecrets', password));
    } catch (err) {
      fail(err);
    }
  }

  async function exportPlain() {
    if (!(await confirm(t('Export secrets as plain text?'), t('Anyone who reads the file can use your secrets. Keep it somewhere safe, and delete it once you no longer need it.'), t('Export…')))) return;
    try {
      exported(await call('ExportSecrets', ''));
    } catch (err) {
      fail(err);
    }
  }

  async function exportKey() {
    if (!(await confirm(t('Export the secrets key?'), t('With the key, anyone who has the secrets file reads your secrets, without the password. Keep it as safe as the password.'), t('Export…')))) return;
    try {
      const result = await call('ExportSecretsKey');
      if (result.path) toast(t('Exported the key to {path}.', { path: result.path }), { duration: 6000 });
    } catch (err) {
      fail(err);
    }
  }

  async function exportVariables() {
    if (dirty()) toast(t('Changes not saved are left out.'));
    try {
      exported(await call('ExportVariables'));
    } catch (err) {
      fail(err);
    }
  }

  async function importFile() {
    if (dirty()) {
      toast(t('Save or revert your changes first.'));
      return;
    }
    let file;
    let plan;
    let password = '';
    try {
      file = await call('ChooseEnvFile', kind);
      if (!file.path) return;
      if (file.encrypted) {
        plan = await passwords({
          title: t('Import {file}', { file: fileName(file.path) }),
          hint: t('This file is encrypted. Enter the password it was exported with.'),
          fields: [{ name: 'password', label: t('Password for the file'), autocomplete: 'off' }],
          ok: t('Open'),
          run: async (v) => {
            const p = await call('PreviewEnvImport', kind, file.path, v.password);
            password = v.password;
            return p;
          },
        });
        if (!plan) return;
      } else {
        plan = await call('PreviewEnvImport', kind, file.path, '');
      }
      const mode = await askImport(plan);
      if (mode !== 'merge' && mode !== 'replace') return;
      if (file.encrypted) toast(t('Importing…'));
      const n = await call('ImportEnv', kind, file.path, password, mode);
      toast(mode === 'replace'
        ? t('Replaced with {file}: {what} now.', { file: fileName(file.path), what: count(n) })
        : t('Merged {file}: {what} now.', { file: fileName(file.path), what: count(n) }), { duration: 6000 });
      accept(await call(secret ? 'Secrets' : 'Variables'));
      rerender();
    } catch (err) {
      fail(err);
    } finally {
      password = '';
    }
  }

  // askImport shows what importing would change, and resolves to merge,
  // replace or cancel.
  function askImport(plan) {
    const dialog = $('#import-dialog');
    const part = (title, ...children) => h('section', { class: 'import-part' }, h('h3', null, title), children);
    const names = (list) => h('p', null, h('span', { class: 'import-names mds-mono' }, list.join(', ')));
    const clash = plan.clash.length > 0;
    dialog.replaceChildren(h('form', { method: 'dialog' },
      h('h2', { class: 'mds-heading', id: 'import-title' }, t('Import {file}', { file: fileName(plan.path) })),
      h('p', { class: 'dialog-hint path', title: plan.path }, plan.path),
      h('div', { class: 'import-body' },
        clash ? h('p', { class: 'danger' }, secret
          ? t('{names} are variables already. Remove them from Variables, then import again.', { names: plan.clash.join(', ') })
          : t('{names} are secrets already. Remove them from Secrets, then import again.', { names: plan.clash.join(', ') })) : null,
        plan.add.length ? part(t('Adds ({n})', { n: plan.add.length }), names(plan.add)) : null,
        plan.change.length ? part(t('Changes ({n})', { n: plan.change.length }), names(plan.change)) : null,
        plan.same.length ? part(t('Unchanged ({n})', { n: plan.same.length }), names(plan.same)) : null,
        !plan.add.length && !plan.change.length && !plan.same.length ? h('p', null, secret ? t('The file lists no secrets.') : t('The file lists no variables.')) : null,
        plan.extra.length ? part(t('Only here ({n})', { n: plan.extra.length }),
          h('p', null, t('Merge keeps them. Replace removes them.')), names(plan.extra)) : null),
      h('div', { class: 'mds-dialog-actions' },
        h('button', { class: 'mds-button', value: 'cancel' }, t('Cancel')),
        !clash && plan.extra.length ? h('button', { class: 'mds-button', value: 'replace' }, t('Replace')) : null,
        clash ? null : h('button', { class: 'mds-button mds-button--primary', value: 'merge' }, t('Merge')))));
    const answer = openDialog(dialog);
    (dialog.querySelector('.mds-button--primary') ?? dialog.querySelector('.mds-button'))?.focus();
    return answer;
  }

  return { load, render, dirty: () => (s.view ? dirty() : false) };
}
