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

const SOURCES = new Set(['user', 'lead', 'yesman', 'schedule']);
const SUBMISSION = /^[a-f0-9]{48}$/;
const PLAN = /^s[1-9][0-9]{0,5}$/;

// originOf returns a message's recorded origin, or null when it has none.
export function originOf(m) {
  const o = m?.role === 'user' ? m.yesman : null;
  if (!o || typeof o !== 'object' || !SOURCES.has(o.source)) return null;
  const word = (s) => (typeof s === 'string' ? s : '');
  return {
    source: o.source, kind: word(o.kind), worker: word(o.worker), submission: SUBMISSION.test(o.submission ?? '') ? o.submission : '',
    plan: o.source === 'schedule' && PLAN.test(o.plan ?? '') ? o.plan : '',
  };
}

const textOf = (content) =>
  typeof content === 'string' ? content : (content ?? []).filter((b) => b?.type === 'text').map((b) => b.text).join('\n');

// project turns messages into rows, in order:
//
//   user     a person's message, with its receipt when the message is bound to one
//   lead     a worker's task or a message from its lead
//   scheduled  a scheduled task's run, sent for the user (the plan it is of)
//   text     one text block of the agent's, shown as it is written
//   note     an error (blank when pi recorded no message for it), a stop, a
//            summary of earlier conversation, an extension's message
//   work     the work between those: thinking, tool calls and yesman's notices to a lead
//   failures runs in a row that ended on nothing but an error, folded (see foldFailures)
//   receipt  a submission the conversation does not show yet, as the person's message;
//            inputs pi appended into one pending occurrence show as that one message
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
  // An input pi appended into another's pending occurrence is part of that
  // message, never a message of its own, as soon as its receipt says so.
  const appended = (d) => !!d?.mergedInto && !convo.deliveries?.get(d.mergedInto)?.mergedInto && convo.deliveries?.has(d.mergedInto);
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
        else if (origin?.source === 'schedule') {
          // A scheduled task's run: the request the user saved, sent for them.
          const delivery = origin.submission ? convo.deliveries?.get(origin.submission) : undefined;
          if (delivery) bound.add(delivery.id);
          main({ type: 'scheduled', key, message: m, origin, delivery });
        }
        else {
          const delivery = origin?.submission ? convo.deliveries?.get(origin.submission) : undefined;
          if (delivery) for (const id of familyOf(convo, delivery)) bound.add(id);
          main({ type: 'user', key, message: m, origin, delivery });
        }
        return;
      }
      case 'assistant': {
        let said = false;
        (m.content ?? []).forEach((block, i) => {
          if (!block) return;
          const k = `${key}:${i}`;
          if (block.type === 'text' && block.text?.trim()) {
            said = true;
            main({ type: 'text', key: k, text: block.text, live });
          } else if (block.type === 'thinking' && block.thinking?.trim()) step({ type: 'thinking', key: k, text: block.thinking, live });
          else if (block.type === 'toolCall' && block.id) step({ type: 'tool', key: k, block });
        });
        if (m.stopReason === 'error') {
          // pi may record an error with no message: the page says it gave
          // none (blank), rather than show nothing or fold it away.
          const text = typeof m.errorMessage === 'string' ? m.errorMessage : '';
          const before = work;
          if (work) work.error = true;
          // pi's record of the failed message: what may fold with others like
          // it. One that said something to the person, even in part, or is
          // still being written, never folds.
          const failure = said || live ? null : {
            work: before, at: typeof m.timestamp === 'number' ? m.timestamp : null,
            provider: typeof m.provider === 'string' ? m.provider : '', model: typeof m.model === 'string' ? m.model : '',
          };
          main({ type: 'note', key: `${key}:end`, tone: 'error', text, blank: !text.trim(), failure });
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

  rows.splice(0, rows.length, ...foldFailures(rows));

  // Submissions not tied to a message come last, in the order they were made.
  // One still to be known about reads as the person's message, from what this
  // window sent when it has that, with its state under it. A receipt also
  // outlives its message's place in the conversation: one from before
  // messages were tied to receipts, or whose message a summary replaced,
  // stays for as long as yesman keeps receipts. Those that need nothing more,
  // and failures a later submission has superseded, fold into one row.
  const all = [...(convo.deliveries?.values() ?? [])].filter((d) => !appended(d));
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
  for (const d of receipts) {
    if (!own(d)) continue;
    const members = familyOf(convo, d);
    rows.push({ type: 'receipt', key: `d:${d.id}`, delivery: d, members, sent: sentOf(convo, members) });
  }
  return rows;
}

// familyOf lists the inputs one occurrence's message is made of, its primary
// input first: those pi reported in order, then any whose own receipt names
// the occurrence before the occurrence's receipt catches up.
export function familyOf(convo, root) {
  const ids = [root.id, ...(Array.isArray(root.merged) ? root.merged : [])];
  const late = [...(convo.deliveries?.values() ?? [])]
    .filter((d) => d.mergedInto === root.id && !ids.includes(d.id))
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  return [...ids, ...late.map((d) => d.id)];
}

// joinText is pi's merge of two inputs' text: one newline between them, each
// kept verbatim, none when either has no text (an image-only input).
export const joinText = (a, b) => (a === '' ? b : b === '' ? a : `${a}\n${b}`);

// joinInputs is the whole message several inputs make: their text joined in
// order, then all their images in order.
export function joinInputs(parts) {
  let text = '';
  const images = [];
  for (const part of parts) {
    text = joinText(text, part.text ?? '');
    images.push(...(part.images ?? []));
  }
  return { text, images };
}

// sentOf is the whole message, exactly as this window sent each of its
// inputs, or nothing when it did not send (or no longer holds) every one.
export function sentOf(convo, members) {
  const parts = members.map((id) => convo.sent?.get(id));
  if (parts.some((part) => !part)) return undefined;
  return members.length === 1 ? parts[0] : joinInputs(parts);
}

// foldFailures folds runs in a row that ended on nothing but an error into
// one failures row, such as a lead failing on a provider's usage limit at
// every worker report. A run is the error note pi recorded, with the work
// row before it if any; only pi's stopReason says it failed, never text.
// Any other row ends the fold: a person's message, a lead's, a reply or part
// of one, a stop, a summary. A single failed run stays as it was.
//
// The failures row keeps every run, with its work row and note as they
// were, under the error each one ended on: errors are the same when their
// provider and message are, whitespace aside. Its key is its first run's,
// so it stays the same as runs join it.
function foldFailures(rows) {
  const out = [];
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i], next = rows[i + 1];
    let run = null;
    if (row.type === 'work' && next?.failure && next.failure.work === row) {
      run = { key: row.key, work: row, note: next };
      i++;
    } else if (row.failure && !row.failure.work) run = { key: row.key, work: null, note: row };
    if (!run) {
      out.push(row);
      continue;
    }
    const prev = out.at(-1);
    if (prev?.type === 'failures') prev.runs.push(run);
    else out.push({ type: 'failures', key: `f:${run.key}`, runs: [run] });
  }
  return out.flatMap((row) => {
    if (row.type !== 'failures') return [row];
    if (row.runs.length < 2) return row.runs.flatMap((run) => (run.work ? [run.work, run.note] : [run.note]));
    const errors = new Map();
    for (const run of row.runs) {
      const { provider } = run.note.failure;
      const id = `${provider}\n${run.note.text.trim().replace(/\s+/g, ' ')}`;
      let e = errors.get(id);
      if (!e) errors.set(id, (e = { key: `${row.key}:${errors.size}`, text: run.note.text, blank: run.note.blank, provider, models: new Set(), runs: [] }));
      if (run.note.failure.model) e.models.add(run.note.failure.model);
      e.runs.push(run);
    }
    row.errors = [...errors.values()];
    return [row];
  });
}

// The states after which nothing more is known about a submission. Any
// other, or one this page does not know, keeps its row.
export const FINISHED = new Set(['unconfirmed', 'failed', 'interrupted', 'handled', 'dispatched', 'cancelled']);

// settled is a finished receipt that needs nothing more: accepted with nothing
// more pi can say, or handled by an extension. pi never says which message it
// took, so this is not a claim that it was. A failure, an interruption or an
// error stays in view while it is the latest submission.
const settled = (d) => d.revision > 0 && !d.error && ['unconfirmed', 'handled', 'dispatched', 'cancelled'].includes(d.state);
