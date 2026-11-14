import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { isAbsolute, relative, resolve } from 'node:path';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';

// Run the actual extension's hooks without pi or network access. Tool schemas
// and the shell factory are inert; these tests only exercise lead restrictions.
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'))
  .replace(/^import .*;\s*$/gm, '')
  .replace('export default function yesman', 'function yesman');

function extension(role, workspace) {
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
    pi, isAbsolute, relative, resolve, Type,
    createBashToolDefinition: factory, createPowerShellToolDefinition: factory,
  });
  return { handlers, active: () => active };
}

test('lead may maintain Knowledge and the exact task log, with code and other metadata blocked', async () => {
  const workspace = resolve('workspace');
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
