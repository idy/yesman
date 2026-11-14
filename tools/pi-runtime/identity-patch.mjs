// Extend the exact reviewed pi patch with host-only occurrence provenance.
// All lookups are batch positions or session entry IDs; never message contents.
export async function identityPatch(patch, core, app) {
 await patch(core+'agent.js',(change)=>{
  change('import { PendingMessageQueue, PendingQueueRegistry } from "./pending-queue.js";', `import { PendingMessageQueue, PendingQueueRegistry, copyInputRefs, appendInputMessage } from "./pending-queue.js";
export { bindInputTrigger, triggerIdentity, bindInputBatch, copyInputRefs, joinInputBatches, inputRefAt, recordedInputRef, hostInputRefs, bindProjectedInputs, recordedInputs, persistInputIdentity, publicInputEntry, INPUT_ENTRY } from "./pending-queue.js";`);
  change('let messages = initialState?.messages?.slice() ?? [];','let messages = copyInputRefs(initialState?.messages ?? [], initialState?.messages?.slice() ?? []);');
  change('    if (messages[0]?.role !== "system" && initialMessage)\n        messages.unshift(initialMessage);','    if (messages[0]?.role !== "system" && initialMessage) {\n        messages.unshift(initialMessage);\n        copyInputRefs(messages, messages, 0);\n    }');
  change('            messages = nextMessages.slice();','            messages = copyInputRefs(nextMessages, nextMessages.slice());');
  change('                this._state.messages.push(event.message);','                event = { ...event, inputPosition: this._state.messages.length };\n                appendInputMessage(this._state.messages, event.message, event.inputIdentity);');
 });
 await patch(core+'agent-loop.js',(change)=>{
  change('import { getDefaultStreamFn } from "./stream-fn.js";','import { getDefaultStreamFn } from "./stream-fn.js";\nimport { copyInputRefs, joinInputBatches, inputRefAt } from "./pending-queue.js";');
  change('    for (const message of initialMessages) {\n        await emit({ type: "message_start", message });\n        await emit({ type: "message_end", message });','    for (const [index, message] of initialMessages.entries()) {\n        const inputIdentity = inputRefAt(initialMessages, index);\n        await emit({ type: "message_start", message, inputIdentity });\n        await emit({ type: "message_end", message, inputIdentity });');
  change('            for (const message of declareToolChanges(currentContext, [...preparedMessages, ...pendingMessages])) {\n                await emit({ type: "message_start", message });\n                await emit({ type: "message_end", message });','            const inputs = declareToolChanges(currentContext, joinInputBatches(preparedMessages, pendingMessages));\n            for (const [index, message] of inputs.entries()) {\n                const inputIdentity = inputRefAt(inputs, index);\n                await emit({ type: "message_start", message, inputIdentity });\n                await emit({ type: "message_end", message, inputIdentity });');
  change('        return baseline.map((message, index) => (index === systemIndex ? withToolChanges(pending, changes) : message));','        return copyInputRefs(pendingMessages, baseline.map((message, index) => (index === systemIndex ? withToolChanges(pending, changes) : message)));');
  change('    return [...pendingMessages.slice(0, index), update, ...pendingMessages.slice(index)];','    return copyInputRefs(pendingMessages, [...pendingMessages.slice(0, index), update, ...pendingMessages.slice(index)], index);');
 });
 await patch(core+'agent.d.ts',(change)=>{
  change('export interface PendingQueueItem {',`export interface HostInputIdentity { submissionId?: string; origin?: { source: "user"|"lead"|"yesman"; kind?: string; worker?: string }; epoch?: string; queueId?: string; }
export interface HostInputRef { native: true; identity: HostInputIdentity|null; entryId?: string; }
export declare function bindInputTrigger<T extends object>(trigger:T,metadata:unknown):T;
export declare function triggerIdentity(trigger:object):HostInputIdentity|undefined;
export declare function bindInputBatch<T>(messages:T[],identities:(HostInputIdentity|undefined|null)[]):T[];
export declare function copyInputRefs<T>(source:T[],target:T[],insert?:number):T[];
export declare function joinInputBatches<T>(...batches:T[][]):T[];
export declare function inputRefAt<T>(messages:T[],index:number):HostInputIdentity|null;
export declare function recordedInputRef<T>(messages:T[],index:number,entryId:string):void;
export declare function hostInputRefs<T>(messages:T[]):(HostInputRef|null)[];
export declare function bindProjectedInputs(projection:any,branch:any[]):any[];
export declare function recordedInputs(branch:any[]):Map<string,HostInputRef>;
export declare function persistInputIdentity(identity:HostInputIdentity|null):HostInputIdentity|null;
export declare function publicInputEntry(entry:any):any;
export declare const INPUT_ENTRY: string;
export interface PendingQueueItem {`);
  change('capability: 2;','capability: 3;');
  // Public callers may supply origin in invocation metadata, never on a message.
  change('steer(message: AgentMessage, metadata?: { submissionId?: string }): string;', 'steer(message: AgentMessage, metadata?: { submissionId?: string; hostOrigin?: HostInputIdentity["origin"] }): string;');
  change('followUp(message: AgentMessage, metadata?: { submissionId?: string }): string;', 'followUp(message: AgentMessage, metadata?: { submissionId?: string; hostOrigin?: HostInputIdentity["origin"] }): string;');
 });
 await patch(core+'types.d.ts',(change)=>{
  for(const type of ['message_start','message_end'])change(`    type: "${type}";\n    message: AgentMessage;`,`    type: "${type}";\n    /** Host-only occurrence envelope, never an AgentMessage field. */\n    inputIdentity?: import("./agent.js").HostInputIdentity|null;\n    inputPosition?: number;\n    hostSession?: string;\n    message: AgentMessage;`);
 });
 await patch(app+'core/agent-session.js',(change)=>{
  change('import { runToolCall, } from "@earendil-works/pi-agent-core";', 'import { runToolCall, bindInputTrigger, triggerIdentity, bindInputBatch, copyInputRefs, joinInputBatches, inputRefAt, recordedInputRef, hostInputRefs, bindProjectedInputs, recordedInputs, persistInputIdentity, publicInputEntry, INPUT_ENTRY } from "@earendil-works/pi-agent-core";');
  change('            contextEntries: projection.entries,','            contextEntries: projection.entries.map(e => ({...e,sourceEntry:publicInputEntry(e.sourceEntry)})),');
  change('                    branchEntries: pathEntries,\n                    customInstructions,','                    branchEntries: pathEntries.map(publicInputEntry),\n                    customInstructions,');
  change('                    branchEntries: pathEntries,\n                    customInstructions: undefined,','                    branchEntries: pathEntries.map(publicInputEntry),\n                    customInstructions: undefined,');
  change('            entriesToSummarize,\n            userWantsSummary:', '            entriesToSummarize: entriesToSummarize.map(publicInputEntry),\n            userWantsSummary:');
  change('        this.agent.state.messages = projection.messages;','        this.agent.state.messages = bindProjectedInputs(projection, this.sessionManager.getBranch());');
  change('    _handleAgentEvent = async (event) => {','    _handleAgentEvent = async (event) => {\n        const inputMessages = this.agent.state.messages;');
  change('this._emit(event.type === "agent_end" ? { ...event, willRetry: this._willRetryAfterAgentEnd(event) } : event);','this._emit(event.type === "agent_end" ? { ...event, willRetry: this._willRetryAfterAgentEnd(event) } : { ...event, hostSession: this.sessionId });');
  change('            if (entryId)\n                this._entryIdsByMessage.set(event.message, entryId);', `            if (entryId) {
                this._entryIdsByMessage.set(event.message, entryId);
                if (event.message.role === "user" && Number.isInteger(event.inputPosition)) {
                    const identity = persistInputIdentity(event.inputIdentity);
                    this.sessionManager.appendCustomEntry(INPUT_ENTRY, { entryId, identity });
                    recordedInputRef(inputMessages, event.inputPosition, entryId);
                    this._emit({ type: "input_recorded", hostSession: this.sessionId, entryId, inputIdentity: identity });
                }
            }`);
  change('    /** Current steering mode */', `    getHostMessages() {
        const branch = this.sessionManager.getBranch();
        const messages = this.isStreaming ? this.messages : bindProjectedInputs(this.sessionManager.buildSessionProjection(), branch);
        const recorded = recordedInputs(branch);
        const inputRefs = hostInputRefs(messages).map((ref,index) => {
            if (ref?.native || messages[index]?.role !== "user") return ref;
            const mapped = this._entryIdsByMessage.get(messages[index]);
            if (mapped) return recorded.get(mapped) ?? null;
            const entries = branch.filter(e=>e.type==="message" && e.message === messages[index]);
            return entries.length === 1 ? recorded.get(entries[0].id) ?? null : null;
        });
        return { messages, inputRefs, hostSession: this.sessionId,
            recordedInputs: [...recorded.values()].filter(r => r.identity?.submissionId).slice(-256) };
    }
    /** Current steering mode */`);
  change('        return { messageType: "user", prompt: expandedText, images: currentImages };','        return bindInputTrigger({ messageType: "user", prompt: expandedText, images: currentImages }, options);');
  change('        return messages;\n    }\n    async _handlePostAgentRun()', '        return bindInputBatch(messages, messages.map((_, index) => index === (updateMessage ? 1 : 0) ? triggerIdentity(trigger) : undefined));\n    }\n    async _handlePostAgentRun()');
  change('await this._queueFollowUp(expandedText, currentImages, options?.submissionId);','await this._queueFollowUp(expandedText, currentImages, options?.submissionId, options?.hostOrigin);');
  change('await this._queueSteer(expandedText, currentImages, options?.submissionId);','await this._queueSteer(expandedText, currentImages, options?.submissionId, options?.hostOrigin);');
  change('async _queueUserInput(text, images, behavior, source, submissionId)', 'async _queueUserInput(text, images, behavior, source, submissionId, hostOrigin)');
  change('await this._queueSteer(expandedText, processedInput.images, submissionId);','await this._queueSteer(expandedText, processedInput.images, submissionId, hostOrigin);');
  change('await this._queueFollowUp(expandedText, processedInput.images, submissionId);','await this._queueFollowUp(expandedText, processedInput.images, submissionId, hostOrigin);');
  change('"steer", options?.source ?? "interactive", options?.submissionId)', '"steer", options?.source ?? "interactive", options?.submissionId, options?.hostOrigin)');
  change('"followUp", options?.source ?? "interactive", options?.submissionId)', '"followUp", options?.source ?? "interactive", options?.submissionId, options?.hostOrigin)');
  change('async _queueSteer(text, images, submissionId)', 'async _queueSteer(text, images, submissionId, hostOrigin)');
  change('async _queueFollowUp(text, images, submissionId)', 'async _queueFollowUp(text, images, submissionId, hostOrigin)');
  change('return this.agent.steer(message, { submissionId });','return this.agent.steer(message, { submissionId, hostOrigin });');
  change('return this.agent.followUp(message, { submissionId });','return this.agent.followUp(message, { submissionId, hostOrigin });');
 });
 await patch(app+'core/agent-session.d.ts',(change)=>{
  change('    type: "agent_settled";', '    type: "input_recorded";\n    hostSession: string; entryId: string; inputIdentity: import("@earendil-works/pi-agent-core").HostInputIdentity|null;\n} | {\n    type: "queue_resume_error"; message: string;\n} | {\n    type: "agent_settled";');
  change('export interface PromptOptions {','export interface PromptOptions {\n    hostOrigin?: import("@earendil-works/pi-agent-core").HostInputIdentity["origin"];');
  for(const name of ['steer','followUp'])change(`    ${name}(text: string, images?: ImageContent[], options?: {`,`    ${name}(text: string, images?: ImageContent[], options?: {\n        hostOrigin?: import("@earendil-works/pi-agent-core").HostInputIdentity["origin"];`);
  change('    getPendingQueue():', '    getHostMessages(): { messages: AgentMessage[]; inputRefs: (import("@earendil-works/pi-agent-core").HostInputRef|null)[]; hostSession: string; recordedInputs: import("@earendil-works/pi-agent-core").HostInputRef[] };\n    getPendingQueue():');
 });
 await patch(app+'modes/rpc/rpc-mode.js',(change)=>{
  change('submissionId: command.submissionId,','submissionId: command.submissionId,\n                    hostOrigin: command.hostOrigin,');
  change('session.steer(command.message, command.images, { source: "rpc", submissionId: command.submissionId })','session.steer(command.message, command.images, { source: "rpc", submissionId: command.submissionId, hostOrigin: command.hostOrigin })');
  change('session.followUp(command.message, command.images, { source: "rpc", submissionId: command.submissionId })','session.followUp(command.message, command.images, { source: "rpc", submissionId: command.submissionId, hostOrigin: command.hostOrigin })');
  change('return success(id, "get_messages", { messages: session.messages });','return success(id, "get_messages", session.getHostMessages());');
  change('runtime: "yesman-pi/1.5.2-2"','runtime: "yesman-pi/1.5.2-3"');
 });
 await patch(app+'modes/rpc/rpc-client.js',(change)=>{
  change('    async getMessages() {','    async getHostMessages() { return this.getData(await this.send({ type: "get_messages" })); }\n    async getMessages() {');
 });
 await patch(app+'modes/rpc/rpc-client.d.ts',(change)=>{
  change('    getMessages(): Promise<AgentMessage[]>;','    getMessages(): Promise<AgentMessage[]>;\n    getHostMessages(): Promise<ReturnType<import("../../core/agent-session.js").AgentSession["getHostMessages"]>>;');
 });
 await patch(app+'modes/rpc/rpc-types.d.ts',(change)=>{
  change('    command: "get_messages";\n    success: true;\n    data: {\n        messages: AgentMessage[];\n    };', '    command: "get_messages";\n    success: true;\n    data: {\n        messages: AgentMessage[];\n        inputRefs?: (import("@earendil-works/pi-agent-core").HostInputRef|null)[];\n        recordedInputs?: import("@earendil-works/pi-agent-core").HostInputRef[];\n        hostSession?: string;\n    };');
  for(const name of ['prompt','steer','follow_up'])change(`    type: "${name}";\n    submissionId?: string;`,`    type: "${name}";\n    hostOrigin?: import("@earendil-works/pi-agent-core").HostInputIdentity["origin"];\n    submissionId?: string;`);
 });
}
