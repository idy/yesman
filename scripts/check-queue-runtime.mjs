// Tests the actual source-patched CLI/RPC, not a loader prototype or fake cancel.
// Run after preparing a runtime: YESMAN_TEST_RUNTIME=/path/to/runtime node ...
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';
const runtime = process.env.YESMAN_TEST_RUNTIME;
if (!runtime) throw new Error('Set YESMAN_TEST_RUNTIME to the prepared application runtime');
const temp = await mkdtemp(`${tmpdir()}/yesman-native-queue-`);
await mkdir(`${temp}/config`);
await writeFile(`${temp}/config/settings.json`, JSON.stringify({ packages: [], extensions: [], skills: [], promptTemplates: [], themes: [], compaction: { enabled: false }, retry: { enabled: false }, defaultProjectTrust: 'no' }));
await writeFile(`${temp}/hook.ts`, `import { appendFileSync } from 'node:fs';
export default (pi) => {
 pi.on('input', e => { appendFileSync(process.env.QUEUE_HOOK_AUDIT, JSON.stringify(e)+'\\n'); if (e.text==='transform:same') return {action:'transform', text:'same'}; });
 pi.on('context', e => { appendFileSync(process.env.QUEUE_CONTEXT_AUDIT, JSON.stringify(e.messages)+'\\n'); });
};`);
const api = createServer((req, res) => {
  res.setHeader('Content-Type', req.url === '/v1/context' ? 'text/plain' : 'application/json');
  res.end(req.url === '/v1/context' ? 'Isolated queue fixture' : JSON.stringify({ env: {}, hide: [] }));
}).listen(0,'127.0.0.1');
await new Promise(r=>api.on('listening',r));
const child = spawn(process.execPath, [resolve(runtime,'node_modules/@earendil-works/pi-coding-agent/dist/cli.js'), '--mode','rpc','--provider','delivery-test','--model','m','--session-dir',`${temp}/sessions`,'--no-builtin-tools',
  '-e',fileURLToPath(new URL('../pi/yesman.ts',import.meta.url)),
  '-e',fileURLToPath(new URL('./fixtures/delivery-provider.ts',import.meta.url)), '-e',`${temp}/hook.ts`],
{cwd:temp,env:{PATH:process.env.PATH,HOME:temp,TMPDIR:tmpdir(),...(process.env.SystemRoot?{SystemRoot:process.env.SystemRoot}:{}),
  PI_CODING_AGENT_DIR:`${temp}/config`,YESMAN_API:`http://127.0.0.1:${api.address().port}`,YESMAN_TOKEN:'fixture',YESMAN_AGENT:'fixture/worker',YESMAN_ROLE:'worker',
  DELIVERY_GATE:`${temp}/gate`,DELIVERY_AUDIT:`${temp}/audit`,QUEUE_HOOK_AUDIT:`${temp}/hooks`,QUEUE_CONTEXT_AUDIT:`${temp}/contexts`}});
