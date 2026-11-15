// Type-check additive SDK/RPC contracts against the prepared managed runtime.
// YESMAN_TEST_TSC may point at a pinned TypeScript compiler outside this repo.
import { mkdtemp, writeFile, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import assert from 'node:assert/strict';
const root=process.env.YESMAN_TEST_RUNTIME;if(!root)throw Error('Prepare YESMAN_TEST_RUNTIME first');
const temp=await mkdtemp(`${tmpdir()}/yesman-sdk-types-`);
try {
 await symlink(resolve(root,'node_modules'),resolve(temp,'node_modules'),'junction');
 await writeFile(resolve(temp,'consumer.mts'),`
import { Agent, type PendingQueueSnapshot, type PendingQueueResult } from '@earendil-works/pi-agent-core';
import { type AgentSession, type PromptOptions, RpcClient } from '@earendil-works/pi-coding-agent';
import type { RpcCommand, RpcResponse } from './node_modules/@earendil-works/pi-coding-agent/dist/modes/rpc/rpc-types.js';
import type { UserMessage } from '@earendil-works/pi-ai';
declare const agent: Agent, session: AgentSession, client: RpcClient, message: UserMessage;
const options: PromptOptions = {submissionId: 'a'.repeat(48), streamingBehavior: 'steer'};
const handle: string = agent.steer(message, {submissionId: options.submissionId});
const snapshot: PendingQueueSnapshot = session.getPendingQueue();
const result: PendingQueueResult = session.cancelQueuedMessage(snapshot.epoch, handle);
agent.subscribePendingQueue(event => { const state: 'pending'|'taken'|'cancelled'|'cleared'|'appended'|'preparing' = event.changes[0].state; const appendedId: string|undefined = event.changes[0].appendedSubmissionId; });
await session.steer('same', undefined, {submissionId: options.submissionId});
await session.followUp('same', undefined, {submissionId: options.submissionId});
await client.prompt('same', undefined, 'steer', options.submissionId);
await client.steer('same', undefined, options.submissionId);
await client.followUp('same', undefined, options.submissionId);
const wire: RpcCommand = {type:'cancel_queued_message',epoch:snapshot.epoch,queueId:handle};
const reply: RpcResponse = {type:'response',command:'cancel_queued_message',success:true,data:result};
const pending: PendingQueueSnapshot = await client.getPendingQueue();
const cancelled: PendingQueueResult = await client.cancelQueuedMessage(pending.epoch, handle);
const promoted = await client.promoteQueuedMessage(pending.epoch, handle, pending.items[0].itemRevision, pending.controlGeneration!);
const promotion: import('@earendil-works/pi-agent-core').PendingPromotionResult = agent.promoteQueuedMessage(snapshot.epoch, handle, snapshot.items[0].itemRevision);
const host = await client.getHostMessages();
const refs: (import('@earendil-works/pi-agent-core').HostInputRef|null)[] = host.inputRefs;
const ownHost = session.getHostMessages();
const sessionPromotion = session.promoteQueuedMessage(snapshot.epoch, handle, 0, snapshot.controlGeneration!);
const promoteWire: RpcCommand = {type:'promote_queued_message',epoch:snapshot.epoch,queueId:handle,expectedRevision:0,controlGeneration:0};
const capability: 4 = snapshot.capability;
const segments: string[]|undefined = snapshot.items[0].segments, preparing: boolean|undefined = snapshot.items[0].preparing, appendAllowed: boolean|undefined = snapshot.appendAllowed;
const target: import('@earendil-works/pi-agent-core').PendingAppendTarget = {epoch:snapshot.epoch,queueId:handle,expectedRevision:0,controlGeneration:snapshot.controlGeneration!};
const appendOptions: PromptOptions = {submissionId:'b'.repeat(48),streamingBehavior:'steer',appendTo:target,hostOrigin:{source:'user'}};
const appended: import('@earendil-works/pi-agent-core').PendingAppendResult = agent.appendQueuedMessage(target,'steer',message,{submissionId:'b'.repeat(48)});
const viaRpc = await client.promptAppend('more',undefined,'steer','b'.repeat(48),target);
const outcome: import('@earendil-works/pi-agent-core').PendingAppendResult['outcome']|undefined = viaRpc.append?.outcome;
const appendWire: RpcCommand = {type:'prompt',message:'more',streamingBehavior:'steer',submissionId:'b'.repeat(48),appendTo:target};
const appendReply: RpcResponse = {type:'response',command:'prompt',success:true,data:{disposition:'queued',append:{outcome:'preparing',queueId:handle,itemRevision:0,enqueued:true}}};
const identitySegments: string[]|undefined = ({} as import('@earendil-works/pi-agent-core').HostInputIdentity).segments;
// @ts-expect-error Queue identity is not part of a model message.
message.queueId = handle;
`);
 const compiler=process.env.YESMAN_TEST_TSC;
 const cmd=compiler?process.execPath:'tsc',args=[...(compiler?[compiler]:[]),'--strict','--noEmit','--module','nodenext','--target','es2022','--skipLibCheck',resolve(temp,'consumer.mts')];
 const p=spawnSync(cmd,args,{encoding:'utf8',timeout:30000});
 assert.equal(p.status,0,p.stdout+p.stderr+(p.error||''));
 console.log('PASS: managed Agent/AgentSession/RpcClient/RpcCommand/Response types; queue identity excluded from UserMessage');
} finally {await rm(temp,{recursive:true,force:true});}
