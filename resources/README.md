# Icons

`icon.svg` is the master artwork: a yak turning back to answer, in white on a sky-blue tile. Its margins are transparent. `icon.png` is the 1024px render MyGo packages as the macOS, Windows and Linux application icons.

`tray.svg` and its 36px render, `tray.png`, use the same outline without the tile. The generated `web/yak.svg` is the titlebar's theme-aware CSS mask. The black-and-transparent PNG is embedded as a template: MyGo tints it for the menu bar or taskbar's appearance on all three platforms.

To regenerate the PNGs and the tray SVG after editing the master, install the asset renderer (only needed for icon maintenance):

```bash
npm install --no-save --package-lock=false @resvg/resvg-js@2.6.2
```

```bash
node scripts/generate-icons.cjs
```

Node packages are not needed to run, test or package yesman. The script can also use a renderer installed outside the repository via Node's `NODE_PATH`.
