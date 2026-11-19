// Needs you: what waits on the user, which the window shows by yesman, in a
// pane beside its conversation. It is built only from structured requests,
// never from what anyone wrote:
//
//   dialog   a lead's own pi dialog (yes or no, choice, text), or a worker's
//            dialog its lead handed over to the user (agent.handedOver)
//   handoff  something a lead registered for the user to do or decide
//            (a decision, information, logging in, installing, a manual step)
//
// A worker's other questions go to its lead, which answers them itself, so
// they are not here. Keys are stable for as long as the request is open: the
// agent's ID and start, or the workspace and when it was hired, and the
// request's own ID.

// The labels, in English: app.js says them in the page's language.
export const TYPE_LABELS = {
  confirm: 'Yes or no', select: 'Choice', input: 'Text', editor: 'Text',
  decision: 'Decision', information: 'Information', login: 'Log in', install: 'Install', manual: 'Manual step',
};
// A handoff of type "input" asks for information, unlike a text dialog.
export const responseKind = (h) => h.response?.kind ?? (['input', 'decision'].includes(h.type) ? 'text' : 'action');
export const typeLabel = (item) => {
  if (item.kind === 'handoff' && item.handoff?.response?.kind === 'choice') return TYPE_LABELS.select;
  return TYPE_LABELS[item.kind === 'handoff' && item.type === 'input' ? 'information' : item.type] ?? 'Other request';
};

export const dialogKey = (a, hiredAt = '') => `d:${a.id}@${a.since ?? ''}:${a.dialogAt ?? ''}:${a.dialog?.id ?? ''}@${hiredAt}`;

const time = (s) => {
  const v = Date.parse(s ?? '');
  return Number.isFinite(v) ? v : 0;
};

export function needsYouItems(snap) {
  const items = [];
  const yesmen = new Map((snap?.yesmen ?? []).map((y) => [y.name, y]));
  for (const a of snap?.agents ?? []) {
    if (a.status !== 'waiting' || !yesmen.has(a.yesman)) continue;
    // A waiting lead always waits on the user; a worker only once its lead
    // handed its question over.
    if (a.role !== 'lead' && !(a.handedOver && a.dialog?.id)) continue;
    const d = a.dialog ?? {};
    items.push({
      key: dialogKey(a, yesmen.get(a.yesman).hiredAt), requestId: d.id,
      kind: 'dialog', yesman: a.yesman, agent: a.id, agentSince: a.since ?? '', role: a.role,
      hiredAt: yesmen.get(a.yesman).hiredAt ?? '', dialog: d,
      type: d.method ?? '', title: d.title || '', summary: d.message || '',
      since: a.dialogAt || '', timeout: d.timeout || 0,
    });
  }
  for (const y of yesmen.values()) {
    for (const h of y.handoffs ?? []) {
      if (h.closedAt) continue;
      items.push({
        key: `h:${y.name}@${y.hiredAt ?? ''}:${h.id}`,
        kind: 'handoff', yesman: y.name, hiredAt: y.hiredAt ?? '', agent: `${y.name}/lead`, role: 'lead',
        type: h.type, title: h.title, summary: h.detail ?? '', since: h.createdAt ?? '', handoff: h,
      });
    }
  }
  // The longest waiting first; ties keep a stable order.
  return items.sort((a, b) => time(a.since) - time(b.since) || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
}

// instant reads a time the app sends, to the millisecond, the same in every
// engine, whatever its digits after that.
const instant = (s) => Date.parse(String(s ?? '').replace(/(\.\d{3})\d+/, '$1'));

// requestsOf is what waits on the user from one yesman, y, for its current
// hire. A dialog of an agent that started before y was hired is a request of
// the yesman of the same name archived since, read in an older list of the
// agents; an agent whose start is unknown is taken as it is.
export function requestsOf(snap, y) {
  const hired = instant(y?.hiredAt);
  return needsYouItems({ agents: snap?.agents ?? [], yesmen: y ? [y] : [] }).filter((item) => {
    if (item.kind !== 'dialog' || !Number.isFinite(hired)) return true;
    const since = instant(item.agentSince);
    return !Number.isFinite(since) || since >= hired;
  });
}

// closedHandoffs is the handoffs recently closed, newest first: history,
// which never counts as waiting. With a yesman named, only its own, chosen
// before the limit.
export function closedHandoffs(snap, limit = 20, yesman = null) {
  const out = [];
  for (const y of snap?.yesmen ?? []) {
    if (yesman && y.name !== yesman) continue;
    for (const h of y.handoffs ?? []) if (h.closedAt) out.push({ yesman: y.name, handoff: h });
  }
  return out.sort((a, b) => time(b.handoff.closedAt) - time(a.handoff.closedAt)).slice(0, limit);
}

// summary is a short, plain line of a request's text, for lists.
export function summary(text, limit = 160) {
  const line = String(text ?? '').replace(/[`*_#>]/g, '').split(/\s+/).filter(Boolean).join(' ');
  return line.length > limit ? `${line.slice(0, limit - 1)}…` : line;
}
