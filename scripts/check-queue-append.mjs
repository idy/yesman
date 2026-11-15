// Capability 4 same-occurrence append, against the actual source-patched CLI in
// RPC mode with the production yesman extension, a third-party hook audit and a
// local deterministic provider. Run: YESMAN_TEST_RUNTIME=/path/to/runtime node ...
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { deflateSync, crc32 } from 'node:zlib';
import assert from 'node:assert/strict';
const runtime = process.env.YESMAN_TEST_RUNTIME;
if (!runtime) throw new Error('Set YESMAN_TEST_RUNTIME to the prepared application runtime');
const temp = await mkdtemp(`${tmpdir()}/yesman-queue-append-`);
await mkdir(`${temp}/config`);
await writeFile(`${temp}/config/settings.json`, JSON.stringify({ packages: [], extensions: [], skills: [], promptTemplates: [], themes: [], compaction: { enabled: false }, retry: { enabled: false }, defaultProjectTrust: 'no' }));
// Third-party extension: every hook view it can see is audited for private IDs.
await writeFile(`${temp}/hook.ts`, `import { appendFileSync } from 'node:fs';
export default (pi) => {
 const audit = (kind, e) => appendFileSync(process.env.APPEND_HOOK_AUDIT, JSON.stringify({ kind, e }) + '\\n');
 pi.on('input', e => audit('input', e));
 pi.on('context', e => audit('context', e.messages));
 pi.on('before_agent_start', e => audit('before_agent_start', e));
 pi.on('message_start', e => audit('message_start', e));
 pi.on('message_end', e => audit('message_end', e));
};`);
let contextHold = null, contextRequests = 0;
const api = createServer(async (req, res) => {
  if (req.url === '/v1/context') {
    contextRequests++;
    if (contextHold) await contextHold.promise; // holds the real idle preparation open
    res.setHeader('Content-Type', 'text/plain');
    res.end('Isolated append fixture');
    return;
  }
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify({ env: {}, hide: [] }));
}).listen(0, '127.0.0.1');
await new Promise(r => api.on('listening', r));
const child = spawn(process.execPath, [resolve(runtime, 'node_modules/@earendil-works/pi-coding-agent/dist/cli.js'), '--mode', 'rpc', '--provider', 'append-test', '--model', 'm', '--session-dir', `${temp}/sessions`, '--no-builtin-tools',
  '-e', fileURLToPath(new URL('../pi/yesman.ts', import.meta.url)),
  '-e', fileURLToPath(new URL('./fixtures/append-provider.ts', import.meta.url)), '-e', `${temp}/hook.ts`],
{ cwd: temp, env: { PATH: process.env.PATH, HOME: temp, TMPDIR: tmpdir(), ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
  PI_CODING_AGENT_DIR: `${temp}/config`, YESMAN_API: `http://127.0.0.1:${api.address().port}`, YESMAN_TOKEN: 'fixture', YESMAN_AGENT: 'fixture/worker', YESMAN_ROLE: 'worker',
  APPEND_GATE: `${temp}/gate`, APPEND_AUDIT: `${temp}/provider`, APPEND_HOOK_AUDIT: `${temp}/hooks` } });
