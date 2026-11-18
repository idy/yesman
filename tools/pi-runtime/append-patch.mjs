// Capability 4: append a later human input into the same still-pending native
// occurrence. The decision is one synchronous registry step at the exact point
// pi would otherwise enqueue it, so it linearizes with checkpoint drain, idle
// preparation claims, cancel, promote and clear. When the occurrence was taken,
// claimed or changed first, the input is enqueued as a new occurrence instead.
export async function appendPatch(patch, core, app) {
 await patch(core+'agent.js',(change)=>{
  change('    async runQueuedInput(epoch, queueId, expectedRevision, prepare, claimed, canCommit, committed) {', `    /** Append into one unclaimed pending occurrence, or enqueue as a new occurrence in the same synchronous step. */
    appendQueuedMessage(target, kind, message, metadata) {
        const queue = kind === "steer" ? this.steeringQueue : kind === "followUp" ? this.followUpQueue : undefined;
        if (!queue) throw new Error("Invalid queue kind");
        const result = this.pendingQueue.append(target?.epoch, target?.queueId, target?.expectedRevision, kind, message, metadata, target?.fence);
        if (result.outcome === "appended" || result.outcome === "already_appended" || result.outcome === "duplicate") return result;
        return { outcome: result.outcome, queueId: queue.enqueue(message, metadata), itemRevision: 0, enqueued: true };
    }
    async runQueuedInput(epoch, queueId, expectedRevision, prepare, claimed, canCommit, committed) {`);
  change(`            let plan, prepared;
            try {`, `            let plan, prepared, claim, ready = false;
            try {`);
  change(`                if (!valid()) return;
                claimed(result); // priority changed; intake has NOT committed
                prepared = await prepare(plan.messages,signal);
                if (!prepared || !valid() || !prepared.commit()) return;
            } catch(error) {`, `                if (!valid()) return;
                // Captured occurrences and the promoted target stay pending and
                // cancellable, but cannot be extended until this preparation ends.
                claim = this.pendingQueue.claim([...plan.entries, target]);
                claimed(result); // priority changed; intake has NOT committed
                prepared = await prepare(plan.messages,signal);
                if (!prepared || !valid() || !prepared.commit()) return;
                ready = true;
            } catch(error) {`);
  change(`                preparationError = error;
                return;
            }
            // All asynchronous preparation is over. No await between validation,
            // consuming Session nextTurn context and exact native intake.
            if (!this.steeringQueue.commitDrain(plan)) return;`, `                preparationError = error;
                return;
            } finally {
                if (!ready) this.pendingQueue.release(claim);
            }
            // All asynchronous preparation is over. No await between validation,
            // consuming Session nextTurn context and exact native intake.
            const intake = this.steeringQueue.commitDrain(plan);
            // A claimed target behind the one-at-a-time prefix becomes extendable again.
            this.pendingQueue.release(claim);
            if (!intake) return;`);
 });
 await patch(core+'agent.d.ts',(change)=>{
  change('export interface HostInputIdentity { submissionId?: string;', 'export interface HostInputIdentity { submissionId?: string; segments?: string[];');
  change('origin?: { source: "user"|"lead"|"yesman"|"schedule"; kind?: string; worker?: string; plan?: string; run?: string };', 'origin?: { source: "user"|"lead"|"yesman"|"schedule"; kind?: string; worker?: string; plan?: string; run?: string; context?: string };');
  change('export interface PendingQueueItem { queueId: string; kind: "steer" | "followUp"; submissionId?: string; itemRevision: number; }', `export interface PendingQueueItem { queueId: string; kind: "steer" | "followUp"; submissionId?: string; itemRevision: number; segments?: string[]; preparing?: boolean; }
export interface PendingAppendTarget { epoch: string; queueId: string; expectedRevision: number; controlGeneration: number; }
export interface PendingAppendResult { outcome: "appended" | "already_appended" | "duplicate" | "stale" | "not_pending" | "preparing" | "stale_item" | "stale_control" | "kind_mismatch" | "not_latest" | "ineligible" | "limit" | "budget"; queueId?: string; itemRevision?: number; enqueued?: boolean; }`);
  change('capability: 3;', 'capability: 4;');
  change('idleResume?: boolean; }', 'idleResume?: boolean; appendAllowed?: boolean; }');
  change('state: "pending" | "taken" | "cancelled" | "cleared" })[]; }', 'state: "pending" | "taken" | "cancelled" | "cleared" | "appended" | "preparing"; appendedSubmissionId?: string })[]; }');
  change('    notifyPendingQueue(): void;', `    notifyPendingQueue(): void;
    /** Synchronously append into one unclaimed pending human occurrence; otherwise enqueue as a new occurrence (enqueued: true). */
    appendQueuedMessage(target: PendingAppendTarget & { fence?: string }, kind: "steer" | "followUp", message: AgentMessage, metadata: { submissionId: string; hostOrigin?: HostInputIdentity["origin"] }): PendingAppendResult;`);
 });
 await patch(app+'core/agent-session.js',(change)=>{
  change(`            if (options.streamingBehavior === "followUp") {
                await this._queueFollowUp(expandedText, currentImages, options?.submissionId, options?.hostOrigin);
            }
            else {
                await this._queueSteer(expandedText, currentImages, options?.submissionId, options?.hostOrigin);
            }
            preflightResult?.("queued");`, `            const queued = options.streamingBehavior === "followUp"
                ? await this._queueFollowUp(expandedText, currentImages, options?.submissionId, options?.hostOrigin, options?.appendTo)
                : await this._queueSteer(expandedText, currentImages, options?.submissionId, options?.hostOrigin, options?.appendTo);
            preflightResult?.("queued", options?.appendTo ? queued : undefined);`);
  change('async _queueSteer(text, images, submissionId, hostOrigin) {', 'async _queueSteer(text, images, submissionId, hostOrigin, appendTo) {');
  change('async _queueFollowUp(text, images, submissionId, hostOrigin) {', 'async _queueFollowUp(text, images, submissionId, hostOrigin, appendTo) {');
  change('        return this.agent.steer(message, { submissionId, hostOrigin });', '        if (appendTo) return this._appendQueued("steer", message, { submissionId, hostOrigin }, appendTo);\n        return this.agent.steer(message, { submissionId, hostOrigin });');
  change('        return this.agent.followUp(message, { submissionId, hostOrigin });', '        if (appendTo) return this._appendQueued("followUp", message, { submissionId, hostOrigin }, appendTo);\n        return this.agent.followUp(message, { submissionId, hostOrigin });');
  // Idle preparation works on copies of the captured queued inputs, which the
  // delivery-time normalization (keyed by the original objects) never sees.
  // Normalize the copies after before_agent_start chose the model, as for an
  // idle prompt; the pending originals stay unchanged until intake commits.
  change(`        if (queuedMessages) {
            messages.push(...queuedMessages);
        }`, `        if (queuedMessages) {
            for (const message of queuedMessages)
                if (message.role === "user")
                    await this._normalizeQueuedMessageImages(message);
            messages.push(...queuedMessages);
        }`);
  change('    getPendingQueue() { return this._decoratePendingQueue(this.agent.getPendingQueue()); }', `    getPendingQueue() { return this._decoratePendingQueue(this.agent.getPendingQueue()); }
    _appendQueued(kind, message, metadata, appendTo) {
        // Stop and settlement are fences: a request prepared before them never
        // extends an occurrence after them; it is enqueued as a new occurrence.
        const fenced = !this.isStreaming || this._agentRunAbortRequested || this._isEmittingAgentSettled ||
            appendTo.controlGeneration !== this._queueControlGeneration;
        const result = this.agent.appendQueuedMessage(fenced ? { ...appendTo, fence: "stale_control" } : appendTo, kind, message, metadata);
        if (result.outcome === "appended" && result.message.content.some(block => block.type === "image"))
            this._queuedMessagesWithImages.add(result.message); // normalized once, at delivery
        const { message: _merged, ...visible } = result;
        return visible;
    }`);
  change('        return { ...snapshot, controlGeneration: this._queueControlGeneration, promoteAllowed, idleResume: true,', '        return { ...snapshot, controlGeneration: this._queueControlGeneration, promoteAllowed, idleResume: true,\n            appendAllowed: this.isStreaming && !this._agentRunAbortRequested && !this._isEmittingAgentSettled,');
 });
 await patch(app+'core/agent-session.d.ts',(change)=>{
  change('export interface PromptOptions {', 'export interface PromptOptions {\n    /** Host-only: while streaming, try to extend this still-pending occurrence; otherwise queue normally. */\n    appendTo?: import("@earendil-works/pi-agent-core").PendingAppendTarget;');
  change('    preflightResult?: (disposition: PromptDisposition) => void;', '    preflightResult?: (disposition: PromptDisposition, append?: import("@earendil-works/pi-agent-core").PendingAppendResult) => void;');
 });
 await patch(app+'modes/rpc/rpc-mode.js',(change)=>{
  change(`            case "prompt": {
`, `            case "prompt": {
                const target = command.appendTo;
                if (target !== undefined && (typeof target !== "object" || target === null || typeof target.epoch !== "string" || typeof target.queueId !== "string" ||
                    !Number.isSafeInteger(target.expectedRevision) || target.expectedRevision < 0 ||
                    !Number.isSafeInteger(target.controlGeneration) || target.controlGeneration < 0))
                    return error(id, "prompt", "appendTo requires epoch, queueId, expectedRevision and controlGeneration");
`);
  change(`                    hostOrigin: command.hostOrigin,
                    source: "rpc",`, `                    hostOrigin: command.hostOrigin,
                    appendTo: target && { epoch: target.epoch, queueId: target.queueId, expectedRevision: target.expectedRevision, controlGeneration: target.controlGeneration },
                    source: "rpc",`);
  change(`                    preflightResult: (disposition) => {
                        preflightSucceeded = true;
                        output(success(id, "prompt", { disposition }));`, `                    preflightResult: (disposition, append) => {
                        preflightSucceeded = true;
                        output(success(id, "prompt", append ? { disposition, append } : { disposition }));`);
  change('runtime: "yesman-pi/1.5.2-4"', 'runtime: "yesman-pi/1.5.2-5"');
 });
 await patch(app+'modes/rpc/rpc-types.d.ts',(change)=>{
  change('    type: "prompt";\n    hostOrigin?:', '    type: "prompt";\n    appendTo?: import("@earendil-works/pi-agent-core").PendingAppendTarget;\n    hostOrigin?:');
  change('    command: "prompt";\n    success: true;\n    data: {\n        disposition: PromptDisposition;\n    };', '    command: "prompt";\n    success: true;\n    data: {\n        disposition: PromptDisposition;\n        append?: import("@earendil-works/pi-agent-core").PendingAppendResult;\n    };');
 });
 await patch(app+'modes/rpc/rpc-client.js',(change)=>{
  change(`    async prompt(message, images, streamingBehavior, submissionId) {`, `    async promptAppend(message, images, streamingBehavior, submissionId, appendTo) {
        return this.getData(await this.send({ type: "prompt", message, images, streamingBehavior, submissionId, appendTo }));
    }
    async prompt(message, images, streamingBehavior, submissionId) {`);
 });
 await patch(app+'modes/rpc/rpc-client.d.ts',(change)=>{
  change('    prompt(message: string, images?: ImageContent[], streamingBehavior?: "steer" | "followUp", submissionId?: string): Promise<PromptDisposition>;', `    prompt(message: string, images?: ImageContent[], streamingBehavior?: "steer" | "followUp", submissionId?: string): Promise<PromptDisposition>;
    /** Like prompt, but while streaming tries to extend the given still-pending occurrence and reports the outcome. */
    promptAppend(message: string, images: ImageContent[] | undefined, streamingBehavior: "steer" | "followUp", submissionId: string, appendTo: import("@earendil-works/pi-agent-core").PendingAppendTarget): Promise<{ disposition: PromptDisposition; append?: import("@earendil-works/pi-agent-core").PendingAppendResult }>;`);
 });
}
