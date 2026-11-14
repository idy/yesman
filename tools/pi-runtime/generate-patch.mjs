// Maintainer tool: generate the reviewed source patch from an unmodified npm ci.
// Runtime installation uses patch.json and verifies every before/after SHA256.
import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { resolve } from 'node:path';
const root = resolve(process.argv[2]);
const dest = resolve(process.argv[3]);
const sha = (s) => createHash('sha256').update(s).digest('hex');
const patches = [];
async function patch(path, transform) {
  const original = await readFile(resolve(root, 'node_modules', path), 'utf8');
  let source = original;
  const edits = [];
  const change = (before, after) => {
    if (source.split(before).length !== 2) throw new Error(`Nonunique anchor in ${path}: ${before.slice(0,100)}`);
    source = source.replace(before, after); edits.push({ before, after });
  };
  transform(change, source);
  patches.push({ path, before: sha(original), after: sha(source), edits });
}
const core = '@earendil-works/pi-agent-core/dist/';
const app = '@earendil-works/pi-coding-agent/dist/';
await patch(app+'bundle/cli.js', (change, source) => {
  // npm's existing platform shim can still target the original bundled path.
  // Keep that public path as a thin entry into the patched source modules.
  change(source, '#!/usr/bin/env node\nimport "../cli.js";\n');
});
await patch(core+'agent.js', (change, source) => {
  const start = source.indexOf('class PendingMessageQueue {');
  const end = source.indexOf('/**\n * Stateful wrapper', start);
  change(source.slice(start,end), 'import { PendingMessageQueue, PendingQueueRegistry } from "./pending-queue.js";\n');
  change('    steeringQueue;\n', '    pendingQueue = new PendingQueueRegistry();\n    steeringQueue;\n');
  change('new PendingMessageQueue(runtimeOptions.steeringMode ?? "one-at-a-time")', 'new PendingMessageQueue(runtimeOptions.steeringMode ?? "one-at-a-time", "steer", this.pendingQueue)');
  change('new PendingMessageQueue(runtimeOptions.followUpMode ?? "one-at-a-time")', 'new PendingMessageQueue(runtimeOptions.followUpMode ?? "one-at-a-time", "followUp", this.pendingQueue)');
  change('    steer(message) {\n        this.steeringQueue.enqueue(message);\n    }', '    steer(message, metadata) {\n        return this.steeringQueue.enqueue(message, metadata);\n    }');
  change('    followUp(message) {\n        this.followUpQueue.enqueue(message);\n    }', '    followUp(message, metadata) {\n        return this.followUpQueue.enqueue(message, metadata);\n    }');
  change('    /** Remove all queued steering messages. */', `    getPendingQueue() { return this.pendingQueue.snapshot(); }
    subscribePendingQueue(listener) { return this.pendingQueue.subscribe(listener); }
    cancelQueuedMessage(epoch, queueId) { return this.pendingQueue.cancel(epoch, queueId); }
    /** Remove all queued steering messages. */`);
});
await patch(core+'agent.d.ts', (change) => {
  change('export declare class Agent {', `export interface PendingQueueItem { queueId: string; kind: "steer" | "followUp"; submissionId?: string; }
export interface PendingQueueSnapshot { capability: 1; epoch: string; revision: number; items: PendingQueueItem[]; steering: string[]; followUp: string[]; }
export interface PendingQueueEvent extends PendingQueueSnapshot { changes: (PendingQueueItem & { state: "pending" | "taken" | "cancelled" | "cleared" })[]; }
export interface PendingQueueResult extends PendingQueueSnapshot { outcome: "cancelled" | "not_pending" | "stale"; }
export declare class Agent {`);
  change('steer(message: AgentMessage): void;', 'steer(message: AgentMessage, metadata?: { submissionId?: string }): string;');
  change('followUp(message: AgentMessage): void;', `followUp(message: AgentMessage, metadata?: { submissionId?: string }): string;
    getPendingQueue(): PendingQueueSnapshot;
    subscribePendingQueue(listener: (event: PendingQueueEvent) => void): () => void;
    cancelQueuedMessage(epoch: string, queueId: string): PendingQueueResult;`);
});
await patch(app+'core/agent-session.js', (change, source) => {
  change('            followUp: [...this._followUpMessages],', '            followUp: [...this._followUpMessages],\n            pending: { ...this.agent.getPendingQueue(), changes: [] },');
  change('        this._unsubscribeAgent = this.agent.subscribe(this._handleAgentEvent);', `        this._unsubscribeAgent = this.agent.subscribe(this._handleAgentEvent);
        this._unsubscribePendingQueue = this.agent.subscribePendingQueue((pending) => {
            this._steeringMessages = pending.steering;
            this._followUpMessages = pending.followUp;
            this._emit({ type: "queue_update", steering: pending.steering, followUp: pending.followUp, pending });
        });`);
  const start = source.indexOf('            const messageText = contentText(event.message.content, "");');
  const end = source.indexOf('            // Normalize queued images at delivery', start);
  change(source.slice(start,end), '            // Pending membership changes synchronously at native drain, never by matching text.\n');
  change('    _disconnectFromAgent() {', '    _disconnectFromAgent() {\n        this._unsubscribePendingQueue?.();\n        this._unsubscribePendingQueue = undefined;');
  change('await this._queueFollowUp(expandedText, currentImages);', 'await this._queueFollowUp(expandedText, currentImages, options?.submissionId);');
  change('await this._queueSteer(expandedText, currentImages);', 'await this._queueSteer(expandedText, currentImages, options?.submissionId);');
  change('async _queueUserInput(text, images, behavior, source) {', 'async _queueUserInput(text, images, behavior, source, submissionId) {');
  change('await this._queueSteer(expandedText, processedInput.images);', 'await this._queueSteer(expandedText, processedInput.images, submissionId);');
  change('await this._queueFollowUp(expandedText, processedInput.images);', 'await this._queueFollowUp(expandedText, processedInput.images, submissionId);');
  change('this._queueUserInput(text, images, "steer", options?.source ?? "interactive")', 'this._queueUserInput(text, images, "steer", options?.source ?? "interactive", options?.submissionId)');
  change('this._queueUserInput(text, images, "followUp", options?.source ?? "interactive")', 'this._queueUserInput(text, images, "followUp", options?.source ?? "interactive", options?.submissionId)');
  change('    async _queueSteer(text, images) {\n        this._steeringMessages.push(text);\n        this._emitQueueUpdate();', '    async _queueSteer(text, images, submissionId) {');
  change('    async _queueFollowUp(text, images) {\n        this._followUpMessages.push(text);\n        this._emitQueueUpdate();', '    async _queueFollowUp(text, images, submissionId) {');
  change('        this.agent.steer(message);', '        return this.agent.steer(message, { submissionId });');
  change('        this.agent.followUp(message);', '        return this.agent.followUp(message, { submissionId });');
  change('    clearQueue() {', `    getPendingQueue() { return this.agent.getPendingQueue(); }
    cancelQueuedMessage(epoch, queueId) { return this.agent.cancelQueuedMessage(epoch, queueId); }
    clearQueue() {`);
});
await patch(app+'core/agent-session.d.ts', (change) => {
  change('export interface PromptOptions {', 'export interface PromptOptions {\n    /** Host-only identity. Never passed to input/context/model hooks. */\n    submissionId?: string;');
  change('    steer(text: string, images?: ImageContent[], options?: {', '    steer(text: string, images?: ImageContent[], options?: {\n        submissionId?: string;');
  change('    followUp(text: string, images?: ImageContent[], options?: {', '    followUp(text: string, images?: ImageContent[], options?: {\n        submissionId?: string;');
  change('    clearQueue(): {', `    getPendingQueue(): import("@earendil-works/pi-agent-core").PendingQueueSnapshot;
    cancelQueuedMessage(epoch: string, queueId: string): import("@earendil-works/pi-agent-core").PendingQueueResult;
    clearQueue(): {`);
  change('    type: "queue_update";', '    type: "queue_update";\n    pending?: import("@earendil-works/pi-agent-core").PendingQueueEvent;');
});
await patch(app+'modes/rpc/rpc-mode.js', (change) => {
  change('                    streamingBehavior: command.streamingBehavior,', '                    streamingBehavior: command.streamingBehavior,\n                    submissionId: command.submissionId,');
  change('session.steer(command.message, command.images, { source: "rpc" })', 'session.steer(command.message, command.images, { source: "rpc", submissionId: command.submissionId })');
  change('session.followUp(command.message, command.images, { source: "rpc" })', 'session.followUp(command.message, command.images, { source: "rpc", submissionId: command.submissionId })');
  change('            case "clear_queue": {', `            case "get_pending_queue": {
                return success(id, "get_pending_queue", session.getPendingQueue());
            }
            case "cancel_queued_message": {
                if (typeof command.epoch !== "string" || typeof command.queueId !== "string")
                    return error(id, "cancel_queued_message", "epoch and queueId are required");
                return success(id, "cancel_queued_message", session.cancelQueuedMessage(command.epoch, command.queueId));
            }
            case "clear_queue": {`);
  change('                    pendingMessageCount: session.pendingMessageCount,', '                    pendingMessageCount: session.pendingMessageCount,\n                    pendingQueue: session.getPendingQueue(),\n                    runtime: "yesman-pi/1.5.2-1",');
});
await patch(app+'modes/rpc/rpc-types.d.ts', (change) => {
  for (const type of ['prompt','steer','follow_up']) change(`    type: "${type}";\n    message: string;`, `    type: "${type}";\n    submissionId?: string;\n    message: string;`);
  change('    type: "clear_queue";\n} | {', `    type: "clear_queue";
} | {
    id?: string; type: "get_pending_queue";
} | {
    id?: string; type: "cancel_queued_message"; epoch: string; queueId: string;
} | {`);
  change('    pendingMessageCount: number;', '    pendingMessageCount: number;\n    pendingQueue?: import("@earendil-works/pi-agent-core").PendingQueueSnapshot;\n    runtime?: string;');
  change('export type RpcResponse = {', `export type RpcResponse = {
    id?: string; type: "response"; command: "get_pending_queue"; success: true;
    data: import("@earendil-works/pi-agent-core").PendingQueueSnapshot;
} | {
    id?: string; type: "response"; command: "cancel_queued_message"; success: true;
    data: import("@earendil-works/pi-agent-core").PendingQueueResult;
} | {`);
});
await patch(app+'modes/rpc/rpc-client.js', (change) => {
  change('async prompt(message, images, streamingBehavior)', 'async prompt(message, images, streamingBehavior, submissionId)');
  change('{ type: "prompt", message, images, streamingBehavior }', '{ type: "prompt", message, images, streamingBehavior, submissionId }');
  for (const [method,type] of [['steer','steer'],['followUp','follow_up']]) {
    change(`async ${method}(message, images)`, `async ${method}(message, images, submissionId)`);
    change(`{ type: "${type}", message, images }`, `{ type: "${type}", message, images, submissionId }`);
  }
  change('    async clearQueue() {', `    async getPendingQueue() { return this.getData(await this.send({ type: "get_pending_queue" })); }
    async cancelQueuedMessage(epoch, queueId) { return this.getData(await this.send({ type: "cancel_queued_message", epoch, queueId })); }
    async clearQueue() {`);
});
await patch(app+'modes/rpc/rpc-client.d.ts', (change) => {
  change('streamingBehavior?: "steer" | "followUp"): Promise<PromptDisposition>;', 'streamingBehavior?: "steer" | "followUp", submissionId?: string): Promise<PromptDisposition>;');
  for (const method of ['steer','followUp']) change(`${method}(message: string, images?: ImageContent[]): Promise<QueuedInputDisposition>;`, `${method}(message: string, images?: ImageContent[], submissionId?: string): Promise<QueuedInputDisposition>;`);
  change('    clearQueue(): Promise<{', `    getPendingQueue(): Promise<import("@earendil-works/pi-agent-core").PendingQueueSnapshot>;
    cancelQueuedMessage(epoch: string, queueId: string): Promise<import("@earendil-works/pi-agent-core").PendingQueueResult>;
    clearQueue(): Promise<{`);
});
await patch('@earendil-works/pi-coding-agent/package.json', (change) => {
  change('"pi": "dist/bundle/cli.js"', '"pi": "dist/cli.js"');
  change('"import": "./dist/bundle/rpc-entry.js"', '"import": "./dist/rpc-entry.js"');
});
await writeFile(dest, JSON.stringify({ upstream: '1.5.2', revision: 1, patches }, null, 2)+'\n');
console.log(`${patches.length} source files recorded in ${dest}`);