const records=[];let buffer='',stderr='',error,seq=0;
child.on('error',e=>{error=e});child.stderr.on('data',d=>{stderr+=d});
child.stdout.on('data',d=>{buffer+=d;let n;while((n=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,n);buffer=buffer.slice(n+1);if(line.trim())records.push(JSON.parse(line));}});
async function until(find){const deadline=Date.now()+20000;while(!find()){if(error)throw error;if(Date.now()>deadline)throw Error(`${stderr}\n${JSON.stringify(records.slice(-8))}`);await new Promise(r=>setTimeout(r,5));}return find();}
async function call(cmd){const id=`fixture-${++seq}`;child.stdin.write(JSON.stringify({...cmd,id})+'\n');const r=await until(()=>records.find(r=>r.type==='response'&&r.id===id));assert.equal(r.success,true,JSON.stringify(r));return r.data;}
const image={type:'image',mimeType:'image/png',data:'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=='};
const id=n=>Date.now().toString(16).padStart(12,'0')+n.toString(16).padStart(36,'0');
const results=[];
try {
 const initial=await call({type:'get_state'});assert.equal(initial.pendingQueue.capability,1);assert.equal(initial.runtime,'yesman-pi/1.5.2-1');
 for(const mode of ['one-at-a-time','all']){
  await rm(`${temp}/gate`,{force:true});
  await call({type:'set_steering_mode',mode});await call({type:'set_follow_up_mode',mode});
  const before=records.length;
  assert.equal((await call({type:'prompt',message:`hold-${mode}`})).disposition,'started');
  await until(()=>records.slice(before).some(r=>r.type==='message_end'&&r.message?.role==='user'));
  const ids=Array.from({length:6},(_,n)=>id(n+(mode==='all'?100:0)));
  // Concurrent RPC writers pass independent invocation identities through hooks.
  await Promise.all(ids.map((submissionId,n)=>call({type:n<3?'steer':'follow_up',message:n===1?'transform:same':'same',images:n===1?[image,image]:[image],submissionId})));
  const pending=await call({type:'get_pending_queue'});assert.equal(pending.items.length,6);
  assert.equal(new Set(pending.items.map(i=>i.queueId)).size,6);
  assert.deepEqual(new Set(pending.items.map(i=>i.submissionId)),new Set(ids));
  const b=pending.items.find(i=>i.submissionId===ids[1]), f=pending.items.find(i=>i.submissionId===ids[4]);
  const cancel=(item,epoch=pending.epoch)=>call({type:'cancel_queued_message',epoch,queueId:item.queueId});
  assert.equal((await cancel(b,'old')).outcome,'stale');
  assert.equal((await call({type:'get_pending_queue'})).items.length,6);
  const twice=await Promise.all([cancel(b),cancel(b)]);assert(twice.every(r=>r.outcome==='cancelled'));
  assert.equal((await cancel(f)).outcome,'cancelled');
  const survivors=await call({type:'get_pending_queue'});
  assert.deepEqual(survivors.items.map(i=>i.queueId),pending.items.filter(i=>i!==b&&i!==f).map(i=>i.queueId));
  assert.equal(survivors.steering.length,2);assert.equal(survivors.followUp.length,2);
  assert.equal((await call({type:'get_state'})).isStreaming,true); // no implicit abort
  assert.equal(records.filter(r=>r.type==='queue_update'&&r.pending?.changes?.some(c=>c.queueId===b.queueId&&c.state==='cancelled')).length,1);
  await writeFile(`${temp}/gate`,'release');
  await until(()=>records.slice(before).some(r=>r.type==='agent_settled'));
  const users=records.slice(before).filter(r=>r.type==='message_end'&&r.message?.role==='user');
  assert.equal(users.length,5);
  assert(users.slice(1).every(r=>r.message.content.filter(b=>b.type==='image').length===1));
  const taken=records.slice(before).flatMap(r=>r.type==='queue_update'?r.pending?.changes??[]:[]).filter(c=>c.state==='taken');
  assert.deepEqual(taken.map(c=>c.submissionId),[ids[0],ids[2],ids[3],ids[5]]);
  assert.equal((await cancel(survivors.items[0])).outcome,'not_pending');
  assert.equal((await cancel(b)).outcome,'cancelled'); // idempotent lost acknowledgement recovery
  assert.equal((await call({type:'get_pending_queue'})).items.length,0);
  results.push({mode,pending:6,cancelled:2,survivors:4,afterDrain:'not_pending',duplicateCancel:'cancelled'});
 }
 const hooks=await readFile(`${temp}/hooks`,'utf8'), contexts=await readFile(`${temp}/contexts`,'utf8');
 for(const field of ['submissionId','queueId','pendingQueue']){assert.equal(hooks.includes(field),false);assert.equal(contexts.includes(field),false);}
 assert(hooks.includes('transform:same')); // real third-party transform remained active
 const report={runtime:initial.runtime,results,extension:'production yesman.ts plus third-party transform/context audit',metadataInHooks:false,scope:'Actual patched CLI + SDK + native queues + RPC; local deterministic provider; no Go/UI claim'};
 if(process.env.YESMAN_QUEUE_RUNTIME_EVIDENCE)await writeFile(process.env.YESMAN_QUEUE_RUNTIME_EVIDENCE,JSON.stringify(report,null,2)+'\n');
 console.log(JSON.stringify(report,null,2));
}finally{child.stdin.end();const kill=setTimeout(()=>child.kill('SIGKILL'),1000);await new Promise(r=>child.exitCode!==null||child.signalCode!==null||error?r():child.once('exit',r));clearTimeout(kill);await new Promise(r=>api.close(r));await rm(temp,{recursive:true,force:true});}
