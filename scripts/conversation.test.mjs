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
    [sub(4), { id: sub(4), state: 'unconfirmed', order: 4, text: 'not tied to any message' }],
  ]);
  const convo = { messages, tools: tools([t1, 'done'], [t2, 'running']), deliveries };
  const rows = project(convo, { status: 'working' });
  assert.deepEqual(shape(rows), ['user', 'work(thinking,tool):done', 'user', 'work(thinking,tool):running', 'receipts', 'receipt']);
  assert.equal(rows[0].delivery.id, sub(1));
  assert.equal(rows[2].delivery.id, sub(2));
  // The queued one is the person's message still to come; the untied one with
  // nothing more to know is folded.
  assert.deepEqual(rows[4].deliveries.map((d) => d.id), [sub(4)]);
  assert.equal(rows[5].delivery.id, sub(3));
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

// Receipts outlive the messages they were for: those from before messages were
// tied to receipts, and those whose message a summary of earlier conversation
// replaced, are kept by the server for its retention period.
const receipt = (n, state, extra = {}) => [sub(n), { id: sub(n), revision: 2, state, order: n, text: `submission ${n}`, imageCount: 0, ...extra }];

test('earlier receipts no message is tied to fold into one row, and the conversation stays whole', () => {
  const messages = [{ role: 'compactionSummary', summary: 's', tokensBefore: 1, timestamp: at() }];
  for (let n = 1; n <= 24; n++) {
    messages.push(user(`question ${n}`, null, n % 5 === 0 ? 2 : 0), assistant([text(`answer ${n}`)]));
  }
  messages.push(user('a bound question', { source: 'user', submission: sub(26) }, 1), assistant([text('bound answer')]));
  messages.push(user('another bound one', { source: 'user', submission: sub(27) }), assistant([text('second bound answer')]));
  const deliveries = new Map([
    ...Array.from({ length: 24 }, (_, i) => receipt(i + 1, 'unconfirmed', i === 9 ? { text: '', imageCount: 2 } : {})),
    receipt(25, 'handled'),
    receipt(26, 'unconfirmed', { imageCount: 1 }),
    receipt(27, 'accepted'),
    receipt(28, 'failed', { error: 'rejected' }),
    receipt(29, 'unconfirmed', { error: 'the bridge lost its answer' }),
    receipt(30, 'interrupted'),
    receipt(31, 'queued'),
    receipt(32, 'sending', { revision: 1 }),
    ['local', { id: 'local', revision: 0, state: 'sending', text: 'on its way' }],
  ]);
  const rows = project({ messages, deliveries }, { status: 'working' });
  // Every person's message and every reply is there, in order, whatever its receipt.
  const people = rows.filter((r) => r.type === 'user');
  assert.deepEqual(people.map((r) => r.message.content[0].text), [...Array.from({ length: 24 }, (_, i) => `question ${i + 1}`), 'a bound question', 'another bound one']);
  assert.deepEqual(people.map((r) => r.message.content.length), [...Array.from({ length: 24 }, (_, i) => ((i + 1) % 5 === 0 ? 3 : 1)), 2, 1]);
  assert.deepEqual(rows.filter((r) => r.type === 'text').map((r) => r.text).slice(-3), ['answer 24', 'bound answer', 'second bound answer']);
  assert.equal(rows.filter((r) => r.type === 'text').length, 26);
  assert.deepEqual(people.filter((r) => r.delivery).map((r) => r.delivery.id), [sub(26), sub(27)]);
  // What is still on its way or queued keeps its own row. What needs nothing
  // more, and failures later submissions superseded, are one row.
  const tail = rows.slice(rows.findIndex((r) => r.type === 'receipts'));
  assert.deepEqual(tail.map((r) => r.type), ['receipts', 'receipt', 'receipt', 'receipt']);
  assert.equal(tail[0].key, 'd:earlier');
  assert.deepEqual(tail[0].deliveries.map((d) => d.id), [...Array.from({ length: 25 }, (_, i) => sub(i + 1)), sub(28), sub(29), sub(30)]);
  assert.deepEqual(tail.slice(1).map((r) => r.delivery.id).sort(), [sub(31), sub(32), 'local'].sort());
});

