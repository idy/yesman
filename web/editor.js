// The files pane's editor: CodeMirror 6, vendored in vendor/codemirror/
// (built by tools/codemirror/build.sh), themed with Magpie's tokens. It
// draws only the lines in view, so large files stay quick.
//
// One view shows one file at a time; each open file keeps its own state,
// with its undo history and selection, while it has a tab.

let cm = null;
async function load() {
  cm ??= await import('./vendor/codemirror/codemirror.js');
  return cm;
}

function theme() {
  const { EditorView } = cm;
  return EditorView.theme({
    '&': { height: '100%', color: 'var(--mds-ink)', backgroundColor: 'var(--app-pane)', fontSize: 'var(--mds-text-code)' },
    '&.cm-focused': { outline: 'none' },
    '.cm-scroller': { fontFamily: 'var(--mds-mono)', lineHeight: '1.6', overscrollBehavior: 'contain' },
    '.cm-content': { padding: '8px 0 40vh', caretColor: 'var(--mds-ink)' },
    '.cm-line': { padding: '0 20px 0 4px' },
    '.cm-gutters': { backgroundColor: 'var(--app-pane)', color: 'var(--mds-faint)', border: 'none' },
    '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 14px', fontVariantNumeric: 'tabular-nums' },
    '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--mds-ink) 4%, transparent)' },
    '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--mds-ink-subtle)' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--mds-ink)' },
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'var(--mds-selected)' },
    '.cm-selectionMatch': { backgroundColor: 'color-mix(in srgb, var(--mds-accent) 14%, transparent)' },
    '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': { backgroundColor: 'color-mix(in srgb, var(--mds-accent) 18%, transparent)', outline: 'none' },
    '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--mds-warn) 22%, transparent)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'color-mix(in srgb, var(--mds-warn) 42%, transparent)' },
    '.cm-foldGutter .cm-gutterElement': { color: 'var(--mds-faint)', padding: '0 4px' },
    '.cm-foldPlaceholder': { backgroundColor: 'var(--mds-raised)', border: 'none', color: 'var(--mds-muted)' },
    '.cm-panels': { backgroundColor: 'var(--mds-sidebar)', color: 'var(--mds-ink)' },
    '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--mds-line)' },
    '.cm-panel.cm-search': { padding: '6px 10px', fontFamily: 'var(--mds-font)', fontSize: 'var(--mds-text-caption)' },
    '.cm-panel.cm-search input, .cm-panel.cm-search button': { font: 'inherit', borderRadius: 'var(--mds-radius-xs)' },
    '.cm-textfield': { border: '1px solid var(--mds-line-strong)', backgroundColor: 'var(--mds-field)', color: 'var(--mds-ink)' },
    '.cm-button': { border: '1px solid var(--mds-line)', backgroundImage: 'none', backgroundColor: 'var(--mds-surface)', color: 'var(--mds-ink)' },
    '.cm-tooltip': { border: '1px solid var(--mds-line)', backgroundColor: 'var(--mds-surface)' },
  });
}

