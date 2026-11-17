// Actual patched SDK lifecycle at the pre-intake and committed-intake boundary.
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
const root=process.env.YESMAN_TEST_RUNTIME;if(!root)throw Error('Prepare YESMAN_TEST_RUNTIME');
const {Agent,hostInputRefs}=await import(pathToFileURL(resolve(root,'node_modules/@earendil-works/pi-agent-core/dist/index.js')));
const model={id:'local',name:'Local',api:'local',provider:'local',baseUrl:'',input:['text','image'],contextWindow:1000,maxTokens:10,cost:{input:0,output:0,cacheRead:0,cacheWrite:0}};
const input={role:'user',timestamp:1,content:[{type:'text',text:' original 中文\n '},{type:'image',mimeType:'image/png',data:'original-A'},{type:'image',mimeType:'image/png',data:'original-B'}]};
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return{promise,resolve}};
for(const mode of ['all','one-at-a-time'])for(const scenario of ['stop','cancel','error','late-enqueue','committed-stop']){
 let calls=0;const agent=new Agent({initialState:{model},steeringMode:mode,streamFn:(_model,_context,options)=>{calls++;assert.equal(options.signal.aborted,true,'committed Stop reached an unaborted stream');throw Error('aborted local stream; no provider request')}});
 const events=[],changes=[];agent.subscribe(e=>events.push(e));agent.subscribePendingQueue(e=>changes.push(...e.changes));
 const id='a'.repeat(48),original=structuredClone(input),handle=agent.followUp(original,{submissionId:id});const q=agent.getPendingQueue();
 const entered=deferred(),release=deferred();let staged;
 const run=agent.runQueuedInput(q.epoch,handle,0,async messages=>{staged=messages;entered.resolve();await release.promise;if(scenario==='error')throw Error('prepare failed');return{messages,commit:()=>true}},()=>{},()=>true,()=>{if(scenario==='committed-stop')agent.abort()});
 // Observe rejection from the outset; errors must retain pending originals.
 const finished=run.then(result=>({result}),error=>({error}));
 await entered.promise;
 assert.equal(agent.getPendingQueue().items[0].queueId,handle);assert.equal(agent.getPendingQueue().items[0].kind,'steer');assert.equal(changes.some(c=>c.state==='taken'),false);
 if(scenario==='stop'||scenario==='late-enqueue'){
  staged[0].content[1].data='prepare mutated its copy';
  if(scenario==='late-enqueue')agent.steer(structuredClone(input),{submissionId:'b'.repeat(48)});
  agent.abort();
 }else if(scenario==='cancel')assert.equal(agent.cancelQueuedMessage(q.epoch,handle,1).outcome,'cancelled');
 release.resolve();const outcome=await finished;
 assert.equal(calls,scenario==='committed-stop'?1:0);
 if(scenario==='committed-stop'){
  assert.equal(outcome.error,undefined);assert.equal(agent.getPendingQueue().items.length,0);
  assert.equal(agent.state.messages.filter(m=>m.role==='user').length,1);
  assert.deepEqual(agent.state.messages.find(m=>m.role==='user'),input);
  assert.equal(hostInputRefs(agent.state.messages).filter(r=>r?.identity?.submissionId===id).length,1);
  assert.equal(changes.filter(c=>c.state==='taken'&&c.queueId===handle).length,1);
  assert.equal(agent.cancelQueuedMessage(q.epoch,handle,1).outcome,'not_pending');
 }else{
  assert.equal(events.filter(e=>e.type==='agent_start').length,0);assert.equal(agent.state.messages.length,0);
  assert.equal(changes.some(c=>c.state==='taken'),false);
  if(scenario==='error')assert.match(String(outcome.error),/prepare failed/);else assert.equal(outcome.error,undefined);
  const pending=agent.getPendingQueue();assert.equal(pending.items.length,scenario==='cancel'?0:scenario==='late-enqueue'?2:1);
  if(scenario!=='cancel'){
   assert.equal(pending.items[0].queueId,handle);assert.deepEqual(original,input);
   assert.equal(agent.cancelQueuedMessage(q.epoch,handle,1).outcome,'cancelled');
  }
 }
 // Private IDs may exist on host event envelopes, but never on actual messages.
 for(const event of events)if(event.message)assert(!JSON.stringify(event.message).includes(id));
 console.log(mode,scenario,'pending until commit; original payload / no fake turn / committed history PASS');
}