test('the latest failure stays in view until a later submission, and old settled ones never take a row', () => {
  const old = Array.from({ length: 22 }, (_, i) => receipt(i + 1, 'unconfirmed'));
  const messages = Array.from({ length: 22 }, (_, i) => user(`old ${i + 1}`));
  // Nothing sent since, and nothing left to know: one folded row, no line of its own.
  let rows = project({ messages, deliveries: new Map(old) });
  assert.deepEqual(rows.slice(-2).map((r) => r.type), ['user', 'receipts']);
  assert.equal(rows.at(-1).deliveries.length, 22);
  const key = rows.at(-1).key;
  // The latest refused, interrupted or uncertain: it is in view.
  for (const last of [receipt(23, 'failed', { error: 'rejected' }), receipt(23, 'interrupted'), receipt(23, 'unconfirmed', { error: 'lost' }),
    ['local', { id: 'local', revision: 0, state: 'unconfirmed', text: 'the bridge failed' }]]) {
    rows = project({ messages, deliveries: new Map([...old, last]) });
    assert.deepEqual(rows.slice(-2).map((r) => r.type), ['receipts', 'receipt'], last[1].state);
    assert.equal(rows.at(-1).delivery.id, last[0]);
    assert.equal(rows.at(-2).key, key, 'the folded row keeps its key');
  }
  // A new submission on its way supersedes the failure; the failure joins the
  // folded row, still saying it was not sent.
  rows = project({ messages, deliveries: new Map([...old, receipt(23, 'failed', { error: 'rejected' }), ['local', { id: 'local', revision: 0, state: 'sending', text: 'new' }]]) });
  assert.deepEqual(rows.slice(-2).map((r) => r.type), ['receipts', 'receipt']);
  assert.deepEqual(rows.at(-2).deliveries.map((d) => d.state).slice(-1), ['failed']);
  assert.equal(rows.at(-1).delivery.id, 'local');
  // ...and when its message arrives tied to it, no receipt row is left.
  const sent = user('new', { source: 'user', submission: sub(24) });
  const snapshot = () => ({ messages: [...messages, sent], deliveries: new Map([...old, receipt(23, 'failed', { error: 'rejected' }), receipt(24, 'accepted')]) });
  rows = project(snapshot());
  assert.deepEqual(rows.slice(-2).map((r) => r.type), ['user', 'receipts']);
  assert.equal(rows.at(-1).deliveries.length, 23);
  assert.equal(rows.at(-2).delivery.id, sub(24));
  // The same snapshot again draws the same rows.
  assert.deepEqual(project(snapshot()).map((r) => r.key), rows.map((r) => r.key));
});

test('what this window sent shows as sent, with its state, until a message is tied to it', () => {
  const payload = { text: 'the whole message, longer than its preview', images: [{ type: 'image', mimeType: 'image/png', data: 'iVBORw0KGgo=' }] };
  const messages = [user('earlier')];
  // Queued, then the run was stopped: pi says nothing more, but it is this
  // window's and in no message yet.
  const deliveries = new Map([receipt(1, 'unconfirmed'), receipt(2, 'unconfirmed', { text: 'the whole', preview: true, imageCount: 1 }), receipt(3, 'handled')]);
  const sent = new Map([[sub(2), payload], [sub(3), { text: '/command', images: [] }]]);
  let rows = project({ messages, deliveries, sent });
  assert.deepEqual(rows.map((r) => r.type), ['user', 'receipts', 'receipt', 'receipt']);
  assert.deepEqual(rows[1].deliveries.map((d) => d.id), [sub(1)]);
  assert.equal(rows[2].delivery.id, sub(2));
  assert.equal(rows[2].sent, payload);
  assert.equal(rows[3].delivery.state, 'handled');
  // Once its message is tied to it, the message carries it.
  rows = project({ messages: [...messages, user('the whole message, longer than its preview', { source: 'user', submission: sub(2) }, 1)], deliveries, sent });
  assert.deepEqual(rows.map((r) => r.type), ['user', 'user', 'receipts', 'receipt']);
  assert.equal(rows[1].delivery.id, sub(2));
  // Two submissions with the same text are two, each tied by its own ID.
  const twice = new Map([receipt(4, 'steering', { text: 'same' }), receipt(5, 'steering', { text: 'same' })]);
  rows = project({ messages: [user('same', { source: 'user', submission: sub(5) })], deliveries: twice });
  assert.deepEqual(rows.map((r) => [r.type, r.delivery?.id]), [['user', sub(5)], ['receipt', sub(4)]]);
});
