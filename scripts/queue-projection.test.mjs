import test from 'node:test';
import assert from 'node:assert/strict';
import { project } from '../web/conversation.js';
const id = n => n.toString(16).padStart(48, '0');
const receipt = (n, state, extra = {}) => [id(n), {id: id(n), session: 'p1:s1', state, revision: 2, order: n, text: 'same', ...extra}];
test('native pending survives cache eviction, later submissions and reload; finished receipts never become a list', () => {
  const deliveries = new Map([
    ...Array.from({length: 26}, (_, i) => receipt(i + 1, i % 2 ? 'unconfirmed' : 'dispatched')),
    ...Array.from({length: 12}, (_, i) => receipt(i + 30, i % 2 ? 'queued' : 'steering', {nativePending: true})),
    receipt(50, 'failed', {agentSince: 'current', error: 'refused'}),
    receipt(51, 'interrupted', {session: 'p0:s1'}),
    receipt(52, 'unconfirmed'),
  ]);
  // The display cache, the ID this window sent last and a reload (no cache)
  // change nothing: membership and state decide.
  for (const sent of [new Map(), new Map(Array.from({length: 8}, (_, i) => [id(i + 34), {text: 'same', images: []}]))]) {
    for (const lastSent of [id(52), null]) {
      const rows = project({deliveries, sent, lastSent}, {since: 'current'});
      assert.equal(rows.filter(r => r.type !== 'receipt').length, 0);
      // The refusal was moved past by later submissions, and the interruption by
      // one to a later session; the latest uncertain one stays.
      assert.deepEqual(rows.map(r => r.delivery.id), [...Array.from({length: 12}, (_, i) => id(i + 30)), id(52)]);
    }
  }
  // Each is taken in turn: its row leaves, and none comes back.
  const left = new Map(deliveries);
  for (let n = 30; n < 42; n++) {
    left.set(id(n), {...left.get(id(n)), state: 'dispatched', nativePending: false, revision: 3});
    const rows = project({deliveries: left});
    assert.deepEqual(rows.map(r => r.delivery.id), [...Array.from({length: 41 - n}, (_, i) => id(n + 1 + i)), id(52)]);
  }
});
test('only submission identity binds; repeated identical inputs and duplicate native events stay distinct and ordered', () => {
  const user = n => ({role: 'user', timestamp: 1, content: [{type: 'image', data: 'same', mimeType: 'image/png'}], yesman: {source: 'user', submission: id(n)}});
  const messages = [user(1), {role: 'assistant', timestamp: 2, content: [{type: 'text', text: 'answer'}]}, user(2), user(1)];
  const rows = project({messages, deliveries: new Map([receipt(1, 'dispatched'), receipt(2, 'dispatched'), receipt(3, 'queued', {nativePending: true})])});
  assert.deepEqual(rows.map(r => r.type), ['user', 'text', 'user', 'receipt']);
  assert.deepEqual(rows.filter(r => r.type === 'user').map(r => r.delivery.id), [id(1), id(2)]);
  assert.equal(rows.at(-1).delivery.id, id(3));
});
