// Runs the installed pi runtime's own write and edit tools, as a worker's
// tools run them, with yesman's managed file operations (pi/yesman.ts
// managedFiles) against a running yesman API (started by
// internal/server/files_pi_test.go):
//
//   1. A write to a file outside the worker's grants: refused, the file
//      unchanged.
//   2. An edit whose file changed after pi read it: refused, the other
//      program's content kept.
//   3. Ordinary write and edit keep CRLF and the file's mode.
//
// Environment: YESMAN_TEST_RUNTIME, YESMAN_API, YESMAN_TOKEN, SCRATCH (the
// worker process's scratch directory), OUTSIDE (a directory outside the
// workspace holding a file named sentinel).
import assert from 'node:assert/strict';
import { chmodSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { join, sep } from 'node:path';
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

// 1. A file outside every grant is not written.
await assert.rejects(pi.createWriteToolDefinition(SCRATCH, { operations: managedFiles() }).execute('1', { path: sentinel, content: 'CHANGED' }, undefined, undefined, ctx));
assert.equal(readFileSync(sentinel, 'utf8'), original);
results.outsideRefused = true;

// 2. An edit of a file another program changed after pi read it.
const file = join(SCRATCH, 'edit.txt');
writeFileSync(file, 'alpha\r\nbeta\r\n');
chmodSync(file, 0o640);
const ops = managedFiles();
const changedAfterRead = { ...ops, readFile: async (p) => { const data = await ops.readFile(p); writeFileSync(p, 'alpha\r\nchanged elsewhere\r\n'); return data; } };
await assert.rejects(pi.createEditToolDefinition(SCRATCH, { operations: changedAfterRead }).execute('2', { path: file, edits: [{ oldText: 'beta', newText: 'gamma' }] }, undefined, undefined, ctx));
assert.equal(readFileSync(file, 'utf8'), 'alpha\r\nchanged elsewhere\r\n');
results.staleEditRefused = true;

// 3. Ordinary edit and write through the managed entry.
await pi.createEditToolDefinition(SCRATCH, { operations: managedFiles() }).execute('3', { path: file, edits: [{ oldText: 'changed elsewhere', newText: 'edited' }] }, undefined, undefined, ctx);
assert.equal(readFileSync(file, 'utf8'), 'alpha\r\nedited\r\n');
if (sep === '/') assert.equal(statSync(file).mode & 0o777, 0o640);
await pi.createWriteToolDefinition(SCRATCH, { operations: managedFiles() }).execute('4', { path: join(SCRATCH, 'deep', 'new.txt'), content: 'x\r\ny' }, undefined, undefined, ctx);
assert.equal(readFileSync(join(SCRATCH, 'deep', 'new.txt'), 'utf8'), 'x\r\ny');
results.ordinary = true;
console.log(JSON.stringify(results));
