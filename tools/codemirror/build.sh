#!/bin/sh
# Rebuilds web/vendor/codemirror/ from entry.js and the exact versions in
# package.json. Needs npm; nothing of this runs in the app.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out="$here/../../web/vendor/codemirror"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$here/package.json" "$here/entry.js" "$work/"
(cd "$work" && npm install --ignore-scripts --no-audit --no-fund --silent)
rm -rf "$out"
mkdir -p "$out"
"$work/node_modules/.bin/esbuild" "$work/entry.js" --bundle --format=esm --splitting --minify \
  --outdir="$out" --entry-names=codemirror --chunk-names=chunks/[name]-[hash] --legal-comments=none
# Licenses of what the bundle includes.
for pkg in $(cd "$work/node_modules" && ls -d @codemirror/* @lezer/* style-mod w3c-keyname crelt @marijn/* 2>/dev/null); do
  [ -f "$work/node_modules/$pkg/LICENSE" ] && { echo "== $pkg"; cat "$work/node_modules/$pkg/LICENSE"; echo; }
done > "$out/LICENSES.txt"
printf 'CodeMirror 6, built by tools/codemirror/build.sh from its package.json\n' > "$out/VERSION"