function highlight() {
  const { HighlightStyle, syntaxHighlighting, tags: t } = cm;
  return syntaxHighlighting(HighlightStyle.define([
    { tag: [t.keyword, t.controlKeyword, t.moduleKeyword, t.operatorKeyword, t.definitionKeyword, t.modifier], color: 'var(--syn-keyword)' },
    { tag: [t.string, t.special(t.string), t.regexp, t.character, t.url, t.escape], color: 'var(--syn-string)' },
    { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
    { tag: [t.typeName, t.className, t.namespace, t.standard(t.typeName)], color: 'var(--syn-type)' },
    { tag: [t.function(t.variableName), t.function(t.propertyName), t.macroName, t.propertyName, t.attributeName, t.labelName], color: 'var(--syn-func)' },
    { tag: [t.tagName, t.angleBracket], color: 'var(--syn-tag)' },
    { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: 'var(--syn-comment)' },
    { tag: [t.meta, t.processingInstruction, t.annotation], color: 'var(--syn-meta)' },
    { tag: t.heading, color: 'var(--syn-func)', fontWeight: '600' },
    { tag: t.emphasis, fontStyle: 'italic' },
    { tag: t.strong, fontWeight: '600' },
    { tag: t.link, color: 'var(--syn-string)', textDecoration: 'underline' },
    { tag: t.inserted, color: 'var(--mds-good)' },
    { tag: [t.deleted, t.invalid], color: 'var(--mds-error)' },
  ]));
}

// createEditor makes the editor. onChange(key) follows edits of a file;
// onSave(key) asks to save it.
export async function createEditor({ onChange, onSave }) {
  await load();
  const {
    EditorState, EditorView, Compartment, Transaction, Annotation, keymap, lineNumbers, highlightActiveLine,
    highlightActiveLineGutter, drawSelection, highlightSpecialChars, rectangularSelection, crosshairCursor, dropCursor,
    history, defaultKeymap, historyKeymap, indentWithTab, search, searchKeymap, highlightSelectionMatches,
    indentOnInput, bracketMatching, foldGutter, foldKeymap, closeBrackets, closeBracketsKeymap, languages,
  } = cm;
  const looks = [theme(), highlight()];
  // fromDisk marks the changes replace makes: not edits, and not undone.
  const fromDisk = Annotation.define();
  const states = new Map(); // key -> EditorState
  const language = new Compartment();
  const wrapping = new Compartment();
  const editable = new Compartment();
  const readOnlyOf = (on) => [EditorState.readOnly.of(on), EditorView.editable.of(!on)];
  let wrap = true;
  let key = null;

  const view = new EditorView({ state: EditorState.create({ doc: '', extensions: looks }) });
  // Where each file was scrolled: moving the editor between panes, or
  // showing another file, would lose it.
  const tops = new Map();
  // A hidden window draws no frames, and CodeMirror measures lines on one:
  // measure again once it shows, or the gutter keeps estimated heights.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && view.dom.isConnected) view.requestMeasure();
  });
  view.scrollDOM.addEventListener('scroll', () => {
    if (key !== null && view.dom.isConnected) tops.set(key, view.scrollDOM.scrollTop);
  }, { passive: true });

  function extensions(k, readOnly) {
    return [
      lineNumbers(), foldGutter(), highlightActiveLineGutter(), highlightSpecialChars(), history(), drawSelection(), dropCursor(),
      EditorState.allowMultipleSelections.of(true), indentOnInput(), bracketMatching(), closeBrackets(),
      rectangularSelection(), crosshairCursor(), highlightActiveLine(), highlightSelectionMatches(), search({ top: true }),
      keymap.of([
        { key: 'Mod-s', preventDefault: true, run: () => { onSave(k); return true; } },
        ...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, indentWithTab,
      ]),
      looks,
      language.of([]),
      wrapping.of(wrap ? EditorView.lineWrapping : []),
      editable.of(readOnlyOf(readOnly)),
      EditorView.updateListener.of((u) => {
        if (u.docChanged && !u.transactions.every((tr) => tr.annotation(fromDisk))) onChange(k);
      }),
      EditorView.contentAttributes.of({ 'aria-label': `Contents of ${k}` }),
    ];
  }

  // update applies a transaction to file k, shown or not.
  function update(k, spec) {
    if (k === key) view.dispatch(spec);
    else if (states.has(k)) states.set(k, states.get(k).update(spec).state);
  }

  async function setLanguage(k, lang) {
    const loader = languages[lang];
    if (!loader) return;
    try {
      update(k, { effects: language.reconfigure(await loader()) });
    } catch {
      // Without its language the file shows plain.
    }
  }

  return {
    dom: view.dom,
    // show shows file k, made from text the first time, in place of the
    // file shown before.
    show(k, { text, lang, readOnly }) {
      if (key !== null && key !== k && states.has(key)) states.set(key, view.state);
      let state = states.get(k);
      const fresh = !state;
      if (fresh) {
        state = EditorState.create({ doc: text, extensions: extensions(k, readOnly) });
        states.set(k, state);
      }
      if (key !== k) {
        key = k;
        view.setState(state);
      }
      const effects = [wrapping.reconfigure(wrap ? EditorView.lineWrapping : [])];
      if (view.state.facet(EditorState.readOnly) !== readOnly) effects.push(editable.reconfigure(readOnlyOf(readOnly)));
      view.dispatch({ effects });
      if (fresh) setLanguage(k, lang);
      view.requestMeasure();
      const top = tops.get(k) ?? 0;
      requestAnimationFrame(() => { if (key === k) view.scrollDOM.scrollTop = top; });
    },
    text(k) {
      if (k === key) return view.state.doc.toString();
      return states.get(k)?.doc.toString() ?? null;
    },
    has: (k) => states.has(k),
    // replace puts new text from disk in file k, changing only what
    // differs, so that the selection and scroll stay where they were.
    replace(k, text) {
      const state = k === key ? view.state : states.get(k);
      if (!state) return;
      const old = state.doc.toString();
      if (old === text) return;
      let from = 0;
      while (from < old.length && from < text.length && old[from] === text[from]) from++;
      let oldTo = old.length;
      let newTo = text.length;
      while (oldTo > from && newTo > from && old[oldTo - 1] === text[newTo - 1]) { oldTo--; newTo--; }
      update(k, {
        changes: { from, to: oldTo, insert: text.slice(from, newTo) },
        annotations: [fromDisk.of(true), Transaction.addToHistory.of(false)],
      });
    },
    setReadOnly: (k, on) => update(k, { effects: editable.reconfigure(readOnlyOf(on)) }),
    setWrap(on) {
      wrap = on;
      if (key !== null) view.dispatch({ effects: wrapping.reconfigure(on ? EditorView.lineWrapping : []) });
    },
    forget(k) {
      states.delete(k);
      tops.delete(k);
      if (key === k) key = null;
    },
    focus: () => view.focus(),
  };
}
