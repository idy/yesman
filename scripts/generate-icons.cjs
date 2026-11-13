// Asset maintenance only; running or building yesman needs no Node packages.
// Install @resvg/resvg-js@2.6.2, then run: node scripts/generate-icons.cjs
const fs = require('node:fs');
const path = require('node:path');
const { Resvg } = require('@resvg/resvg-js');

const resources = path.join(__dirname, '..', 'resources');
const source = fs.readFileSync(path.join(resources, 'icon.svg'), 'utf8');
const defs = source.match(/<defs>[\s\S]*?<\/defs>/);
const yak = source.match(/<g id="yak"[^>]*>([\s\S]*)<\/g>\s*<\/svg>/);
if (!defs || !yak) throw new Error('icon.svg must contain defs and the yak group');

// Fill the tray canvas with the same mark, without the app icon's blue tile.
// A black template keeps alpha only; MyGo tints it for the system's theme.
const tray = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="120 120 784 784">
  ${defs[0]}
  <g fill="#000">${yak[1]}</g>
</svg>
`;
fs.writeFileSync(path.join(resources, 'tray.svg'), tray);
fs.writeFileSync(path.join(__dirname, '..', 'web', 'yak.svg'), tray);
for (const [name, svg, width] of [['icon', source, 1024], ['tray', tray, 36]]) {
  const image = new Resvg(svg, { fitTo: { mode: 'width', value: width } });
  fs.writeFileSync(path.join(resources, `${name}.png`), image.render().asPng());
}
