# Interface icons

yesman's app-owned navigation, action and status icons use **Phosphor** through
`icon(name)` in `web/dom.js`: **Regular** by default and the official **Fill**
weight for the solid rounded Stop square. There is no separate “Phor” dependency in
this repository; the existing Magpie icon manifest names Phosphor explicitly.

The local `phosphor-regular.woff2` is the same file already shipped by the website
SDK, byte for byte. It matches `src/regular/Phosphor.woff2` from
`@phosphor-icons/web@2.1.2`. The accompanying `phosphor-fill.woff2` comes from
`src/fill/Phosphor-Fill.woff2` in that same archive; only `stop` selects this
weight, explicitly via `data-icon-weight="fill"`. This preserves its solid
rounded-square meaning without custom geometry. `manifest.json` records the
upstream archive, npm integrity, each font's SHA-256 and semantic alias →
upstream name/codepoint/weight mapping.
The upstream MIT license is in `LICENSE`. Go's `//go:embed all:web` packages the
font, mapping and license, so the desktop app never needs a font CDN or npm at
runtime. `web/icons.css` loads the font and overrides Magpie's app controls
without modifying the vendored design system. The website retains its SDK's
local Phosphor font and license.

## Adding an icon

Use `icon('semanticName')`; preserve the surrounding button's translated label
or `aria-label`. All helper glyphs are decorative (`aria-hidden="true"`). Add
new aliases and their exact Regular codepoints to both `dom.js` and the manifest
using the pinned upstream CSS. Do not paste SVG paths, Unicode/emoji symbols or
invent a glyph by styling text. Keep real text, Git status letters and keyboard
shortcut keycaps as text. When changing an icon's width/height in CSS, set its
font-size to the same size. Busy spinners and status dots use that same font;
their generated CSS content has an empty accessibility alternative.

## Scope and intentional exceptions

The audit covered sidebar, titlebar, composer, settings (crew, repositories,
Secrets/Variables, storage, pi, import/export, about), file menus/tree/tabs,
changes, info, board, tool results and website navigation. The previous desktop
helper combined Phosphor SVG paths and Magpie's vendored Phosphor Regular
`@phosphor-icons/core@2.1.1` sprite, plus a custom 24px Enter arrow; CSS drew
status/dirty dots. Those app-owned icons now use the local font.

- Native HTML selects keep Magpie's local Phosphor SVG caret as their fallback;
  CSS pseudo-elements cannot be placed inside a native select reliably. The
  composer's enhanced select caret and selected-option check use the font.
  Both the custom Model picker and enhanced Effort select render exactly one
  decorative font caret. SVG backgrounds are suppressed only on controls
  explicitly marked `mds-select--font-caret`; unconverted/native controls keep
  their existing fallback.
  Unused Magpie sprite/standalone assets remain intact for vendor compatibility.
- The context usage ring is a quantitative chart with a live fraction, not an
  interchangeable icon. Storage bars are charts too.
- Yak application/window/tray/notification branding (`resources/`, `icons.go`,
  `web/yak.svg`, `site/icon.svg`) stays as authored artwork. Native menus and
  notifications use OS/MyGo controls or app branding, not web icon fonts.
- Attachments, rendered Markdown/document images and package README imagery
  are user or external content. CodeMirror's vendored editor decorations and
  native control affordances remain owned by those components.
- Git's A/U/M/D/R/C/T badges, diff +/- counts, directory-tree documentation,
  punctuation, password masking and keyboard shortcut labels remain real text.

The older `web/mds/icons/README.md` describes the vendored Magpie SVG assets;
it is not the app's rendering policy. Keep branding separate from interface
icons and retain both projects' license files when updating resources.
