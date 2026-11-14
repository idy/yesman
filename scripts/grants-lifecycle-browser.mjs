// Production page + persistent controlled bridge: no OS permissions are changed.
// Override YESMAN_TEST_WEB_DIR with a fixed old source archive for regression proof.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = process.env.YESMAN_TEST_WEB_DIR || fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_GRANT_EVIDENCE || '/tmp/yesman-grants-lifecycle';
await mkdir(out, { recursive: true });
const ids = ['bluetooth','notifications','full-disk','microphone','camera','accessibility','screen','input','automation'];
let data, reads, actions, heldReads, pending, holdReads;
function reset() {
  data = { platform: 'darwin', items: ids.map(id => ({ id, state: ['full-disk','automation'].includes(id) ? 'manual' : 'not-determined', request: !['full-disk','automation'].includes(id), settings: true, busy: false })) };
  reads = 0; actions = []; heldReads = []; pending = null; holdReads = false;
}
async function bridge(method, args) {
  switch(method.replace('Yesman.','')) {
    case 'State': return {home:'/fixture',pool:'/fixture/repos',yesmen:[],agents:[]};
    case 'Repos': case 'Clones': return [];
    case 'Fetches': return {fetching:[],errors:{}};
    case 'Settings': return {appearance:'system',language:'en',autoFetch:0};
    case 'Update': return {enabled:false};
    case 'TakeFocus': return '';
    case 'Crew': return {members:[],models:[],date:'2026-11-14',top:100};
    case 'Shortcuts': return {}; // no keys changed from their defaults, as Go says
    case 'Grants': {
      reads++;
      const snapshot = structuredClone(data);
      if (holdReads) await new Promise((resolve,reject) => heldReads.push({resolve,reject}));
      return snapshot;
    }
    case 'GrantAction': {
      actions.push(args);
      assert.equal(pending, null, 'UI dispatched a second action while native request was pending');
      const item = data.items.find(i => i.id === args[0]); item.busy = true;
      return new Promise((resolve,reject) => { pending = {resolve,reject,item}; });
    }
    default: throw Error('Unmocked '+method);
  }
}
function finish({ failure = '', state = 'granted', nativePending = false } = {}) {
  assert(pending);
  const op = pending; pending = null;
  Object.assign(op.item, {state, busy:nativePending, request:state==='not-determined'});
  if (failure) op.reject(Error(failure));
  else op.resolve({item:structuredClone(op.item),outcome:state==='granted'?'granted':'unchanged'});
}
const injection = `<script>
window.mygo={locale:'en',on:()=>{},call:async(method,...args)=>{
 const response=await fetch('/__bridge',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({method,args})});
 const value=await response.json();if(value.error)throw Error(value.error);return value.result;
}};
</script>`;
const server = createServer(async(req,res) => {
  try {
    if (req.url === '/__bridge') {
      let text='';for await(const chunk of req)text+=chunk;
      const {method,args}=JSON.parse(text);
      let reply;try{reply={result:await bridge(method,args)};}catch(err){reply={error:err.message};}
      res.setHeader('Content-Type','application/json');res.end(JSON.stringify(reply));return;
    }
    const path = req.url === '/' ? 'index.html' : req.url.slice(1);
    if (path.includes('..')) throw Error('path');
    let body = await readFile(root+'/'+path);
    if(path==='index.html')body=body.toString().replace('<head>','<head>'+injection);
    res.setHeader('Content-Type',path.endsWith('.html')?'text/html':path.endsWith('.js')?'text/javascript':path.endsWith('.css')?'text/css':'application/octet-stream');res.end(body);
  } catch {res.statusCode=404;res.end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const url='http://127.0.0.1:'+server.address().port;
const results=[];
const until=async(test)=>{for(let i=0;i<200;i++){if(test())return;await new Promise(r=>setTimeout(r,10));}assert.fail('fixture did not reach expected state');};
try {
  for(const [engineName,engine] of [['chromium',chromium],['webkit',webkit]]){
    const browser=await engine.launch({headless:true});
    let page;
    const errors=[];
    const open=async()=>{
      page=await browser.newPage({viewport:{width:1180,height:900}});
      page.on('pageerror',err=>errors.push(err.message));
      await page.goto(url);await page.locator('#settings-button').click();
      await page.locator('#settings-nav [data-page="grant"]').click();
      await page.locator('[data-permission="camera"]').waitFor();await ready();
    };
    const ready=()=>page.waitForFunction(()=>document.querySelector('[data-grant-focus="refresh"]')?.getAttribute('aria-disabled')==='false');
    const refresh=()=>page.locator('[data-grant-focus="refresh"]');
    const camera=()=>page.locator('[data-permission="camera"]');
    const request=()=>camera().locator('[data-grant-action="request"]');
    const leave=()=>page.locator('#settings-nav [data-page="general"]').click();
    const enter=()=>page.locator('#settings-nav [data-page="grant"]').click();
    const state=()=>camera().locator('.grant-state').getAttribute('data-state');
    try {
      reset();await open();
      await request().click();await until(()=>!!pending);await leave();await enter();
      await page.waitForTimeout(250);
      const returnState={rows:await page.locator('[data-permission]').count(),refreshDisabled:await refresh().getAttribute('aria-disabled')};
      await page.screenshot({path:out+'/'+engineName+'-return-pending.png'});
      assert.equal(returnState.rows,9,'return during a pending action must read and display all permissions');
      await ready();assert.equal(await camera().locator('.grant-pending').count(),1);
      assert.equal(await request().getAttribute('aria-disabled'),'true');
      await request().click({force:true});assert.equal(actions.length,1);
      // Many refresh/focus events coalesce to one read. Action finishes while
      // its pre-completion snapshot is in flight; that snapshot must be retired.
      holdReads=true;await refresh().click();await until(()=>heldReads.length===1);
      const before=reads;
      await page.evaluate(()=>{for(let i=0;i<20;i++){document.querySelector('[data-grant-focus="refresh"]').click();window.dispatchEvent(new Event('focus'));}});
      assert.equal(reads,before);
      finish();await page.waitForTimeout(50);
      heldReads[0].resolve();await until(()=>heldReads.length===2);
      assert.equal(reads,before+1,'completion must schedule one genuinely fresh read');
      heldReads[1].resolve();holdReads=false;await ready();
      assert.equal(await state(),'granted');assert.equal(await camera().locator('.grant-pending').count(),0);
      assert.equal(await page.locator('.grant-feedback').count(),0,'old visit feedback must not return');
      // On the same visit a stale successful OR failed read cannot erase the
      // authoritative action result while its queued fresh read is still held.
      for(const staleFailure of [false,true]){
        data.items.find(i=>i.id==='camera').state='not-determined';data.items.find(i=>i.id==='camera').request=true;
        await refresh().click();await ready();await request().click();await until(()=>!!pending);
        heldReads=[];holdReads=true;await refresh().click();await until(()=>heldReads.length===1);
        finish();await page.waitForFunction(()=>document.querySelector('[data-permission="camera"] .grant-state').dataset.state==='granted');
        if(staleFailure)heldReads[0].reject(Error('stale query failure'));else heldReads[0].resolve();
        await until(()=>heldReads.length===2);
        assert.equal(await state(),'granted');assert.equal(await page.locator('.grant-page > [role="alert"]').count(),0);
        heldReads[1].resolve();holdReads=false;await ready();assert.equal(await state(),'granted');
      }
      // Failure/timeout on an old visit still invalidates an in-flight read.
      // The OS can retain its prompt after a caller times out: report busy,
      // permit reads, and keep requests blocked until a later true snapshot.
      for(const failure of ['Synthetic native failure','context deadline exceeded']){
        Object.assign(data.items.find(i=>i.id==='camera'),{state:'not-determined',request:true});
        await refresh().click();await ready();await request().click();await until(()=>!!pending);
        await leave();await enter();await ready();
        heldReads=[];holdReads=true;await refresh().click();await until(()=>heldReads.length===1);
        finish({failure,state:'not-determined',nativePending:true});await page.waitForTimeout(50);
        heldReads[0].resolve();await until(()=>heldReads.length===2);heldReads[1].resolve();holdReads=false;await ready();
        assert.equal(await page.locator('.grant-feedback').count(),0);assert.equal(await request().getAttribute('aria-disabled'),'true');
        Object.assign(data.items.find(i=>i.id==='camera'),{state:'granted',request:false,busy:false});
        await page.evaluate(()=>window.dispatchEvent(new Event('focus')));await ready();assert.equal(await state(),'granted');
      }
      // An older visit's read cannot clear a newer read/dirty marker after a
      // second leave/return; every response is explicitly released by index.
      Object.assign(data.items.find(i=>i.id==='camera'),{state:'not-determined',request:true});
      await refresh().click();await ready();await request().click();await until(()=>!!pending);
      heldReads=[];holdReads=true;await refresh().click();await until(()=>heldReads.length===1);
      await leave();await enter();await until(()=>heldReads.length===2);
      finish();await page.waitForTimeout(50);heldReads[0].resolve();await page.waitForTimeout(50);
      assert.equal(await refresh().getAttribute('aria-disabled'),'true');assert.equal(heldReads.length,2);
      heldReads[1].resolve();await until(()=>heldReads.length===3);heldReads[2].resolve();holdReads=false;await ready();assert.equal(await state(),'granted');
      // Close/reopen the actual page while the native fixture retains its
      // request. The new JS instance reads busy and never consumes old feedback.
      Object.assign(data.items.find(i=>i.id==='camera'),{state:'not-determined',request:true});
      await refresh().click();await ready();await request().click();await until(()=>!!pending);
      await page.close();await open();assert.equal(await request().getAttribute('aria-disabled'),'true');
      finish();await page.evaluate(()=>window.dispatchEvent(new Event('focus')));await ready();
      assert.equal(await state(),'granted');assert.equal(await page.locator('.grant-feedback').count(),0);
      assert.deepEqual(errors,[]);
      results.push({engine:engineName,passed:true,returnState,actions:actions.length,reads});
    }catch(err){results.push({engine:engineName,passed:false,error:err.stack});}
    finally{if(pending)finish();for(const read of heldReads)read.resolve();await browser.close();}
  }
}finally{await new Promise(resolve=>server.close(resolve));}
await writeFile(out+'/results.json',JSON.stringify(results,null,2));
console.log(JSON.stringify(results,null,2));
assert(results.every(r=>r.passed),'Grant lifecycle regression failed; see results.json');
