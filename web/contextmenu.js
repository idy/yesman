// The page's context menu. yesman, not the web view, answers every
// right-click, Control-click on macOS and menu key (ContextMenu, Shift+F10):
// the web view's own menu, with Look Up, Translate, Search with Google,
// Share, Speech and Services on macOS, never shows.
//
// Every context menu goes one way. A menu key sends a contextmenu event to
// the focused element, as a right-click sends one to what it is on. Rows
// with menus of their own (the sidebar's agents and requests, the file
// tree) handle it first and prevent it, and the page leaves it alone: one
// menu opens. Anywhere else, on text, Markdown, links, code, fields,
// dialogs or empty space, the event reaches the window unprevented, and the
// page prevents it and shows its own menu: Copy for a selection, Open Link
// and Copy Link for a link, Cut, Copy, Paste and Select All in a field. The
// menu is native, drawn by Yesman.PageMenu as the rows' menus are, so focus,
// the selection and a draft stay as they were while it is open; app.go
// shows none for a context menu the page missed.
//
// Copy and Cut put the text the menu opened on through Yesman.CopyValue,
// the clipboard's bridge; Paste inserts the text PageMenu read, as typing
// does, with its undo. Nothing is copied out of a password or a secret's
// value. The keyboard's own ⌘/Ctrl+C, V, X and A are untouched.

// Inputs that hold no text.
const NOT_TEXT = new Set(['button', 'checkbox', 'color', 'file', 'hidden', 'image', 'radio', 'range', 'reset', 'submit']);

