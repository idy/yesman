import test from 'node:test';
import assert from 'node:assert/strict';
import { needsYouItems, requestsOf, closedHandoffs, summary, typeLabel, responseKind, dialogKey } from '../web/needsyou.js';

const snap = (agents, yesmen) => ({ agents, yesmen });
const alice = { name: 'alice', hiredAt: '2026-01-01T00:00:00Z', handoffs: [] };
const bob = { name: 'bob', hiredAt: '2026-02-01T00:00:00Z', handoffs: [] };

test('only structured requests reach Needs you', () => {
  const items = needsYouItems(snap([
    { id: 'alice/lead', yesman: 'alice', role: 'lead', status: 'waiting', since: alice.hiredAt, dialogAt: '2026-03-01T00:00:02Z', dialog: { id: 'q1', method: 'confirm', title: 'Deploy?' } },
    // A worker asking its lead is the lead's to answer.
    { id: 'alice/w1', yesman: 'alice', role: 'worker', status: 'waiting', since: '2026-03-01T00:00:00Z', dialog: { id: 'q2', method: 'select', title: 'Allow?' } },
    { id: 'bob/w2', yesman: 'bob', role: 'worker', status: 'waiting', handedOver: true, since: '2026-03-01T00:00:00Z', dialogAt: '2026-03-01T00:00:01Z', dialog: { id: 'q3', method: 'input', title: 'Name?' } },
    // Text, failure or completion is never a request.
    { id: 'bob/lead', yesman: 'bob', role: 'lead', status: 'idle', detail: 'Please log in', since: bob.hiredAt },
    { id: 'bob/w3', yesman: 'bob', role: 'worker', status: 'failed', finishedAt: '2026-03-01T00:00:00Z' },
  ], [alice, { ...bob, handoffs: [
    { id: 'n1', type: 'login', title: 'Log in to npm', createdAt: '2026-03-01T00:00:00Z' },
    { id: 'n2', type: 'input', title: 'Old', createdAt: '2026-02-01T00:00:00Z', closedAt: '2026-02-02T00:00:00Z', resolution: 'done' },
  ] }]));
  assert.deepEqual(items.map((i) => i.key), [
    'h:bob@2026-02-01T00:00:00Z:n1',
    'd:bob/w2@2026-03-01T00:00:00Z:2026-03-01T00:00:01Z:q3@2026-02-01T00:00:00Z',
    'd:alice/lead@2026-01-01T00:00:00Z:2026-03-01T00:00:02Z:q1@2026-01-01T00:00:00Z',
  ]);
  assert.equal(items[0].agent, 'bob/lead');
  assert.equal(typeLabel(items[0]), 'Log in');
  assert.equal(typeLabel(items[1]), 'Text');
  assert.equal(typeLabel({ kind: 'handoff', type: 'input' }), 'Information');
});

test('an archived yesman has nothing waiting; a waiting lead always does', () => {
  const items = needsYouItems(snap([
    { id: 'gone/lead', yesman: 'gone', role: 'lead', status: 'waiting', dialog: { id: 'q1', method: 'confirm' } },
    { id: 'alice/lead', yesman: 'alice', role: 'lead', status: 'waiting', since: alice.hiredAt },
    { id: 'alice/w1', yesman: 'alice', role: 'worker', status: 'waiting', handedOver: true },
  ], [alice]));
  assert.deepEqual(items.map((i) => i.agent), ['alice/lead']);
});

test('response kinds are explicit or use the legacy type, and a repeated dialog ID gets a new key', () => {
  assert.equal(responseKind({ type: 'decision', detail: 'Choose a, b or c' }), 'text');
  assert.equal(responseKind({ type: 'login' }), 'action');
  assert.equal(responseKind({ type: 'decision', response: { kind: 'choice', options: ['a', 'b'] } }), 'choice');
  const a = { id: 'alice/lead', since: alice.hiredAt, dialog: { id: 'repeated' }, dialogAt: '2026-05-03T00:00:00Z' };
  assert.notEqual(dialogKey(a, alice.hiredAt), dialogKey({ ...a, dialogAt: '2026-05-03T00:00:01Z' }, alice.hiredAt));
});

