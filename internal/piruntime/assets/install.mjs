// Installs only inside the application-owned staging directory passed by Go.
// npm verifies every tarball against package-lock integrity; lifecycle scripts
// are disabled. Source patches are exact SHA256-checked build steps, not hooks.
import { readFile, writeFile, copyFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('.', import.meta.url));
const sha = (s) => createHash('sha256').update(s).digest('hex');
const [major, minor] = process.versions.node.split('.').map(Number);
if (major < 22 || major === 22 && minor < 19) throw new Error('yesman requires Node.js 22.19 or later');
const patchOnly = process.argv.includes('--patch-only');
function npmCommand(command) {
  // Invoke npm with this same Node, with an explicit local prefix. User npm
  // defaults (including global=true) cannot redirect this build to global pi.
  const npm = process.env.YESMAN_RUNTIME_NPM;
  if (!npm) throw new Error('npm was not found; install Node.js with npm');
  const result = spawnSync(process.execPath, [npm, command, '--prefix', root, '--global=false', '--ignore-scripts', '--no-audit', '--no-fund'], {
    cwd: root, stdio: ['ignore', 'ignore', 'inherit'], env: process.env,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Pinned pi ${command} failed (${result.status})`);
}
if (!patchOnly) npmCommand('ci');
const manifest = JSON.parse(await readFile(resolve(root, 'patch.json'), 'utf8'));
for (const patch of manifest.patches) {
  const path = resolve(root, 'node_modules', patch.path);
  let source = await readFile(path, 'utf8');
  if (sha(source) !== patch.before) throw new Error(`Unexpected pi source: ${patch.path}`);
  for (const { before, after } of patch.edits) {
    if (source.split(before).length !== 2) throw new Error(`Patch anchor mismatch: ${patch.path}`);
    source = source.replace(before, after);
  }
  if (sha(source) !== patch.after) throw new Error(`Patched source integrity failed: ${patch.path}`);
  await writeFile(path, source);
}
await copyFile(resolve(root, 'pending-queue.js'), resolve(root, 'node_modules/@earendil-works/pi-agent-core/dist/pending-queue.js'));
await writeFile(resolve(root, 'ready.json'), JSON.stringify({ version: '1.5.2-yesman.5',
  lock: sha(await readFile(resolve(root, 'package-lock.json'))), patch: sha(await readFile(resolve(root, 'patch.json'))),
  queue: sha(await readFile(resolve(root, 'pending-queue.js'))) })+'\n');
