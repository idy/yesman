// Capability probe for the installed pi, using only synthetic input and a local
// deterministic provider. This deliberately exercises clear_queue ONLY in its
// isolated child; it is never an implementation of per-message cancellation.
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const temp = await mkdtemp(`${tmpdir()}/yesman-queue-capabilities-`);
await mkdir(`${temp}/config`);
await writeFile(`${temp}/config/settings.json`, JSON.stringify({
  packages: [], extensions: [], skills: [], promptTemplates: [], themes: [],
  compaction: { enabled: false }, retry: { enabled: false }, defaultProjectTrust: 'no',
}));
const child = spawn(process.env.YESMAN_TEST_PI || 'pi', [
  '--mode', 'rpc', '--provider', 'delivery-test', '--model', 'm', '--no-session',
  '--no-builtin-tools', '-e', fileURLToPath(new URL('./fixtures/delivery-provider.ts', import.meta.url)),
], { cwd: temp, env: {
  PATH: process.env.PATH, HOME: temp, TMPDIR: tmpdir(),
  ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
  PI_CODING_AGENT_DIR: `${temp}/config`, DELIVERY_AUDIT: `${temp}/audit`, DELIVERY_GATE: `${temp}/gate`,
} });
const records = [];
let buffer = '', stderr = '', spawnError, sequence = 0;
child.on('error', (error) => { spawnError = error; });
child.stderr.on('data', (data) => { stderr += data; });
child.stdout.on('data', (data) => {
  buffer += data;
  let end;
  while ((end = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, end);
    buffer = buffer.slice(end + 1);
    if (line.trim()) records.push(JSON.parse(line));
  }
});
async function until(find) {
  const deadline = Date.now() + 20000;
  while (!find()) {
    if (spawnError) throw spawnError;
    if (Date.now() > deadline) throw new Error(`pi timeout: ${stderr}\n${JSON.stringify(records.slice(-5))}`);
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  return find();
}
async function call(command) {
  const id = `probe-${++sequence}`;
  child.stdin.write(JSON.stringify({ ...command, id }) + '\n');
  return until(() => records.find((record) => record.type === 'response' && record.id === id));
}
const image = {
  type: 'image', mimeType: 'image/png',
  data: 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==',
};
try {
  assert.equal((await call({ type: 'get_state' })).success, true);
  assert.equal((await call({ type: 'prompt', message: 'hold' })).data.disposition, 'started');
  await until(() => records.find((r) => r.type === 'message_end' && r.message?.role === 'user'));
  // Distinct RPC request IDs do not become native queue item identities.
  for (const command of [
    { type: 'steer', message: 'same', images: [image] },
    { type: 'steer', message: 'same', images: [image, image] },
    { type: 'follow_up', message: 'same', images: [image] },
  ]) assert.equal((await call(command)).data.disposition, 'queued');
  const stateBefore = await call({ type: 'get_state' });
  assert.equal(stateBefore.data.pendingMessageCount, 3);
  const queue = records.filter((r) => r.type === 'queue_update').at(-1);
  assert.deepEqual(queue.steering, ['same', 'same']);
  assert.deepEqual(queue.followUp, ['same']);
  const attempt = await call({ type: 'cancel_queued_message', queueId: 'probe-4' });
  assert.equal(attempt.success, false);
  const stateAfterAttempt = await call({ type: 'get_state' });
  assert.equal(stateAfterAttempt.data.pendingMessageCount, 3);
  // Supplying a selected ID to clear_queue does not narrow its scope. This is
  // why yesman must never use it for a single row's Cancel button.
  const clear = await call({ type: 'clear_queue', queueId: 'probe-4' });
  assert.deepEqual(clear.data, { steering: ['same', 'same'], followUp: ['same'] });
  assert.equal((await call({ type: 'get_state' })).data.pendingMessageCount, 0);
  assert.equal(JSON.stringify(clear.data).includes(image.data), false);
  await writeFile(`${temp}/gate`, 'release');
  await until(() => records.find((r) => r.type === 'agent_settled'));
  const history = await call({ type: 'get_messages' });
  assert.equal(history.data.messages.filter((m) => m.role === 'user').length, 1);
  const report = { result: 'capability-gap-confirmed', queue, attempt, clear,
    before: stateBefore.data.pendingMessageCount, afterRejectedCancel: stateAfterAttempt.data.pendingMessageCount,
    usersAfterClear: history.data.messages.filter((m) => m.role === 'user').length,
    providerRequests: (await readFile(`${temp}/audit`, 'utf8')).trim().split('\n').length,
    scope: 'Unmodified installed pi RPC, isolated synthetic inputs, local deterministic provider; no per-item cancellation available.' };
  if (process.env.YESMAN_QUEUE_EVIDENCE) await writeFile(process.env.YESMAN_QUEUE_EVIDENCE, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report, null, 2));
} finally {
  child.stdin.end();
  const kill = setTimeout(() => child.kill('SIGKILL'), 1000);
  await new Promise((resolve) => {
    if (child.exitCode !== null || child.signalCode !== null || spawnError) resolve();
    else child.once('exit', resolve);
  });
  clearTimeout(kill);
  await rm(temp, { recursive: true, force: true });
}
