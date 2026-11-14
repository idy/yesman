import assert from 'node:assert/strict';
import { test } from 'node:test';
import { orderWorkers } from '../web/sidebar.js';

const worker = (number, status, finishedAt) => ({ id: `alma/w${number}`, number, status, finishedAt });
const numbers = (workers) => orderWorkers(workers).map((w) => w.number);

test('active workers precede ended workers, preserving both orders and identities', () => {
  const workers = [
    worker(1, 'stopped', '2026-11-14T00:00:00Z'),
    worker(2, 'waiting'),
    worker(3, 'failed'),
    worker(4, 'starting'),
    worker(5, 'stopped'),
    worker(6, 'idle'),
    worker(7, 'working'),
    worker(8, 'idle', '2026-11-14T00:00:00Z'),
  ].map(Object.freeze);
  Object.freeze(workers);
  const sorted = orderWorkers(workers);
  assert.deepEqual(sorted.map((w) => w.number), [2, 4, 6, 7, 1, 3, 5, 8]);
  assert.deepEqual(workers.map((w) => w.number), [1, 2, 3, 4, 5, 6, 7, 8]);
  for (const w of sorted) assert.equal(w, workers[w.number - 1]);
  assert.deepEqual(orderWorkers([]), []);
});

test('finishing and reactivating reorders the display from the original snapshot', () => {
  const workers = [worker(1, 'working'), worker(2, 'waiting'), worker(3, 'stopped', '2026-11-14T00:00:00Z')];
  assert.deepEqual(numbers(workers), [1, 2, 3]);
  workers[0] = { ...workers[0], status: 'stopped', finishedAt: '2026-11-14T00:01:00Z' };
  assert.deepEqual(numbers(workers), [2, 1, 3]);
  workers[2] = { ...workers[2], status: 'starting', finishedAt: undefined };
  assert.deepEqual(numbers(workers), [2, 3, 1]);
  workers[0] = { ...workers[0], status: 'working', finishedAt: undefined };
  assert.deepEqual(numbers(workers), [1, 2, 3]);
});
