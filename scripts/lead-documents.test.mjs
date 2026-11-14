import assert from 'node:assert/strict';
import { lstatSync, realpathSync, readFileSync, mkdtempSync, mkdirSync, writeFileSync, symlinkSync, linkSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { stripTypeScriptTypes } from 'node:module';
import { isAbsolute, relative, resolve, join } from 'node:path';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';

// Run the actual extension's hooks without pi or network access. Tool schemas
// and the shell factory are inert; these tests only exercise lead restrictions.
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'))
  .replace(/^import .*;\s*$/gm, '')
  .replace('export default function yesman', 'function yesman');

function extension(role, workspace, filesystem = { lstatSync, realpathSync }) {
  const handlers = new Map();
  let active = ['read', 'write', 'edit', 'bash'];
  const pi = {
    on(name, handler) {
      const list = handlers.get(name) ?? [];
      handlers.set(name, [...list, handler]);
    },
    registerTool() {},
    getAllTools: () => ['read', 'write', 'edit', 'bash', 'grep', 'find', 'ls'].map((name) => ({ name })),
    getActiveTools: () => active,
    setActiveTools: (names) => { active = names; },
  };
  const factory = () => ({});
  const Type = new Proxy({}, { get: () => factory });
  runInNewContext(`${source}\nyesman(pi);`, {
    process: { env: { YESMAN_API: 'http://unused', YESMAN_TOKEN: 'test', YESMAN_AGENT: `alice/${role}`, YESMAN_ROLE: role, YESMAN_WORKSPACE: workspace }, platform: process.platform, cwd: () => workspace },
    pi, ...filesystem, isAbsolute, relative, resolve, Type,
    createBashToolDefinition: factory, createPowerShellToolDefinition: factory,
  });
  return { handlers, active: () => active };
}

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'yesman-lead-documents-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const workspace = join(root, 'alice');
  mkdirSync(join(workspace, '.yesman', 'knowledge'), { recursive: true });
  mkdirSync(join(workspace, 'repo'));
  return { root, workspace, log: join(workspace, '.yesman', 'task-log.md') };
}

test('lead may maintain Knowledge and the exact task log, with code and other metadata blocked', async (t) => {
  const { workspace } = fixture(t);
  const { handlers } = extension('lead', workspace);
  const gate = handlers.get('tool_call')[0];
  for (const toolName of ['write', 'edit']) {
    for (const path of ['.yesman/task-log.md', '@.yesman/task-log.md', resolve(workspace, '.yesman/task-log.md'), '.yesman/knowledge/icons.md', '.yesman/knowledge/INDEX.md', '.yesman/knowledge/deep/rules.md']) {
      assert.equal(await gate({ toolName, input: { path } }), undefined, `${toolName} should allow ${path}`);
    }
    for (const path of [undefined, '', '.yesman/board.json', '.yesman/yesman.json', '.yesman/task-log.md.bak', '.yesman/task-log.md/child.md', '.yesman/knowledge', '.yesman/knowledge/../workers.json', '.yesman/task-log.md/../../code.go', 'repo/main.go', '../other/.yesman/task-log.md', resolve(workspace, '..', 'other', '.yesman/task-log.md')]) {
      const blocked = await gate({ toolName, input: { path } });
      assert.equal(blocked?.block, true, `${toolName} should block ${path}`);
      assert.match(blocked.reason, /\.yesman\/task-log\.md/);
      assert.match(blocked.reason, /worker/);
    }
  }
  assert.equal((await gate({ toolName: 'bash', input: { command: 'true' } }))?.block, true);
  assert.equal(await gate({ toolName: 'read', input: { path: 'repo/main.go' } }), undefined);
});

test('lead accepts ordinary existing or new logs but fails closed on invalid parent paths', async (t) => {
  const { workspace, log } = fixture(t);
  const gate = extension('lead', workspace).handlers.get('tool_call')[0];
  const check = async (allowed) => {
    for (const toolName of ['write', 'edit']) {
      const result = await gate({ toolName, input: { path: log } });
      assert.equal(result?.block ?? false, !allowed, toolName);
    }
  };
  await check(true); // missing log, ordinary .yesman directory
  writeFileSync(log, '# Task log\n');
  await check(true);
  assert.equal(readFileSync(log, 'utf8'), '# Task log\n');
  rmSync(log);
  mkdirSync(log);
  await check(false); // a directory is preserved by bootstrap, never writable
  rmSync(join(workspace, '.yesman'), { recursive: true });
  await check(true); // write may create the ordinary missing metadata directory
  writeFileSync(join(workspace, '.yesman'), 'user-owned parent file');
  await check(false);
  assert.equal(readFileSync(join(workspace, '.yesman'), 'utf8'), 'user-owned parent file');
  rmSync(workspace, { recursive: true });
  await check(false); // never create a missing workspace via the exception
});

