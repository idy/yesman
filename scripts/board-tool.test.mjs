import assert from 'node:assert/strict';
import { readFileSync, lstatSync, realpathSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { isAbsolute, relative, resolve } from 'node:path';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';

// The yesman_board tool of the actual extension, with the app's API stubbed:
// what it returns to the model, as text and as details, keeps a scheduled
// run's plan and its task's own fields, and nothing else the board holds.
const source = stripTypeScriptTypes(readFileSync(new URL('../pi/yesman.ts', import.meta.url), 'utf8'))
  .replace(/^import .*;\s*$/gm, '')
  .replace('export default function yesman', 'function yesman');

function boardTool(role, answer) {
  const tools = new Map();
  const pi = {
    on() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    getAllTools: () => [],
    getActiveTools: () => [],
    setActiveTools() {},
  };
  const factory = () => ({});
  const Type = new Proxy({}, { get: () => factory });
  const requests = [];
  const fetch = async (url, init) => {
    requests.push({ url, method: init.method });
    return { ok: true, headers: { get: () => 'application/json' }, text: async () => JSON.stringify(answer) };
  };
  runInNewContext(`${source}\nyesman(pi);`, {
    process: { env: { YESMAN_API: 'http://unused', YESMAN_TOKEN: 'test', YESMAN_AGENT: `alice/${role}`, YESMAN_ROLE: role, YESMAN_WORKSPACE: '/tmp' }, platform: process.platform, cwd: () => '/tmp' },
    pi, lstatSync, realpathSync, isAbsolute, relative, resolve, Type, fetch, AbortSignal,
    createBashToolDefinition: factory, createPowerShellToolDefinition: factory,
  });
  return { tool: tools.get('yesman_board'), requests };
}

const RUN = 's1-m-aaaaaaaaaaaaaaaa';
const board = {
  next: 3,
  internal: { run: RUN },
  tasks: [
    { id: 't1', title: 'Morning check', status: 'todo', note: 'n', detail: 'd', createdAt: '2026-11-16T09:00:00Z', updatedAt: '2026-11-16T09:00:00Z', schedule: { plan: 's1', run: RUN, submission: 'f'.repeat(48) }, future: RUN },
    { id: 't2', title: 'Ordinary', status: 'doing', owner: 'w1' },
  ],
};

for (const role of ['lead', 'worker']) {
  test(`yesman_board shows a ${role} the plan, never the run handle`, async () => {
    for (const params of [{}, { update: [{ id: 't1', status: 'doing' }] }]) {
      const { tool, requests } = boardTool(role, board);
      // Objects of the extension's realm, compared as JSON.
      const out = JSON.parse(JSON.stringify(await tool.execute('call', params)));
      const seen = JSON.stringify(out);
      assert.ok(!seen.includes(RUN), `the run handle reached the model: ${seen}`);
      assert.ok(!seen.includes('f'.repeat(48)), 'a submission ID reached the model');
      assert.ok(!seen.includes('internal'), 'an unknown board field reached the model');
      assert.match(out.content[0].text, /- t1 \[todo\] Morning check \(a run of scheduled task s1\)/);
      assert.match(out.content[0].text, /- t2 \[doing\] Ordinary \(owner w1\)/);
      assert.deepEqual(out.details.tasks[0].schedule, { plan: 's1' });
      assert.deepEqual(out.details.tasks.map((t) => t.id), ['t1', 't2']);
      assert.equal(out.details.tasks[0].note, 'n');
      assert.equal(out.details.next, 3);
      assert.equal(requests[0].method, Object.keys(params).length ? 'POST' : 'GET');
    }
  });
}

test('a plan ID that is not one is dropped', async () => {
  const { tool } = boardTool('lead', { tasks: [{ id: 't1', title: 'x', status: 'todo', schedule: { plan: RUN } }] });
  const out = JSON.parse(JSON.stringify(await tool.execute('call', {})));
  assert.equal(out.details.tasks[0].schedule, undefined);
  assert.ok(!JSON.stringify(out).includes(RUN));
});

test('a blocked task shows what it waits for, and nothing else of its wait', async () => {
  const wait = { kind: 'tasks', tasks: ['t2', 7], reason: 'after the build', since: '2026-11-18T09:00:00Z', secret: RUN };
  const { tool } = boardTool('lead', { tasks: [{ id: 't1', title: 'Release', status: 'blocked', wait }, { id: 't2', title: 'Build', status: 'doing' }] });
  const out = JSON.parse(JSON.stringify(await tool.execute('call', {})));
  assert.deepEqual(out.details.tasks[0].wait, { kind: 'tasks', tasks: ['t2'], reason: 'after the build', since: '2026-11-18T09:00:00Z' });
  assert.match(out.content[0].text, /- t1 \[blocked\] Release\n {2}wait: tasks t2: after the build/);
  assert.ok(!JSON.stringify(out).includes(RUN));
  assert.equal(out.details.tasks[1].wait, undefined);
});
