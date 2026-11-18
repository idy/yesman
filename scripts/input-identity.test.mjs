// The managed runtime's host-only input identity (pending-queue.js): what
// yesman sends with a submission (hostOrigin) is kept in bounds, and a
// scheduled task's run stays a scheduled run, with its plan and run, never
// a person's message (R5-F6).
import test from 'node:test';
import assert from 'node:assert/strict';

const queue = await import(process.env.YESMAN_TEST_PENDING_QUEUE ?? new URL('../internal/piruntime/assets/pending-queue.js', import.meta.url));
const { inputIdentity, persistInputIdentity, recordedInputs, INPUT_ENTRY } = queue;

const submissionId = '01a1eb57f673db7bcdc8f54715307de164d20c417fcacd50';

test('a scheduled run keeps its source, plan and run', () => {
  const id = inputIdentity({ submissionId, hostOrigin: { source: 'schedule', plan: 's1', run: 's1-20261116t0900', submission: submissionId } });
  assert.deepEqual(id, { submissionId, origin: { source: 'schedule', plan: 's1', run: 's1-20261116t0900' } });
  assert.ok(Object.isFrozen(id) && Object.isFrozen(id.origin));
  // Persisted beside the message and read back from the session the same.
  assert.deepEqual(persistInputIdentity(id), id);
  const branch = [
    { type: 'message', id: 'm1', parentId: null, message: { role: 'user', content: 'Scheduled task s1', timestamp: 1 } },
    { type: 'custom', id: 'c1', parentId: 'm1', customType: INPUT_ENTRY, data: { entryId: 'm1', identity: persistInputIdentity(id) } },
  ];
  assert.deepEqual(recordedInputs(branch).get('m1').identity, id);
});

test('plan and run are kept only for a scheduled run, and only in bounds', () => {
  for (const [plan, run] of [['s1', 'x'], ['plan', 's1-a'], ['s0', 's0-a'], ['s1', 's1-' + 'a'.repeat(81)], ['s1', 's1-A']]) {
    assert.deepEqual(inputIdentity({ submissionId, hostOrigin: { source: 'schedule', plan, run } }), { submissionId, origin: { source: 'schedule' } });
  }
  assert.deepEqual(inputIdentity({ submissionId, hostOrigin: { source: 'user', plan: 's1', run: 's1-a' } }), { submissionId, origin: { source: 'user' } });
});

test('other origins are as before', () => {
  assert.deepEqual(inputIdentity({ submissionId }), { submissionId, origin: { source: 'user' } });
  assert.deepEqual(inputIdentity({ hostOrigin: { source: 'lead', kind: 'task' } }), { origin: { source: 'lead', kind: 'task' } });
  assert.deepEqual(inputIdentity({ hostOrigin: { source: 'yesman', kind: 'report', worker: 'w1' } }), { origin: { source: 'yesman', kind: 'report', worker: 'w1' } });
  assert.equal(inputIdentity({ hostOrigin: { source: 'model' } }), undefined);
  assert.equal(inputIdentity(undefined), undefined);
});
