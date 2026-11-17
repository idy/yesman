// Real managed Agent loop: promotion cannot preempt a provider response/tool batch.
import assert from 'node:assert/strict';
import {resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
const root=process.env.YESMAN_TEST_RUNTIME;if(!root)throw Error('Set YESMAN_TEST_RUNTIME');
const {Agent}=await import(pathToFileURL(resolve(root,'node_modules/@earendil-works/pi-agent-core/dist/index.js')));
const {createAssistantMessageEventStream}=await import(pathToFileURL(resolve(root,'node_modules/@earendil-works/pi-ai/dist/index.js')));
const model={id:'fixture',name:'Fixture',api:'fixture',provider:'fixture',baseUrl:'',input:['text'],contextWindow:10000,maxTokens:100,cost:{input:0,output:0,cacheRead:0,cacheWrite:0}};
const usage={input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
const user=text=>({role:'user',content:[{type:'text',text}],timestamp:1});
async function until(f){const until=Date.now()+5000;while(!f()){if(Date.now()>until)throw Error('checkpoint deadline');await new Promise(r=>setTimeout(r,2))}}
for(const toolExecution of ['sequential','parallel']){
 let finishProvider,providerCalls=0;const started=new Set(),finished=[],releases=new Map(),seen=[],taken=[];
 const tools=[{name:'gate',description:'fixture',parameters:{type:'object',properties:{n:{type:'number'}},required:['n']},execute:async(_id,{n},signal)=>{
  started.add(n);await new Promise(r=>{releases.set(n,r);if(signal?.aborted)r();else signal?.addEventListener('abort',r,{once:true})});finished.push(n);return {content:[{type:'text',text:'finished'}],details:null};
 }}];
 const agent=new Agent({initialState:{model,tools},toolExecution,steeringMode:'one-at-a-time',followUpMode:'one-at-a-time',streamFn:()=>{
  const stream=createAssistantMessageEventStream(),first=++providerCalls===1;
  const message={role:'assistant',content:first?[1,2].map(n=>({type:'toolCall',id:`t${n}`,name:'gate',arguments:{n}})):[{type:'text',text:'done'}],api:'fixture',provider:'fixture',model:'fixture',usage,stopReason:first?'toolUse':'stop',timestamp:2};
  const finish=()=>{stream.push({type:'done',reason:message.stopReason,message});stream.end()};
  if(first)finishProvider=finish;else queueMicrotask(finish);return stream;
 }});
 agent.subscribe(e=>{if(e.type==='message_start'&&e.message.role==='user')seen.push(e.message.content[0].text)});
 agent.subscribePendingQueue(e=>taken.push(...e.changes.filter(c=>c.state==='taken').map(c=>c.queueId)));
 const run=agent.prompt(user('start'));
 try{
  await until(()=>finishProvider);
  const s1=agent.steer(user('older')),a=agent.followUp(user('A')),b=agent.followUp(user('B')),c=agent.followUp(user('C')),s2=agent.steer(user('later'));
  const q=agent.getPendingQueue();assert.equal(agent.promoteQueuedMessage(q.epoch,b,0).outcome,'promoted');
  assert.deepEqual(agent.getPendingQueue().steering,['older','later','B']);assert.deepEqual(agent.getPendingQueue().followUp,['A','C']);
  assert.deepEqual(seen,['start']);assert.equal(providerCalls,1,'provider was interrupted/replaced');
  finishProvider();await until(()=>started.has(1));
  if(toolExecution==='parallel')await until(()=>started.has(2));
  releases.get(1)();await until(()=>started.has(2)&&finished.includes(1));
  assert.deepEqual(seen,['start'],'input entered between tools');assert.equal(providerCalls,1);
  releases.get(2)();await run;
  assert.deepEqual(finished.sort(),[1,2]);assert.deepEqual(seen,['start','older','later','B','A','C']);
  assert.deepEqual(taken,[s1,s2,b,a,c]);assert.equal(providerCalls,6);
  console.log(`${toolExecution}: pending provider and whole tool batch preserved; FIFO promotion handled once at real checkpoint PASS`);
 }finally{finishProvider?.();for(const release of releases.values())release();agent.abort();await run;}
}
