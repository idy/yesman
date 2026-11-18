import assert from 'node:assert/strict';
import { test } from 'node:test';
import { project, originOf } from '../web/conversation.js';

// Messages shaped like pi's (docs/message-types.md), with origins as yesman's
// extension records them. Contents are synthetic.
let clock = 1000;
const at = () => ++clock;
const sub = (n) => n.toString(16).padStart(48, '0');
const user = (text, yesman, images = 0) => ({
  role: 'user', timestamp: at(), ...(yesman ? { yesman } : {}),
  content: [{ type: 'text', text }, ...Array.from({ length: images }, () => ({ type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgo=' }))],
});
const assistant = (blocks, stopReason = 'stop', extra = {}) => ({ role: 'assistant', timestamp: at(), stopReason, content: blocks, ...extra });
const thinking = (text) => ({ type: 'thinking', thinking: text, thinkingSignature: 'opaque' });
const text = (t) => ({ type: 'text', text: t });
let calls = 0;
const call = (name, args = {}) => ({ type: 'toolCall', id: `call_${++calls}`, name, arguments: args });
const result = (c, isError = false) => ({ role: 'toolResult', timestamp: at(), toolCallId: c.id, toolName: c.name, isError, content: [{ type: 'text', text: 'ok' }] });
const tools = (...entries) => new Map(entries.map(([c, status]) => [c.id, { status }]));
const report = (worker, body) => user(`[yesman] Worker "${worker}" finished a run. Its final message:\n\n${body}`, { source: 'yesman', kind: 'report', worker });
const shape = (rows) => rows.map((r) => (r.type === 'work' ? `work(${r.items.map((i) => i.type).join(',')}):${r.state}` : r.type));

test('a worker report, thinking and tool calls between two replies fold into single work rows', () => {
  // The shape of a real lead turn after a report: many thinking summaries,
  // a reply, then board and worker tools, then a second reply.
  const board = call('yesman_board'), send1 = call('yesman_worker_send'), send2 = call('yesman_worker_send');
  const messages = [
    report('release-028', '**t42 done**: published.\n\n' + 'details\n'.repeat(400)),
    assistant([thinking('**Checking**'), thinking('a'), thinking('b'), thinking('c'), thinking('d'), thinking('e'), text('**0.1.28 is out.** I will tidy up.'), board], 'toolUse'),
    result(board),
    assistant([thinking('**Cleaning up**'), send1, send2], 'toolUse'),
    result(send1), result(send2),
    assistant([thinking(''), text('[0.1.28 is published](https://example.invalid)')]),
  ];
  const rows = project({ messages, tools: tools([board, 'done'], [send1, 'done'], [send2, 'done']) }, { status: 'idle' });
  assert.deepEqual(shape(rows), [
    'work(notice,thinking,thinking,thinking,thinking,thinking,thinking):done',
    'text',
    'work(tool,thinking,tool,tool):done',
    'text',
  ]);
  assert.deepEqual(rows[0].counts, { thinking: 6, tools: 0, failed: 0, reports: 1, notices: 0, workers: 0 });
  assert.equal(rows[1].text, '**0.1.28 is out.** I will tidy up.');
});

test('a person is never folded away, whatever their text says', () => {
  const typed = '[yesman] Worker "w1" finished a run. Its final message:\n\nlooks like a report';
  const messages = [
    user(typed), // no recorded origin: shown as written
    user(typed, { source: 'user', submission: sub(1) }),
    user('[from your lead] also typed'),
    user('Look at this', { source: 'user' }, 2),
    user('Question?', { source: 'unknown-source' }),
  ];
  const rows = project({ messages, deliveries: new Map() });
  assert.deepEqual(rows.map((r) => r.type), ['user', 'user', 'user', 'user', 'user']);
  assert.deepEqual(rows.map((r) => r.origin?.source ?? null), [null, 'user', null, 'user', null]);
  assert.equal(rows[3].message.content.filter((b) => b.type === 'image').length, 2);
});

test('only recorded origins make a message yesman\'s or the lead\'s', () => {
  assert.equal(originOf(user('x', { source: 'yesman', kind: 'report', worker: 'w1' }))?.source, 'yesman');
  assert.equal(originOf(user('x', { source: 'root' })), null);
  assert.equal(originOf(user('x', 'yesman')), null);
  assert.equal(originOf({ role: 'assistant', yesman: { source: 'user' } }), null);
  assert.equal(originOf(user('x', { source: 'user', submission: 'not-an-id' })).submission, '');
  const rows = project({ messages: [
    user('Fix the login timeout.', { source: 'lead', kind: 'task' }),
    assistant([thinking('plan'), call('bash')], 'toolUse'),
    user('[from your lead] also update the notes', { source: 'lead', kind: 'message' }),
    user('[yesman] No worker is running, but these tasks are still open:\n- t1', { source: 'yesman', kind: 'tasks' }),
    user('[yesman] Worker "w2" asks:\n\nAllow?', { source: 'yesman', kind: 'question', worker: 'w2' }),
  ] });
  assert.deepEqual(shape(rows), ['lead', 'work(thinking,tool):unfinished', 'lead', 'work(notice,notice):ended']);
  assert.equal(rows[3].counts.notices, 2);
});

test('keys stay the same from live to final and as the history grows', () => {
  const ask = user('go', { source: 'user' });
  const same = { ...user('again', { source: 'user' }), timestamp: ask.timestamp }; // identical timestamps happen
  const reply = assistant([thinking('one'), thinking('two'), text('Partial')]);
  const live = { message: { ...reply, stopReason: undefined, content: reply.content.slice(0, 2) } };
  const streaming = project({ messages: [ask, same], live }, { status: 'working' });
  assert.notEqual(streaming[0].key, streaming[1].key);
  assert.deepEqual(shape(streaming), ['user', 'user', 'work(thinking,thinking):running']);
  const final = project({ messages: [ask, same, reply] }, { status: 'working' });
  assert.equal(final[2].key, streaming[2].key, 'the work row keeps its key when its message ends');
  assert.deepEqual(final[2].items.map((i) => i.key), streaming[2].items.map((i) => i.key));
  assert.equal(final[2].state, 'done', 'a reply after the work ends it, not message_end alone');
  const later = project({ messages: [ask, same, reply, user('more', { source: 'user' })] });
  assert.deepEqual(later.slice(0, 4).map((r) => r.key), final.map((r) => r.key));
});

test('a message sent while the agent works splits its work without moving or swallowing it', () => {
  const t1 = call('read'), t2 = call('bash');
  const messages = [
    user('first', { source: 'user', submission: sub(1) }),
    assistant([thinking('look'), t1], 'toolUse'),
    result(t1),
    user('also check the notes', { source: 'user', submission: sub(2) }), // steering, delivered after the tool
    assistant([thinking('more'), t2], 'toolUse'),
  ];
  const deliveries = new Map([
    [sub(1), { id: sub(1), state: 'accepted', order: 1 }],
    [sub(2), { id: sub(2), state: 'steering', order: 2 }],
    [sub(3), { id: sub(3), state: 'queued', order: 3, text: 'a third, still queued' }],
    [sub(4), { id: sub(4), revision: 2, state: 'dispatched', order: 4, text: 'taken, its message not in view' }],
  ]);
  const convo = { messages, tools: tools([t1, 'done'], [t2, 'running']), deliveries };
  const rows = project(convo, { status: 'working' });
  assert.deepEqual(shape(rows), ['user', 'work(thinking,tool):done', 'user', 'work(thinking,tool):running', 'receipt']);
  assert.equal(rows[0].delivery.id, sub(1));
  assert.equal(rows[2].delivery.id, sub(2));
  // The queued one is the person's message still to come; the one pi took
  // shows nothing more.
  assert.equal(rows[4].delivery.id, sub(3));
  // The run ends without a reply: its last work ended, nothing says done for it.
  assert.equal(project({ ...convo, tools: tools([t1, 'done'], [t2, 'done']) }, { status: 'idle' })[3].state, 'ended');
  // A process that died mid-tool is interrupted; in older history the tool is unfinished.
  assert.equal(project(convo, { status: 'failed' })[3].state, 'interrupted');
  assert.equal(project({ ...convo, messages: [...messages, assistant([text('next')])] }, { status: 'idle' })[3].state, 'unfinished');
  // Waiting on a question.
  assert.equal(project(convo, { status: 'waiting' })[3].state, 'waiting');
});

test('stops, errors and failed tools are stated, and errors stay in the conversation', () => {
  const bad = call('bash'), start = call('yesman_worker_start', { name: 'w1' }), taken = call('yesman_worker_start', { name: 'w1' });
  const rows = project({
    messages: [
      user('go', { source: 'user' }),
      assistant([thinking('try'), bad, start, taken], 'toolUse'),
      result(bad, true), result(start), result(taken, true),
      assistant([thinking('again')], 'aborted'),
      user('retry', { source: 'user' }),
      assistant([thinking('x')], 'error', { errorMessage: 'provider unavailable' }),
    ],
    tools: tools([bad, 'error'], [start, 'done'], [taken, 'error']),
  }, { status: 'idle' });
  assert.deepEqual(shape(rows), ['user', 'work(thinking,tool,tool,tool,thinking):stopped', 'note', 'user', 'work(thinking):error', 'note']);
  // A worker start that failed, or has not run, started no worker.
  assert.deepEqual(rows[1].counts, { thinking: 2, tools: 3, failed: 2, reports: 0, notices: 0, workers: 1 });
  const streaming = project({ messages: [assistant([call('yesman_worker_start')], 'toolUse')], tools: new Map() }, { status: 'working' });
  assert.equal(streaming[0].counts.workers, 0);
  assert.equal(rows[2].stopped, true);
  assert.equal(rows[5].text, 'provider unavailable');
});

// The shape of a lead out of provider quota: every worker report starts a run
// that thinks, calls a tool or two and ends on the same error.
const LIMIT = 'server_error: Codex Magpie: Codex: The usage limit has been reached (429: you can retry your request)';
const failedRun = (n, message = LIMIT, extra = {}) => {
  const c = call('yesman_board');
  return [
    report(`w${n}`, `report ${n}`),
    assistant([thinking('a'), thinking('b'), c], 'toolUse'),
    result(c),
    assistant([thinking('c')], 'error', { errorMessage: message, provider: 'codex', model: 'gpt-5', ...extra }),
  ];
};
const shapes = (rows) => rows.map((r) => (r.type === 'failures' ? `failures(${r.errors.map((e) => e.runs.length).join('+')})` : r.type === 'work' ? `work:${r.state}` : r.type));

test('runs in a row that end on nothing but an error fold into one row that keeps each of them', () => {
  const messages = Array.from({ length: 7 }, (_, i) => failedRun(i)).flat();
  const convo = { messages, tools: new Map() };
  const rows = project(convo, { status: 'idle' });
  assert.deepEqual(shapes(rows), ['failures(7)']);
  const [row] = rows;
  assert.equal(row.runs.length, 7);
  assert.equal(row.errors.length, 1);
  assert.equal(row.errors[0].text, LIMIT);
  assert.equal(row.errors[0].provider, 'codex');
  assert.deepEqual([...row.errors[0].models], ['gpt-5']);
  // Every run keeps its work and note as they were: the report, thinking and tools.
  for (const run of row.runs) {
    assert.equal(run.work.state, 'error');
    assert.deepEqual(run.work.counts, { thinking: 3, tools: 1, failed: 0, reports: 1, notices: 0, workers: 0 });
    assert.equal(run.note.text, LIMIT);
    assert.equal(typeof run.note.failure.at, 'number');
  }
  // Projecting again, or with the history read again, folds the same: the
  // count does not grow and the key stays.
  const again = project({ messages: structuredClone(messages), tools: new Map() }, { status: 'idle' });
  assert.equal(again[0].key, row.key);
  assert.equal(again[0].runs.length, 7);
  // An eighth run joins the same row, under the same key.
  const more = project({ messages: [...messages, ...failedRun(8)], tools: new Map() }, { status: 'idle' });
  assert.deepEqual(shapes(more), ['failures(8)']);
  assert.equal(more[0].key, row.key);
  // The messages are not changed.
  assert.equal(messages.length, 28);
});

test('a single failed run stays as it was; different errors stay apart in one row', () => {
  assert.deepEqual(shapes(project({ messages: failedRun(1), tools: new Map() }, { status: 'idle' })), ['work:error', 'note']);
  const rate = 'rate_limit_exceeded: Too many requests, please retry in 20s (429)';
  const rows = project({ messages: [...failedRun(1), ...failedRun(2, rate), ...failedRun(3), ...failedRun(4, `  ${LIMIT}\n`)], tools: new Map() }, { status: 'idle' });
  assert.deepEqual(shapes(rows), ['failures(3+1)']);
  // Same message from another provider is another error, and a message that
  // only shares a beginning is another error too.
  const other = project({ messages: [...failedRun(1), ...failedRun(2, LIMIT, { provider: 'anthropic' }), ...failedRun(3, `${LIMIT} for org-2`)], tools: new Map() }, { status: 'idle' });
  assert.deepEqual(shapes(other), ['failures(1+1+1)']);
  // Error runs with no work before them fold too, as notes alone.
  const bare = project({ messages: [assistant([], 'error', { errorMessage: LIMIT, provider: 'codex' }), assistant([], 'error', { errorMessage: LIMIT, provider: 'codex' })] }, { status: 'idle' });
  assert.deepEqual(shapes(bare), ['failures(2)']);
  assert.equal(bare[0].runs[0].work, null);
});

test('a person, a reply, part of a reply or a stop ends a fold, and none of them is folded', () => {
  const partial = [report('w9', 'r'), assistant([thinking('x'), text('Here is what I found so far: …')], 'error', { errorMessage: LIMIT, provider: 'codex' })];
  const rows = project({ messages: [
    ...failedRun(1), ...failedRun(2),
    user('Is the quota back? It said error 429 before.', { source: 'user' }, 1),
    ...failedRun(3), ...failedRun(4),
    assistant([text('Recovered: the board is up to date.')]),
    ...failedRun(5), ...failedRun(6),
    ...partial,
    ...failedRun(7), ...failedRun(8),
    assistant([thinking('y')], 'aborted'),
    ...failedRun(10), ...failedRun(11),
    user('[from your lead] carry on', { source: 'lead', kind: 'message' }),
    ...failedRun(12), ...failedRun(13),
  ], tools: new Map() }, { status: 'idle' });
  assert.deepEqual(shapes(rows), [
    'failures(2)', 'user', 'failures(2)', 'text', 'failures(2)',
    'work:done', 'text', 'note',
    'failures(2)', 'work:stopped', 'note', 'failures(2)', 'lead', 'failures(2)',
  ]);
  assert.equal(rows[1].message.content.filter((b) => b.type === 'image').length, 1);
  assert.equal(rows[6].text, 'Here is what I found so far: …');
  assert.equal(rows[7].failure, null, 'an error after part of a reply never folds');
  // A reply being written that fails is not folded either until pi records it.
  const live = project({ messages: [...failedRun(1), ...failedRun(2)], live: { message: assistant([thinking('z')], 'error', { errorMessage: LIMIT, provider: 'codex' }) }, tools: new Map() }, { status: 'working' });
  assert.deepEqual(shapes(live), ['failures(2)', 'work:error', 'note']);
});

test('an error pi recorded without a message is stated and folds like any other', () => {
  // pi's stopReason says the run failed, whatever errorMessage holds.
  const one = project({ messages: failedRun(1, '') }, { status: 'idle' });
  assert.deepEqual(shapes(one), ['work:error', 'note']);
  assert.equal(one[1].tone, 'error');
  assert.equal(one[1].blank, true);
  const missing = failedRun(1);
  delete missing.at(-1).errorMessage;
  assert.equal(project({ messages: missing }, { status: 'idle' })[1].blank, true);
  // Seven of them fold into one row under one blank error; one with a
  // message stays its own error.
  const rows = project({ messages: [...Array.from({ length: 7 }, (_, i) => failedRun(i, i % 2 ? '' : '  ')).flat(), ...failedRun(8)] }, { status: 'idle' });
  assert.deepEqual(shapes(rows), ['failures(7+1)']);
  assert.equal(rows[0].errors[0].blank, true);
  assert.equal(rows[0].errors[1].blank, false);
  assert.equal(rows[0].errors[1].text, LIMIT);
  // Part of a reply before it, or a person's message after it, stays apart.
  const partial = project({ messages: [...failedRun(1, ''), assistant([text('So far: …')], 'error', { provider: 'codex' }), user('error?', { source: 'user' }), ...failedRun(2, ''), ...failedRun(3, '')] }, { status: 'idle' });
  assert.deepEqual(shapes(partial), ['work:error', 'note', 'text', 'note', 'user', 'failures(2)']);
  assert.equal(partial[3].failure, null);
});

test('a text that reads like an error is never a failure', () => {
  const rows = project({ messages: [
    user('error: 429 usage limit reached', { source: 'user' }),
    report('w1', 'server_error: usage limit reached (429)'),
    assistant([thinking('x')]),
    report('w2', 'server_error: usage limit reached (429)'),
    assistant([thinking('y')]),
  ], tools: new Map() }, { status: 'idle' });
  assert.deepEqual(shapes(rows), ['user', 'work:ended']);
  assert(!rows.some((r) => r.failure || r.type === 'failures' || r.type === 'note'));
});

test('pending and failed receipts stay apart from folded runs', () => {
  const pending = { id: sub(41), revision: 1, state: 'queued', order: 1, nativePending: true, text: 'queued while failing' };
  const failed = { id: sub(42), revision: 2, state: 'failed', order: 2, error: 'rejected', text: 'failed', agentSince: 's1' };
  const rows = project({ messages: [...failedRun(1), ...failedRun(2)], tools: new Map(), deliveries: new Map([[pending.id, pending], [failed.id, failed]]) }, { status: 'idle', since: 's1' });
  assert.deepEqual(shapes(rows), ['failures(2)', 'receipt', 'receipt']);
});

test('empty and redacted thinking add nothing; text is kept even when it reads like process', () => {
  const rows = project({ messages: [
    assistant([thinking(''), thinking('  '), { type: 'thinking', thinking: '', redacted: true, thinkingSignature: 'x' }]),
    assistant([text('Let me check the board.'), call('yesman_board')], 'toolUse'),
    { role: 'system', content: '', sections: { yesman: 'context' }, timestamp: at() },
    { role: 'compactionSummary', summary: 's', tokensBefore: 1, timestamp: at() },
    { role: 'custom', customType: 'ext', display: false, content: 'hidden', timestamp: at() },
    { role: 'custom', customType: 'ext', display: true, content: 'shown', timestamp: at() },
  ] });
  assert.deepEqual(shape(rows), ['text', 'work(tool):unfinished', 'note', 'note']);
  assert.equal(rows[0].text, 'Let me check the board.');
  assert.equal(rows[2].summary, true);
  assert.equal(rows[3].text, 'shown');
});

test('a scheduled run is its own row, bound to its receipt, never a person', () => {
  const id = sub(7);
  const messages = [
    user('[yesman] Scheduled task s1 "Check" is due.', { source: 'schedule', plan: 's1', run: 's1-20261116t0900', submission: id }),
    user('typed by a person', { source: 'user' }),
    user('[yesman] Scheduled task s2 is due, but nothing recorded who wrote it'),
    user('bad plan', { source: 'schedule', plan: '../x' }),
  ];
  const deliveries = new Map([[id, { id, state: 'queued', order: 1 }], [sub(8), { id: sub(8), state: 'sending', order: 2, origin: { source: 'schedule', plan: 's1', run: 's1-m-0123456789abcdef' } }]]);
  const rows = project({ messages, deliveries });
  assert.deepEqual(rows.map((r) => r.type), ['scheduled', 'user', 'user', 'scheduled', 'receipt']);
  assert.equal(rows[0].origin.plan, 's1');
  assert.equal(rows[0].delivery.id, id);
  assert.equal(rows[3].origin.plan, '');
  assert.equal(rows[4].delivery.origin.source, 'schedule');
  assert.equal(originOf(messages[0]).source, 'schedule');
});

// Receipts outlive the messages they were for: those from before messages were
// tied to receipts, and those whose message a summary of earlier conversation
// replaced, are kept by the server for its retention period. The conversation
// is pi's messages; a receipt shows only while it is open.
const receipt = (n, state, extra = {}) => [sub(n), { id: sub(n), session: 'p1:s1', revision: 2, state, order: n, text: `submission ${n}`, imageCount: 0, ...extra }];
const receiptRows = (rows) => rows.filter((r) => r.type === 'receipt').map((r) => r.delivery.id);

test('submissions pi took take no row, even when their messages are summarized away, and the conversation stays whole', () => {
  // The reported case: six messages pi recorded, then a summary replaced them.
  const summary = { role: 'compactionSummary', summary: 's', tokensBefore: 1, timestamp: at() };
  const taken = new Map(Array.from({ length: 6 }, (_, i) => receipt(i + 1, 'dispatched')));
  let rows = project({ messages: [summary, user('after the summary', { source: 'user', submission: sub(7) }), assistant([text('answer')])],
    deliveries: new Map([...taken, receipt(7, 'dispatched')]) }, { status: 'idle' });
  assert.deepEqual(rows.map((r) => r.type), ['note', 'user', 'text']);
  assert.equal(rows[1].delivery.id, sub(7));
  assert(!rows.some((r) => r.type === 'receipt' || r.type === 'receipts'));
  // A long history from before messages were tied to receipts.
  const messages = [summary];
  for (let n = 1; n <= 24; n++) {
    messages.push(user(`question ${n}`, null, n % 5 === 0 ? 2 : 0), assistant([text(`answer ${n}`)]));
  }
  messages.push(user('a bound question', { source: 'user', submission: sub(26) }, 1), assistant([text('bound answer')]));
  messages.push(user('another bound one', { source: 'user', submission: sub(27) }), assistant([text('second bound answer')]));
  const deliveries = new Map([
    ...Array.from({ length: 24 }, (_, i) => receipt(i + 1, i % 2 ? 'unconfirmed' : 'dispatched', i === 9 ? { text: '', imageCount: 2 } : {})),
    receipt(25, 'handled'),
    receipt(26, 'unconfirmed', { imageCount: 1 }),
    receipt(27, 'accepted'),
    receipt(28, 'failed', { error: 'rejected' }),
    receipt(29, 'unconfirmed', { error: 'the bridge lost its answer' }),
    receipt(30, 'cancelled'),
    receipt(31, 'queued'),
    receipt(32, 'sending', { revision: 1 }),
    ['local', { id: 'local', revision: 0, state: 'sending', text: 'on its way' }],
  ]);
  rows = project({ messages, deliveries, lastSent: 'local' }, { status: 'working' });
  // Every person's message and every reply is there, in order, whatever its receipt.
  const people = rows.filter((r) => r.type === 'user');
  assert.deepEqual(people.map((r) => r.message.content[0].text), [...Array.from({ length: 24 }, (_, i) => `question ${i + 1}`), 'a bound question', 'another bound one']);
  assert.deepEqual(people.map((r) => r.message.content.length), [...Array.from({ length: 24 }, (_, i) => ((i + 1) % 5 === 0 ? 3 : 1)), 2, 1]);
  assert.deepEqual(rows.filter((r) => r.type === 'text').map((r) => r.text).slice(-3), ['answer 24', 'bound answer', 'second bound answer']);
  assert.equal(rows.filter((r) => r.type === 'text').length, 26);
  assert.deepEqual(people.filter((r) => r.delivery).map((r) => r.delivery.id), [sub(26), sub(27)]);
  // Only what is still on its way or queued, and problems the person has not
  // dealt with, have a row; taken, handled and recalled ones have none.
  assert.deepEqual(rows.slice(rows.length - 5).map((r) => r.type), ['receipt', 'receipt', 'receipt', 'receipt', 'receipt']);
  assert.deepEqual(receiptRows(rows), [sub(28), sub(29), sub(31), sub(32), 'local']);
  // Once the person dismisses the two problems, only what is waiting is left.
  assert.deepEqual(receiptRows(project({ messages, deliveries, lastSent: 'local', handled: new Set([sub(28), sub(29)]) }, { status: 'working' })), [sub(31), sub(32), 'local']);
  assert(!rows.some((r) => r.type === 'receipts'));
});

test('a refusal or an answer with an error stays until the person deals with it, whatever is sent after it', () => {
  const old = Array.from({ length: 22 }, (_, i) => receipt(i + 1, i % 3 === 1 ? 'unconfirmed' : 'dispatched'));
  const messages = Array.from({ length: 22 }, (_, i) => user(`old ${i + 1}`));
  // Nothing sent since, and nothing left to know: nothing at the foot.
  let rows = project({ messages, deliveries: new Map(old) });
  assert.deepEqual(rows.map((r) => r.type), Array(22).fill('user'));
  // The latest refused, uncertain, or never heard of by the server: in view.
  for (const last of [receipt(23, 'failed', { error: 'rejected' }), receipt(23, 'unconfirmed', { error: 'lost' }), receipt(23, 'unconfirmed'),
    ['local', { id: 'local', revision: 0, state: 'unconfirmed', text: 'the bridge failed' }]]) {
    rows = project({ messages, deliveries: new Map([...old, last]), lastSent: 'local' });
    assert.deepEqual(receiptRows(rows), [last[0]], last[1].state);
  }
  // A new submission sent after a refusal does not retire it: the person may
  // not have seen it (it can come after the new one, to a page reloaded since).
  for (const problem of [receipt(23, 'failed', { error: 'rejected', session: '' }), receipt(23, 'unconfirmed', { error: 'the bridge lost its answer' })]) {
    rows = project({ messages, deliveries: new Map([...old, problem, ['local', { id: 'local', revision: 0, state: 'sending', text: 'new' }]]), lastSent: 'local' });
    assert.deepEqual(receiptRows(rows), [sub(23), 'local'], problem[1].state);
    // Nor does the new one being answered, taken, or failing itself.
    for (const next of [receipt(24, 'accepted'), receipt(24, 'dispatched'), receipt(24, 'failed', { error: 'again', session: '' })]) {
      rows = project({ messages, deliveries: new Map([...old, problem, next]) });
      assert.deepEqual(receiptRows(rows), next[1].state === 'dispatched' ? [sub(23)] : [sub(23), sub(24)], `${problem[1].state} then ${next[1].state}`);
    }
  }
  // An uncertain answer without an error is not a refusal: it shows only while it is the latest.
  rows = project({ messages, deliveries: new Map([...old, receipt(23, 'unconfirmed'), receipt(24, 'accepted')]) });
  assert.deepEqual(receiptRows(rows), [sub(24)]);
  // The person dismissed it, or sent its draft again unchanged: it leaves,
  // and stays gone through any later state of others.
  const failed = receipt(23, 'failed', { error: 'rejected', session: '' });
  for (const next of [[], [receipt(24, 'failed', { error: 'again', session: '' })], [receipt(24, 'dispatched')]]) {
    rows = project({ messages, deliveries: new Map([...old, failed, ...next]), handled: new Set([sub(23)]) });
    assert.deepEqual(receiptRows(rows), next.length && next[0][1].state === 'failed' ? [sub(24)] : []);
  }
  // Dismissing never hides one that is waiting again (a late answer for a
  // local row, or a recall that can still return to the input).
  for (const waiting of [receipt(23, 'steering', { nativePending: true }), receipt(23, 'sending'), receipt(23, 'cancelled', { canRestore: true })]) {
    rows = project({ messages, deliveries: new Map([...old, waiting]), handled: new Set([sub(23)]) });
    assert.deepEqual(receiptRows(rows), [sub(23)], waiting[1].state);
  }
  // When its message arrives tied to it, no receipt row is left.
  const sent = user('new', { source: 'user', submission: sub(24) });
  const snapshot = () => ({ messages: [...messages, sent], deliveries: new Map([...old, failed, receipt(24, 'accepted')]), handled: new Set([sub(23)]) });
  rows = project(snapshot());
  assert.deepEqual(rows.slice(-2).map((r) => r.type), ['user', 'user']);
  assert.equal(rows.at(-1).delivery.id, sub(24));
  // The same snapshot again draws the same rows.
  assert.deepEqual(project(snapshot()).map((r) => r.key), rows.map((r) => r.key));
});

test('a refusal that comes after a newer submission, to a page reloaded since, keeps both rows', () => {
  // The shape a real Manager returns when durable registration fails late for
  // A, after the page that sent it reloaded and B was sent: both refused,
  // neither reached pi. A's whole short text stays copyable from its row.
  const hold = receipt(1, 'dispatched', { text: 'hold' });
  const a = receipt(2, 'failed', { session: '', text: 'late A original', error: 'disk I/O failure' });
  const b = receipt(3, 'failed', { session: '', text: 'new B also fails', error: 'disk I/O failure' });
  for (const deliveries of [new Map([hold, a, b]), new Map([hold, a, ['local', { id: 'local', revision: 0, state: 'sending', text: 'C' }]])]) {
    const rows = project({ messages: [], deliveries, lastSent: 'local' });
    assert.equal(receiptRows(rows)[0], sub(2));
    assert.equal(rows[0].delivery.text, 'late A original');
    assert.equal(rows.length, 2);
  }
});

test('interrupted submissions stay until the person deals with them, never guessed taken', () => {
  // The app quit with a follow-up and a steering message queued; the steering
  // one was taken first. The follow-up's interruption comes after that.
  const follow = receipt(1, 'interrupted', { text: 'continue' });
  const steer = receipt(2, 'dispatched');
  const also = receipt(3, 'interrupted', { session: '' }); // registered, never reached pi
  let rows = project({ messages: [user('steer', { source: 'user', submission: sub(2) })], deliveries: new Map([follow, steer, also]) });
  assert.deepEqual(receiptRows(rows), [sub(1), sub(3)]);
  // A reply or an empty queue says nothing about them.
  rows = project({ messages: [user('steer', { source: 'user', submission: sub(2) }), assistant([text('done')])], deliveries: new Map([follow, steer, also]) }, { status: 'idle' });
  assert.deepEqual(receiptRows(rows), [sub(1), sub(3)]);
  // A submission this window has not heard back about yet does not decide it.
  rows = project({ messages: [], deliveries: new Map([follow, steer, also, ['local', { id: 'local', revision: 0, state: 'sending', text: 'next' }]]), lastSent: 'local' });
  assert.deepEqual(receiptRows(rows), [sub(1), sub(3), 'local']);
  // Nor does one in the restarted session: pending, taken, or itself refused there.
  for (const next of [receipt(4, 'steering', { session: 'p2:s1' }), receipt(4, 'dispatched', { session: 'p2:s1' }), receipt(4, 'failed', { session: 'p2:s1', error: 'refused' })]) {
    rows = project({ messages: [], deliveries: new Map([follow, steer, also, next]) });
    assert.deepEqual(receiptRows(rows), next[1].state === 'dispatched' ? [sub(1), sub(3)] : [sub(1), sub(3), sub(4)], next[1].state);
  }
  // The person dismisses them.
  rows = project({ messages: [], deliveries: new Map([follow, steer, also, receipt(4, 'dispatched', { session: 'p2:s1' })]), handled: new Set([sub(1), sub(3)]) });
  assert.deepEqual(receiptRows(rows), []);
  // Pi's own message tied to an interrupted one: recorded, so it is the message.
  rows = project({ messages: [user('continue', { source: 'user', submission: sub(1) })], deliveries: new Map([follow, steer]) });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['user', sub(1)]]);
});

test('what this window sent shows as sent while its row is open, and leaves once pi takes it', () => {
  const payload = { text: 'the whole message, longer than its preview', images: [{ type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgo=' }] };
  const messages = [user('earlier')];
  const queued = receipt(2, 'queued', { text: 'the whole', preview: true, imageCount: 1, nativePending: true });
  const sent = new Map([[sub(2), payload], [sub(3), { text: '/command', images: [] }]]);
  let rows = project({ messages, deliveries: new Map([receipt(1, 'dispatched'), queued, receipt(3, 'handled')]), sent, lastSent: sub(3) });
  assert.deepEqual(rows.map((r) => r.type), ['user', 'receipt']);
  assert.equal(rows[1].delivery.id, sub(2));
  assert.equal(rows[1].sent, payload);
  // Taken while still held here and the latest: it leaves, even before pi's
  // message is tied to it (a summary may have replaced it).
  rows = project({ messages, deliveries: new Map([receipt(1, 'dispatched'), receipt(2, 'dispatched'), receipt(3, 'handled')]), sent, lastSent: sub(2) });
  assert.deepEqual(rows.map((r) => r.type), ['user']);
  // Once its message is tied to it, the message carries it.
  rows = project({ messages: [...messages, user('the whole message, longer than its preview', { source: 'user', submission: sub(2) }, 1)], deliveries: new Map([queued]), sent });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['user', undefined], ['user', sub(2)]]);
  // Two submissions with the same text are two, each tied by its own ID.
  const twice = new Map([receipt(4, 'steering', { text: 'same' }), receipt(5, 'steering', { text: 'same' })]);
  rows = project({ messages: [user('same', { source: 'user', submission: sub(5) })], deliveries: twice });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['user', sub(5)], ['receipt', sub(4)]]);
  // Recalled: its row stays while the original can still go back to the input.
  rows = project({ messages: [], deliveries: new Map([receipt(6, 'cancelled', { canRestore: true }), receipt(7, 'cancelled')]) });
  assert.deepEqual(receiptRows(rows), [sub(6)]);
});

test('a submission the server never heard of is superseded by the next one this window sends', () => {
  const old = Array.from({ length: 3 }, (_, i) => receipt(i + 1, 'dispatched'));
  // The bridge failed before the server heard of it: only this window knows it.
  const lost = ['lost', { id: 'lost', revision: 0, state: 'unconfirmed', text: 'did this arrive?' }];
  let rows = project({ messages: [], deliveries: new Map([...old, lost]), lastSent: 'lost' });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['receipt', 'lost']]);
  // An edited draft is sent instead, and the server answers for it.
  rows = project({ messages: [], deliveries: new Map([...old, lost, receipt(4, 'steering', { text: 'edited' })]), lastSent: sub(4) });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['receipt', sub(4)]]);
  rows = project({ messages: [], deliveries: new Map([...old, lost, receipt(4, 'unconfirmed', { error: 'lost' })]), lastSent: sub(4) });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['receipt', sub(4)]]);
});
