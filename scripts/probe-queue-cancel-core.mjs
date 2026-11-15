// TEST-ONLY prototype of the proposed pi-agent-core API. Node's loader applies
// the small delta to the installed core IN MEMORY; no package file is written.
// This is not a yesman runtime shim and does not provide RPC cancellation.
import assert from 'node:assert/strict';
import { registerHooks, createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { readFile, writeFile, realpath } from 'node:fs/promises';
import { createHash } from 'node:crypto';

const root = process.env.YESMAN_TEST_PI_PACKAGE || resolve(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), '@earendil-works/pi-coding-agent');
const require = createRequire(resolve(root, 'package.json'));
const coreIndex = await realpath(resolve(require.resolve('@earendil-works/pi-agent-core/package.json'), '../dist/index.js'));
const coreFile = resolve(coreIndex, '../agent.js');
const coreURL = pathToFileURL(coreFile).href;
const original = await readFile(coreFile, 'utf8');
const digest = createHash('sha256').update(original).digest('hex');
// This prototype must be re-reviewed against any changed dependency source.
assert.equal(digest, 'f45ae001e2b14fced8ae151f0a00303b5fbdb5fc4b25418d45de6a5546f48a8c', 'Expected the audited pi-agent-core 1.5.2 source');
const changes = [
  ['    enqueue(message) {\n        this.messages.push(message);\n    }', `    enqueue(message) {
        const handle = Symbol("pending-message");
        this.messages.push({ handle, message });
        return handle;
    }
    remove(handle) {
        const index = this.messages.findIndex((entry) => entry.handle === handle);
        if (index < 0) return false;
        this.messages.splice(index, 1);
        return true;
    }`],
  ['            return this.messages.slice();\n        const first = this.messages[0];\n        return first ? [first] : [];', '            return this.messages.map((entry) => entry.message);\n        const first = this.messages[0];\n        return first ? [first.message] : [];'],
  ['        this.steeringQueue.enqueue(message);', '        return this.steeringQueue.enqueue(message);'],
  ['        this.followUpQueue.enqueue(message);', '        return this.followUpQueue.enqueue(message);'],
  ['    /** Remove all queued steering messages. */', `    /** Synchronous: competes with drain(), not with later message events. */
    cancelQueued(handle) {
        return this.steeringQueue.remove(handle) || this.followUpQueue.remove(handle);
    }
    /** Remove all queued steering messages. */`],
];
let patched = original;
for (const [before, after] of changes) {
  assert.equal(patched.split(before).length, 2, 'Patch anchor must be unique');
  patched = patched.replace(before, after);
}
const hook = registerHooks({ load(url, context, nextLoad) {
  const result = nextLoad(url, context);
  return url === coreURL ? { ...result, source: patched } : result;
} });
const { Agent } = await import(pathToFileURL(coreIndex));
const { createAssistantMessageEventStream } = await import(pathToFileURL(resolve(root, 'node_modules/@earendil-works/pi-ai/dist/index.js')));
hook.deregister();
const image = { type: 'image', mimeType: 'image/png', data: 'synthetic-a' };
const user = (text = 'same', images = []) => ({ role: 'user', content: [{ type: 'text', text }, ...images], timestamp: 1 });
const model = { id: 'fixture', name: 'Fixture', api: 'fixture', provider: 'fixture', baseUrl: '', input: ['text', 'image'], contextWindow: 1000, maxTokens: 10, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } };
const usage = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
async function until(find) {
  const deadline = Date.now() + 3000;
  while (!find()) {
    if (Date.now() > deadline) throw new Error('Core probe timed out');
    await new Promise((r) => setTimeout(r, 1));
  }
}
const results = [];
for (const mode of ['all', 'one-at-a-time']) {
  let release, requested = false;
  const gate = new Promise((resolve) => { release = resolve; });
  const received = [], requests = [];
  const agent = new Agent({ initialState: { model }, steeringMode: mode, followUpMode: mode,
    streamFn: (_model, context) => {
      const stream = createAssistantMessageEventStream();
      requested = true;
      requests.push(context.messages);
      void gate.then(() => {
        const message = { role: 'assistant', content: [{ type: 'text', text: 'done' }], api: 'fixture', provider: 'fixture', model: 'fixture', usage, stopReason: 'stop', timestamp: Date.now() };
        stream.push({ type: 'done', reason: 'stop', message });
        stream.end();
      });
      return stream;
    },
  });
  agent.subscribe((event) => { if (event.type === 'message_start' && event.message.role === 'user') received.push(event.message); });
  const initial = user('initial');
  const running = agent.prompt(initial);
  await until(() => requested);
  const a = user('same', [image]), b = user('same', [{ ...image, data: 'synthetic-b' }]), c = user('same', [image]);
  const steering = [a, b, c].map((m) => agent.steer(m));
  // Two writers can even enqueue the very same message object. Handles must
  // still identify occurrences, not content or message object identity.
  const f = user('same', [image, image]);
  const follow = [f, f, f].map((m) => agent.followUp(m));
  assert.equal(new Set([...steering, ...follow]).size, 6);
  assert.equal(agent.cancelQueued(Symbol('unknown')), false);
  assert.deepEqual(await Promise.all([agent.cancelQueued(steering[1]), agent.cancelQueued(steering[1])]), [true, false]);
  assert.equal(agent.cancelQueued(follow[1]), true);
  // Peeking does not consume; a successfully removed item must never reach
  // the real installed agent loop's user-message events or model context.
  assert(agent.peekQueuedMessages().length > 0);
  release();
  await running;
  assert.deepEqual(received, [initial, a, c, f, f]);
  assert.equal(requests.some((messages) => messages.includes(b)), false);
  assert.equal(agent.hasQueuedMessages(), false);
  for (const handle of [...steering, ...follow]) assert.equal(agent.cancelQueued(handle), false);
  assert.equal(JSON.stringify(requests).includes('pending-message'), false);
  // Both race orderings: cancellation wins before drain; consumption wins
  // before the (awaited) message_start observer. No removed item is replayed.
  for (const behavior of ['steer', 'followUp']) {
    const m = user('race');
    const handle = agent[behavior](m);
    let duringDelivery;
    const unsubscribe = agent.subscribe((e) => {
      if (e.type === 'message_start' && e.message === m) duringDelivery = agent.cancelQueued(handle);
    });
    await agent.continue();
    unsubscribe();
    assert.equal(duringDelivery, false);
    assert.equal(received.filter((message) => message === m).length, 1);
  }
  results.push({ mode, uniqueHandles: 6, canceledMiddle: 2, survivorsInOrder: 4, duplicateCancel: false, afterDrainCancel: false });
}
// Guard that the installed module remained byte-for-byte unchanged.
assert.equal(await readFile(coreFile, 'utf8'), original);
const result = { result: 'prototype-core-primitive-pass', digest, results,
  limitation: 'In-memory proposed core patch only. Installed RPC/SDK/extension APIs remain unchanged and unsupported; no yesman cancellation implemented.' };
if (process.env.YESMAN_QUEUE_CORE_EVIDENCE) await writeFile(process.env.YESMAN_QUEUE_CORE_EVIDENCE, JSON.stringify(result, null, 2) + '\n');
console.log(JSON.stringify(result, null, 2));
