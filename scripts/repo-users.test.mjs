import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import vm from 'node:vm';

// Exercise the actual row renderer with synthetic DOM nodes, without loading
// app startup or using any real workspace. Check rendered users, not its Set.
const source = await readFile(new URL('../web/app.js', import.meta.url), 'utf8');
const renderer = source.slice(source.indexOf('function repoRow('), source.indexOf('function cloneRow('));
const label = source.match(/^const yesmanLabel = .*;$/m)[0];
function fixture(yesmen) {
  const state = { snap: { yesmen }, fetches: { fetching: [], errors: {} } };
  const context = vm.createContext({
    state,
    h: (tag, attrs, ...children) => ({ tag, attrs, children: children.flat(Infinity).filter(c => c != null) }),
    icon: () => null,
    githubName: () => 'synthetic/shared',
    ago: () => 'just now',
    t: text => text,
    tn: (n, singular, plural) => (n === 1 ? singular : plural).replace('{n}', n),
  });
  const row = vm.runInContext(`${label}\n${renderer}\nrepoRow`, context);
  return () => row({ name: 'github.com/synthetic/shared', url: 'https://github.com/synthetic/shared' }).children.find(c => c.attrs?.class === 'card-row-aside');
}
const wt = dir => ({ repo: 'github.com/synthetic/shared', dir });

test('different stable IDs with the same display label remain distinct repo users', () => {
  const render = fixture([
    { name: 'alice', displayName: '同名 Project', worktrees: [wt('one'), wt('two')] },
    { name: 'bob', displayName: '同名 Project', worktrees: [wt('three')] },
    { name: 'unused', displayName: '同名 Project', worktrees: [] },
  ]);
  const aside = render();
  assert.equal(aside.children.join(''), '3 worktrees · 同名 Project, 同名 Project');
  assert.equal(aside.attrs.title, '同名 Project: one\n同名 Project: two\n同名 Project: three');
});

test('multiple checkouts under one stable ID count as one repo user', () => {
  const render = fixture([{ name: 'alice', displayName: '团队 Alpha', worktrees: [wt('one'), wt('two')] }]);
  assert.equal(render().children.join(''), '2 worktrees · 团队 Alpha');
});

test('a refreshed rename changes labels without changing repo users or checkout counts', () => {
  const yesmen = [
    { name: 'alice', worktrees: [wt('one'), wt('two')] },
    { name: 'bob', displayName: '团队 Alpha', worktrees: [wt('three')] },
  ];
  const render = fixture(yesmen);
  assert.equal(render().children.join(''), '3 worktrees · alice, 团队 Alpha');
  yesmen[0] = { ...yesmen[0], displayName: '团队 Alpha' };
  assert.equal(render().children.join(''), '3 worktrees · 团队 Alpha, 团队 Alpha');
  yesmen[1] = { ...yesmen[1], displayName: 'Beta 工作区' };
  assert.equal(render().children.join(''), '3 worktrees · 团队 Alpha, Beta 工作区');
  assert.equal(render().attrs.title, '团队 Alpha: one\n团队 Alpha: two\nBeta 工作区: three');
});