test('a same-name yesman hired again has new keys', () => {
  const h = { id: 'n1', type: 'manual', title: 'Plug in', createdAt: '2026-03-01T00:00:00Z' };
  const before = needsYouItems(snap([], [{ ...alice, handoffs: [h] }]))[0].key;
  const after = needsYouItems(snap([], [{ ...alice, hiredAt: '2026-04-01T00:00:00Z', handoffs: [h] }]))[0].key;
  assert.notEqual(before, after);
});

test('a yesman\'s requests are its own, for its current hire', () => {
  const hired = '2026-03-01T10:00:00.123456789Z';
  const bobNow = { ...bob, hiredAt: hired, handoffs: [{ id: 'n1', type: 'manual', title: 'Plug in the phone', createdAt: hired }] };
  const dialog = { id: 'q', method: 'confirm', title: 'Ship?' };
  const agents = [
    // Alice's lead asks: not Bob's.
    { id: 'alice/lead', yesman: 'alice', role: 'lead', status: 'waiting', since: alice.hiredAt, dialog },
    // The old Bob's lead, from a list of the agents read before he was hired again.
    { id: 'bob/lead', yesman: 'bob', role: 'lead', status: 'waiting', since: '2026-02-01T00:00:00Z', dialog },
    // A worker of this Bob, started after the hire, which its lead handed over.
    { id: 'bob/w1', yesman: 'bob', role: 'worker', status: 'waiting', since: '2026-03-01T10:00:00.124Z', handedOver: true, dialog },
  ];
  const s = snap(agents, [alice, bobNow]);
  assert.deepEqual(requestsOf(s, bobNow).map((i) => i.key).sort(), [`d:bob/w1@2026-03-01T10:00:00.124Z::q@${hired}`, `h:bob@${hired}:n1`]);
  assert.deepEqual(requestsOf(s, alice).map((i) => i.agent), ['alice/lead']);
  // This Bob's own lead, started when he was hired, to the nanosecond.
  const lead = { ...agents[1], since: hired };
  assert.deepEqual(requestsOf(snap([lead], [bobNow]), bobNow).map((i) => i.kind).sort(), ['dialog', 'handoff']);
  // A start not known is taken as it is, as is a yesman whose hire is not.
  assert.equal(requestsOf(snap([{ ...lead, since: undefined }], [bobNow]), bobNow).length, 2);
  assert.equal(requestsOf(snap(agents, [{ ...bobNow, hiredAt: undefined }]), { ...bobNow, hiredAt: undefined }).length, 3);
  assert.deepEqual(requestsOf(s, null), []);
});

test('history is newest first and never pending', () => {
  const s = snap([], [{ ...alice, handoffs: [
    { id: 'n1', type: 'manual', title: 'A', closedAt: '2026-03-01T00:00:00Z' },
    { id: 'n2', type: 'manual', title: 'B', closedAt: '2026-03-02T00:00:00Z' },
    { id: 'n3', type: 'manual', title: 'C' },
  ] }]);
  assert.deepEqual(closedHandoffs(s).map((x) => x.handoff.id), ['n2', 'n1']);
  assert.deepEqual(needsYouItems(s).map((i) => i.handoff.id), ['n3']);
});

test('one yesman\'s history is chosen before the limit', () => {
  const closed = (name, n, at) => ({ id: `n${n}`, type: 'manual', title: `${name} ${n}`, createdAt: at, closedAt: at, resolution: 'done', closedBy: 'user' });
  const s = { agents: [], yesmen: [
    { name: 'alice', hiredAt: 'h', handoffs: [closed('alice', 1, '2026-01-01T00:00:00Z')] },
    { name: 'bob', hiredAt: 'h', handoffs: Array.from({ length: 25 }, (_, i) => closed('bob', i + 1, `2026-01-02T00:00:${String(i).padStart(2, '0')}Z`)) },
  ] };
  assert.equal(closedHandoffs(s).length, 20);
  assert.ok(closedHandoffs(s).every((x) => x.yesman === 'bob'));
  assert.deepEqual(closedHandoffs(s, 20, 'alice').map((x) => x.handoff.title), ['alice 1']);
});

test('summaries are short plain lines', () => {
  assert.equal(summary('Run `npm login`\n\nthen **retry**'), 'Run npm login then retry');
  assert.equal(summary('x'.repeat(200)).length, 160);
});
