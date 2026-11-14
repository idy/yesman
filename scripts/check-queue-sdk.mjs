// Actual installed managed SDK, without a loader patch or private-field access.
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
const root=process.env.YESMAN_TEST_RUNTIME;
if(!root)throw Error('Prepare YESMAN_TEST_RUNTIME first');
const {Agent}=await import(pathToFileURL(resolve(root,'node_modules/@earendil-works/pi-agent-core/dist/index.js')));
const {createAssistantMessageEventStream}=await import(pathToFileURL(resolve(root,'node_modules/@earendil-works/pi-ai/dist/index.js')));
const model={id:'fixture',name:'Fixture',api:'fixture',provider:'fixture',baseUrl:'',input:['text'],contextWindow:1000,maxTokens:10,cost:{input:0,output:0,cacheRead:0,cacheWrite:0}};
const usage={input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
const user=()=>({role:'user',timestamp:1,content:[{type:'text',text:'same'},{type:'image',mimeType:'image/png',data:'identical'}]});
for(const mode of ['all','one-at-a-time']){
 const seen=[],events=[],requests=[];
 const agent=new Agent({initialState:{model},steeringMode:mode,followUpMode:mode,streamFn:(_m,ctx)=>{
  requests.push(structuredClone(ctx.messages));const stream=createAssistantMessageEventStream();
  const message={role:'assistant',content:[{type:'text',text:'done'}],api:'fixture',provider:'fixture',model:'fixture',usage,stopReason:'stop',timestamp:2};
  queueMicrotask(()=>{stream.push({type:'done',reason:'stop',message});stream.end()});return stream;
 }});
 agent.subscribe(e=>{if(e.type==='message_start'&&e.message.role==='user')seen.push(e.message)});
 agent.subscribePendingQueue(e=>events.push(e));
 await agent.prompt(user());seen.length=0;requests.length=0;
 const same=user(),handles=[];
 for(let n=0;n<100;n++)handles.push({id:agent[n%2?'followUp':'steer'](same),kind:n%2?'followUp':'steer'});
 const epoch=agent.getPendingQueue().epoch;
 assert.equal(new Set(handles.map(h=>h.id)).size,100);
 const survivors=[];
 for(let n=0;n<100;n++){
  const h=handles[n];if(n%3===1){assert.equal(agent.cancelQueuedMessage(epoch,h.id).outcome,'cancelled');assert.equal(agent.cancelQueuedMessage(epoch,h.id).outcome,'cancelled');}
  else survivors.push(h);
 }
 assert.deepEqual(agent.getPendingQueue().items.map(i=>i.queueId),survivors.map(h=>h.id));
 assert.equal(agent.cancelQueuedMessage('old',survivors[0].id).outcome,'stale');
 assert.equal(agent.cancelQueuedMessage(epoch,'unknown').outcome,'not_pending');
 let cancelAtStart;
 agent.subscribe(e=>{if(e.type==='message_start'&&e.message===same&&cancelAtStart===undefined){cancelAtStart=agent.cancelQueuedMessage(epoch,survivors[0].id).outcome}});
 await agent.continue();
 assert.equal(seen.length,survivors.length);assert(seen.every(m=>m===same));
 assert.equal(cancelAtStart,'not_pending');
 const drained=events.flatMap(e=>e.changes).filter(c=>c.state==='taken').map(c=>c.queueId);
 assert.deepEqual(drained,[...survivors.filter(h=>h.kind==='steer'),...survivors.filter(h=>h.kind==='followUp')].map(h=>h.id));
 assert.equal(agent.getPendingQueue().items.length,0);
 assert.equal(JSON.stringify(requests).includes('queueId'),false);
 // Bounded cancellation receipts can expire; a never-reused old handle still
 // cannot remove a different/new occurrence after that bounded window.
 const oldest=handles.find((_,i)=>i%3===1).id;
 for(let n=0;n<1030;n++){const id=agent.steer(same);assert.equal(agent.cancelQueuedMessage(epoch,id).outcome,'cancelled')}
 assert.equal(agent.cancelQueuedMessage(epoch,oldest).outcome,'not_pending');
 const fresh=agent.steer(same);assert.equal(agent.getPendingQueue().items[0].queueId,fresh);assert.equal(agent.cancelQueuedMessage(epoch,oldest).outcome,'not_pending');assert.equal(agent.getPendingQueue().items[0].queueId,fresh);
 agent.cancelQueuedMessage(epoch,fresh);
 console.log(`${mode}: actual SDK occurrence identity / 100 interleaved writers / order / race / 1024 terminal bound PASS`);
}
