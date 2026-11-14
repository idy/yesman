// Part of yesman's pinned pi source patch. Queue identities never live on an
// AgentMessage, so context/input/provider hooks cannot serialize them by accident.
import { randomUUID } from 'node:crypto';

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
    return { capability: 2, epoch: this.epoch, revision: this.revision,
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
      submissionId, message, queue: this, itemRevision: 0 };
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
    return entries.map((e) => e.message);
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
