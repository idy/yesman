// What yesman's editor (web/editor.js) uses of CodeMirror. Languages load
// on demand, each as a chunk of its own.
import { StreamLanguage } from '@codemirror/language';

export { EditorState, Compartment, Transaction, Annotation } from '@codemirror/state';
export {
  EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection,
  highlightSpecialChars, rectangularSelection, crosshairCursor, dropCursor, highlightWhitespace,
} from '@codemirror/view';
export { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
export { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search';
export {
  syntaxHighlighting, HighlightStyle, indentOnInput, bracketMatching, foldGutter, foldKeymap, indentUnit,
} from '@codemirror/language';
export { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
export { tags } from '@lezer/highlight';

const legacy = (load, name) => () => load().then((m) => StreamLanguage.define(m[name]));

// languages maps yesman's language names to loaders of their support.
export const languages = {
  javascript: () => import('@codemirror/lang-javascript').then((m) => m.javascript({ jsx: true })),
  typescript: () => import('@codemirror/lang-javascript').then((m) => m.javascript({ typescript: true, jsx: true })),
  python: () => import('@codemirror/lang-python').then((m) => m.python()),
  go: () => import('@codemirror/lang-go').then((m) => m.go()),
  rust: () => import('@codemirror/lang-rust').then((m) => m.rust()),
  json: () => import('@codemirror/lang-json').then((m) => m.json()),
  markdown: () => import('@codemirror/lang-markdown').then((m) => m.markdown()),
  css: () => import('@codemirror/lang-css').then((m) => m.css()),
  html: () => import('@codemirror/lang-html').then((m) => m.html()),
  xml: () => import('@codemirror/lang-xml').then((m) => m.xml()),
  yaml: () => import('@codemirror/lang-yaml').then((m) => m.yaml()),
  sql: () => import('@codemirror/lang-sql').then((m) => m.sql()),
  cpp: () => import('@codemirror/lang-cpp').then((m) => m.cpp()),
  java: () => import('@codemirror/lang-java').then((m) => m.java()),
  php: () => import('@codemirror/lang-php').then((m) => m.php()),
  shell: legacy(() => import('@codemirror/legacy-modes/mode/shell'), 'shell'),
  toml: legacy(() => import('@codemirror/legacy-modes/mode/toml'), 'toml'),
  ini: legacy(() => import('@codemirror/legacy-modes/mode/properties'), 'properties'),
  dockerfile: legacy(() => import('@codemirror/legacy-modes/mode/dockerfile'), 'dockerFile'),
  ruby: legacy(() => import('@codemirror/legacy-modes/mode/ruby'), 'ruby'),
  swift: legacy(() => import('@codemirror/legacy-modes/mode/swift'), 'swift'),
  lua: legacy(() => import('@codemirror/legacy-modes/mode/lua'), 'lua'),
  perl: legacy(() => import('@codemirror/legacy-modes/mode/perl'), 'perl'),
  r: legacy(() => import('@codemirror/legacy-modes/mode/r'), 'r'),
  diff: legacy(() => import('@codemirror/legacy-modes/mode/diff'), 'diff'),
  protobuf: legacy(() => import('@codemirror/legacy-modes/mode/protobuf'), 'protobuf'),
  kotlin: legacy(() => import('@codemirror/legacy-modes/mode/clike'), 'kotlin'),
  csharp: legacy(() => import('@codemirror/legacy-modes/mode/clike'), 'csharp'),
  objectivec: legacy(() => import('@codemirror/legacy-modes/mode/clike'), 'objectiveC'),
  powershell: legacy(() => import('@codemirror/legacy-modes/mode/powershell'), 'powerShell'),
  nginx: legacy(() => import('@codemirror/legacy-modes/mode/nginx'), 'nginx'),
};
