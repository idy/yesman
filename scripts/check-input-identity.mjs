// Actual Session/RPC: host-only identity, idle queue ownership, stop and restart.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {existsSync} from 'node:fs';
import {mkdtemp,mkdir,writeFile,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
const runtime=process.env.YESMAN_TEST_RUNTIME;if(!runtime)throw Error('Set YESMAN_TEST_RUNTIME');
const temp=await mkdtemp(`${tmpdir()}/yesman-input-identity-`);await mkdir(`${temp}/config`);
await writeFile(`${temp}/config/settings.json`,JSON.stringify({packages:[],extensions:[],skills:[],promptTemplates:[],themes:[],compaction:{enabled:false,keepRecentTokens:1,reserveTokens:100},retry:{enabled:false},defaultProjectTrust:'no'}));
await writeFile(`${temp}/hooks.ts`,`import {appendFileSync,existsSync} from 'node:fs';
export default pi=>{
 for(const name of ['input','before_agent_start','message_start','message_end','context','context_with_system','turn_end','agent_before_settle','session_before_compact'])pi.on(name,async e=>{
  appendFileSync(process.env.IDENTITY_AUDIT,JSON.stringify({name,event:e})+'\\n');
  if(name==='before_agent_start'&&e.prompt==='pause-prepare'){await import('node:fs/promises').then(f=>f.writeFile(process.env.IDENTITY_READY,'ready'));while(!existsSync(process.env.IDENTITY_RELEASE))await new Promise(r=>setTimeout(r,5));}
  if(name==='input'&&e.text==='pause-input'){await import('node:fs/promises').then(f=>f.writeFile(process.env.IDENTITY_READY+'-input','ready'));while(!existsSync(process.env.IDENTITY_RELEASE+'-input'))await new Promise(r=>setTimeout(r,5));}
  if(name==='input'&&e.text==='transform')return {action:'transform',text:'same'};
  if(name==='message_end'&&e.message?.role==='user'&&e.message.content?.[0]?.text==='replace')return {message:{...e.message,content:[{type:'text',text:'replaced'}]}};
 });
};`);
const ids=Array.from({length:10},(_,n)=>Date.now().toString(16).padStart(12,'0')+n.toString(16).padStart(36,'0'));
let child,records,buffer,error,stderr,sequence=0;
async function until(fn){const end=Date.now()+15000;while(!fn()){if(error)throw error;if(Date.now()>end)throw Error(`deadline ${stderr} ${JSON.stringify(records.slice(-8))}`);await new Promise(r=>setTimeout(r,5))}return fn()}
async function launch(resume=false){
 records=[];buffer='';stderr='';error=null;
 const env={PATH:process.env.PATH,HOME:temp,USERPROFILE:temp,APPDATA:temp,LOCALAPPDATA:temp,TMPDIR:tmpdir(),PI_CODING_AGENT_DIR:`${temp}/config`,IDENTITY_AUDIT:`${temp}/audit`,IDENTITY_READY:`${temp}/ready`,IDENTITY_RELEASE:`${temp}/release`,DELIVERY_GATE:`${temp}/provider`,DELIVERY_AUDIT:`${temp}/provider-audit`,...(process.env.SystemRoot?{SystemRoot:process.env.SystemRoot}:{})};
 child=spawn(process.execPath,[resolve(runtime,'node_modules/@earendil-works/pi-coding-agent/dist/cli.js'),'--mode','rpc','--provider','delivery-test','--model','m','--session-dir',`${temp}/sessions`,'--no-builtin-tools',...(resume?['--continue']:[]),'-e',fileURLToPath(new URL('./fixtures/delivery-provider.ts',import.meta.url)),'-e',`${temp}/hooks.ts`],{cwd:temp,env});
 child.on('error',e=>error=e);child.stderr.on('data',d=>stderr+=d);child.stdout.on('data',d=>{buffer+=d;let n;while((n=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,n);buffer=buffer.slice(n+1);if(line.trim())records.push(JSON.parse(line))}});
 return call({type:'get_state'});
}
async function close(){if(!child||child.exitCode!==null||child.signalCode!==null)return;child.stdin.end();const kill=setTimeout(()=>child.kill('SIGKILL'),1500);await new Promise(r=>child.once('exit',r));clearTimeout(kill)}
async function call(cmd){const id=`identity-${++sequence}`;child.stdin.write(JSON.stringify({...cmd,id})+'\n');const r=await until(()=>records.find(r=>r.id===id&&r.type==='response'));if(!r.success)throw Error(r.error);return r.data}
const promote=(q,item)=>call({type:'promote_queued_message',epoch:q.epoch,queueId:item.queueId,expectedRevision:item.itemRevision,controlGeneration:q.controlGeneration});
try{
 await writeFile(`${temp}/provider`,'release');
 const state=await launch();assert.equal(state.pendingQueue.capability,4);assert.equal(state.pendingQueue.idleResume,true);
 // Completely empty transcript: run the queued occurrence, no fabricated prompt.
 await call({type:'follow_up',message:'empty-start',submissionId:ids[0],hostOrigin:{source:'user'}});
 let q=await call({type:'get_pending_queue'});assert.equal(q.promoteAllowed,true);
 const before=records.length;
 const admissions=await Promise.all([promote(q,q.items[0]),promote(q,q.items[0])]);
 assert.equal(admissions.filter(r=>r.outcome==='promoted').length,1);
 assert(admissions.every(r=>['promoted','not_pending','already_steering'].includes(r.outcome)));
 await until(()=>records.slice(before).some(e=>e.type==='agent_settled'));
 let history=await call({type:'get_messages'});
 assert.equal(history.messages.filter(m=>m.role==='user').length,1);
 let refs=history.inputRefs.filter(r=>r?.identity?.submissionId);assert.equal(refs[0].identity.submissionId,ids[0]);assert(refs[0].entryId);
 // Direct prompt and transformed queued messages preserve separate input IDs.
 await rm(`${temp}/provider`);
 const first=records.length;await call({type:'prompt',message:'holding',submissionId:ids[1],hostOrigin:{source:'user'}});
 await until(()=>records.slice(first).some(e=>e.type==='message_end'&&e.message?.role==='user'));
 await Promise.all([
  call({type:'follow_up',message:'same',submissionId:ids[2],hostOrigin:{source:'user'}}),
  call({type:'follow_up',message:'transform',submissionId:ids[3],hostOrigin:{source:'user'}}),
  call({type:'steer',message:'replace',hostOrigin:{source:'yesman',kind:'report',worker:'worker'}})
 ]);
 q=await call({type:'get_pending_queue'});await promote(q,q.items.find(i=>i.submissionId===ids[3]));
 await writeFile(`${temp}/provider`,'release');await until(()=>records.slice(first).some(e=>e.type==='agent_settled'));
 history=await call({type:'get_messages'});refs=history.inputRefs.filter(r=>r?.identity?.submissionId);
 assert.deepEqual(refs.map(r=>r.identity.submissionId),[ids[0],ids[1],ids[3],ids[2]]);
 assert.equal(new Set(refs.map(r=>r.entryId)).size,4);
 assert.equal(history.inputRefs.find(r=>r?.identity?.origin?.source==='yesman').identity.origin.worker,'worker');
 assert(history.messages.some(m=>m.role==='user'&&m.content?.[0]?.text==='replaced'));
 const entries=await call({type:'get_entries'});
 const sides=entries.entries.filter(e=>e.type==='custom'&&e.customType==='yesman-input-v1');assert.equal(sides.length,5);
 assert(sides.every(e=>!JSON.stringify(e.data).includes('queueId')&&!JSON.stringify(e.data).includes('epoch')));
 assert(entries.entries.filter(e=>e.type==='message').every(e=>!JSON.stringify(e.message).includes('submissionId')));
 const reference=refs.map(r=>({id:r.identity.submissionId,entryId:r.entryId}));
 await close();await launch(true);
 history=await call({type:'get_messages'});assert.deepEqual(history.inputRefs.filter(r=>r?.identity?.submissionId).map(r=>({id:r.identity.submissionId,entryId:r.entryId})),reference);
 const compacted=await call({type:'compact'});
 history=await call({type:'get_messages'});
 assert(history.recordedInputs.some(r=>r.identity?.submissionId===ids[0]),'compaction discarded intake evidence');
 assert(history.inputRefs.length===history.messages.length);
 // Preparation belongs to one Session, but native intake is not committed.
 // Stop during its hook preserves both the promoted original and other input.
 await call({type:'follow_up',message:'pause-prepare',submissionId:ids[4]});
 q=await call({type:'get_pending_queue'});const start=records.length;
 const admission=await promote(q,q.items[0]);assert.equal(admission.outcome,'promoted');
 await until(()=>existsSync(`${temp}/ready`));
 assert.equal(records.slice(start).filter(e=>e.type==='queue_update'&&e.pending.changes.some(c=>c.state==='taken')).length,0,'preparation prematurely took input');
 await call({type:'prompt',message:'other pending',streamingBehavior:'followUp',submissionId:ids[5]});
 const stopped=call({type:'abort'});
 await until(()=>records.slice(start).some(e=>e.type==='queue_update'&&e.pending.controlGeneration>q.controlGeneration));
 await writeFile(`${temp}/release`,'release');await stopped;
 assert.equal(records.slice(start).filter(e=>e.type==='agent_start').length,0,'Stop during queued preparation still ran a model loop');
 const afterStop=await call({type:'get_pending_queue'});assert(afterStop.items.some(i=>i.submissionId===ids[5]));
 const retained=afterStop.items.find(i=>i.submissionId===ids[4]);
 assert(retained,'Stop discarded the original during preparation');
 assert.equal(retained.queueId,q.items[0].queueId);assert.equal(retained.kind,'steer');
 assert.equal(records.slice(start).filter(e=>e.type==='queue_update'&&e.pending.changes.some(c=>c.state==='taken')).length,0);
 assert.equal((await call({type:'get_messages'})).recordedInputs.some(r=>r.identity?.submissionId===ids[4]),false);
 assert.equal((await call({type:'cancel_queued_message',epoch:afterStop.epoch,queueId:retained.queueId,expectedRevision:retained.itemRevision})).outcome,'cancelled');
 assert.equal((await promote(q,q.items[0])).outcome,'stale_control');
 await call({type:'cancel_queued_message',epoch:afterStop.epoch,queueId:afterStop.items.find(i=>i.submissionId===ids[5]).queueId});
 await call({type:'new_session'});
 await call({type:'follow_up',message:'queued before prompt',submissionId:ids[6]});
 q=await call({type:'get_pending_queue'});
 const concurrent=call({type:'prompt',message:'pause-input',submissionId:ids[7]});
 await until(()=>existsSync(`${temp}/ready-input`));
 const busy=await promote(q,q.items[0]);assert.equal(busy.outcome,'inactive');
 assert.equal(busy.items[0].kind,'followUp');
 const runStart=records.length;
 await writeFile(`${temp}/release-input`,'release');await concurrent;
 await until(()=>records.slice(runStart).some(e=>e.type==='agent_settled'));
 assert.equal(records.slice(runStart).filter(e=>e.type==='agent_start').length,1,'concurrent preflight created a second run');
 history=await call({type:'get_messages'});assert.deepEqual(history.inputRefs.filter(r=>r?.identity?.submissionId).map(r=>r.identity.submissionId),[ids[7],ids[6]]);
 const audit=await readFile(`${temp}/audit`,'utf8'),provider=await readFile(`${temp}/provider-audit`,'utf8');
 for(const id of ids){assert(!audit.includes(id));assert(!provider.includes(id))}
 for(const field of ['inputIdentity','inputPosition','submissionId','queueId','hostOrigin']){assert(!audit.includes(field),`hook leaked ${field}`);assert(!provider.includes(field),`provider leaked ${field}`)}
 console.log('PASS: empty idle resume / exact occurrence and entry IDs / transform+replacement / restart / stop during owned preflight / no second loop / no identity in hooks or provider');
 }finally{await writeFile(`${temp}/release`,'release');await writeFile(`${temp}/release-input`,'release');await close();await rm(temp,{recursive:true,force:true})}
