// How a conversation reads: what people said and what the agent said to
// them, with the work in between folded into one line each time. This module
// only projects data; app.js draws it, live and from history alike.
//
// Who wrote a user message comes from its "yesman" field, which the app adds
// to the messages it serves, from pi's own records of each prompt
// (internal/agent/origin.go); pi's messages, and so the model, never carry
// it. Text is never used to decide who wrote a
// message: a message without the field, such as one from before it existed,
// is shown as it was written, as a person's.

const SOURCES = new Set(['user', 'lead', 'yesman']);
const SUBMISSION = /^[a-f0-9]{48}$/;

// originOf returns a message's recorded origin, or null when it has none.
export function originOf(m) {
  const o = m?.role === 'user' ? m.yesman : null;
  if (!o || typeof o !== 'object' || !SOURCES.has(o.source)) return null;
  const word = (s) => (typeof s === 'string' ? s : '');
  return { source: o.source, kind: word(o.kind), worker: word(o.worker), submission: SUBMISSION.test(o.submission ?? '') ? o.submission : '' };
}

const textOf = (content) =>
  typeof content === 'string' ? content : (content ?? []).filter((b) => b?.type === 'text').map((b) => b.text).join('\n');

// project turns messages into rows, in order:
//
//   user     a person's message, with its receipt when the message is bound to one
//   lead     a worker's task or a message from its lead
//   text     one text block of the agent's, shown as it is written
//   note     an error, a stop, a summary of earlier conversation, an extension's message
//   work     the work between those: thinking, tool calls and yesman's notices to a lead
//   receipt  a submission the conversation does not show yet, as the person's message
//   receipts earlier submissions not tied to a message that need nothing more, folded
//
// Every row and item has a key that stays the same while the conversation
// grows, from history or live, so the page can keep what is open.
//
// convo: { messages, live: message being written or null, tools: Map(id -> {status}),
//          deliveries: Map(id -> receipt), sent: Map(id -> { text, images }) of what
//          this window submitted and has not seen tied to a message yet,
//          lastSent: the ID this window submitted last }
// agent: { status } — working, waiting, failed... of the agent shown.
export function project(convo, agent = {}) {
  const rows = [];
  const seen = new Map();
  const bound = new Set();
  const submissions = new Set();
  let work = null;
  const keyFor = (m) => {
    const submission = originOf(m)?.submission;
    const base = submission ? `submission:${submission}` : `${m?.role}:${m?.timestamp ?? ''}`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return n ? `${base}#${n}` : base;
  };
  const main = (row) => {
    work = null;
    rows.push(row);
  };
  const step = (item) => {
    if (!work) rows.push((work = { type: 'work', key: `w:${item.key}`, items: [], stopped: false, error: false }));
    work.items.push(item);
  };
  const visit = (m, live) => {
    const key = keyFor(m);
    switch (m?.role) {
      case 'user': {
        const origin = originOf(m);
        if (origin?.submission) {
          if (submissions.has(origin.submission)) return;
          submissions.add(origin.submission);
        }
        if (origin?.source === 'yesman') step({ type: 'notice', key, message: m, origin });
        else if (origin?.source === 'lead') main({ type: 'lead', key, message: m, origin });
        else {
          const delivery = origin?.submission ? convo.deliveries?.get(origin.submission) : undefined;
          if (delivery) bound.add(delivery.id);
          main({ type: 'user', key, message: m, origin, delivery });
        }
        return;
      }
      case 'assistant': {
        (m.content ?? []).forEach((block, i) => {
          if (!block) return;
          const k = `${key}:${i}`;
          if (block.type === 'text' && block.text?.trim()) main({ type: 'text', key: k, text: block.text, live });
          else if (block.type === 'thinking' && block.thinking?.trim()) step({ type: 'thinking', key: k, text: block.thinking, live });
          else if (block.type === 'toolCall' && block.id) step({ type: 'tool', key: k, block });
        });
        if (m.stopReason === 'error' && m.errorMessage) {
          if (work) work.error = true;
          main({ type: 'note', key: `${key}:end`, tone: 'error', text: m.errorMessage });
        } else if (m.stopReason === 'aborted') {
          if (work) work.stopped = true;
          main({ type: 'note', key: `${key}:end`, tone: 'quiet', stopped: true });
        }
        return;
      }
      case 'toolResult':
      case 'system':
        return; // a result shows with its call; system messages are pi's prompt
      case 'compactionSummary':
      case 'branchSummary':
        main({ type: 'note', key, tone: 'quiet', summary: true });
        return;
      default: {
        if (m?.display === false) return;
        const text = textOf(m?.content);
        if (text) main({ type: 'note', key, tone: 'quiet', text });
      }
    }
  };
  for (const m of convo.messages ?? []) visit(m, false);
  if (convo.live) visit(convo.live.message, true);

  // The work last shown is what the agent is doing while it works.
  const last = rows.at(-1);
  for (const row of rows) {
    if (row.type !== 'work') continue;
    const counts = { thinking: 0, tools: 0, failed: 0, reports: 0, notices: 0, workers: 0 };
    let pending = false;
    for (const item of row.items) {
      if (item.type === 'thinking') counts.thinking++;
      else if (item.type === 'notice') item.origin.kind === 'report' ? counts.reports++ : counts.notices++;
      else {
        counts.tools++;
        const status = convo.tools?.get(item.block.id)?.status ?? 'pending';
        if (status === 'error') counts.failed++;
        if (status === 'pending' || status === 'running') pending = true;
        // Only a start that succeeded started a worker.
        if (item.block.name === 'yesman_worker_start' && status === 'done') counts.workers++;
      }
    }
    row.counts = counts;
    const current = row === last;
    row.state =
      current && agent.status === 'working' ? 'running'
        : current && agent.status === 'waiting' ? 'waiting'
          : row.stopped ? 'stopped'
            : row.error ? 'error'
              : pending ? (current && agent.status === 'failed' ? 'interrupted' : 'unfinished')
                : current ? 'ended' : 'done';
  }

  // Submissions not tied to a message come last, in the order they were made.
  // One still to be known about reads as the person's message, from what this
  // window sent when it has that, with its state under it. A receipt also
  // outlives its message's place in the conversation: one from before
  // messages were tied to receipts, or whose message a summary replaced,
  // stays for as long as yesman keeps receipts. Those that need nothing more,
  // and failures a later submission has superseded, fold into one row.
  const all = [...(convo.deliveries?.values() ?? [])];
  // A receipt the server has not answered for yet has no order: the one this
  // window sent last is the latest; an earlier one never is.
  const order = (d) => (d.revision > 0 ? (d.order ?? 0) : d.id === convo.lastSent ? Infinity : -Infinity);
  const latest = all.reduce((a, d) => (!a || order(d) > order(a) ? d : a), null);
  // Those the server has not answered for yet were sent after all it has.
  const made = (d) => (d.revision > 0 ? (d.order ?? 0) : Infinity);
  const receipts = all.filter((d) => !bound.has(d.id)).sort((a, b) => made(a) - made(b) || 0);
  const currentFailure = (d) => (d.state === 'failed' || d.state === 'interrupted' || !!d.error) &&
    (convo.submitted?.has(d.id) || (d.agentSince && d.agentSince === agent.since));
  // Display caches and the last submitted ID cannot revoke native membership
  // or hide a failure from this agent generation. They never grant actions.
  const own = (d) => d.nativePending || d.canRestore || currentFailure(d) ||
    !FINISHED.has(d.state) || convo.sent?.has(d.id) || (d === latest && !settled(d));
  const earlier = receipts.filter((d) => !own(d));
  if (earlier.length) rows.push({ type: 'receipts', key: 'd:earlier', deliveries: earlier });
  for (const d of receipts) if (own(d)) rows.push({ type: 'receipt', key: `d:${d.id}`, delivery: d, sent: convo.sent?.get(d.id) });
  return rows;
}

// The states after which nothing more is known about a submission. Any
// other, or one this page does not know, keeps its row.
export const FINISHED = new Set(['unconfirmed', 'failed', 'interrupted', 'handled', 'dispatched', 'cancelled']);

// settled is a finished receipt that needs nothing more: accepted with nothing
// more pi can say, or handled by an extension. pi never says which message it
// took, so this is not a claim that it was. A failure, an interruption or an
// error stays in view while it is the latest submission.
const settled = (d) => d.revision > 0 && !d.error && ['unconfirmed', 'handled', 'dispatched', 'cancelled'].includes(d.state);
