// How a conversation reads: what people said and what the agent said to
// them, with the work in between folded into one line each time. This module
// only projects data; app.js draws it, live and from history alike.
//
// Who wrote a user message comes from its "yesman" field, which yesman's
// extension records from what the app knew while the prompt was in flight
// (internal/agent/origin.go). Text is never used to decide who wrote a
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
//   receipt  a submission the conversation does not show yet, or cannot be tied to
//
// Every row and item has a key that stays the same while the conversation
// grows, from history or live, so the page can keep what is open.
//
// convo: { messages, live: message being written or null, tools: Map(id -> {status}),
//          deliveries: Map(id -> receipt) }
// agent: { status } — working, waiting, failed... of the agent shown.
export function project(convo, agent = {}) {
  const rows = [];
  const seen = new Map();
  const bound = new Set();
  let work = null;
  const keyFor = (m) => {
    const base = `${m?.role}:${m?.timestamp ?? ''}`;
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
        if (item.block.name === 'yesman_worker_start') counts.workers++;
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
  const receipts = [...(convo.deliveries?.values() ?? [])].filter((d) => !bound.has(d.id)).sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  for (const d of receipts) rows.push({ type: 'receipt', key: `d:${d.id}`, delivery: d });
  return rows;
}