const records = []; let buffer = '', stderr = '', error, seq = 0;
child.on('error', e => { error = e; }); child.stderr.on('data', d => { stderr += d; });
child.stdout.on('data', d => { buffer += d; let n; while ((n = buffer.indexOf('\n')) >= 0) { const line = buffer.slice(0, n); buffer = buffer.slice(n + 1); if (line.trim()) records.push(JSON.parse(line)); } });
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function until(find, what = 'condition') { const deadline = Date.now() + 20000; while (!find()) { if (error) throw error; if (Date.now() > deadline) throw Error(`timeout: ${what}\n${stderr}\n${JSON.stringify(records.slice(-8))}`); await sleep(5); } return find(); }
async function send(cmd) { const id = `fixture-${++seq}`; child.stdin.write(JSON.stringify({ ...cmd, id }) + '\n'); return until(() => records.find(r => r.type === 'response' && r.id === id), cmd.type); }
async function call(cmd) { const r = await send(cmd); assert.equal(r.success, true, JSON.stringify(r)); return r.data; }
async function providerCalls() { try { return (await readFile(`${temp}/provider`, 'utf8')).trim().split('\n').filter(Boolean).map(l => JSON.parse(l)); } catch { return []; } }
async function untilProvider(count) { const deadline = Date.now() + 20000; for (;;) { const calls = await providerCalls(); if (calls.length >= count) return calls; if (Date.now() > deadline) throw Error(`provider call ${count} never started: ${stderr}`); await sleep(5); } }
const open = n => writeFile(`${temp}/gate.${n}`, 'open');
const openAll = () => writeFile(`${temp}/gate.all`, 'open');
const closeAll = () => rm(`${temp}/gate.all`, { force: true });
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
// Distinct valid PNGs, so image order is observable in the provider context.
function png(r, g, b) {
  const chunk = (type, data) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const td = Buffer.concat([Buffer.from(type), data]); const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td)); return Buffer.concat([len, td, crc]); };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(2, 0); ihdr.writeUInt32BE(2, 4); ihdr[8] = 8; ihdr[9] = 2;
  const row = Buffer.from([0, r, g, b, r, g, b]);
  return { type: 'image', mimeType: 'image/png', data: Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', deflateSync(Buffer.concat([row, row]))), chunk('IEND', Buffer.alloc(0))]).toString('base64') };
}
const [img1, img2, img3, img4] = [png(255, 0, 0), png(0, 255, 0), png(0, 0, 255), png(9, 9, 9)];
let idCounter = 0; const ids = [];
const newId = () => { const id = Date.now().toString(16).padStart(12, '0') + (++idCounter).toString(16).padStart(36, '0'); ids.push(id); return id; };
const user = { source: 'user' };
const text = m => typeof m.content === 'string' ? m.content : m.content.filter(b => b.type === 'text').map(b => b.text).join('\n');
const images = m => typeof m.content === 'string' ? [] : m.content.filter(b => b.type === 'image');
const changes = (from, state) => records.slice(from).flatMap(r => r.type === 'queue_update' ? r.pending?.changes ?? [] : []).filter(c => !state || c.state === state);
const usersSince = from => records.slice(from).filter(r => r.type === 'message_end' && r.message?.role === 'user');
let providerCount = 0;
async function holdTurn(message) {
  const r = await call({ type: 'prompt', message });
  assert.equal(r.disposition, 'started');
  providerCount++;
  await untilProvider(providerCount); // the initial steering poll has passed
}
const results = {};
try {
  const initial = await call({ type: 'get_state' });
  assert.equal(initial.pendingQueue.capability, 4); assert.equal(initial.runtime, 'yesman-pi/1.5.2-5');
  await call({ type: 'set_steering_mode', mode: 'one-at-a-time' }); await call({ type: 'set_follow_up_mode', mode: 'one-at-a-time' });
  const malformed = await send({ type: 'prompt', message: 'x', streamingBehavior: 'steer', appendTo: { epoch: 'e', queueId: 'q' } });
  assert.equal(malformed.success, false); assert.match(malformed.error, /appendTo requires/);

  // 1. Unclaimed A: B (two images) and C (image-only) join A's occurrence; pi
  // takes and the provider receives one merged user message exactly once.
  {
    const from = records.length;
    await holdTurn('hold append');
    const idA = newId(), idB = newId(), idC = newId(), idD = newId(), idE = newId(), idF = newId(), idG = newId(), idH = newId();
    const aText = ' alpha 原文\n  keep spacing  ';
    const rA = await call({ type: 'prompt', message: aText, images: [img1], streamingBehavior: 'steer', submissionId: idA, hostOrigin: user });
    assert.deepEqual(rA, { disposition: 'queued' });
    const q0 = await call({ type: 'get_pending_queue' });
    const a = q0.items.find(i => i.submissionId === idA);
    assert.equal(q0.items.length, 1); assert.equal(a.kind, 'steer'); assert.equal(a.itemRevision, 0); assert.equal(a.segments, undefined);
    assert.equal(q0.appendAllowed, true);
    const target = { epoch: q0.epoch, queueId: a.queueId, expectedRevision: 0, controlGeneration: q0.controlGeneration };
    const rB = await call({ type: 'prompt', message: 'beta 补充', images: [img2, img3], streamingBehavior: 'steer', submissionId: idB, hostOrigin: user, appendTo: target });
    assert.deepEqual(rB, { disposition: 'queued', append: { outcome: 'appended', queueId: a.queueId, itemRevision: 1 } });
    // Lost-ACK retry of the same append op: no second append, no new occurrence.
    const rB2 = await call({ type: 'prompt', message: 'beta 补充', images: [img2, img3], streamingBehavior: 'steer', submissionId: idB, hostOrigin: user, appendTo: target });
    assert.deepEqual(rB2.append, { outcome: 'already_appended', queueId: a.queueId, itemRevision: 1 });
    const rC = await call({ type: 'prompt', message: '', images: [img4], streamingBehavior: 'steer', submissionId: idC, hostOrigin: user, appendTo: { ...target, expectedRevision: 1 } });
    assert.equal(rC.append.outcome, 'appended'); assert.equal(rC.append.itemRevision, 2);
    const q1 = await call({ type: 'get_pending_queue' });
    assert.equal(q1.items.length, 1); assert.deepEqual(q1.items[0].segments, [idB, idC]); assert.equal(q1.items[0].itemRevision, 2);
    assert.deepEqual(q1.steering, [`${aText}\n\nbeta 补充`]);
    assert.equal(changes(from, 'appended').length, 2);
    assert.deepEqual(changes(from, 'appended').map(c => c.appendedSubmissionId), [idB, idC]);
    // Every non-eligible request is enqueued once as its own occurrence.
    const rD = await call({ type: 'prompt', message: 'delta stale', streamingBehavior: 'steer', submissionId: idD, hostOrigin: user, appendTo: target });
    assert.equal(rD.append.outcome, 'stale_item'); assert.equal(rD.append.enqueued, true);
    const rE = await call({ type: 'prompt', message: 'echo not latest', streamingBehavior: 'steer', submissionId: idE, hostOrigin: user, appendTo: { ...target, expectedRevision: 2 } });
    assert.equal(rE.append.outcome, 'not_latest'); assert.equal(rE.append.enqueued, true);
    const q2 = await call({ type: 'get_pending_queue' });
    const e = q2.items.find(i => i.submissionId === idE);
    const rF = await call({ type: 'prompt', message: 'foxtrot from lead', streamingBehavior: 'steer', submissionId: idF, hostOrigin: { source: 'lead', kind: 'message' }, appendTo: { ...target, queueId: e.queueId, expectedRevision: 0 } });
    assert.equal(rF.append.outcome, 'ineligible');
    const f = (await call({ type: 'get_pending_queue' })).items.find(i => i.submissionId === idF);
    const rG = await call({ type: 'prompt', message: 'golf stale control', streamingBehavior: 'steer', submissionId: idG, hostOrigin: user, appendTo: { ...target, queueId: f.queueId, expectedRevision: 0, controlGeneration: q0.controlGeneration + 1 } });
    assert.equal(rG.append.outcome, 'stale_control');
    const g = (await call({ type: 'get_pending_queue' })).items.find(i => i.submissionId === idG);
    const rH = await call({ type: 'prompt', message: 'hotel follow-up', streamingBehavior: 'followUp', submissionId: idH, hostOrigin: user, appendTo: { ...target, queueId: g.queueId, expectedRevision: 0 } });
    assert.equal(rH.append.outcome, 'kind_mismatch');
    const q3 = await call({ type: 'get_pending_queue' });
    assert.deepEqual(q3.items.map(i => i.submissionId), [idA, idD, idE, idF, idG, idH]);
    assert.equal((await call({ type: 'get_state' })).isStreaming, true); // no implicit abort
    await openAll();
    await until(() => records.slice(from).some(r => r.type === 'agent_settled'), 'phase 1 settle');
    await closeAll();
    const users = usersSince(from);
    assert.deepEqual(users.map(r => text(r.message)), ['hold append', `${aText}\n\nbeta 补充`, 'delta stale', 'echo not latest', 'foxtrot from lead', 'golf stale control', 'hotel follow-up']);
    const merged = users[1].message;
    assert.deepEqual(merged.content.map(b => b.type), ['text', 'image', 'image', 'image', 'image']);
    assert.deepEqual(images(merged).map(i => i.data), [img1, img2, img3, img4].map(i => i.data));
    const taken = changes(from, 'taken');
    assert.deepEqual(taken.map(c => c.submissionId), [idA, idD, idE, idF, idG, idH]);
    assert.deepEqual(taken[0].segments, [idB, idC]);
    const recorded = records.slice(from).filter(r => r.type === 'input_recorded' && r.inputIdentity?.submissionId === idA);
    assert.equal(recorded.length, 1); assert.deepEqual(recorded[0].inputIdentity.segments, [idB, idC]);
    const calls = await providerCalls(); providerCount = calls.length;
    const last = calls.at(-1).users;
    assert.equal(last.filter(m => text(m).includes('beta 补充')).length, 1, 'merged input must reach the provider once');
    assert.equal(last.filter(m => text(m) === 'beta 补充' || text(m) === '').length, 0, 'no separate B/C user message');
    const firstMerged = calls.findIndex(c => c.users.some(m => text(m).includes('beta 补充')));
    assert.equal(text(calls[firstMerged].users.at(-1)), `${aText}\n\nbeta 补充`);
    assert.deepEqual(images(calls[firstMerged].users.at(-1)).map(i => i.data), [img1, img2, img3, img4].map(i => i.data));
    results.appendWins = { occurrence: 'A+B+C one queueId', duplicateRetry: 'already_appended', providerIntroductions: 1, images: 4,
      independent: { stale_item: 'D', not_latest: 'E', ineligibleOrigin: 'F', stale_control: 'G', kind_mismatch: 'H' } };
  }

  // 2. Checkpoint drain wins: A is taken first, so B becomes its own occurrence once.
  {
    const from = records.length;
    await holdTurn('hold take');
    const idA = newId(), idB = newId();
    await call({ type: 'prompt', message: 'alpha taken', streamingBehavior: 'steer', submissionId: idA, hostOrigin: user });
    const q = await call({ type: 'get_pending_queue' });
    const a = q.items.find(i => i.submissionId === idA);
    await open(providerCount); providerCount++;
    await untilProvider(providerCount); // A drained at the checkpoint; next turn held
    assert.equal(changes(from, 'taken').filter(c => c.submissionId === idA).length, 1);
    const rB = await call({ type: 'prompt', message: 'beta after take', streamingBehavior: 'steer', submissionId: idB, hostOrigin: user,
      appendTo: { epoch: q.epoch, queueId: a.queueId, expectedRevision: 0, controlGeneration: q.controlGeneration } });
    assert.equal(rB.append.outcome, 'not_pending'); assert.equal(rB.append.enqueued, true); assert.notEqual(rB.append.queueId, a.queueId);
    await openAll();
    await until(() => records.slice(from).some(r => r.type === 'agent_settled'), 'phase 2 settle');
    await closeAll();
    assert.deepEqual(usersSince(from).map(r => text(r.message)), ['hold take', 'alpha taken', 'beta after take']);
    assert.deepEqual(changes(from, 'taken').map(c => c.submissionId), [idA, idB]);
    providerCount = (await providerCalls()).length;
    results.takeWins = { b: 'not_pending -> new occurrence', deliveredOnce: true };
  }

  // 3. Idle preparation claims A first: A is Preparing (not taken), B is queued
  // independently, the preparation still commits A alone without a stall.
  async function pendingFollowUpAfterStop(label) {
    const from = records.length;
    await holdTurn(`hold ${label}`);
    const id = newId();
    await call({ type: 'prompt', message: `alpha ${label}`, images: [img1], streamingBehavior: 'followUp', submissionId: id, hostOrigin: user });
    await call({ type: 'abort' });
    await until(() => records.slice(from).some(r => r.type === 'agent_settled'), `${label} stop settle`);
    providerCount = (await providerCalls()).length;
    const q = await call({ type: 'get_pending_queue' });
    assert.deepEqual(q.items.map(i => i.submissionId), [id]); assert.equal(q.appendAllowed, false);
    return { id, q, item: q.items[0], from };
  }
  {
    const { id: idA, q, item } = await pendingFollowUpAfterStop('prepare');
    const from = records.length;
    contextHold = deferred();
    const contextBefore = contextRequests;
    const promoted = await call({ type: 'promote_queued_message', epoch: q.epoch, queueId: item.queueId, expectedRevision: 0, controlGeneration: q.controlGeneration });
    assert.equal(promoted.outcome, 'promoted');
    await until(() => contextRequests > contextBefore, 'preparation context request');
    const p = await call({ type: 'get_pending_queue' });
    const a = p.items.find(i => i.submissionId === idA);
    assert.equal(a.preparing, true); assert.equal(a.kind, 'steer'); assert.equal(a.itemRevision, 1);
    assert.equal(changes(from, 'taken').length, 0, 'Preparing is not Taken');
    assert.equal(changes(from, 'preparing').filter(c => c.submissionId === idA).length, 1);
    const idB = newId();
    const rB = await call({ type: 'prompt', message: 'beta during preparation', images: [img2, img3], streamingBehavior: 'steer', submissionId: idB, hostOrigin: user,
      appendTo: { epoch: p.epoch, queueId: a.queueId, expectedRevision: 1, controlGeneration: p.controlGeneration } });
    assert.equal(rB.append.outcome, 'preparing'); assert.equal(rB.append.enqueued, true);
    const started = Date.now();
    await openAll();
    contextHold.resolve(); contextHold = null;
    await until(() => records.slice(from).some(r => r.type === 'agent_settled'), 'phase 3 settle');
    await closeAll();
    const elapsed = Date.now() - started;
    assert(elapsed < 5000, `preparation stalled ${elapsed} ms`);
    const users = usersSince(from);
    assert.deepEqual(users.map(r => text(r.message)), ['alpha prepare', 'beta during preparation']);
    assert.equal(images(users[0].message).length, 1); assert.equal(images(users[1].message).length, 2);
    assert.deepEqual(changes(from, 'taken').map(c => c.submissionId), [idA, idB]);
    assert.equal(changes(from, 'taken')[0].segments, undefined);
    providerCount = (await providerCalls()).length;
    results.claimWins = { a: 'preparing (not taken) then committed alone', b: 'preparing -> new occurrence', elapsedMs: elapsed };
  }

  // 4. Stop during preparation (original safety): both survive pending with
  // complete payloads, nothing reaches the provider, both stay cancellable.
  {
    const { id: idA, q, item } = await pendingFollowUpAfterStop('stop');
    const from = records.length, callsBefore = (await providerCalls()).length;
    contextHold = deferred();
    const contextBefore = contextRequests;
    await call({ type: 'promote_queued_message', epoch: q.epoch, queueId: item.queueId, expectedRevision: 0, controlGeneration: q.controlGeneration });
    await until(() => contextRequests > contextBefore, 'preparation context request');
    const p = await call({ type: 'get_pending_queue' });
    const idB = newId();
    const rB = await call({ type: 'prompt', message: 'beta before stop', images: [img2], streamingBehavior: 'steer', submissionId: idB, hostOrigin: user,
      appendTo: { epoch: p.epoch, queueId: item.queueId, expectedRevision: 1, controlGeneration: p.controlGeneration } });
    assert.equal(rB.append.outcome, 'preparing');
    // Stop first; pi's abort then waits for the held context hook to return.
    const stopping = call({ type: 'abort' });
    await until(() => records.slice(from).some(r => r.type === 'queue_update' && r.pending?.controlGeneration > p.controlGeneration), 'Stop fence');
    contextHold.resolve(); contextHold = null;
    await stopping;
    await until(() => records.slice(from).some(r => r.type === 'agent_settled'), 'phase 4 settle');
    const s = await call({ type: 'get_pending_queue' });
    assert.deepEqual(s.items.map(i => i.submissionId), [idA, idB]);
    assert.equal(s.items[0].preparing, undefined, 'claim released after Stop');
    assert.equal(changes(from, 'taken').length, 0);
    assert.equal((await providerCalls()).length, callsBefore, 'no provider request for untaken input');
    assert.equal((await call({ type: 'cancel_queued_message', epoch: s.epoch, queueId: s.items[0].queueId, expectedRevision: 1 })).outcome, 'cancelled');
    assert.equal((await call({ type: 'cancel_queued_message', epoch: s.epoch, queueId: s.items[1].queueId, expectedRevision: 0 })).outcome, 'cancelled');
    assert.equal((await call({ type: 'get_pending_queue' })).items.length, 0);
    results.stopDuringPreparation = { a: 'pending, claim released, cancelled', b: 'new occurrence, cancelled', provider: 0 };
  }

  // Private identities never reach hooks, extension events or the provider.
  const hooks = await readFile(`${temp}/hooks`, 'utf8'), provider = await readFile(`${temp}/provider`, 'utf8');
  for (const id of ids) { assert.equal(hooks.includes(id), false, `hook saw ${id}`); assert.equal(provider.includes(id), false, `provider saw ${id}`); }
  for (const field of ['segments', 'appendTo', 'appendedSubmissionId', 'queueId', 'submissionId']) {
    assert.equal(hooks.includes(`"${field}"`), false, `hook saw ${field}`); assert.equal(provider.includes(`"${field}"`), false, `provider saw ${field}`);
  }
  const report = { runtime: initial.runtime, capability: initial.pendingQueue.capability, results, privateIdsInHooksOrProvider: false,
    scope: 'Actual patched CLI + Session + native queue + RPC; production yesman.ts; local deterministic provider; no Go/UI claim' };
  if (process.env.YESMAN_QUEUE_APPEND_EVIDENCE) await writeFile(process.env.YESMAN_QUEUE_APPEND_EVIDENCE, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report, null, 2));
} finally {
  contextHold?.resolve();
  child.stdin.end(); const kill = setTimeout(() => child.kill('SIGKILL'), 1000);
  await new Promise(r => child.exitCode !== null || child.signalCode !== null || error ? r() : child.once('exit', r)); clearTimeout(kill);
  await new Promise(r => api.close(r)); await rm(temp, { recursive: true, force: true });
}
