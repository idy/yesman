import assert from 'node:assert/strict';
import { test } from 'node:test';
import { project, familyOf, joinInputs, joinText, sentOf } from '../web/conversation.js';

// Inputs pi appended into one pending occurrence: synthetic receipts shaped
// like the Manager's (internal/agent/coalesce.go), with private IDs only.
const id = (n) => n.toString(16).padStart(48, '0');
const A = id(1), B = id(2), C = id(3), D = id(4);
const receipt = (n, extra = {}) => ({ id: n, revision: 3, order: parseInt(n, 16), state: 'steering', text: `t${parseInt(n, 16)}`, imageCount: 0, ...extra });
const convo = (deliveries, extra = {}) => ({ messages: [], tools: new Map(), deliveries: new Map(deliveries.map((d) => [d.id, d])), sent: new Map(), ...extra });
const receiptRows = (rows) => rows.filter((r) => r.type === 'receipt' || r.type === 'receipts');
const png = (n) => ({ type: 'image', mimeType: 'image/png', data: `iVBOR${n}` });

test('pi\'s merge joins text with one newline, none around an empty input, then all images in order', () => {
  assert.equal(joinText('a', 'b'), 'a\nb');
  assert.equal(joinText('', 'b'), 'b');
  assert.equal(joinText('a', ''), 'a');
  assert.equal(joinText(' a\n', '  b '), ' a\n\n  b ', 'each input keeps its own characters, a trailing newline too');
  assert.equal(joinText('   ', 'b'), '   \nb', 'whitespace-only text is still text: nothing is trimmed away');
  assert.deepEqual(joinInputs([{ text: 'A', images: [png(1)] }, { text: '', images: [png(2), png(3)] }, { text: 'C', images: [] }]),
    { text: 'A\nC', images: [png(1), png(2), png(3)] });
});

test('an appended input is part of its occurrence\'s message, never a row of its own', () => {
  const c = convo([
    receipt(A, { nativePending: true, merged: [B, C], canCancel: true, originalPayload: true }),
    receipt(B, { mergedInto: A }), receipt(C, { mergedInto: A }),
  ]);
  const rows = receiptRows(project(c, { status: 'working' }));
  assert.deepEqual(rows.map((r) => r.key), [`d:${A}`]);
  assert.deepEqual(rows[0].members, [A, B, C]);
});

test('an occurrence whose receipt has not caught up still shows the input whose own receipt names it', () => {
  // B's reply arrived before A's own update: B must neither vanish nor show twice.
  const c = convo([receipt(A, { nativePending: true }), receipt(B, { mergedInto: A })]);
  c.sent.set(A, { text: 'first', images: [png(1)] });
  c.sent.set(B, { text: 'second', images: [png(2)] });
  const rows = receiptRows(project(c, { status: 'working' }));
  assert.deepEqual(rows.map((r) => r.key), [`d:${A}`]);
  assert.deepEqual(rows[0].members, [A, B]);
  assert.deepEqual(rows[0].sent, { text: 'first\nsecond', images: [png(1), png(2)] });
  assert.deepEqual(familyOf(c, c.deliveries.get(A)), [A, B]);
});

test('the whole message is shown from this window only when it holds every input', () => {
  const c = convo([receipt(A, { nativePending: true, merged: [B] }), receipt(B, { mergedInto: A })]);
  c.sent.set(B, { text: 'second', images: [] });
  assert.equal(sentOf(c, [A, B]), undefined, 'a missing first input is never replaced by the later one alone');
  assert.equal(receiptRows(project(c, { status: 'working' }))[0].sent, undefined);
});

test('a later input pi kept as its own message has its own row', () => {
  const c = convo([receipt(A, { nativePending: true }), receipt(B, { nativePending: true })]);
  assert.deepEqual(receiptRows(project(c, { status: 'working' })).map((r) => r.key), [`d:${A}`, `d:${B}`]);
});

test('once pi takes the occurrence, its one canonical message binds every input it joined', () => {
  const merged = { role: 'user', timestamp: 9, yesman: { source: 'user', submission: A }, content: [{ type: 'text', text: 'first\nsecond' }] };
  const c = convo([receipt(A, { state: 'dispatched', merged: [B, C] }), receipt(B, { state: 'dispatched', mergedInto: A }), receipt(C, { state: 'dispatched', mergedInto: A })], { messages: [merged] });
  const rows = project(c, { status: 'idle' });
  assert.deepEqual(rows.map((r) => r.type), ['user'], 'no child in the earlier-records fold and no second bubble');
  assert.equal(rows[0].delivery.id, A);
});

test('an unbound finished family has no second history row', () => {
  const c = convo([receipt(A, { state: 'dispatched', merged: [B] }), receipt(B, { state: 'dispatched', mergedInto: A }), receipt(D, { state: 'dispatched' })]);
  assert.deepEqual(project(c, { status: 'idle' }), []);
});

test('an interrupted family stays actionable once, until hidden, while another pending input remains visible', () => {
  const c = convo([
    receipt(A, { state: 'interrupted', merged: [B], error: 'interrupted before intake' }),
    receipt(B, { state: 'interrupted', mergedInto: A, error: 'interrupted before intake' }),
    receipt(D, { nativePending: true, canCancel: true }),
  ]);
  assert.deepEqual(receiptRows(project(c)).map((r) => r.key), [`d:${A}`, `d:${D}`]);
  c.handled = new Set([A, D]);
  assert.deepEqual(receiptRows(project(c)).map((r) => r.key), [`d:${D}`], 'Hide never suppresses native pending');
  c.deliveries.set(A, { ...c.deliveries.get(A), state: 'steering', error: '', nativePending: true });
  assert.deepEqual(receiptRows(project(c)).map((r) => r.key), [`d:${A}`, `d:${D}`], 'a trusted new pending attempt remains visible');
});

test('an appended input whose occurrence receipt is gone shows honestly as itself', () => {
  const c = convo([receipt(B, { state: 'interrupted', mergedInto: A })], { submitted: new Set([B]) });
  assert.deepEqual(receiptRows(project(c, { status: 'idle' })).map((r) => r.key), [`d:${B}`]);
});
