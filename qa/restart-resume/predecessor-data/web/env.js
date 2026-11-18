// Settings › Secrets and Settings › Variables: the environment variables
// every agent's commands get. A page edits its entries as a form or as
// YAML, which the app converts between, and saves them whole. Secrets are
// kept encrypted and open with a password; agents never read their values.
import { $, h, icon } from './dom.js';
import { initTabs, openDialog, toast } from './mds/js/magpie.js';
import { t, tn } from './i18n.js';
import { createEditor } from './editor.js';

const errorText = (err) => String(err?.message || err?.error || err);
const dirName = (path) => path.slice(0, Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\')));

// What each page says, in English: t says it in the page's language.
const WORDS = {
  secrets: {
    lead: 'Secrets are environment variables every agent’s commands get, such as API keys and tokens. They are kept encrypted with sops and age. Agents read a secret’s name and description with yesman_env, never its value: output shows it as [secret NAME].',
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
    // A variables file that does not read opens as its text, to mend.
    if (!secret && view.error && view.text) s.mode = 'yaml';
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
      focusOnce('setup', id(v.keychain ? 'setup-button' : 'setup-password'));
      return;
    }
    if (secret && !v.unlocked) {
      page.append(...unlockSection().filter(Boolean));
      focusOnce('unlock', id(v.stored ? 'unlock-button' : 'unlock-password'));
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
    else if (s.editing) reopenEditor();
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
    editing = null;
    s.editing = null;
    $(`#${id('list')}`)?.replaceChildren(...[rowEls()].flat());
  }

  // ---------- Cells ----------

  // An entry is a line of cells: its name, value and description, shown as
  // underlined text in columns that never move. Clicking one, or reaching
  // it with the keyboard, opens an editor of one width floating below it,
  // as tall as its text; leaving the editor closes it, and Escape undoes
  // the edit. A
  // secret's value shows as dots, and its editor alone has the eye that
  // shows or hides it.
  const FIELDS = {
    name: { line: true, mono: true, placeholder: () => 'NAME', label: () => t('Name') },
    value: { mono: true, placeholder: () => t('Value'), label: (n) => t('Value of {name}', { name: n }) },
    description: { placeholder: () => t(w.describe), label: (n) => t('Description of {name}', { name: n }) },
  };
  const editorWidth = 400; // an editor's width, whatever its text: it grows in height
  const entryLabel = (r) => r.name || t('this entry');
  const eyeLabel = (hidden, name) => (hidden ? t('Show the value of {name}', { name }) : t('Hide the value of {name}', { name }));
  let editing = null; // the editor open: { r, field, box, input }
  let quiet = false; // yesman moves the focus to a cell: open nothing

  function cellContent(r, field) {
    const v = r[field];
    if (!v) return h('span', { class: 'env-cell-empty' }, FIELDS[field].placeholder());
    return secret && field === 'value' ? '••••••••••••' : v;
  }

  function cellEl(r, field) {
    const f = FIELDS[field];
    return h('button', { class: `env-cell env-cell--${field}${f.mono ? ' mds-mono' : ''}`, type: 'button', id: id(field, r.id), 'data-row': r.id, 'data-field': field,
      'aria-label': secret && field === 'value' ? f.label(entryLabel(r)) : null,
      onclick: () => openEditor(r, field),
      onfocus: (e) => { if (!quiet && e.currentTarget.matches(':focus-visible')) openEditor(r, field); } }, cellContent(r, field));
  }

  function focusCell(el) {
    quiet = true;
    try { el?.focus(); } finally { quiet = false; }
  }

  function rowEl(r) {
    const label = entryLabel(r);
    const iconButton = (glyph, cls, title, onclick) => h('button', { class: `mds-icon-button mds-icon-button--compact ${cls}`, type: 'button', title, 'aria-label': title, onclick }, icon(glyph));
    return h('div', { class: 'env-row', role: 'listitem' },
      cellEl(r, 'name'), cellEl(r, 'value'), cellEl(r, 'description'),
      h('div', { class: 'env-row-actions' },
        iconButton('copy', 'env-copy', t('Copy the value of {name}', { name: label }), () => call('CopyValue', r.value).then(() => toast(t('Copied.')), fail)),
        iconButton('trash', 'env-remove', t('Remove {name}', { name: label }), () => removeRow(r))));
  }

  // relabel says an entry's new name in the labels of its row.
  function relabel(row, r) {
    const label = entryLabel(r);
    const set = (el, text) => { if (el) { el.title = text; el.setAttribute('aria-label', text); } };
    set(row.querySelector('.env-copy'), t('Copy the value of {name}', { name: label }));
    set(row.querySelector('.env-remove'), t('Remove {name}', { name: label }));
    if (secret) row.querySelector('.env-cell--value')?.setAttribute('aria-label', FIELDS.value.label(label));
  }

  function openEditor(r, field, caret) {
    if (editing?.r === r && editing.field === field && editing.box.isConnected) {
      editing.input.focus();
      return;
    }
    const cell = $(`#${id(field, r.id)}`);
    const row = cell?.closest('.env-row');
    const page = $(`#${prefix}-page`);
    const list = $(`#${id('list')}`);
    if (!row || !page || !list) return;
    const f = FIELDS[field];
    const before = r[field];
    const isSecret = secret && field === 'value';
    const props = { class: `${f.line ? 'mds-input' : 'mds-textarea'} env-cell-input${f.mono ? ' mds-mono' : ''}`,
      id: id(`${field}-edit`, r.id), placeholder: f.placeholder(), 'aria-label': f.label(entryLabel(r)),
      spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off' };
    const input = f.line ? h('input', props) : h('textarea', { ...props, rows: 1 });
    input.value = before;
    // A textarea is as tall as its lines, wrapped or not, up to eight.
    const fit = () => {
      if (f.line) return;
      const style = getComputedStyle(input);
      const border = parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth);
      const most = 8 * (parseFloat(style.lineHeight) || 20) + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + border;
      input.style.height = 'auto';
      const height = input.scrollHeight + border;
      input.style.height = `${Math.min(height, most)}px`;
      input.style.overflowY = height > most ? 'auto' : 'hidden';
    };
    const masked = () => isSecret && !s.shown.has(r.id);
    // Pressing the eye keeps the focus in the value.
    const eye = isSecret ? h('button', { class: 'mds-icon-button mds-icon-button--compact env-eye', type: 'button', 'aria-controls': input.id,
      onmousedown: (e) => e.preventDefault(),
      onclick: () => {
        if (s.shown.has(r.id)) s.shown.delete(r.id);
        else s.shown.add(r.id);
        paintEye();
      } }) : null;
    function paintEye() {
      input.classList.toggle('env-hidden', masked());
      if (!eye) return;
      const hidden = masked();
      const text = eyeLabel(hidden, entryLabel(r));
      eye.replaceChildren(icon(hidden ? 'eye' : 'eyeSlash'));
      eye.title = text;
      eye.setAttribute('aria-label', text);
      eye.setAttribute('aria-pressed', String(!hidden));
    }
    paintEye();
    // The editor floats below its cell, over what follows, and moves
    // nothing; the cell's text stays in view and follows the edit.
    const box = h('div', { class: `env-cell-edit${isSecret ? ' env-cell-edit--secret' : ''}`, role: 'group', 'aria-label': f.label(entryLabel(r)) }, input, eye);
    let done = false;
    const onResize = () => close(false);
    const close = (undo) => {
      if (done) return;
      done = true;
      if (undo) r[field] = before;
      if (editing?.box === box) { editing = null; s.editing = null; }
      if (isSecret) s.shown.delete(r.id); // shown again only by the eye
      window.removeEventListener('resize', onResize);
      cell.replaceChildren(cellContent(r, field));
      cell.classList.remove('env-cell--editing');
      box.remove();
      if (field === 'name') relabel(row, r);
      edited();
    };
    // Tab goes on to the next cell, or back, as a sheet does; a secret's
    // value goes to its eye first.
    const move = (step) => {
      const cells = [...list.querySelectorAll('.env-cell')];
      const next = cells[cells.indexOf(cell) + step];
      close(false);
      const nr = next && s.rows.find((x) => String(x.id) === next.dataset.row);
      if (nr) openEditor(nr, next.dataset.field);
      else focusCell(step > 0 ? $(`#${prefix}-page .env-add`) : cell);
    };
    input.addEventListener('input', () => {
      r[field] = input.value;
      fit();
      cell.replaceChildren(cellContent(r, field));
      s.editing = { rowId: r.id, field, caret: input.selectionStart };
      edited();
    });
    box.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' || (e.key === 'Enter' && f.line && e.target === input)) {
        e.preventDefault();
        e.stopPropagation();
        close(e.key === 'Escape');
        focusCell(cell);
      } else if (e.key === 'Tab') {
        if (eye && (e.shiftKey ? e.target === eye : e.target === input)) return; // between the value and its eye
        e.preventDefault();
        move(e.shiftKey ? -1 : 1);
      }
    });
    box.addEventListener('focusout', (e) => {
      if (box.isConnected && !box.contains(e.relatedTarget)) close(false);
    });
    cell.classList.add('env-cell--editing');
    page.append(box);
    const pageBox = page.getBoundingClientRect();
    const listBox = list.getBoundingClientRect();
    const cellBox = cell.getBoundingClientRect();
    const pad = 8;
    const width = Math.min(listBox.width - 2 * pad, editorWidth);
    const left = Math.max(listBox.left + pad, Math.min(cellBox.left, listBox.right - pad - width));
    Object.assign(box.style, { left: `${left - pageBox.left}px`, top: `${cellBox.bottom - pageBox.top + 4}px`, width: `${width}px` });
    fit();
    window.addEventListener('resize', onResize);
    editing = { r, field, box, input };
    s.editing = { rowId: r.id, field, caret: caret ?? before.length };
    input.focus();
    const at = Math.min(caret ?? input.value.length, input.value.length);
    input.setSelectionRange(at, at);
  }

  // A page drawn again opens the editor that was open.
  function reopenEditor() {
    const r = s.rows.find((x) => x.id === s.editing?.rowId);
    if (r) openEditor(r, s.editing.field, s.editing.caret);
    else s.editing = null;
  }

  function addRow() {
    const r = { id: nextId++, name: '', value: '', description: '' };
    s.rows.push(r);
    s.shown.add(r.id); // a value you type in shows
    paintList();
    paintActions();
    openEditor(r, 'name');
  }

  function removeRow(r) {
    const at = s.rows.indexOf(r);
    s.rows.splice(at, 1);
    paintList();
    paintActions();
    const next = s.rows[Math.min(at, s.rows.length - 1)];
    focusCell(next ? $(`#${id('name', next.id)}`) : $(`#${prefix}-page .env-add`));
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
        // A variables file that does not read keeps its text, unless the
        // form changed it.
        const broken = !secret && s.view?.error && JSON.stringify(strip(s.rows)) === s.saved;
        s.text = broken ? s.savedText : await call('FormatEntries', kind, strip(s.rows));
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
    if (s.view.keychain) {
      // A random password, which the keychain keeps.
      const error = h('p', { class: 'danger', role: 'alert', hidden: true });
      const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit', id: id('setup-button') }, t('Set up secrets'));
      return settingsSection(t('Set up secrets'),
        t('yesman makes a random password for them and keeps it in the system keychain, which unlocks them each time yesman starts. The exported configuration carries it, with the secrets.'),
        h('form', { class: 'env-lock', onsubmit: async (e) => {
          e.preventDefault();
          submit.disabled = true;
          error.hidden = true;
          try {
            accept(await call('SetUpSecrets', ''));
            rerender();
          } catch (err) {
            error.textContent = errorText(err);
            error.hidden = false;
          } finally {
            submit.disabled = false;
          }
        } }, error, h('div', null, submit)));
    }
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
    // The keychain keeps the password: a button unlocks them. Otherwise it
    // is typed, and the keychain, when there is one, keeps it then.
    const password = v.stored ? null : passwordInput(id('unlock-password'), 'current-password');
    const error = h('p', { class: 'danger', role: 'alert', id: id('unlock-error'), hidden: true });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit', id: id('unlock-button') }, t('Unlock'));
    const form = h('form', { class: 'env-lock', onsubmit: async (e) => {
      e.preventDefault();
      submit.disabled = true;
      error.hidden = true;
      try {
        accept(await call('UnlockSecrets', password?.value ?? ''));
        s.error = '';
        rerender();
      } catch (err) {
        error.textContent = errorText(err);
        error.hidden = false;
        password?.select();
      } finally {
        submit.disabled = false;
      }
    } },
    v.stored ? null : v.keychain ? h('p', null, t('The keychain has no password for these secrets. Enter it, from the configuration file you exported, and the keychain keeps it.')) : null,
    v.stored ? null : h('label', { class: 'field', for: id('unlock-password') }, t('Password')),
    v.stored ? h('div', null, submit) : h('div', { class: 'storage-input' }, password, submit),
    error);
    const names = v.names ?? [];
    const hint = names.length
      ? tn(names.length, '1 secret is locked: {names}. Commands run without it until you unlock it.', '{n} secrets are locked: {names}. Commands run without them until you unlock them.', { names: names.join(', ') })
      : t('No secrets yet. Unlock to add some.');
    return [
      settingsSection(t('Unlock'), hint, form),
      v.error ? h('p', { class: 'danger' }, v.error) : null,
      v.stored ? null : settingsSection(t('Forgot the password?'), null,
        preferenceRow(t('Start over'), h('p', null, t('A forgotten password cannot be recovered. To start over, quit yesman and move this folder away; your secrets stay in it, encrypted.')),
          revealButton(dirName(v.file), t('Secrets')))),
    ];
  }

  // ---------- Back up ----------

  const smallButton = (label, onclick) => h('button', { class: 'mds-button mds-button--small', type: 'button', onclick }, label);

  function secretSections(v) {
    return [
      settingsSection(t('Password'), t('Settings › Import and export backs the secrets up, with the rest of yesman’s configuration.'),
        v.stored
          ? preferenceRow(t('In the keychain'), h('p', null, t('Random, and kept in the system keychain, which unlocks the secrets when yesman starts. The exported configuration carries it.')),
            smallButton(t('Copy'), () => call('CopySecretsPassword').then(() => toast(t('Copied.')), fail)))
          : preferenceRow(t('Password'), h('p', null, t('The secrets stay as they are; the new password opens them from then on.')), smallButton(t('Change…'), changePassword))),
    ];
  }

  // Settings › Import and export backs the variables up.
  const variableSections = () => [];

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

  return { load, render, dirty: () => (s.view ? dirty() : false) };
}
