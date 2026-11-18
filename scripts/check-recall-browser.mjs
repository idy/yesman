// Production page -> controlled transport -> actual Go service/Manager ->
// production extension -> managed pi CLI/RPC -> deterministic local provider.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, rm, writeFile, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || 'playwright');
const root=fileURLToPath(new URL('..',import.meta.url));
const out=process.env.YESMAN_RECALL_BROWSER_EVIDENCE;
if(out)await mkdir(out,{recursive:true});
const http=createServer(async(req,res)=>{try{const path=new URL(req.url,'http://localhost').pathname;res.setHeader('Content-Type',path.endsWith('.js')?'application/javascript':path.endsWith('.css')?'text/css':path.endsWith('.woff2')?'font/woff2':'text/html');res.end(await readFile(`${root}/web${path==='/'?'/index.html':path}`));}catch{res.writeHead(404).end();}}).listen(0,'127.0.0.1');
await new Promise(r=>http.on('listening',r));
const reports=[];
const selectedEngines=Object.entries({chromium,webkit}).filter(([name])=>!process.env.YESMAN_TEST_BROWSERS||process.env.YESMAN_TEST_BROWSERS.split(',').includes(name));
assert(selectedEngines.length,'No browser engine selected');
try{for(const[name,engine]of selectedEngines)for(const language of ['en','zh-Hans']){
 const temp=await mkdtemp(`${tmpdir()}/recall-browser-`),ready=`${temp}/ready`;
 const executable=process.env.YESMAN_TEST_BRIDGE_BIN || 'go';
 const args=process.env.YESMAN_TEST_BRIDGE_BIN ? ['-test.run=^TestRecallBrowserBridge$','-test.timeout=100s'] : ['test','-run','^TestRecallBrowserBridge$','-count=1','-timeout=100s','.'];
 const fixture=spawn(executable,args,{cwd:root,env:{...process.env,YESMAN_RECALL_BRIDGE:ready},stdio:['ignore','pipe','pipe']});
 let log='',exit;fixture.stdout.on('data',d=>log+=d);fixture.stderr.on('data',d=>log+=d);fixture.on('exit',c=>exit=c);
 const deadline=Date.now()+90000;let api;
 while(!api){try{api=await readFile(ready,'utf8')}catch{if(exit!==undefined||Date.now()>deadline){fixture.kill('SIGTERM');await rm(temp,{recursive:true,force:true});throw Error(`bridge startup: ${log}`);}await new Promise(r=>setTimeout(r,20));}}
 const control=async(method,...args)=>{const res=await(await fetch(api,{method:'POST',body:JSON.stringify({method,args})})).json();if(res.error)throw Error(res.error);return res.value;};
 const browser=await engine.launch({headless:true});
 let activePage;
 try{
  const page=activePage=await browser.newPage({viewport:{width:1000,height:760}}),errors=[];
  if(process.env.YESMAN_TEST_POLL_FAILURE){
   let failed=false;
   await page.route('**/events?*',route=>{if(!failed){failed=true;return route.abort('failed')}return route.continue()});
  }
  page.on('pageerror',e=>errors.push(String(e)));
  await page.addInitScript(({api,language})=>{
   localStorage.setItem('yesman.info','hidden');window.events={};window.sent=[];window.copied=[];
   window.mygo={locale:language,on:(n,fn)=>window.events[n]=fn,call:async(method,...args)=>{
    window.sent.push({method,args});
    if(method==='Yesman.CopyValue'){window.copied.push(args[0]);return;}
    const result=await(await fetch(api,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({method,args})})).json();
    if(result.error)throw Error(result.error);return result.value;
   }};
   let cursor=0,agents='',stopped=false,timer,inFlight=Promise.resolve();
   const pollErrors=[];
   const poll=()=>{
    if(stopped)return;
    inFlight=(async()=>{
     try{
      const response=await fetch(`${api}/events?cursor=${cursor}`);
      if(!response.ok)throw Error(`event poll HTTP ${response.status}`);
      const r=await response.json();cursor=r.cursor;
      for(const e of r.events??[])window.events['agent-event']?.(e);
      const s=JSON.stringify(r.agents);if(s!==agents){agents=s;window.events.agents?.(r.agents);}
     }catch(error){pollErrors.push(String(error));}
     finally{if(!stopped)timer=setTimeout(poll,15);}
    })();
   };
   window.stopFixturePolling=async()=>{stopped=true;clearTimeout(timer);await inFlight;return pollErrors;};
   window.addEventListener('pagehide',()=>{stopped=true;clearTimeout(timer);});
   timer=setTimeout(poll,30);
  },{api,language});
  await page.goto(`http://127.0.0.1:${http.address().port}`);
  const input=page.locator('.composer textarea');await input.waitFor();
  const stopPolling=async()=>assert.deepEqual(await page.evaluate(()=>window.stopFixturePolling()),[],'Unexpected bridge poll failure');
  const reload=async()=>{await stopPolling();await page.reload();await input.waitFor();};
  const text='  Original 中文\n'+('full text **markdown** '.repeat(50))+'END  ';
  const bytes=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==','base64');
  const attach=async(count)=>page.locator('.composer input[type=file]').setInputFiles(Array.from({length:count},(_,i)=>({name:`original-${i}.png`,mimeType:'image/png',buffer:bytes})));
  const submit=async(text,count=0)=>{await input.fill(text);if(count)await attach(count);await page.waitForFunction(()=>!document.querySelector('.composer input[type=file]').disabled&&!document.querySelector('.composer textarea').readOnly);if(count)await page.locator('.composer-image').nth(count-1).waitFor();await input.press('Enter');await page.waitForFunction(()=>!document.querySelector('.composer textarea').readOnly&&document.querySelector('.composer textarea').value==='');return page.evaluate(()=>window.sent.filter(s=>/^Yesman\.Submit(Coalescing)?$/.test(s.method)).at(-1));};
  const first=await submit(text,2),id=first.args[1];
  const row=id=>page.locator(`[data-delivery="${id}"]`);
  await row(id).locator('.message-recall').waitFor();
  await page.waitForFunction(()=>document.querySelector('.pi-queue-status'));
  assert.equal(await page.locator('.pi-queue-status').innerText().then(s=>/Individual recall is unavailable|无法逐条撤回/.test(s)),false,'managed capability falsely reports recall unavailable');
  assert.equal(await row(id).locator('.message-recall').getAttribute('aria-label'),language==='en'?'Cancel and edit':'撤回并编辑');
  if(out)await page.screenshot({path:`${out}/${name}-${language}-queued.png`});
  await row(id).locator('.message-copy:not(.message-recall)').click();
  await page.waitForFunction(()=>window.copied.length===1);
  assert.equal(await page.evaluate(()=>window.copied[0]),text);
  await input.fill('protected draft');
  const before=(await control('Counts')).cancel;
  await row(id).locator('.message-recall').click();
  assert.equal((await control('Counts')).cancel,before);assert.equal(await input.inputValue(),'protected draft');
  await input.fill('');
  await row(id).locator('.message-recall').evaluate(button=>{button.click();button.click()});
  await page.waitForFunction(text=>document.querySelector('.composer textarea').value===text,text);
  assert.equal(await page.locator('.composer-image').count(),2);
  assert.equal(await page.locator('.composer-image img').first().getAttribute('src'),`data:image/png;base64,${bytes.toString('base64')}`);
  assert.equal(await row(id).locator('.message-recall').count(),0);
  // Edit + explicit resend produces a new intent and exactly the same images.
  await input.fill(text+' EDITED');await input.press('Enter');
  await page.waitForFunction(()=>window.sent.filter(s=>/^Yesman\.Submit(Coalescing)?$/.test(s.method)).length===2&&!document.querySelector('.composer textarea').readOnly);
  const second=await page.evaluate(()=>window.sent.filter(s=>/^Yesman\.Submit(Coalescing)?$/.test(s.method)).at(-1));
  assert.notEqual(second.args[1],id);assert.deepEqual(second.args[3],first.args[3]);
  await row(second.args[1]).locator('.message-recall').waitFor();
  const beforeArrow=(await control('Counts')).cancel;
  // IME composition, visible menu, existing text and attachment own ArrowUp.
  await input.dispatchEvent('compositionstart');await input.press('ArrowUp');await input.dispatchEvent('compositionend');
  await page.evaluate(()=>{const menu=document.createElement('div');menu.id='priority-menu';menu.setAttribute('role','menu');menu.textContent='menu';document.body.append(menu)});
  await input.press('ArrowUp');await page.locator('#priority-menu').evaluate(el=>el.remove());
  await input.fill('line1\nline2');await input.press('ArrowUp');assert.equal(await input.inputValue(),'line1\nline2');await input.fill('');
  await attach(1);await page.locator('.composer-image').waitFor();await input.press('ArrowUp');
  assert.equal((await control('Counts')).cancel,beforeArrow);
  await page.locator('.composer-image button').click();
  await input.press('ArrowUp');
  await page.waitForFunction(text=>document.querySelector('.composer textarea').value===text,text+' EDITED');
  assert.equal((await control('Counts')).cancel,beforeArrow+1);assert.equal(await page.locator('.composer-image').count(),2);
  await input.fill('');for(let n=0;n<2;n++)await page.locator('.composer-image button').first().click();
  const third=await submit('late reply stays with alice',1);
  await control('HoldCancel');
  await row(third.args[1]).locator('.message-recall').click();
  await page.waitForFunction(()=>document.querySelector('.composer textarea').readOnly);
  await page.locator('[data-agent="bob/lead"]').first().click();await input.fill('bob draft');
  await control('ReleaseCancel');
  await page.waitForFunction(()=>window.sent.some(s=>s.method==='Yesman.AckRecall'&&s.args[1]===window.sent.filter(s=>/^Yesman\.Submit(Coalescing)?$/.test(s.method)).at(-1).args[1]));
  assert.equal(await input.inputValue(),'bob draft');assert.equal(await page.locator('.composer-image').count(),0);
  await page.locator('[data-agent="alice/lead"]').first().click();
  assert.equal(await input.inputValue(),'late reply stays with alice');assert.equal(await page.locator('.composer-image').count(),1);
  await input.fill('');await page.locator('.composer-image button').click();
  const imageOnly=await submit('',2);await row(imageOnly.args[1]).locator('.message-recall').waitFor();
  assert.equal(await row(imageOnly.args[1]).locator('.message-copy:not(.message-recall)').isDisabled(),true);
  await input.press('ArrowUp');await page.waitForFunction(()=>document.querySelectorAll('.composer-image').length===2&&!document.querySelector('.composer textarea').readOnly);
  assert.equal(await input.inputValue(),'');
  // Returned to the input, it is no longer sent: its row leaves.
  await page.waitForFunction(id=>!document.querySelector(`[data-delivery="${id}"]`),imageOnly.args[1]);
  for(let n=0;n<2;n++)await page.locator('.composer-image button').first().click();
  const recovery=await submit('recover original after reload',2);
  await control('HoldCancel');
  await row(recovery.args[1]).locator('.message-recall').click();
  await page.waitForFunction(()=>document.querySelector('.composer textarea').readOnly);
  await page.waitForFunction(()=>[...document.querySelectorAll('.delivery-status')].some(el=>['Recalled','已撤回'].includes(el.textContent)));
  // Wait for this exact original to be cancelled before severing the reply.
  await page.waitForFunction(id=>document.querySelector(`[data-delivery="${id}"] .message-recall`)?.getAttribute('aria-label')?.match(/Return recalled|已撤回/),recovery.args[1]);
  await reload();
  await row(recovery.args[1]).locator('.message-recall').waitFor();
  await control('ReleaseCancel');
  await row(recovery.args[1]).locator('.message-recall').click();
  await page.waitForFunction(()=>document.querySelector('.composer textarea').value==='recover original after reload');
  assert.equal(await page.locator('.composer-image').count(),2);
  assert.equal(await page.locator('.composer-image img').first().getAttribute('src'),`data:image/png;base64,${bytes.toString('base64')}`);
  await control('ReleaseProvider');
  const nativeCopy = page.locator('#thread > .msg-user .message-copy, #thread > .msg-delivery[data-bound="true"] > .msg-user > .message-actions > .message-copy');
  await nativeCopy.first().waitFor();
  await nativeCopy.first().click();assert.equal(await page.evaluate(()=>window.copied.at(-1)),'hold');
  assert.equal(await page.locator('#thread > .msg-user .message-recall, #thread > .msg-delivery[data-bound="true"] .message-recall').count(),0);
  await page.setViewportSize({width:480,height:740});await page.evaluate(()=>document.documentElement.dataset.theme='dark');
  if(out)await page.screenshot({path:`${out}/${name}-${language}-real-recall.png`});
  const counts=await control('Counts');assert.equal(counts.submit,5);assert.equal(counts.cancel,6);
  await reload();
  assert.equal(await page.locator('.message-recall').count(),0); // acknowledged originals cannot reappear
  await stopPolling();
  assert.deepEqual(errors,[]);reports.push({engine:name,language,...counts,originalImages:'exact',editedIntent:'new',drafts:'protected',native:'real managed pi + production Go service'});
 }finally{if(activePage&&!activePage.isClosed())await activePage.evaluate(()=>window.stopFixturePolling?.()).catch(()=>{});await browser.close();await fetch(`${api}/stop`);await new Promise(r=>exit!==undefined?r():fixture.once('exit',r));if(out)await writeFile(`${out}/${name}-${language}-bridge.log`,log);await rm(temp,{recursive:true,force:true});}
 assert.equal(exit,0,log);
}}finally{await new Promise(r=>http.close(r));}
if(out)await writeFile(`${out}/results.json`,JSON.stringify(reports,null,2)+'\n');console.log(JSON.stringify(reports,null,2));