test('task-log writes fail closed when the actual path cannot be checked', async (t) => {
  const { workspace } = fixture(t);
  for (const code of ['EACCES', 'EIO', 'ENOTDIR']) {
    const gate = extension('lead', workspace, {
      realpathSync,
      lstatSync: () => { throw Object.assign(new Error(code), { code }); },
    }).handlers.get('tool_call')[0];
    for (const toolName of ['write', 'edit']) assert.equal((await gate({ toolName, input: { path: '.yesman/task-log.md' } }))?.block, true, `${code}: ${toolName}`);
  }
});

for (const kind of ['code', 'dangling code', 'other workspace', 'hard link']) {
  test(`lead write/edit rejects a task-log alias to ${kind}`, async (t) => {
    const { root, workspace, log } = fixture(t);
    const target = kind === 'other workspace' ? join(root, 'bob', '.yesman', 'task-log.md') : join(workspace, 'repo', 'main.go');
    mkdirSync(resolve(target, '..'), { recursive: true });
    const content = 'const userSentinel = 1;\n';
    if (kind !== 'dangling code') writeFileSync(target, content);
    try {
      if (kind === 'hard link') linkSync(target, log);
      else symlinkSync(target, log, 'file');
    } catch (err) {
      if (process.platform === 'win32' && ['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) return t.skip(`links unavailable: ${err.code}`);
      throw err;
    }
    const gate = extension('lead', workspace).handlers.get('tool_call')[0];
    for (const toolName of ['write', 'edit']) {
      for (const path of [log, '.yesman/task-log.md', '@.yesman/task-log.md', '.yesman/knowledge/../task-log.md']) {
        assert.equal((await gate({ toolName, input: { path } }))?.block, true, `${toolName}: ${path}`);
      }
    }
    assert.equal(existsSync(target), kind !== 'dangling code');
    if (kind !== 'dangling code') assert.equal(readFileSync(target, 'utf8'), content);
    assert.equal(lstatSync(log).isSymbolicLink(), kind !== 'hard link');
  });
}

for (const kind of ['repo directory', 'other workspace', 'dangling directory']) {
  test(`lead write/edit rejects a linked metadata parent to ${kind}`, async (t) => {
    const { root, workspace } = fixture(t);
    const meta = join(workspace, '.yesman');
    rmSync(meta, { recursive: true });
    const target = kind === 'repo directory' ? join(workspace, 'repo') : join(root, 'bob', '.yesman');
    if (kind !== 'dangling directory') mkdirSync(target, { recursive: true });
    try {
      symlinkSync(target, meta, 'junction');
    } catch (err) {
      if (process.platform === 'win32' && ['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) return t.skip(`links unavailable: ${err.code}`);
      throw err;
    }
    const gate = extension('lead', workspace).handlers.get('tool_call')[0];
    for (const toolName of ['write', 'edit']) assert.equal((await gate({ toolName, input: { path: '.yesman/task-log.md' } }))?.block, true);
    assert.equal(lstatSync(meta).isSymbolicLink(), true);
    assert.equal(existsSync(join(target, 'task-log.md')), false);
  });
}

test('workspace aliases resolve to the same safe regular task log', async (t) => {
  const { root, workspace, log } = fixture(t);
  const alias = join(root, 'workspace-alias');
  try {
    symlinkSync(workspace, alias, 'junction');
  } catch (err) {
    if (process.platform === 'win32' && ['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) return t.skip(`links unavailable: ${err.code}`);
    throw err;
  }
  writeFileSync(log, '# Task log\n');
  const gate = extension('lead', alias).handlers.get('tool_call')[0];
  for (const toolName of ['write', 'edit']) assert.equal(await gate({ toolName, input: { path: '.yesman/task-log.md' } }), undefined);
});

test('lead tool narrowing still removes the shell and workers retain their tools', async () => {
  const lead = extension('lead', resolve('workspace'));
  // The first hook narrows tools; the second fetches context and is not used.
  await lead.handlers.get('before_agent_start')[0]();
  assert.equal(lead.active().includes('bash'), false);
  for (const name of ['read', 'write', 'edit', 'grep', 'find', 'ls']) assert.equal(lead.active().includes(name), true);
  const worker = extension('worker', resolve('workspace'));
  assert.equal(worker.handlers.has('tool_call'), false);
  assert.equal(worker.active().includes('bash'), true);
});
