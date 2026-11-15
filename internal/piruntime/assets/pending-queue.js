// Part of yesman's pinned pi source patch. Queue identities never live on an
// AgentMessage, so context/input/provider hooks cannot serialize them by accident.
import { randomUUID } from 'node:crypto';

// Identity belongs to a batch position, never to the AgentMessage. In
// particular the same object may occur twice, or also occur in prepared context.
const inputRefs = new WeakMap();
const triggerRefs = new WeakMap();
const SUBMISSION_ID = /^[a-f0-9]{48}$/;
// One occurrence holds its primary human input plus at most 15 appended segments.
export const MAX_SEGMENTS = 16;
export const SEGMENT_SEPARATOR = '\n\n';
export function inputIdentity(metadata) {
  if (!metadata) return undefined;
  const submissionId = SUBMISSION_ID.test(metadata.submissionId ?? '') ? metadata.submissionId : undefined;
  // Host-only IDs of later inputs appended into this same occurrence. Bounded,
  // never on an AgentMessage, and only meaningful with a primary submission.
  const segments = submissionId && Array.isArray(metadata.segments) ?
    metadata.segments.filter(s => typeof s === 'string' && SUBMISSION_ID.test(s) && s !== submissionId).slice(0, MAX_SEGMENTS - 1) : [];
  const source = metadata.hostOrigin?.source;
  const origin = ['user','lead','yesman'].includes(source) ? {source} : submissionId ? {source:'user'} : undefined;
  if (origin) {
    if (/^[a-z]{1,16}$/.test(metadata.hostOrigin?.kind ?? '')) origin.kind = metadata.hostOrigin.kind;
    if (/^[a-z0-9][a-z0-9-]{0,31}$/.test(metadata.hostOrigin?.worker ?? '')) origin.worker = metadata.hostOrigin.worker;
  }
  return submissionId || origin ? Object.freeze({...(submissionId ? {submissionId}:{}), ...(origin ? {origin:Object.freeze(origin)}:{}),
    ...(segments.length ? {segments:Object.freeze(segments)}:{})}) : undefined;
}
// The merged occurrence keeps pi's queued user-message shape exactly:
// one text block, then images in original order. Anything else is not merged.
function queuedParts(message) {
  if (message?.role !== 'user' || !Array.isArray(message.content) || message.content[0]?.type !== 'text' ||
      typeof message.content[0].text !== 'string' || !message.content.slice(1).every(b => b?.type === 'image')) return undefined;
  return { text: message.content[0].text, images: message.content.slice(1) };
}
export function mergeQueuedMessage(base, extra) {
  const a = queuedParts(base), b = queuedParts(extra);
  if (!a || !b) return undefined;
  const text = a.text === '' ? b.text : b.text === '' ? a.text : a.text + SEGMENT_SEPARATOR + b.text;
  // A new object: the original may be shared, and identity is positional.
  return { ...base, content: [{ type: 'text', text }, ...a.images, ...b.images] };
}
function sameOrigin(a, b) {
  return a?.source === b?.source && a?.kind === b?.kind && a?.worker === b?.worker;
}
export function bindInputTrigger(trigger, metadata) { triggerRefs.set(trigger,inputIdentity(metadata)); return trigger; }
export function triggerIdentity(trigger) { return triggerRefs.get(trigger); }
export function bindInputBatch(messages, identities) {
  inputRefs.set(messages,messages.map((_,i)=>identities[i] ? {native:true,identity:identities[i]} : null));return messages;
}
export function copyInputRefs(source, target, insert = -1) {
  const refs = (inputRefs.get(source) ?? source.map(()=>null)).slice();
  if (insert >= 0) refs.splice(insert,0,null);
  inputRefs.set(target,refs);return target;
}
export function joinInputBatches(...batches) {
  const result=batches.flat();inputRefs.set(result,batches.flatMap(b=>inputRefs.get(b)??b.map(()=>null)));return result;
}
export function inputRefAt(messages,index) { return inputRefs.get(messages)?.[index]?.identity ?? null; }
export function appendInputMessage(messages,message,identity) {
  const refs=(inputRefs.get(messages)??messages.map(()=>null)).slice();
  messages.push(message);refs.push(message.role==='user'?{native:true,identity:identity??null}:null);inputRefs.set(messages,refs);
}
export function recordedInputRef(messages,index,entryId) {
  const refs=inputRefs.get(messages);if(refs?.[index]?.native)refs[index]={...refs[index],entryId};
}
export function hostInputRefs(messages) { return structuredClone(inputRefs.get(messages)??messages.map(()=>null)); }
export const INPUT_ENTRY = 'yesman-input-v1';
export function publicInputEntry(entry) {
  if (entry.type === 'custom' && entry.customType === INPUT_ENTRY) return {...entry,data:{}};
  if (entry.type === 'custom' && entry.customType === 'yesman-origin' && entry.data?.origin) {
    const {submission,...origin}=entry.data.origin;return {...entry,data:{...entry.data,origin}};
  }
  return entry;
}
export function recordedInputs(branch) {
  const messages=new Map(branch.filter(e=>e.type==='message').map(e=>[e.id,e]));
  const records=new Map();
  const byId=new Map(branch.map(e=>[e.id,e]));
  // Published .30 side entries precede their exact message in the session tree.
  // Follow immutable entry links, never a digest/body join, for legacy origins.
  for (const message of messages.values()) if (message.message?.role === 'user') {
    let identity=null, parentId=message.parentId;
    for(let n=0;n<16&&parentId;n++) {
      const parent=byId.get(parentId);if(!parent||parent.type==='message')break;
      if(parent.type==='custom'&&parent.customType==='yesman-origin') {
        if(parent.data?.timestamp===message.message.timestamp) identity=inputIdentity({submissionId:parent.data.origin?.submission,hostOrigin:parent.data.origin})??null;
        break;
      }
      parentId=parent.parentId;
    }
    records.set(message.id,{native:true,entryId:message.id,identity});
  }
  for(const e of branch)if(e.type==='custom'&&e.customType===INPUT_ENTRY&&e.parentId===e.data?.entryId&&messages.has(e.data?.entryId)) {
    const i=e.data.identity;
    records.set(e.data.entryId,{native:true,entryId:e.data.entryId,identity:inputIdentity(i?{submissionId:i.submissionId,hostOrigin:i.origin,segments:i.segments}:null)??null});
  }
  return records;
}
export function bindProjectedInputs(projection,branch) {
  const records=recordedInputs(branch);
  inputRefs.set(projection.messages,projection.entries.flatMap(e=>e.messages.map(m=>m.role==='user'?(records.get(e.sourceEntry.id)??null):null)));
  return projection.messages;
}
export function persistInputIdentity(identity) {
  return inputIdentity(identity?{submissionId:identity.submissionId,hostOrigin:identity.origin,segments:identity.segments}:null)??null;
}

