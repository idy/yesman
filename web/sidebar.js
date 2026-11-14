// A report closes a worker and records finishedAt. Stopped/failed workers
// may have ended without a report. Starting, idle, working and waiting workers
// still belong at the front, including workers asking their lead a question.
const ended = (worker) => Boolean(worker.finishedAt) || worker.status === 'stopped' || worker.status === 'failed';

// Sort a copy for display only. Stable sort preserves start order within each
// class, and keeps the original agent objects (IDs, numbers and sessions).
export function orderWorkers(workers) {
  return [...workers].sort((a, b) => Number(ended(a)) - Number(ended(b)));
}
