// The visible topic line and submission helpers shared by both composers.
import test from 'node:test';
import assert from 'node:assert/strict';

globalThis.crypto ??= (await import('node:crypto')).webcrypto;
const { withoutLine, topicMarker, isCommand, newSubmissionID, sameSubmissionImages } = await import('../web/submission.js');

test('the topic line matches the Go marker', () => {
  const line = topicMarker({ id: 'tp3', title: '  How do\n topics   relate? ' });
  assert.equal(line, '[Topic tp3: How do topics relate?]');
  const long = topicMarker({ id: 'tp9', title: '长'.repeat(130) });
  assert.equal(Array.from(long).length, '[Topic tp9: '.length + 120 + 1 + 1);
  assert.ok(long.endsWith('…]'));
});

test('only the line the app recorded is taken off a message', () => {
  const line = '[Topic tp3: How do topics relate?]';
  assert.equal(withoutLine(`${line}\n\nWhat about tasks?`, line), 'What about tasks?');
  assert.equal(withoutLine(line, line), ''); // images only
  // Another topic's line, a changed title, text after the line without a
  // blank line, or no recorded line: the text stays whole.
  for (const [text, recorded] of [
    ['[Topic tp2: copied]\n\nbody', line],
    ['[Topic tp3: How do topics relate]\n\nbody', line],
    [`${line}\nbody`, line],
    [`${line}\n\nbody`, ''],
    [`${line}\n\nbody`, undefined],
    ['/skill:x', ''],
  ]) assert.equal(withoutLine(text, recorded), text, JSON.stringify([text, recorded]));
  assert.equal(withoutLine(undefined, line), undefined);
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
