// Production DOM -> real Go Manager -> managed pi, local gated provider only.
// FixtureFollowUp seeds real native follow-ups through Manager.submit; all user
// buttons invoke the production methods. It is not a fake native cancel/promote.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, writeFile, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { randomBytes } from 'node:crypto';
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || 'playwright');
const root=fileURLToPath(new URL('..',import.meta.url));
const out=process.env.YESMAN_QUEUE_BROWSER_EVIDENCE || `${tmpdir()}/yesman-queue-ui`;
await mkdir(out,{recursive:true});
const server=createServer(async(req,res)=>{try{const path=new URL(req.url,'http://localhost').pathname;res.setHeader('Content-Type',path.endsWith('.js')?'application/javascript':path.endsWith('.css')?'text/css':path.endsWith('.woff2')?'font/woff2':'text/html');res.end(await readFile(`${root}/web${path==='/'?'/index.html':path}`));}catch{res.writeHead(404).end();}}).listen(0,'127.0.0.1');
await new Promise(r=>server.on('listening',r));
const png='iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==';
const image={type:'image',mimeType:'image/png',data:png};
const newID=()=>Date.now().toString(16).padStart(12,'0')+randomBytes(18).toString('hex');
const reports=[];
try { for(const [engineName,engine]of Object.entries({chromium,webkit})) for(const language of ['en','zh-Hans']) {
 const temp=await mkdtemp(`${tmpdir()}/queue-actions-`),ready=`${temp}/ready`;
 const child=spawn(process.env.YESMAN_QUEUE_BRIDGE_BIN||'go',process.env.YESMAN_QUEUE_BRIDGE_BIN?['-test.run=^TestQueueActionsBrowserBridge$','-test.timeout=160s']:['test','./internal/agent','-run','^TestQueueActionsBrowserBridge$','-count=1','-timeout=160s'],{cwd:process.env.YESMAN_QUEUE_BRIDGE_BIN?`${root}/internal/agent`:root,env:{...process.env,YESMAN_QUEUE_BRIDGE:ready},stdio:['ignore','pipe','pipe']});
 let log='',exit;child.stdout.on('data',d=>log+=d);child.stderr.on('data',d=>log+=d);child.on('exit',c=>exit=c);
 const until=Date.now()+90000;let api;
 while(!api){try{api=await readFile(ready,'utf8')}catch{if(exit!==undefined||Date.now()>until){child.kill();throw Error(log||'bridge startup timeout')}await new Promise(r=>setTimeout(r,25))}}
 const control=async(method,...args)=>{const r=await(await fetch(api,{method:'POST',body:JSON.stringify({method,args})})).json();if(r.error)throw Error(r.error);return r.value};
 const browser=await engine.launch({headless:true});let page;
 try {
  page=await browser.newPage({viewport:{width:language==='en'?1000:480,height:800},colorScheme:language==='en'?'light':'dark'});
  const errors=[],outside=[];page.on('pageerror',e=>errors.push(String(e)));
  await page.route('**/*',route=>{const u=route.request().url();if(u.startsWith('http://127.0.0.1:')||u.startsWith('data:'))return route.continue();outside.push(u);return route.abort()});
  await page.addInitScript(({api,language})=>{
   localStorage.setItem('yesman.info','hidden');window.events={};window.sent=[];window.copied=[];
   window.mygo={locale:language,on:(n,fn)=>window.events[n]=fn,call:async(method,...args)=>{window.sent.push({method,args});if(method==='Yesman.CopyValue'){window.copied.push(args[0]);return;}const r=await(await fetch(api,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({method,args})})).json();if(r.error)throw Error(r.error);return r.value;}};
   let cursor=0,agents='',stopped=false,timer,inFlight=Promise.resolve();const pollErrors=[];
   const poll=()=>{if(stopped)return;inFlight=(async()=>{try{const response=await fetch(`${api}/events?cursor=${cursor}`);if(!response.ok)throw Error(`HTTP ${response.status}`);const r=await response.json();cursor=r.cursor;for(const e of r.events??[])window.events['agent-event']?.(e);const s=JSON.stringify(r.agents);if(s!==agents){agents=s;window.events.agents?.(r.agents)}}catch(e){pollErrors.push(String(e))}finally{if(!stopped)timer=setTimeout(poll,15)}})()};
   window.stopFixturePolling=async()=>{stopped=true;clearTimeout(timer);await inFlight;return pollErrors};window.addEventListener('pagehide',()=>{stopped=true;clearTimeout(timer)});timer=setTimeout(poll,30);
  },{api,language});
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  const input=page.locator('.composer textarea');await input.waitFor();
  const row=id=>page.locator(`#thread > [data-delivery="${id}"]`);
  const reload=async()=>{assert.deepEqual(await page.evaluate(()=>window.stopFixturePolling()),[]);await page.reload();await input.waitFor()};
  const upload=async(n)=>{await page.locator('.composer input[type=file]').setInputFiles(Array.from({length:n},(_,i)=>({name:`original-${i}.png`,mimeType:'image/png',buffer:Buffer.from(png,'base64')})));await page.locator('.composer-image').nth(n-1).waitFor()};
  const submit=async(text,n=0)=>{await input.fill(text);if(n)await upload(n);await page.waitForFunction(()=>!document.querySelector('.composer input[type=file]').disabled&&!document.querySelector('.composer textarea').readOnly);await input.press('Enter');await page.waitForFunction(()=>!document.querySelector('.composer textarea').readOnly&&document.querySelector('.composer textarea').value==='');return page.evaluate(()=>window.sent.filter(s=>s.method==='Yesman.Submit').at(-1).args[1])};
  const long='  complete 中文\n'+'full original **text** '.repeat(60)+'END  ';
  const ids=[];ids.push(await submit(long,2));ids.push(await submit('',1));for(let i=2;i<11;i++)ids.push(await submit(`pending ${i}`));
  await page.waitForFunction(id=>document.querySelector(`[data-delivery="${id}"]`)?.dataset.original==='true',ids[0]);
  assert.equal(await row(ids[0]).locator('.msg-user').innerText(),long);
  assert.equal(await row(ids[0]).locator('img').count(),2);
  assert.equal(await row(ids[1]).locator('.message-copy:not(.message-recall)').isDisabled(),true);
  assert.equal(await row(ids[0]).locator('.message-promote').count(),0,'already steering must not offer fake acceleration');
  await reload();
  await page.waitForFunction(id=>document.querySelector(`[data-delivery="${id}"]`)?.dataset.original==='true',ids[0]);
  for(const id of ids){assert.equal(await row(id).count(),1);await row(id).locator('.message-recall').waitFor()}
  assert.equal(await row(ids[0]).locator('.msg-user').innerText(),long);
  assert.equal(await row(ids[0]).locator('img').count(),2);
  const state=await control('State'),a=state.agents.find(a=>a.id==='alice/lead'),follow=[newID(),newID(),newID()];
  for(let i=0;i<3;i++)await control('FixtureFollowUp',a.id,follow[i],i===1?long:'same',[image,image],a.since);
  const target=follow[1];await row(target).locator('.message-promote').waitFor();
  await page.waitForFunction(id=>document.querySelector(`[data-delivery="${id}"]`)?.dataset.original==='true',target);
  assert.match(await row(target).locator('.delivery-status').innerText(),language==='en'?/Waiting/:/等待/);
  await page.screenshot({path:`${out}/${engineName}-${language}-pending.png`});
  const before=await control('NativeSnapshot'),item=before.items.find(x=>x.submissionId===target);
  await row(target).locator('.message-promote').evaluate(el=>{el.click();el.click()});
  await page.waitForFunction(id=>!document.querySelector(`[data-delivery="${id}"] .message-promote`),target);
  const after=await control('NativeSnapshot'),moved=after.items.find(x=>x.submissionId===target);
  assert.equal(moved.queueId,item.queueId);assert.equal(moved.kind,'steer');
  assert.deepEqual(after.items.filter(x=>x.kind==='followUp').map(x=>x.submissionId),[follow[0],follow[2]]);
  assert.deepEqual(after.items.filter(x=>x.kind==='steer').map(x=>x.submissionId),[...ids,target]);
  assert.equal((await control('Counts')).promote,1,'repeat click dispatched twice');
  assert.deepEqual(await control('SubmissionPayload',a.id,target,a.since),{text:long,images:[image,image]});
  await input.fill('protected draft');await row(target).locator('.message-recall').click();assert.equal(await input.inputValue(),'protected draft');assert.equal((await control('NativeSnapshot')).items.some(x=>x.submissionId===target),true);
  await input.fill('');await row(target).locator('.message-recall').click();await page.waitForFunction(text=>document.querySelector('.composer textarea').value===text,long);
  assert.equal(await page.locator('.composer-image').count(),2);assert.equal((await control('NativeSnapshot')).items.some(x=>x.submissionId===target),false);
  await input.fill(long+' edited');await input.press('Enter');await page.waitForFunction(()=>!document.querySelector('.composer textarea').readOnly&&document.querySelector('.composer textarea').value==='');
  const edited=await page.evaluate(()=>window.sent.filter(s=>s.method==='Yesman.Submit').at(-1).args[1]);assert.notEqual(edited,target);
  await row(follow[0]).locator('.message-copy:not(.message-recall):not(.message-promote)').click();await page.waitForFunction(()=>window.copied.length>0);assert.equal(await page.evaluate(()=>window.copied.at(-1)),'same');
  await control('ReleaseProvider');
  await page.waitForFunction(ids=>ids.every(id=>document.querySelectorAll(`[data-delivery="${id}"][data-bound="true"]`).length===1),[...ids,follow[0],follow[2],edited]);
  const boundOrder=await page.locator('[data-bound=true]').evaluateAll(els=>els.map(el=>el.dataset.delivery));
  assert.deepEqual(boundOrder.filter(id=>id!==boundOrder[0]),[...ids,edited,follow[0],follow[2]],'native history changed queue order');
  assert.equal(await page.locator('[data-bound=true] .message-recall,[data-bound=true] .message-promote').count(),0);
  assert.equal(await page.locator(`[data-delivery="${target}"][data-bound=true]`).count(),0,'cancelled original ran');
  await reload();await page.waitForFunction(n=>document.querySelectorAll('[data-bound=true]').length===n,boundOrder.length);
  assert.deepEqual(await page.locator('[data-bound=true]').evaluateAll(els=>els.map(el=>el.dataset.delivery)),boundOrder);
  assert.equal(await page.locator('[data-bound=true] .message-recall,[data-bound=true] .message-promote').count(),0);
  assert.equal(await row(ids[0]).locator('.msg-user').innerText(),long);
  assert.equal(await row(ids[1]).locator('img').count(),1);
  await page.screenshot({path:`${out}/${engineName}-${language}-taken.png`});
  assert.deepEqual(await page.evaluate(()=>window.stopFixturePolling()),[]);assert.deepEqual(errors,[]);assert.deepEqual(outside,[]);
  reports.push({engine:engineName,language,pending:ids.length,followUp:follow,before,after,boundOrder,canonicalReload:true,originalPayload:true,cacheEviction:true,errors});
 } catch(error){if(page)await page.screenshot({path:`${out}/${engineName}-${language}-failure.png`}).catch(()=>{});throw error}
 finally {await browser.close();await fetch(`${api}/stop`).catch(()=>{});const deadline=Date.now()+15000;while(exit===undefined&&Date.now()<deadline)await new Promise(r=>setTimeout(r,25));if(exit===undefined)child.kill();await writeFile(`${out}/${engineName}-${language}-bridge.log`,log);await rm(temp,{recursive:true,force:true})}
 await writeFile(`${out}/results.json`,JSON.stringify(reports,null,2));
 console.log(engineName,language,'real pending/cache/reload/promote/exact occurrence/order/cancel/edit/canonical Copy PASS');
}} finally {server.close()}
