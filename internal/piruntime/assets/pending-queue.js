// Part of yesman's pinned pi source patch. Queue identities never live on an
// AgentMessage, so context/input/provider hooks cannot serialize them by accident.
import { randomUUID } from 'node:crypto';

// Identity belongs to a batch position, never to the AgentMessage. In
// particular the same object may occur twice, or also occur in prepared context.
const inputRefs = new WeakMap();
const triggerRefs = new WeakMap();
export function inputIdentity(metadata) {
  if (!metadata) return undefined;
  const submissionId = /^[a-f0-9]{48}$/.test(metadata.submissionId ?? '') ? metadata.submissionId : undefined;
  const source = metadata.hostOrigin?.source;
  const origin = ['user','lead','yesman'].includes(source) ? {source} : submissionId ? {source:'user'} : undefined;
  if (origin) {
    if (/^[a-z]{1,16}$/.test(metadata.hostOrigin?.kind ?? '')) origin.kind = metadata.hostOrigin.kind;
    if (/^[a-z0-9][a-z0-9-]{0,31}$/.test(metadata.hostOrigin?.worker ?? '')) origin.worker = metadata.hostOrigin.worker;
  }
  return submissionId || origin ? Object.freeze({...(submissionId ? {submissionId}:{}), ...(origin ? {origin:Object.freeze(origin)}:{})}) : undefined;
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
    records.set(e.data.entryId,{native:true,entryId:e.data.entryId,identity:inputIdentity(i?{submissionId:i.submissionId,hostOrigin:i.origin}:null)??null});
  }
  return records;
}
export function bindProjectedInputs(projection,branch) {
  const records=recordedInputs(branch);
  inputRefs.set(projection.messages,projection.entries.flatMap(e=>e.messages.map(m=>m.role==='user'?(records.get(e.sourceEntry.id)??null):null)));
  return projection.messages;
}
export function persistInputIdentity(identity) {
  return inputIdentity(identity?{submissionId:identity.submissionId,hostOrigin:identity.origin}:null)??null;
}

export class PendingQueueRegistry {
  epoch = randomUUID();
  revision = 0;
  sequence = 0;
  pending = new Map();
  queues = new Map();
  cancelled = new Set();
  listeners = new Set();

  snapshot() {
    const entries = [...this.pending.values()];
    const text = (entry) => typeof entry.message.content === 'string' ? entry.message.content :
      (entry.message.content ?? []).filter((b) => b.type === 'text').map((b) => b.text).join('\n');
    const lane = (kind) => (this.queues.get(kind)?.messages ?? []).map(text);
    return { capability: 3, epoch: this.epoch, revision: this.revision,
      items: entries.map(({ queueId, kind, submissionId, itemRevision }) => ({ queueId, kind, itemRevision, ...(submissionId ? { submissionId } : {}) })),
      steering: lane('steer'), followUp: lane('followUp') };
  }
  change(entries, state) {
    this.revision++;
    const event = { ...this.snapshot(), changes: entries.map(({ queueId, kind, submissionId, itemRevision }) =>
      ({ queueId, kind, state, itemRevision, ...(submissionId ? { submissionId } : {}) })) };
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
    this.change([entry], 'cancelled');
    return { outcome: 'cancelled', ...this.snapshot() };
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
    const entry = { queueId: `${this.registry.epoch}:${++this.registry.sequence}`, kind: this.kind,
      submissionId, message, queue: this, itemRevision: 0, identity: inputIdentity(metadata) };
    this.messages.push(entry);
    this.registry.pending.set(entry.queueId, entry);
    this.registry.change([entry], 'pending');
    return entry.queueId;
  }
  hasItems() { return this.messages.length > 0; }
  peek() { return (this.mode === 'all' ? this.messages : this.messages.slice(0, 1)).map((e) => e.message); }
  drain() {
    const entries = this.messages.splice(0, this.mode === 'all' ? this.messages.length : 1);
    for (const entry of entries) this.registry.pending.delete(entry.queueId);
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
    if (entries.length) this.registry.change(entries, 'cleared');
  }
}
