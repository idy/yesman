import test from 'node:test';
import assert from 'node:assert/strict';
import { project } from '../web/conversation.js';
const id = n => n.toString(16).padStart(48, '0');
const receipt = (n, state, extra = {}) => [id(n), {id: id(n), state, revision: 2, order: n, text: 'same', ...extra}];
test('native pending and current failures survive cache eviction, later submissions and reload', () => {
  const deliveries = new Map([
    ...Array.from({length: 26}, (_, i) => receipt(i + 1, 'unconfirmed')),
    ...Array.from({length: 12}, (_, i) => receipt(i + 30, i % 2 ? 'queued' : 'steering', {nativePending: true})),
    receipt(50, 'failed', {agentSince: 'current', error: 'refused'}),
    receipt(51, 'unconfirmed'),
  ]);
  for (const sent of [new Map(), new Map(Array.from({length: 8}, (_, i) => [id(i + 34), {text: 'same', images: []}]))]) {
    const rows = project({deliveries, sent, lastSent: id(51)}, {since: 'current'});
    assert.equal(rows.filter(r => r.type === 'receipts').length, 1);
    assert.equal(rows.find(r => r.type === 'receipts').deliveries.length, 27);
    assert.deepEqual(rows.filter(r => r.type === 'receipt').map(r => r.delivery.id), [...Array.from({length: 12}, (_, i) => id(i + 30)), id(50)]);
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
