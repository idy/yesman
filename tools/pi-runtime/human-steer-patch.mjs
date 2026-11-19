// Opt-in Yes Man human prompt policy. Keep AgentSession's command, input,
// resource expansion and delivery-time image paths, and leave Agent polls alone.
export async function humanSteerPatch(patch, core, app) {
    await patch(core + 'agent.js', change => {
        change('    getPendingQueue() { return this.pendingQueue.snapshot(); }', `    getPendingQueue() { return this.pendingQueue.snapshot(); }
    steerHuman(message, metadata, original, registerImages) {
        return this.pendingQueue.steerHuman(message, metadata, original, registerImages);
    }`);
    });
    await patch(core + 'agent.d.ts', change => {
        change('appendAllowed?: boolean; }', 'appendAllowed?: boolean; humanSteer?: true; }');
    });
    await patch(app + 'core/agent-session.js', change => {
        change(`        if (this._isEmittingAgentSettled) {
            this._deferredSettledActions.push(async () => await this.prompt(text, options));`, `        if (this._isEmittingAgentSettled) {
            if (options?.humanSteer) {
                // Preserve the real preflight result even when settlement defers it.
                return await new Promise((resolve, reject) => {
                    this._deferredSettledActions.push(() => this.prompt(text, options).then(resolve, reject));
                });
            }
            this._deferredSettledActions.push(async () => await this.prompt(text, options));`);
        change('        // Handle extension commands first (execute immediately, even during streaming)', `        this._checkHumanSteer(options?.humanSteer);
        // Handle extension commands first (execute immediately, even during streaming)`);
        change('        if (trigger)\n            await this._startRun(trigger, () => preflightResult?.("started"));', `        if (trigger) {
            this._checkHumanSteer(options?.humanSteer);
            this._checkIdleHumanSteer(options?.humanSteer);
            await this._startRun(trigger, () => preflightResult?.("started"));
        }`);
        change('    async _preparePrompt(text, options) {', `    _checkHumanSteer(human) {
        if (human && (human.controlGeneration !== this._queueControlGeneration ||
            human.epoch !== this.agent.pendingQueue.epoch || this.isStreaming && this._agentRunAbortRequested))
            throw new Error("The run was stopped. Send the draft again to continue.");
    }
    _checkIdleHumanSteer(human) {
        if (human && !this.isStreaming && this.agent.pendingQueue.steeringHuman())
            throw new Error("Recall the waiting message before sending this draft.");
    }
    async _preparePrompt(text, options) {`);
        change('        // Emit input event for extension interception (before skill/template expansion)', `        const human = options?.humanSteer;
        this._checkHumanSteer(human);
        // Emit input event for extension interception (before skill/template expansion)`);
        change('        // If streaming, queue via steer() or followUp() based on option', `        this._checkHumanSteer(human);
        this._checkIdleHumanSteer(human);
        // If streaming, queue via steer() or followUp() based on option`);
        change(`                : await this._queueSteer(expandedText, currentImages, options?.submissionId, options?.hostOrigin, options?.appendTo);
            preflightResult?.("queued", options?.appendTo ? queued : undefined);`, `                : await this._queueSteer(expandedText, currentImages, options?.submissionId, options?.hostOrigin, options?.appendTo,
                    human && { imageSizes: human.imageSizes, joinable: human.joinable && !currentText.trimStart().startsWith("/") });
            preflightResult?.("queued", (options?.appendTo || human) && typeof queued === "object" ? queued : undefined);`);
        change('    async _queueSteer(text, images, submissionId, hostOrigin, appendTo) {', '    async _queueSteer(text, images, submissionId, hostOrigin, appendTo, human) {');
        change(`        if (appendTo) return this._appendQueued("steer", message, { submissionId, hostOrigin }, appendTo);`, `        if (human) return this.agent.steerHuman(message, { submissionId, hostOrigin }, human, queued => {
            if (queued.content.length > 1) this._queuedMessagesWithImages.add(queued);
        });
        if (appendTo) return this._appendQueued("steer", message, { submissionId, hostOrigin }, appendTo);`);
    });
    await patch(app + 'modes/rpc/rpc-mode.js', change => {
        change('    let activeCommands = 0;', '    let activeCommands = 0;\n    let humanPreflight = Promise.resolve();');
        change('            case "abort": {\n                await session.abort();', `            case "abort": {
                // Abort increments the Session generation before awaiting idle.
                // New admissions need not wait for input hooks from that generation.
                const stopped = session.abort();
                humanPreflight = Promise.resolve();
                await stopped;`);
        change(`                void session
                    .prompt(command.message, {`, `                const promptSession = session;
                const human = command.humanSteer;
                let prior = Promise.resolve(), release = () => {};
                if (human !== undefined) {
                    if (!human || typeof human.joinable !== "boolean" || typeof human.epoch !== "string" ||
                        !Number.isSafeInteger(human.controlGeneration) || !/^[a-f0-9]{48}$/.test(command.submissionId ?? "") ||
                        command.streamingBehavior !== "steer" || command.hostOrigin?.source !== "user" || target !== undefined) {
                        activeCommands--;
                        return error(id, "prompt", "Invalid human steering input");
                    }
                    prior = humanPreflight;
                    humanPreflight = new Promise(resolve => { release = resolve; });
                }
                void (async () => {
                    if (human) {
                        await prior;
                        if (session !== promptSession) throw new Error("The session changed. Reopen the conversation before sending.");
                    }
                    return promptSession.prompt(command.message, {`);
        change('                    source: "rpc",\n                    preflightResult:', `                    source: "rpc",
                    humanSteer: human && { ...human, imageSizes: (command.images ?? []).map(image => {
                        const data = image.data;
                        return Math.floor(data.length * 3 / 4) - (data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0);
                    }) },
                    preflightResult:`);
        change('                        preflightSucceeded = true;\n                        output(success(id, "prompt", append ? { disposition, append } : { disposition }));\n                    },\n                })', `                        preflightSucceeded = true;
                        release();
                        output(success(id, "prompt", append ? { disposition, append } : { disposition }));
                    },
                    });
                })()`);
        change('                    .finally(() => {\n                    activeCommands--;', '                    .finally(() => {\n                    release();\n                    activeCommands--;');
    });
    await patch(app + 'modes/rpc/rpc-types.d.ts', change => {
        change('    type: "prompt";\n    appendTo?:', '    type: "prompt";\n    humanSteer?: { joinable: boolean; epoch: string; controlGeneration: number };\n    appendTo?:');
    });
}