// createContextMenu makes the page's menu. call is Yesman's bridge; fail
// shows an error; editorOf(el) gives the CodeMirror view el is in.
export function createContextMenu({ call, fail, platform = 'mac', editorOf = () => null, win = window }) {
  const doc = win.document;
  let showing = false; // a menu is open: another only prevents the web view's
  let pressed = null; // a field's selection as a right button went down in it

  const fieldOf = (el) => {
    const f = el?.closest?.('input, textarea');
    return f && !(f.localName === 'input' && NOT_TEXT.has((f.getAttribute('type') || 'text').toLowerCase())) ? f : null;
  };
  // The editing host el is in, for editable content other than a field.
  const hostOf = (el) => {
    if (!el?.isContentEditable) return null;
    while (el.parentElement?.isContentEditable) el = el.parentElement;
    return el;
  };
  // A password, a secret's value (Settings › Secrets) or text drawn as
  // dots: nothing is copied out of it.
  const isSecret = (f) => f.type === 'password' || !!f.closest('.env-cell-edit--secret')
    || !['', 'none'].includes(win.getComputedStyle(f).getPropertyValue('-webkit-text-security'));
  const range = (f) => {
    try {
      return [f.selectionStart, f.selectionEnd, f.selectionDirection];
    } catch {
      return [null, null, null];
    }
  };
  const isRight = (e) => e.button === 2 || (platform === 'mac' && e.button === 0 && e.ctrlKey);

  // macOS WebKit selects the word under a right-click in a field before
  // the contextmenu event. Note the field's selection as the button goes
  // down, to keep the caret, the selection and Paste's place as they were.
  function onMouseDown(e) {
    pressed = null;
    if (!isRight(e)) return;
    const active = doc.activeElement;
    const field = fieldOf(active);
    if (field && field === fieldOf(e.target)) {
      const [start, end, dir] = range(field);
      if (start !== null) pressed = { el: field, start, end, dir };
      return;
    }
    const host = hostOf(active);
    const sel = win.getSelection();
    if (host && host.contains(e.target) && sel?.rangeCount) {
      pressed = { el: host, anchor: [sel.anchorNode, sel.anchorOffset], focus: [sel.focusNode, sel.focusOffset] };
    }
  }

  function restore(el) {
    const p = pressed;
    pressed = null;
    if (!p || p.el !== el) return;
    if ('start' in p) {
      const [start, end] = range(el);
      if (start !== p.start || end !== p.end) el.setSelectionRange(p.start, p.end, p.dir);
      return;
    }
    const sel = win.getSelection();
    if (!sel || !p.anchor[0].isConnected || !p.focus[0].isConnected) return;
    if (sel.anchorNode !== p.anchor[0] || sel.anchorOffset !== p.anchor[1] || sel.focusNode !== p.focus[0] || sel.focusOffset !== p.focus[1]) {
      sel.setBaseAndExtent(...p.anchor, ...p.focus);
    }
  }

  // linkOf returns the link el is in that the page opens, with what Copy
  // Link copies: a file's path, or the link's address.
  function linkOf(el) {
    const a = el?.closest?.('a[href]');
    if (!a) return null;
    const href = a.getAttribute('href');
    const value = a.dataset.path || (href && href !== '#' ? href : '');
    return value ? { el: a, value } : null;
  }

  // targetOf says what a context menu on el opened on, as it is now: the
  // selection's text, read once, which Copy and Cut copy whatever happens
  // while the menu is open.
  function targetOf(el) {
    const link = linkOf(el);
    const view = editorOf(el);
    if (view) {
      // The editor reads the page's selection after this: keep its own.
      const host = hostOf(el);
      if (host) restore(host);
      const state = view.state;
      const text = state.selection.ranges.filter((r) => !r.empty).map((r) => state.sliceDoc(r.from, r.to)).join(state.lineBreak);
      return { kind: 'editor', view, state, text, link, editable: true, readOnly: state.readOnly, content: state.doc.length > 0 };
    }
    const field = fieldOf(el);
    if (field) {
      restore(field);
      const secret = isSecret(field);
      const [start, end] = range(field);
      const selected = start !== null && end > start;
      return {
        kind: 'field', el: field, start, end, link: null, editable: true, secret, selection: selected,
        // A secret's text never leaves its field.
        text: selected && !secret ? field.value.slice(start, end) : '',
        value: secret ? null : field.value,
        readOnly: field.readOnly || field.disabled, content: field.value.length > 0,
      };
    }
    const host = hostOf(el);
    const sel = win.getSelection();
    if (host) {
      restore(host);
      const inside = sel?.rangeCount && host.contains(sel.getRangeAt(0).commonAncestorContainer);
      return {
        kind: 'host', el: host, link, editable: true, text: inside ? sel.toString() : '',
        range: inside ? sel.getRangeAt(0).cloneRange() : null, content: host.textContent.length > 0,
      };
    }
    // Text, Markdown, code or empty space. A focused password's selection
    // is not read through the page's.
    const active = fieldOf(doc.activeElement);
    const text = sel && !sel.isCollapsed && !(active && isSecret(active)) ? sel.toString() : '';
    return { kind: 'text', text, link, editable: false };
  }

  // Where a menu opened by a key shows: below the start of the selection,
  // or at the top left of the focused element, inside the window.
  function keyPoint(el) {
    const sel = win.getSelection();
    let r = null;
    if (sel?.rangeCount && !sel.isCollapsed && !fieldOf(el)) {
      const box = sel.getRangeAt(0).getBoundingClientRect();
      if (box.width || box.height) r = box;
    }
    r ??= el.getBoundingClientRect();
    const clamp = (v, most) => Math.round(Math.min(Math.max(v, 1), Math.max(most - 1, 1)));
    return [clamp(r.left + 12, win.innerWidth), clamp(Math.min(r.bottom, r.top + 24), win.innerHeight)];
  }

  async function show(target, x, y) {
    showing = true;
    try {
      const choice = await call('PageMenu', Math.round(x), Math.round(y), {
        link: !!target.link, editable: target.editable, readOnly: !!target.readOnly, secret: !!target.secret,
        selection: target.selection ?? target.text.length > 0, content: !!target.content,
      });
      await act(target, choice?.action ?? '', choice?.text ?? '');
    } catch (err) {
      fail(err);
    } finally {
      showing = false;
    }
  }

  const copy = (text) => call('CopyValue', text);

  // unchanged says the target still holds the selection the menu opened on.
  function unchanged(t) {
    if (t.kind === 'editor') return t.view.state.doc === t.state.doc && t.view.state.selection.eq(t.state.selection);
    if (t.kind === 'field') {
      const [start, end] = range(t.el);
      return t.el.isConnected && t.el.value === t.value && start === t.start && end === t.end;
    }
    return t.el.isConnected && !!t.range;
  }

  // writable says the target may still be changed.
  function writable(t) {
    if (!t.editable) return false;
    if (t.kind === 'editor') return !t.view.state.readOnly;
    return t.el.isConnected && !(t.kind === 'field' && (t.el.readOnly || t.el.disabled));
  }

  // insert puts text in place of the target's selection, as typing does:
  // with the field's input event and its undo.
  function insert(t, text, inputType) {
    if (t.kind === 'editor') {
      const { view } = t;
      view.dispatch(view.state.update(view.state.replaceSelection(text), { userEvent: inputType === 'deleteByCut' ? 'delete.cut' : 'input.paste', scrollIntoView: true }));
      view.focus();
      return;
    }
    t.el.focus({ preventScroll: true });
    if (t.kind === 'host' && t.range && inputType === 'deleteByCut') {
      const sel = win.getSelection();
      sel.removeAllRanges();
      sel.addRange(t.range);
    }
    const done = text ? doc.execCommand('insertText', false, text) : doc.execCommand('delete');
    if (done || t.kind !== 'field') return;
    const [start, end] = range(t.el);
    if (start === null) return;
    t.el.setRangeText(text, start, end, 'end');
    t.el.dispatchEvent(new InputEvent('input', { bubbles: true, inputType, data: text || null }));
  }

  async function act(t, action, pasted) {
    switch (action) {
      case 'copy':
        if (t.text) await copy(t.text);
        return;
      case 'cut':
        if (!t.text || t.readOnly || t.secret) return;
        await copy(t.text);
        // Only what was cut goes: not after the text has changed.
        if (writable(t) && unchanged(t)) insert(t, '', 'deleteByCut');
        return;
      case 'paste':
        if (!pasted || !writable(t)) return;
        insert(t, pasted, 'insertFromPaste');
        return;
      case 'select-all':
        if (t.kind === 'editor') {
          t.view.dispatch({ selection: { anchor: 0, head: t.view.state.doc.length }, userEvent: 'select' });
          t.view.focus();
        } else if (t.editable && t.el.isConnected) {
          t.el.focus({ preventScroll: true });
          if (t.kind === 'field') t.el.select();
          else win.getSelection().selectAllChildren(t.el);
        }
        return;
      case 'open-link':
        // The page's own click handlers open it, as a click does.
        if (t.link?.el.isConnected) t.link.el.click();
        return;
      case 'copy-link':
        if (t.link) await copy(t.link.value);
    }
  }

  function onContextMenu(e) {
    // A row's menu took it.
    if (e.defaultPrevented) return;
    e.preventDefault();
    if (showing) return;
    const el = e.target instanceof Element ? e.target : doc.body;
    show(targetOf(el), e.clientX, e.clientY);
  }

  // On macOS a menu key that nothing took opens the focused element's
  // menu, as a right-click on it would: its row's, or the page's. Windows'
  // and Linux's web views send the contextmenu event for these keys
  // themselves.
  function onKeyDown(e) {
    if (platform !== 'mac' || e.defaultPrevented || e.isComposing) return;
    if (e.key !== 'ContextMenu' && !(e.key === 'F10' && e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey)) return;
    e.preventDefault();
    if (showing) return;
    const active = doc.activeElement;
    const el = active instanceof Element && active !== doc.documentElement ? active : doc.body;
    const [clientX, clientY] = keyPoint(el);
    el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, composed: true, clientX, clientY }));
  }

  return {
    install() {
      win.addEventListener('mousedown', onMouseDown, true);
      win.addEventListener('keydown', onKeyDown);
      win.addEventListener('contextmenu', onContextMenu);
    },
    targetOf,
  };
}
