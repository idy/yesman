// The page's DOM helpers: h builds elements, icon renders local Phosphor glyphs.

export const $ = (selector, root = document) => root.querySelector(selector);

export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(props ?? {})) {
    if (value == null || value === false) continue;
    if (key === 'class') el.className = value;
    else if (key === 'html') el.innerHTML = value; // only rendered Markdown, which escapes its input
    else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
    else if (value === true) el.setAttribute(key, '');
    else el.setAttribute(key, value);
  }
  for (const child of children.flat(Infinity)) {
    if (child == null || child === false) continue;
    el.append(child instanceof Node ? child : String(child));
  }
  return el;
}

// Semantic aliases keep callers independent of Phosphor's upstream names.
// Source, version and codepoints: icons/manifest.json. All glyphs are decorative;
// the surrounding control or status supplies its accessible name.
export const ICONS = Object.freeze({
  link: '\ue2e2', // link
  globe: '\ue288', // globe
  route: '\ue39c', // path
  device: '\ue560', // desktop
  code: '\ue1bc', // code
  copy: '\ue1ca', // copy
  reload: '\ue036', // arrow-clockwise
  check: '\ue182', // check
  file: '\ue23a', // file-text
  lock: '\ue308', // lock-simple
  up: '\ue08e', // arrow-up
  x: '\ue4f6', // x
  alert: '\ue4e0', // warning
  info: '\ue2ce', // info
  palette: '\ue6c8', // palette
  type: '\ue48a', // text-t
  toggle: '\ue676', // toggle-right
  layout: '\ueab6', // sidebar
  bell: '\ue0ce', // bell
  minimize: '\ue32a', // minus
  maximize: '\ue45e', // square
  'caret-down': '\ue136', // caret-down
  spinner: '\ueb44', // circle-notch
  send: '\ue08e', // arrow-up
  stop: '\ue46c', // stop, Fill weight (solid rounded square)
  folder: '\ue24a', // folder
  panel: '\ueab6', // sidebar
  power: '\ue3da', // power
  gear: '\ue270', // gear
  sliders: '\ue432', // sliders
  about: '\ue2ce', // info
  branch: '\ue278', // git-branch
  search: '\ue30c', // magnifying-glass
  plus: '\ue3d4', // plus
  chevron: '\ue13a', // caret-right
  fileText: '\ue23a', // file-text
  fileCode: '\ue914', // file-code
  image: '\ue2ca', // image
  tree: '\ue2f0', // list
  collapse: '\ue532', // arrows-in-line-vertical
  expand: '\ue0a6', // arrows-out-simple
  restore: '\ue09e', // arrows-in-simple
  transfer: '\ue098', // arrows-down-up
  package: '\ue390', // package
  trash: '\ue4a6', // trash
  external: '\ue5de', // arrow-square-out
  eye: '\ue220', // eye
  eyeSlash: '\ue224', // eye-slash
  more: '\ue1fe', // dots-three
  users: '\ue4d6', // users
  listDashes: '\ue2f4', // list-dashes
  book: '\ue0e6', // book-open
  arrowLeft: '\ue058', // arrow-left
  pullRequest: '\ue282', // git-pull-request
  merge: '\ue280', // git-merge
  listChecks: '\ueadc', // list-checks
  enter: '\ue018', // arrow-bend-down-left
  circle: '\ue18a', // circle
  record: '\ue3ee', // record
  pencil: '\ue3b4', // pencil-simple
});

export function icon(name, cls = 'mds-icon') {
  if (!ICONS[name]) throw new Error(`Unknown UI icon: ${name}`);
  return h('span', { class: cls, 'aria-hidden': 'true', 'data-icon': name, 'data-glyph': ICONS[name], 'data-icon-weight': name === 'stop' ? 'fill' : null });
}
