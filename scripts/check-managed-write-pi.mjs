// Runs the installed pi runtime's own write and edit tools, as a worker's
// tools run them, against a running yesman API (started by
// internal/server/files_pi_test.go), and shows why a hook's check before
// pi's write is not enough while the managed entry is:
//
//   1. pi's default file operations after a passing lstat check of every
//      parent: a parent swapped for a link in between writes outside (the
//      old behavior this change replaces; it must reproduce).
//   2. The same swap with yesman's managed operations (pi/yesman.ts
//      managedFiles): refused, outside unchanged.
//   3. An edit whose file changes between pi's read and its write: refused.
//   4. Ordinary write and edit keep CRLF and the file's mode.
//
// Environment: YESMAN_TEST_RUNTIME, YESMAN_API, YESMAN_TOKEN, SCRATCH (the
// worker process's scratch directory), OUTSIDE (a directory outside the
// workspace holding a file named sentinel).
import assert from 'node:assert/strict';
import { chmodSync, lstatSync, mkdirSync, readFileSync, renameSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { dirname, join, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { runInNewContext } from 'node:vm';

const { YESMAN_TEST_RUNTIME: runtime, YESMAN_API: API, YESMAN_TOKEN: TOKEN, SCRATCH, OUTSIDE } = process.env;
const pi = await import(pathToFileURL(join(runtime, 'node_modules', '@earendil-works', 'pi-coding-agent', 'dist', 'index.js')).href);
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'));
const slice = (from, to) => source.slice(source.indexOf(from), source.indexOf(to, source.indexOf(from)));
const managedFiles = runInNewContext(`${slice('async function call(', 'function result(')}\n${slice('function managedFiles(', '// refreshHidden')}\nmanagedFiles`, {
  API, TOKEN, fetch, Buffer, JSON, Map, Error,
});
const sentinel = join(OUTSIDE, 'sentinel');
const original = readFileSync(sentinel, 'utf8');
const ctx = { cwd: SCRATCH };
const results = {};

// Every parent below the scratch directory is a plain directory: the check
// a tool_call hook can make.
function preflight(path) {
  let cur = dirname(path);
  while (cur.length > SCRATCH.length) {
    const st = lstatSync(cur);
    assert.ok(st.isDirectory() && !st.isSymbolicLink(), `${cur} passes the check`);
    cur = dirname(cur);
  }
}
function swapForLink(dir) {
  renameSync(dir, `${dir}.before`);
  symlinkSync(OUTSIDE, dir, 'dir');
}

// 1. Old: check, then pi's own write.
mkdirSync(join(SCRATCH, 'old'), { recursive: true });
const oldTarget = join(SCRATCH, 'old', 'sentinel');
preflight(oldTarget);
swapForLink(join(SCRATCH, 'old'));
await pi.createWriteToolDefinition(SCRATCH).execute('1', { path: oldTarget, content: 'OLD ESCAPED' }, undefined, undefined, ctx);
results.oldEscaped = readFileSync(sentinel, 'utf8') === 'OLD ESCAPED';
assert.ok(results.oldEscaped, 'the unmanaged write reproduces the escape');
writeFileSync(sentinel, original);

// 2. New: the same swap with the managed operations.
mkdirSync(join(SCRATCH, 'new'), { recursive: true });
const newTarget = join(SCRATCH, 'new', 'sentinel');
preflight(newTarget);
swapForLink(join(SCRATCH, 'new'));
await assert.rejects(pi.createWriteToolDefinition(SCRATCH, { operations: managedFiles() }).execute('2', { path: newTarget, content: 'NEW ESCAPED' }, undefined, undefined, ctx));
assert.equal(readFileSync(sentinel, 'utf8'), original);
results.newRefused = true;

// 3. An edit racing another program's change.
const file = join(SCRATCH, 'edit.txt');
writeFileSync(file, 'alpha\r\nbeta\r\n');
chmodSync(file, 0o640);
const ops = managedFiles();
const racing = { ...ops, readFile: async (p) => { const data = await ops.readFile(p); writeFileSync(p, 'alpha\r\nchanged elsewhere\r\n'); return data; } };
await assert.rejects(pi.createEditToolDefinition(SCRATCH, { operations: racing }).execute('3', { path: file, edits: [{ oldText: 'beta', newText: 'gamma' }] }, undefined, undefined, ctx));
assert.equal(readFileSync(file, 'utf8'), 'alpha\r\nchanged elsewhere\r\n');
results.staleEditRefused = true;

// 4. Ordinary edit and write through the managed entry.
await pi.createEditToolDefinition(SCRATCH, { operations: managedFiles() }).execute('4', { path: file, edits: [{ oldText: 'changed elsewhere', newText: 'edited' }] }, undefined, undefined, ctx);
assert.equal(readFileSync(file, 'utf8'), 'alpha\r\nedited\r\n');
if (sep === '/') assert.equal(statSync(file).mode & 0o777, 0o640);
await pi.createWriteToolDefinition(SCRATCH, { operations: managedFiles() }).execute('5', { path: join(SCRATCH, 'deep', 'new.txt'), content: 'x\r\ny' }, undefined, undefined, ctx);
assert.equal(readFileSync(join(SCRATCH, 'deep', 'new.txt'), 'utf8'), 'x\r\ny');
results.ordinary = true;
console.log(JSON.stringify(results));