export class PendingQueueRegistry {
  epoch = randomUUID();
  revision = 0;
  sequence = 0;
  pending = new Map();
  queues = new Map();
  cancelled = new Set();
  // Submission IDs (primary and appended) recently taken, cancelled or cleared.
  // A repeated append request for one of them neither appends nor enqueues.
  retired = new Set();
  listeners = new Set();

  retire(entries) {
    for (const entry of entries) for (const id of [entry.submissionId, ...entry.segments]) if (id) {
      this.retired.add(id);
      if (this.retired.size > 1024) this.retired.delete(this.retired.values().next().value);
    }
  }
  snapshot() {
    const entries = [...this.pending.values()];
    const text = (entry) => typeof entry.message.content === 'string' ? entry.message.content :
      (entry.message.content ?? []).filter((b) => b.type === 'text').map((b) => b.text).join('\n');
    const lane = (kind) => (this.queues.get(kind)?.messages ?? []).map(text);
    return { capability: 4, epoch: this.epoch, revision: this.revision,
      items: entries.map(entry => this.item(entry)),
      steering: lane('steer'), followUp: lane('followUp') };
  }
  item({ queueId, kind, submissionId, itemRevision, segments, claim }) {
    return { queueId, kind, itemRevision, ...(submissionId ? { submissionId } : {}),
      ...(segments.length ? { segments: [...segments] } : {}), ...(claim ? { preparing: true } : {}) };
  }
  change(entries, state, detail = {}) {
    this.revision++;
    const event = { ...this.snapshot(), changes: entries.map(entry => ({ ...this.item(entry), state, ...detail })) };
    // Queue mutation has already committed. Observers cannot turn successful
    // admission/removal into a rejected operation by throwing.
    for (const listener of this.listeners) { try { listener(structuredClone(event)); } catch {} }
  }
  subscribe(listener) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
  promote(epoch, queueId, expectedRevision) {
    if (epoch !== this.epoch) return { outcome: 'stale', ...this.snapshot() };
    const entry = this.pending.get(queueId);
    if (!entry) return { outcome: 'not_pending', ...this.snapshot() };
    if (!Number.isSafeInteger(expectedRevision) || expectedRevision < 0) throw new Error('item revision is required');
    if (entry.kind === 'steer' && (entry.itemRevision === expectedRevision || entry.promotedFrom === expectedRevision))
      return { outcome: 'already_steering', ...this.snapshot() };
    if (entry.itemRevision !== expectedRevision) return { outcome: 'stale_item', ...this.snapshot() };
    const target = this.queues.get('steer');
    const source = entry.queue, index = source.messages.indexOf(entry);
    if (entry.kind !== 'followUp' || index < 0 || !target || target.registry !== this)
      throw new Error('Queue registry invariant violated');
    // Move one occurrence, not its message. No await, callback, or intermediate
    // snapshot can observe it absent from both lanes or present in both.
    source.messages.splice(index, 1);
    target.messages.push(entry);
    entry.queue = target;
    entry.kind = 'steer';
    entry.promotedFrom = entry.itemRevision++;
    this.change([entry], 'pending');
    return { outcome: 'promoted', ...this.snapshot() };
  }
  cancel(epoch, queueId, expectedRevision) {
    if (epoch !== this.epoch) return { outcome: 'stale', ...this.snapshot() };
    const entry = this.pending.get(queueId);
    if (!entry) return { outcome: this.cancelled.has(queueId) ? 'cancelled' : 'not_pending', ...this.snapshot() };
    if (expectedRevision !== undefined && entry.itemRevision !== expectedRevision)
      return { outcome: 'stale_item', ...this.snapshot() };
    entry.queue.remove(entry);
    this.cancelled.add(queueId);
    if (this.cancelled.size > 1024) this.cancelled.delete(this.cancelled.values().next().value);
    this.retire([entry]);
    this.change([entry], 'cancelled');
    return { outcome: 'cancelled', ...this.snapshot() };
  }
  // Freeze occurrences captured by an idle preparation. They stay pending and
  // cancellable with their complete payload, but can no longer be extended:
  // extending would invalidate the preparation instead of the later input
  // simply waiting for its own checkpoint.
  claim(entries) {
    const token = {};
    const claimed = entries.filter((e, i, all) => e && all.indexOf(e) === i && this.pending.get(e.queueId) === e && !e.claim);
    for (const entry of claimed) entry.claim = token;
    if (claimed.length) this.change(claimed, 'preparing');
    return token;
  }
  release(token) {
    if (!token) return;
    const released = [...this.pending.values()].filter(e => e.claim === token);
    for (const entry of released) delete entry.claim;
    if (released.length) this.change(released, 'pending');
  }
  // Synchronously extend one unclaimed pending human occurrence with a later
  // human input. Never awaits, so it linearizes with drain/commit/cancel/promote.
  // Only 'appended' mutates; every other outcome leaves the registry unchanged.
  append(epoch, queueId, expectedRevision, kind, message, metadata, fence) {
    const submissionId = metadata?.submissionId, identity = inputIdentity(metadata);
    const target = this.pending.get(queueId);
    if (typeof submissionId !== 'string' || !SUBMISSION_ID.test(submissionId)) return { outcome: 'ineligible' };
    if (target?.segments.includes(submissionId)) return { outcome: 'already_appended', queueId, itemRevision: target.itemRevision };
    if (this.retired.has(submissionId) || [...this.pending.values()].some(e => e.submissionId === submissionId || e.segments.includes(submissionId)))
      return { outcome: 'duplicate' };
    if (fence) return { outcome: fence }; // Session Stop/settlement fence
    if (epoch !== this.epoch) return { outcome: 'stale' };
    if (!target) return { outcome: 'not_pending' };
    if (target.claim) return { outcome: 'preparing' };
    if (target.itemRevision !== expectedRevision) return { outcome: 'stale_item' };
    if (target.kind !== kind) return { outcome: 'kind_mismatch' };
    // Nothing else may be queued between the occurrence and this input.
    if ([...this.pending.values()].some(e => e.sequence > target.sequence)) return { outcome: 'not_latest' };
    if (!target.submissionId || target.identity?.origin?.source !== 'user' || !sameOrigin(target.identity.origin, identity?.origin))
      return { outcome: 'ineligible' };
    if (target.segments.length + 1 >= MAX_SEGMENTS) return { outcome: 'limit' };
    const merged = mergeQueuedMessage(target.message, message);
    if (!merged) return { outcome: 'ineligible' };
    target.message = merged;
    target.segments = [...target.segments, submissionId];
    target.identity = inputIdentity({ submissionId: target.submissionId, hostOrigin: target.identity.origin, segments: target.segments });
    target.itemRevision++;
    this.change([target], 'appended', { appendedSubmissionId: submissionId });
    return { outcome: 'appended', queueId, itemRevision: target.itemRevision, message: merged };
  }
}

