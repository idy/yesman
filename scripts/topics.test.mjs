// The visible topic line and submission helpers shared by both composers.
import test from 'node:test';
import assert from 'node:assert/strict';

globalThis.crypto ??= (await import('node:crypto')).webcrypto;
const { parseTopicMarker, topicMarker, isCommand, newSubmissionID, sameSubmissionImages } = await import('../web/submission.js');

test('the topic line matches the Go marker and parses back', () => {
  const line = topicMarker({ id: 'tp3', title: '  How do\n topics   relate? ' });
  assert.equal(line, '[Topic tp3: How do topics relate?]');
  assert.deepEqual(parseTopicMarker(`${line}\n\nWhat about tasks?`), { id: 'tp3', title: 'How do topics relate?', rest: 'What about tasks?' });
  assert.deepEqual(parseTopicMarker(line), { id: 'tp3', title: 'How do topics relate?', rest: '' }); // images only
  const long = topicMarker({ id: 'tp9', title: '长'.repeat(130) });
  assert.equal(Array.from(long).length, '[Topic tp9: '.length + 120 + 1 + 1);
  assert.ok(long.endsWith('…]'));
});

test('only an exact leading line names a topic', () => {
  for (const text of [
    'Hello [Topic tp3: x]\n\nbody', '[Topic tp0: x]\n\nbody', '[Topic t3: x]\n\nbody', '[topic tp3: x]\n\nbody',
    '[Topic tp3: x]\nbody', '[Topic tp3: x\ny]\n\nbody', '[Topic ../x: y]\n\nz', '', null, undefined, 42,
  ]) assert.equal(parseTopicMarker(text), null, String(text));
  // A title containing brackets still parses to the line's end.
  assert.equal(parseTopicMarker('[Topic tp4: a [b] c]\n\nq').title, 'a [b] c');
});

test('commands are sent as typed', () => {
  assert.equal(isCommand('/skill:review x'), true);
  assert.equal(isCommand('  \n/template'), true);
  assert.equal(isCommand('a /b'), false);
  assert.equal(isCommand(''), false);
});

test('submission IDs and image identity follow the receipt contract', () => {
  const id = newSubmissionID();
  assert.match(id, /^[a-f0-9]{48}$/);
  assert.notEqual(id, newSubmissionID());
  const a = [{ type: 'image', mimeType: 'image/png', data: 'AA==' }];
  assert.equal(sameSubmissionImages(a, [{ ...a[0] }]), true);
  assert.equal(sameSubmissionImages(a, [{ ...a[0], data: 'AB==' }]), false);
  assert.equal(sameSubmissionImages(a, []), false);
});
