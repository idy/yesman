// Session-owned idle queue admission. No detached wake or second core loop.
export async function idlePatch(patch,core,app) {
 await patch(core+'agent.js',(change)=>{
  change('    /** Remove all queued steering messages. */', `    async runQueuedInput(epoch, queueId, expectedRevision, prepare, claimed, canCommit, committed) {
        if (this.activeRun) throw new Error("Agent is already processing.");
        const before = this.pendingQueue.snapshot();
        const target = before.items.find(i => i.queueId === queueId);
        if (before.epoch !== epoch || !target || target.kind !== "followUp" || target.itemRevision !== expectedRevision)
            return { ...before, outcome: before.epoch !== epoch ? "stale" : target ? "stale_item" : "not_pending" };
        let result, preparationError;
        await this.runWithLifecycle(async signal => {
            // The lifecycle lock precedes observers of the committed queue change.
            result = this.pendingQueue.promote(epoch,queueId,expectedRevision);
            if (result.outcome !== "promoted") return;
            let plan, prepared;
            try {
                const target = this.pendingQueue.pending.get(queueId);
                const revision = target?.itemRevision;
                plan = this.steeringQueue.prepareDrain();
                const valid = () => !signal.aborted && canCommit() && this.pendingQueue.epoch === epoch &&
                    this.pendingQueue.pending.get(queueId) === target && target?.itemRevision === revision && this.steeringQueue.canCommitDrain(plan);
                if (!valid()) return;
                claimed(result); // priority changed; intake has NOT committed
                prepared = await prepare(plan.messages,signal);
                if (!prepared || !valid() || !prepared.commit()) return;
            } catch(error) {
                // Preparation is not a model turn: keep originals and do not
                // fabricate an assistant failure/usage record for untaken input.
                preparationError = error;
                return;
            }
            // All asynchronous preparation is over. No await between validation,
            // consuming Session nextTurn context and exact native intake.
            if (!this.steeringQueue.commitDrain(plan)) return;
            committed();
            // Intake has committed. Even if a queue observer just requested Stop,
            // use the normal loop's prompt recording with its aborted signal.
            // Never reinsert messages after this point or discard them on abort.
            await runAgentLoop(prepared.messages, this.createContextSnapshot(), this.createLoopConfig({skipInitialSteeringPoll:true}),
                event => this.processEvents(event), signal, this.streamFunction);
        });
        if (preparationError) throw preparationError;
        return result;
    }
    /** Remove all queued steering messages. */`);
 });
 await patch(core+'agent.d.ts',(change)=>{
  change('    notifyPendingQueue(): void;', `    notifyPendingQueue(): void;
    runQueuedInput(epoch:string,queueId:string,expectedRevision:number,prepare:(messages:AgentMessage[],signal:AbortSignal)=>Promise<{messages:AgentMessage[];commit:()=>boolean}|undefined>,claimed:(result:PendingPromotionResult)=>void,canCommit:()=>boolean,committed:()=>void):Promise<PendingPromotionResult>;`);
 });
 await patch(app+'core/agent-session.js',(change,source)=>{
  // Factor the entire existing recovery/settlement driver without changing it.
  const start=source.indexOf('        try {\n            await this.agent.prompt(messages);');
  const end=source.indexOf('    /**\n     * Prepare a run',start);
  if(start<0||end<0)throw Error('run driver anchor missing');
  const block=source.slice(start,end);
  change(block,`        await this._driveOwnedRun(() => this.agent.prompt(messages));
    }
    async _driveOwnedRun(run) {
${block.replace('            await this.agent.prompt(messages);','            await run();')}`);
  change('    async abort() {\n        this._queueControlGeneration++;', '    async abort() {\n        this._queueControlGeneration++;\n        this._queuedResume?.abort.abort();');
  change('    async _prepareRun(trigger) {','    async _prepareRun(trigger, queuedMessages) {');
  change('        if (trigger.messageType === "custom") {\n            messages.push(trigger.message);','        if (queuedMessages) {\n            messages.push(...queuedMessages);\n        }\n        else if (trigger.messageType === "custom") {\n            messages.push(trigger.message);');
  change('        messages.push(...this._pendingNextTurnMessages);\n        this._pendingNextTurnMessages = [];', `        const nextTurn = queuedMessages ? this._pendingNextTurnMessages.slice() : this._pendingNextTurnMessages;
        messages.push(...nextTurn);
        if (!queuedMessages) this._pendingNextTurnMessages = [];`);
  change('        return bindInputBatch(messages, messages.map((_, index) => index === (updateMessage ? 1 : 0) ? triggerIdentity(trigger) : undefined));',`        if (queuedMessages) {
            const offset = updateMessage ? 1 : 0;
            const batch = bindInputBatch(messages, messages.map((_,i) => i >= offset && i < offset + queuedMessages.length ? inputRefAt(queuedMessages,i-offset) : undefined));
            return { messages: batch, commit: () => {
                if (!nextTurn.every((m,i) => this._pendingNextTurnMessages[i] === m)) return false;
                this._recordSelection();
                this._pendingToolNames.clear();
                this._pendingNextTurnMessages.splice(0,nextTurn.length);
                return true;
            }};
        }
        return bindInputBatch(messages, messages.map((_, index) => index === (updateMessage ? 1 : 0) ? triggerIdentity(trigger) : undefined));`);
  change('    _queueControlGeneration = 0;', '    _queueControlGeneration = 0;\n    _queuedResume = undefined;');
  change('        const promoteAllowed = this.isStreaming && !this._agentRunAbortRequested && !this._isEmittingAgentSettled;\n        return { ...snapshot, controlGeneration: this._queueControlGeneration, promoteAllowed, idleResume: false,\n            promotionBlocked: promoteAllowed ? "" : this._agentRunAbortRequested ? "stopped" : "idle_resume_unavailable" };',`        const idleReady = !this.isStreaming && !this.isCompacting && !this._isEmittingAgentSettled && this._promptPreflightCount === 0 && !this._queuedResume;
        const promoteAllowed = !this._queuedResume && (idleReady || this.isStreaming && !this._agentRunAbortRequested && !this._isEmittingAgentSettled);
        return { ...snapshot, controlGeneration: this._queueControlGeneration, promoteAllowed, idleResume: true,
            promotionBlocked: promoteAllowed ? "" : "session_busy" };`);
  change('    promoteQueuedMessage(epoch, queueId, expectedRevision, controlGeneration) {','    async promoteQueuedMessage(epoch, queueId, expectedRevision, controlGeneration) {');
  change('        return this._decoratePendingQueue(this.agent.promoteQueuedMessage(epoch, queueId, expectedRevision));\n    }',`        if (item.kind === "steer" || this.isStreaming)
            return this._decoratePendingQueue(this.agent.promoteQueuedMessage(epoch, queueId, expectedRevision));
        // Reserve the Session synchronously; prompt preflights after this point
        // queue into this run. No input hook is repeated for the target.
        this._isAgentRunActive = true;
        this._agentRunAbortRequested = false;
        let resolveAdmission, rejectAdmission;
        const admission = new Promise((resolve,reject) => {resolveAdmission=resolve;rejectAdmission=reject});
        const reservation = { abort: new AbortController() };
        const admissionTimer = setTimeout(() => reservation.abort.abort(), 15000);
        this._queuedResume = reservation;
        this._emit({type:"queue_resume_start"});
        this.agent.notifyPendingQueue();
        let admitted = false, driven = false, started = false;
        reservation.done = (async () => {
            try {
                if (!this.model) throw new Error(formatNoModelSelectedMessage());
                const hasAuth = this._modelRuntime.hasConfiguredAuth(this.model.provider) ||
                    (await Promise.race([
                        this._modelRuntime.checkAuth(this.model.provider),
                        new Promise((_,reject) => reservation.abort.signal.addEventListener("abort", () => reject(new Error("Queue admission was stopped or timed out")), {once:true}))
                    ])) !== undefined;
                if (!hasAuth) throw new Error(formatNoApiKeyFoundMessage(this.model.provider));
                if (reservation.abort.signal.aborted || this._agentRunAbortRequested || controlGeneration !== this._queueControlGeneration)
                    throw new Error("Queue admission was stopped");
                const result = await this.agent.runQueuedInput(epoch,queueId,expectedRevision,
                    async (messages,signal) => {
                        const first = messages[0];
                        const images = Array.isArray(first?.content) ? first.content.filter(b=>b.type==='image') : undefined;
                        const prepared = await this._prepareRun({messageType:"user",prompt:contentText(first?.content,""),images},messages);
                        if (signal.aborted || this._agentRunAbortRequested) return undefined;
                        return prepared;
                    }, result => {
                        admitted = true;
                        // Admission/auth is complete. Context hooks and normal
                        // pre-run compaction have their own lifecycle, not this deadline.
                        clearTimeout(admissionTimer);
                        resolveAdmission(this._decoratePendingQueue(result));
                    }, () => !reservation.abort.signal.aborted && !this._agentRunAbortRequested &&
                        this._queuedResume === reservation && controlGeneration === this._queueControlGeneration,
                    () => {
                        started = true;
                        this._queuedResume = undefined;
                        this.agent.notifyPendingQueue();
                    });
                if (!admitted) {resolveAdmission(this._decoratePendingQueue(result));return;}
                if (!started) return; // aborted/changed preparation never wakes a generic continuation
                // Core's initial queue run finished. Existing Session recovery,
                // before-settle, retries and final cleanup remain the sole owner.
                driven = true;
                await this._driveOwnedRun(async () => {});
            } catch(error) {
                if (!admitted) rejectAdmission(error);
                else this._emit({type:"queue_resume_error",message:String(error)});
            } finally {
                clearTimeout(admissionTimer);
                if (this._queuedResume === reservation) this._queuedResume = undefined;
                if (!driven && this._isAgentRunActive) {
                    if (!started) this._agentRunAbortRequested = true;
                    this._failedResponse = undefined;
                    this._runSystemPromptOptions = undefined;
                    await this._emitAgentSettled();
                }
            }
        })();
        // The promise is retained/observed by the Session and RPC shutdown waits
        // for waitForIdle. This is not a wake-on-settle background task.
        void reservation.done.catch(error => this._emit({type:"queue_resume_error",message:String(error)}));
        return admission;
    }`);
 });
 await patch(app+'core/agent-session.d.ts',(change)=>{
  change('    type: "queue_update";', '    type: "queue_resume_start";\n} | {\n    type: "queue_update";');
  change('promoteQueuedMessage(epoch: string, queueId: string, expectedRevision: number, controlGeneration: number): import("@earendil-works/pi-agent-core").PendingPromotionResult;', 'promoteQueuedMessage(epoch: string, queueId: string, expectedRevision: number, controlGeneration: number): Promise<import("@earendil-works/pi-agent-core").PendingPromotionResult>;');
 });
 await patch(app+'modes/rpc/rpc-mode.js',(change)=>{
  change('                return success(id, "promote_queued_message", session.promoteQueuedMessage(command.epoch, command.queueId, command.expectedRevision, command.controlGeneration));',`                activeCommands++;
                try {
                    return success(id, "promote_queued_message", await session.promoteQueuedMessage(command.epoch, command.queueId, command.expectedRevision, command.controlGeneration));
                } finally {
                    void session.waitForIdle().finally(() => { activeCommands--; checkShutdownRequested(); });
                }`);
 });
}