export class PendingMessageQueue {
  messages = [];
  constructor(mode, kind, registry) {
    this.mode = mode; this.kind = kind; this.registry = registry;
    if (registry.queues.has(kind)) throw new Error('Duplicate queue kind');
    registry.queues.set(kind, this);
  }
  enqueue(message, metadata) {
    const submissionId = metadata?.submissionId;
    if (submissionId !== undefined && (typeof submissionId !== 'string' || !/^[a-f0-9]{48}$/.test(submissionId)))
      throw new Error('Invalid submission ID');
    const sequence = ++this.registry.sequence;
    const entry = { queueId: `${this.registry.epoch}:${sequence}`, sequence, kind: this.kind,
      submissionId, message, queue: this, itemRevision: 0, identity: inputIdentity(metadata ? { ...metadata, segments: undefined } : metadata), segments: [] };
    this.messages.push(entry);
    this.registry.pending.set(entry.queueId, entry);
    this.registry.change([entry], 'pending');
    return entry.queueId;
  }
  hasItems() { return this.messages.length > 0; }
  peek() { return (this.mode === 'all' ? this.messages : this.messages.slice(0, 1)).map((e) => e.message); }
  // Prepare from an isolated copy while keeping the exact native occurrences
  // pending/cancellable. A later commit only takes the unchanged captured prefix.
  prepareDrain() {
    const entries = (this.mode === 'all' ? this.messages : this.messages.slice(0, 1)).slice();
    const epoch = this.registry.epoch, mode = this.mode;
    const revisions = entries.map(e => e.itemRevision);
    return { epoch, mode, entries, revisions,
      messages: bindInputBatch(structuredClone(entries.map(e => e.message)),
        entries.map(e => Object.freeze({ ...e.identity, epoch, queueId:e.queueId }))) };
  }
  canCommitDrain(plan) {
    return plan.epoch === this.registry.epoch && plan.mode === this.mode && plan.entries.length > 0 &&
      plan.entries.every((e,i) => this.messages[i] === e && this.registry.pending.get(e.queueId) === e && e.itemRevision === plan.revisions[i]);
  }
  commitDrain(plan) {
    if (!this.canCommitDrain(plan)) return false;
    this.messages.splice(0,plan.entries.length);
    for (const entry of plan.entries) this.registry.pending.delete(entry.queueId);
    this.registry.retire(plan.entries);
    this.registry.change(plan.entries,'taken');
    return true;
  }
  drain() {
    const entries = this.messages.splice(0, this.mode === 'all' ? this.messages.length : 1);
    for (const entry of entries) this.registry.pending.delete(entry.queueId);
    this.registry.retire(entries);
    if (entries.length) this.registry.change(entries, 'taken');
    return bindInputBatch(entries.map((e) => e.message), entries.map(e => Object.freeze({ ...e.identity, epoch:this.registry.epoch, queueId:e.queueId })));
  }
  remove(entry) {
    const index = this.messages.indexOf(entry);
    if (index < 0) throw new Error('Queue registry invariant violated');
    this.messages.splice(index, 1);
    this.registry.pending.delete(entry.queueId);
  }
  clear() {
    const entries = this.messages.splice(0);
    for (const entry of entries) this.registry.pending.delete(entry.queueId);
    this.registry.retire(entries);
    if (entries.length) this.registry.change(entries, 'cleared');
  }
}
