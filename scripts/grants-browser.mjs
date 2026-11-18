// Production Settings DOM with a controlled bridge. No native prompts, settings
// changes, or user workspaces. Install Playwright outside the repository.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { permissionIds } from './grant-ids.mjs';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = process.env.YESMAN_TEST_WEB_DIR || fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_GRANT_EVIDENCE || '/tmp/yesman-grants-evidence/browser';
await mkdir(out, { recursive: true });
const bridge = `<script>
const handlers={};window.calls=[];window.unmocked=[];window.failRead=false;window.failAction=false;window.holdRead=false;window.holdAction=false;
window.fixture={platform:'darwin',os:'27.0.1',app:{path:'/Applications/yesman.app',signing:'adhoc'},items:[
 {id:'bluetooth',state:'not-determined',request:true,settings:true},
 {id:'notifications',state:'limited',detail:'notification-alerts-off',settings:true},
 {id:'full-disk',state:'manual',detail:'full-disk-manual',settings:true},
 {id:'microphone',state:'not-determined',request:true,settings:true},
 {id:'camera',state:'not-determined',request:true,settings:true},
 {id:'accessibility',state:'not-granted',detail:'adhoc-build',request:true,settings:true},
 {id:'screen',state:'not-granted',detail:'restart-may-be-needed',request:true,settings:true},
 {id:'system-audio',state:'manual',detail:'system-audio-manual',settings:true},
 {id:'input',state:'restricted',settings:true},
 {id:'automation',state:'manual',detail:'automation-manual',settings:true}
]};
window.nextState='granted';
window.mygo={locale:'en',on:(name,fn)=>handlers[name]=fn,call:async(method,...args)=>{
 const m=method.replace('Yesman.','');calls.push([m,...args]);
 switch(m){
 case 'State':return {home:'/fixture',pool:'/fixture/repos',yesmen:[],agents:[]};
 case 'Repos':case 'Clones':return [];
 case 'Fetches':return {fetching:[],errors:{}};
 case 'Settings':return {appearance:'system',language:'en',autoFetch:0};
 case 'Update':return {enabled:false};
 case 'TakeFocus':return '';
 case 'Crew':return {members:[],models:[],date:'2026-11-14',top:100};
 case 'Shortcuts':return {}; // no keys changed from their defaults, as Go says
 case 'Grants':{
  const value=structuredClone(fixture);
  if(holdRead)await new Promise(resolve=>window.resolveRead=resolve);
  if(failRead)throw Error('Synthetic query failure');
  return value;
 }
 case 'GrantAction':{
  if(holdAction)await new Promise(resolve=>window.resolveAction=resolve);
  if(failAction)throw Error('Synthetic native failure');
  const item=fixture.items.find(i=>i.id===args[0]);
  if(args[1]==='settings')return {item:structuredClone(item),outcome:'settings-opened'};
  item.state=nextState;item.request=['not-determined','not-granted'].includes(nextState);
  return {item:structuredClone(item),outcome:['granted','denied','restricted'].includes(nextState)?nextState:'unchanged'};
 }
 default:unmocked.push(m);throw Error('Unmocked method '+m);
 }
}};
window.changeLocale=lang=>{mygo.locale=lang;handlers['mygo:locale']?.({locale:lang});};
</script>`;
const server = createServer(async(req,res)=>{
 try {
  const path=req.url.split('?')[0]==='/'?'index.html':req.url.slice(1).split('?')[0];
  if(path.includes('..'))throw Error('path');
  let data=await readFile(root+path);
  if(path==='index.html')data=data.toString().replace('<head>','<head>'+bridge);
  res.setHeader('Content-Type',path.endsWith('.html')?'text/html':path.endsWith('.js')?'text/javascript':path.endsWith('.css')?'text/css':'application/octet-stream');res.end(data);
 } catch{res.statusCode=404;res.end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const results=[];
const ids=permissionIds();
// The fixture covers every source permission, in the native order.
assert.deepEqual([...bridge.matchAll(/\{id:'([a-z-]+)'/g)].map(m=>m[1]),ids);
try {
 for(const [name,engine] of [['chromium',chromium],['webkit',webkit]]){
  const browser=await engine.launch({headless:true});
  try{
   const page=await browser.newPage({viewport:{width:1180,height:900}});
   const errors=[];page.on('pageerror',e=>errors.push(e.message));
   await page.goto('http://127.0.0.1:'+server.address().port);
   await page.locator('#settings-button').click();
   await page.screenshot({path:out+'/'+name+'-before-general.png'});
   const nav=page.locator('#settings-nav [data-page="grant"]');
   await nav.focus();await nav.press('Enter');
   const row=id=>page.locator('[data-permission="'+id+'"]');
   const action=(id,kind)=>row(id).locator('[data-grant-action="'+kind+'"]');
   const refresh=()=>page.locator('[data-grant-focus="refresh"]');
   const ready=()=>page.waitForFunction(()=>document.querySelector('[data-grant-focus="refresh"]')?.getAttribute('aria-disabled')==='false');
   await row('bluetooth').waitFor();await ready();
   assert.deepEqual(await page.locator('[data-permission]').evaluateAll(rows=>rows.map(r=>r.dataset.permission)),ids);
   assert.equal(await nav.getAttribute('aria-current'),'page');
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length),0);
   assert.equal(await row('full-disk').locator('[data-grant-action="request"]').count(),0);
   assert.equal(await row('automation').locator('[data-grant-action="request"]').count(),0);
   assert.match(await row('notifications').innerText(),/Limited/);
   assert.match(await row('input').innerText(),/Restricted/);
   // System Audio Recording Only: its own row and Phosphor glyph, manual
   // status, Settings only. It is neither Microphone nor Screen Recording.
   const audio=row('system-audio');
   assert.equal(await audio.locator('h3').innerText(),'System Audio Recording Only');
   assert.equal(await audio.locator('h3 .mds-icon').getAttribute('data-icon'),'speaker');
   assert.equal(await audio.locator('h3 .mds-icon').getAttribute('data-glyph'),'\ue44a');
   assert.equal(await audio.locator('.grant-state').innerText(),'Manual · cannot detect');
   assert.match(await audio.innerText(),/no public status check[\s\S]*System Audio Recording Only/);
   assert.equal(await audio.locator('[data-grant-action="request"]').count(),0);
   assert.equal(await audio.locator('[data-grant-action="settings"]').count(),1);
   // Permission-category names do not guess the OS pane's rename version.
   // Identity and the ad-hoc build explanation remain visible.
   assert.equal(await row('screen').locator('h3').innerText(),'Screen Recording');
   assert.equal(await action('screen','settings').getAttribute('aria-label'),'Open Settings: Screen Recording');
   for(const lang of ['en','zh-Hans'])for(const os of ['13.0','14.0','14.2','14.7','15.0']){
    await page.evaluate(([lang,os])=>{fixture.os=os;changeLocale(lang);},[lang,os]);
    await refresh().click();await ready();
    assert.equal(await row('screen').locator('h3').innerText(),lang==='en'?'Screen Recording':'屏幕录制');
    assert.equal(await action('screen','settings').getAttribute('aria-label'),lang==='en'?'Open Settings: Screen Recording':'打开系统设置：屏幕录制');
   }
   await page.evaluate(()=>{fixture.os='15.0';changeLocale('en');});
   await refresh().click();await ready();
   assert.match(await page.locator('.grant-identity').innerText(),/macOS checks this copy: \/Applications\/yesman\.app \(ad-hoc signed\)/);
   assert.match(await row('accessibility').innerText(),/different build[\s\S]*cannot determine why[\s\S]*remove that entry with − and add this copy again with \+/);
   assert.equal(await row('accessibility').locator('.grant-state').innerText(),'Not granted');
   assert.equal(await row('accessibility').locator('[data-grant-action="request"]').count(),1);
   // Every settings action is a fixed ID and never changes permission state.
   for(const id of ids){
    const before=await row(id).locator('.grant-state').innerText();
    await action(id,'settings').click();
    await row(id).getByRole('status').getByText(/System Settings opened/).waitFor();
    assert.equal(await row(id).locator('.grant-state').innerText(),before);
   }
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction'&&c[2]==='request').length),0);
   // Older macOS: same permission-category name, no audio permission/Settings,
   // and no identity line for an unidentified copy.
   await page.evaluate(()=>{window.modern=structuredClone(fixture);fixture.os='14.1';delete fixture.app;Object.assign(fixture.items.find(i=>i.id==='system-audio'),{state:'not-applicable',detail:'system-audio-os',settings:false});});
   await refresh().click();await ready();
   assert.equal(await row('screen').locator('h3').innerText(),'Screen Recording');
   assert.equal(await row('system-audio').locator('.grant-state').innerText(),'Not applicable');
   assert.match(await row('system-audio').innerText(),/needs macOS 14\.2 or later/);
   assert.equal(await row('system-audio').locator('button').count(),0);
   assert.equal(await page.locator('.grant-identity').count(),0);
   await page.evaluate(()=>{fixture=modern;});
   await refresh().click();await ready();
   await page.evaluate(()=>{for(const id of ['notifications','input']){const item=fixture.items.find(i=>i.id===id);item.state='not-determined';item.request=true;}});
   await refresh().click();await ready();
   // Denial, restriction and unresolved/cancelled system response stay honest.
   for(const [id,state] of [['notifications','denied'],['input','denied'],['microphone','denied'],['camera','restricted'],['screen','not-granted']]){
    await page.evaluate(state=>nextState=state,state);
    await action(id,'request').click();
    await page.waitForFunction(([id,state])=>document.querySelector('[data-permission="'+id+'"] .grant-state')?.dataset.state===state,[id,state]);
    await ready();
   }
   // A pending click cannot launch a second request. Keyboard focus survives.
   await page.evaluate(()=>{holdAction=true;nextState='granted';});
   await action('bluetooth','request').focus();await action('bluetooth','request').press('Enter');
   await page.waitForFunction(()=>!!window.resolveAction);
   const before=await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length);
   await action('bluetooth','request').press('Enter');
   await action('accessibility','request').click({force:true});
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length),before);
   await action('bluetooth','request').focus();
   await page.evaluate(()=>{holdAction=false;resolveAction();});await ready();
   assert.equal(await row('bluetooth').locator('.grant-state').innerText(),'Granted');
   assert.equal(await page.evaluate(()=>document.activeElement.dataset.grantFocus),'bluetooth-settings');
   // Inline failures can be retried; opening Settings is never a success grant.
   await page.evaluate(()=>failAction=true);await action('notifications','settings').click();
   await row('notifications').getByRole('alert').waitFor();
   assert.match(await row('notifications').innerText(),/Synthetic native failure/);
   await page.evaluate(()=>failAction=false);await action('notifications','settings').click();
   await row('notifications').getByRole('status').waitFor();
   // Refresh failure clears old positive states, retry restores actual data.
   await page.evaluate(()=>failRead=true);await refresh().click();
   await page.locator('.grant-page > [role="alert"]').waitFor();
   assert.equal(await page.locator('[data-permission]').count(),0);
   await page.evaluate(()=>failRead=false);await refresh().click();await row('bluetooth').waitFor();await ready();
   // A late read from a previous visit cannot overwrite a newer visit.
   await page.evaluate(()=>{holdRead=true;});await refresh().click();
   await page.waitForFunction(()=>!!window.resolveRead);
   await page.locator('#settings-nav [data-page="general"]').click();
   await page.evaluate(()=>{holdRead=false;fixture.items[0].state='denied';});
   await nav.click();await row('bluetooth').waitFor();await ready();
   await page.evaluate(()=>resolveRead());
   assert.equal(await row('bluetooth').locator('.grant-state').innerText(),'Denied');
   // A late request from a closed tab carries no feedback to the new visit.
   await page.evaluate(()=>{holdAction=true;nextState='granted';});
   await action('accessibility','request').click();
   await page.locator('#settings-nav [data-page="general"]').click();
   await page.evaluate(()=>{holdAction=false;resolveAction();});
   assert.equal(await page.locator('.grant-page').count(),0);
   await nav.click();await row('accessibility').waitFor();await ready();
   assert.equal(await page.locator('.grant-feedback').count(),0);
   // Returning from another app refreshes without a request or focus stealing.
   const requests=await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length);
   await nav.focus();await page.evaluate(()=>{fixture.items[0].state='restricted';window.dispatchEvent(new Event('focus'));});
   await page.waitForFunction(()=>document.querySelector('[data-permission="bluetooth"] .grant-state').dataset.state==='restricted');
   assert.equal(await page.evaluate(()=>document.activeElement.dataset.page),'grant');
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length),requests);
   // Per-item query errors must not paint a granted status.
   await page.evaluate(()=>{fixture.items[1].state='granted';fixture.items[1].error='Synthetic per-item error';});
   await refresh().click();await ready();
   assert.equal(await row('notifications').locator('.grant-state').innerText(),'Unknown');
   await page.evaluate(()=>{delete fixture.items[1].error;fixture.items[1].state='limited';});await refresh().click();await ready();
   // Native pending requests survive a closed/timed-out caller; they prevent
   // further prompts but still allow manual settings and read-only refresh.
   await page.evaluate(()=>{fixture.items.find(i=>i.id==='screen').busy=true;});await refresh().click();await ready();
   const actionCount=await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length);
   assert.equal(await action('screen','request').getAttribute('aria-disabled'),'true');
   await action('screen','request').click({force:true});
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='GrantAction').length),actionCount);
   await page.evaluate(()=>{fixture.items.find(i=>i.id==='screen').busy=false;});await refresh().click();await ready();
   // Cross-platform data never invents a per-app grant or Linux settings URI.
   await page.evaluate(()=>window.macFixture=structuredClone(fixture));
   for(const platform of ['windows','linux']){
    await page.evaluate(platform=>{fixture.platform=platform;delete fixture.os;delete fixture.app;fixture.items=macFixture.items.map(item=>({id:item.id,state:platform==='windows'&&['full-disk','accessibility','screen','system-audio','input','automation'].includes(item.id)?'not-applicable':'system',detail:platform==='windows'?'windows-system':'linux-system',settings:platform==='windows'&&['bluetooth','notifications','camera','microphone'].includes(item.id)}));},platform);
    await refresh().click();await ready();
    assert.equal(await page.locator('[data-grant-action="request"]').count(),0);
    assert.equal(await page.locator('[data-grant-action="settings"]').count(),platform==='windows'?4:0);
    assert.equal(await page.locator('.grant-state[data-state="granted"]').count(),0);
    assert.equal(await page.locator('.grant-identity').count(),0);
    assert.equal(await row('screen').locator('h3').innerText(),'Screen Recording');
    await page.screenshot({path:out+'/'+name+'-'+platform+'.png'});
   }
   await page.evaluate(()=>fixture=macFixture);await refresh().click();await ready();
   // Real production typography, themes, translation, wrapping, and controls.
   for(const lang of ['en','zh-Hans'])for(const theme of ['light','dark'])for(const width of [1180,760,640,480])for(const pathCase of (width===480?['short','long']:['short'])){
    // A legal unspaced 209-byte directory component must wrap without moving
    // Refresh or permission actions outside the narrow Settings pane.
    const appPath=pathCase==='long'?'/Applications/'+'DerivedData_'.repeat(17)+'build/yesman.app':'/Applications/yesman.app';
    await page.setViewportSize({width,height:900});
    await page.evaluate(([lang,theme,appPath])=>{fixture.app.path=appPath;changeLocale(lang);document.documentElement.dataset.theme=theme;},[lang,theme,appPath]);
    await refresh().click();await ready();
    await row('full-disk').waitFor();
    await page.waitForTimeout(250); // allow the existing Settings drawer transition to settle
    assert(!/null|undefined/.test(await page.locator('.grant-page').innerText()));
    await page.evaluate(()=>document.querySelector('.page').scrollTop=0);
    const geometry=await page.locator('.grant-page').evaluate(el=>({width:el.clientWidth,scroll:el.scrollWidth,pageWidth:el.closest('.page').clientWidth,pageScroll:el.closest('.page').scrollWidth,rows:[...el.querySelectorAll('.grant-row')].map(r=>({id:r.dataset.permission,width:r.clientWidth,scroll:r.scrollWidth}))}));
    assert(geometry.scroll<=geometry.width+1,JSON.stringify({name,lang,theme,width,pathCase,geometry}));
    assert(geometry.pageScroll<=geometry.pageWidth+1,JSON.stringify({name,lang,theme,width,pathCase,geometry}));
    assert((await page.locator('.grant-identity').innerText()).includes(appPath),'complete literal app path is preserved');
    assert.equal(await action('screen','settings').getAttribute('aria-label'),lang==='en'?'Open Settings: Screen Recording':'打开系统设置：屏幕录制');
    assert(geometry.rows.every(r=>r.scroll<=r.width+1));
    assert.equal(await page.locator('.grant-toolbar h2').innerText(),lang==='en'?'Grant':'授权');
    await page.screenshot({path:out+'/'+[name,lang,theme,width,pathCase].join('-')+'.png'});
    if(pathCase==='long'){
     // Real hit-tested clicks, not forced/DOM-dispatched events.
     await refresh().click();await ready();
     const before=await row('system-audio').locator('.grant-state').innerText();
     await action('system-audio','settings').click();await ready();
     assert.equal(await row('system-audio').locator('.grant-state').innerText(),before);
    }
    const navigation=await page.locator('#settings-nav').evaluate(el=>({items:el.children.length,pages:[...el.children].map(c=>c.dataset.page),height:el.getBoundingClientRect().height,scroll:el.scrollHeight,branch:el.closest('.settings-branch').outerHTML}));
    assert.deepEqual(navigation.pages,['general','shortcuts','grant','yesman','usage','repositories','github-app','secrets','variables','storage','pi','config','about']);
    assert(navigation.height>250,JSON.stringify({name,lang,theme,width,navigation}));
    results.push({engine:name,lang,theme,width,pathCase,geometry,navigation});
   }
   // Closing the browser retires an outstanding action without page errors.
   await page.evaluate(()=>holdAction=true);
   await action('full-disk','settings').click();
   assert.deepEqual(errors,[]);
   assert.deepEqual(await page.evaluate(()=>unmocked),[]);
   await page.close();
  }finally{await browser.close();}
 }
 await writeFile(out+'/results.json',JSON.stringify(results,null,2));
 console.log('Grant: Chromium + WebKit lifecycle, actions, errors, focus, localization and 40 layout checks (including long paths) passed');
}finally{await new Promise(resolve=>server.close(resolve));}
