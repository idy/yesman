// Full production page + controlled MyGo bridge, using only synthetic files.
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/task-log-browser.mjs
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_TASKLOG_EVIDENCE || '/tmp/t68-browser';
await mkdir(out, { recursive: true });
const bridge = `<script>
window.calls=[];window.unmocked=[];window.menuAction='';window.delayed={};window.releases={};
const handlers={}; const toc='.yesman/task-log.md';
const hash='a'.repeat(64); const legacy='.yesman/task-log/legacy-'+hash+'-0001.md';
window.filesFixture={
 [toc]:'# Working log\\n\\n<!-- yesman-task-log-toc v1 -->\\n\\n[Picker](task-log/2026-01-15-t3-picker.md#validation)\\n\\n[Legacy](task-log/legacy-'+hash+'-0001.md)\\n\\n[Original](task-log/legacy-'+hash+'-original.md)\\n\\n[Empty](task-log/empty.md)\\n\\n[Missing](task-log/missing.md)\\n\\n[Large](task-log/large.md)\\n',
 '.yesman/task-log/2026-01-15-t3-picker.md':'# Picker detail\\n\\n[Contents](../task-log.md)\\n\\n[Jump](#validation)\\n\\n[Plain](../../notes.md)\\n\\n[Backup](../../notes.md.bak)\\n\\n## Validation\\n\\nFull ref c1047a2a8e338698e96c31fd5dd41dc569475539\\n\\n[External](https://example.com/evidence)\\n',
 [legacy]:'# Legacy slice\\n\\n[Knowledge](knowledge/rules.md#durable)\\n',
 ['.yesman/task-log/legacy-'+hash+'-original.md']:'# Legacy original\\n\\n## Old heading\\n\\nOriginal evidence and [Knowledge](knowledge/rules.md#durable)\\n',
 '.yesman/knowledge/INDEX.md':'# Knowledge\\n[Rules](rules.md)',
 '.yesman/knowledge/rules.md':'# Durable\\nRules only',
 '.yesman/task-log/资料/旧 记录.md':'# Existing custom history\\n\\n[Contents](../../task-log.md)',
 '.yesman/task-log/empty.md':'',
 '.yesman/task-log/large.md':'# Large preview\\n'+('bounded preview line\\n'.repeat(26000)),
 'notes.md':'# Plain file\\nEdit me',
 'notes.md.bak':'backup text'
};
const recordPath='.yesman/task-log/2026-01-15-t3-picker.md';
const nl=String.fromCharCode(10);
filesFixture[recordPath]=filesFixture[recordPath].replace('## Validation',('Synthetic historical paragraph'+nl+nl).repeat(160)+'## Validation')+nl+nl+'[Top](#picker-detail)'+nl+nl+('Later historical evidence paragraph'+nl+nl).repeat(180);
filesFixture[toc]+=nl+'[Custom history](task-log/'+encodeURIComponent('资料')+'/'+encodeURIComponent('旧 记录.md')+')'+nl;
filesFixture[toc]+=nl+Array.from({length:100},(_,i)=>'- [Record '+i+'](task-log/2026-01-15-t3-picker.md) — historical result '+i).join(nl)+nl;
const agents=['alice','bob'].map(name=>({id:name+'/lead',name:'lead',yesman:name,role:'lead',status:'idle'}));
window.mygo={locale:'en',on:(name,fn)=>handlers[name]=fn,call:async(method,...args)=>{
 const m=method.replace('Yesman.','');calls.push([m,...args]);
 switch(m){
 case 'State':return {home:'/synthetic',pool:'/synthetic/repos',yesmen:['alice','bob'].map(name=>({name,path:'/synthetic/'+name,worktrees:[]})),agents};
 case 'Settings':return {appearance:'system',autoFetch:0};
 case 'Shortcuts':return {};
 case 'Context':return {agent:args[0],available:true};
 case 'Usage':return {bytes:0,files:0,parts:[],measuredAt:'2026-01-15T00:00:00Z'};
 case 'Repos':case 'Clones':case 'Pulls':return [];
 case 'Fetches':return {fetching:[],errors:{}};
 case 'TakeFocus':return '';case 'Update':return {enabled:false};
 case 'Messages':return {messages:[]};case 'Models':return {models:[],agent:agents.find(a=>a.id===args[0])};
 case 'Info':return {knowledge:{notes:1,index:true},worktrees:[]};case 'Board':return {tasks:[]};
 case 'WatchFiles':case 'Reveal':case 'OpenURL':return;
 case 'FilesMenu':return menuAction;
 case 'ReadFile':{
   const [name,path]=args;
   const text=name==='bob'&&path===toc?'# Bob TOC\\nOnly Bob':filesFixture[path];
   if(delayed[name+':'+path])await new Promise(r=>releases[name+':'+path]=r);
   if(text===undefined)throw Error('no such file: '+path);
   return {path,kind:'text',text,size:path.endsWith('/large.md')?6*1024*1024:text.length,hash:'fixture-'+text.length,truncated:path.endsWith('/large.md')};
 }
 case 'WriteFile':filesFixture[args[1]]=args[2];return {path:args[1],kind:'text',text:args[2],hash:'saved',size:args[2].length};
 case 'ListFiles':{
   const dir=args[1]==='.'?'':args[1]+'/';const paths=Object.keys(filesFixture).filter(p=>p.startsWith(dir));const map=new Map();
   for(const p of paths){const rest=p.slice(dir.length),base=rest.split('/')[0],full=dir+base;map.set(full,{name:base,path:full,dir:rest.includes('/')});}return [...map.values()];
 }
 default:unmocked.push(m);throw Error('Unmocked '+m);
 }
}};
window.emitFile=(path)=>handlers.files({yesman:'alice',paths:[path]});
window.setFixtureLocale=(locale)=>handlers['mygo:locale']({locale});
</script>`;
const server = createServer(async (req,res)=>{
 try {
  const path=req.url.split('?')[0]==='/'?'index.html':req.url.slice(1).split('?')[0];
  if(path.includes('..'))throw Error('invalid path');
  let b=await readFile(join(root,path));
  res.setHeader('Content-Type',path.endsWith('.html')?'text/html':path.endsWith('.js')?'text/javascript':path.endsWith('.css')?'text/css':'application/octet-stream');
  if(path==='index.html')b=b.toString().replace('<head>','<head>'+bridge);
  res.end(b);
 }catch{res.statusCode=404;res.end();}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const url='http://127.0.0.1:'+server.address().port;
const report=[];
try {
 for(const [name,engine] of [['chromium',chromium],['webkit',webkit]]){
  const browser=await engine.launch({headless:true});
  try {
   const page=await browser.newPage({viewport:{width:1360,height:900}});
   const errors=[];page.on('pageerror',e=>errors.push(e.message));
   await page.goto(url);
   await page.locator('[data-agent="alice/lead"]').click();
   const files=page.locator('#files');
   assert.equal(await files.isVisible(),false);
   assert.equal(await page.evaluate(()=>location.hash),'');
   await page.locator('#dock textarea').fill('Keep the unsent draft');
   // Back to a no-log entry closes only this reader, including an in-flight
   // read. Its late result must not reopen it; Forward explicitly can.
   const toc='.yesman/task-log.md';
   await page.evaluate(path=>{delayed['alice:'+path]=true;},toc);
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   await page.waitForFunction(path=>releases['alice:'+path],toc);
   await page.goBack();await page.waitForFunction(()=>location.hash==='');
   assert.equal(await files.isVisible(),false);
   await page.evaluate(async path=>{delayed['alice:'+path]=false;releases['alice:'+path]();await new Promise(requestAnimationFrame);},toc);
   assert.equal(await files.isVisible(),false,'late log read cannot reclaim a no-route view');
   await page.goForward();
   await files.getByRole('link',{name:'Picker',exact:true}).waitFor();
   // A different pane and its conversation draft keep ownership when Back
   // leaves the log URL. Forward still opens the explicit log destination.
   await page.locator('[data-info-entry="needs-you"]').click();
   await page.getByRole('button',{name:'Close Needs You',exact:true}).waitFor();
   await page.goBack();await page.waitForFunction(()=>location.hash==='');
   assert.equal(await page.getByRole('button',{name:'Close Needs You',exact:true}).isVisible(),true);
   assert.equal(await page.locator('#dock textarea').inputValue(),'Keep the unsent draft');
   await page.goForward();await files.getByRole('link',{name:'Picker',exact:true}).waitFor();
   assert.equal(await files.getByRole('button',{name:'Source',exact:true}).count(),0);
   assert.equal(await files.getByText('<!-- yesman-task-log-toc v1 -->',{exact:true}).count(),0);
   await files.getByRole('link',{name:'Picker',exact:true}).click();
   await files.locator('#validation').waitFor();
   assert.equal(await page.locator('[data-info-entry="working-logs"]').getAttribute('aria-current'),'page');
   assert.deepEqual(await page.locator('[data-info-entry]').evaluateAll(es=>es.map(e=>e.dataset.infoEntry)),['knowledge','working-logs','needs-you','topics']);
   await page.locator('[data-info-entry="needs-you"]').focus();await page.keyboard.press('Enter');
   await page.getByRole('button',{name:'Close Needs You',exact:true}).waitFor();
   assert.equal(await page.locator('[data-info-entry="needs-you"]').getAttribute('aria-current'),'page');
   await page.getByRole('button',{name:'Close Needs You',exact:true}).click();
   assert.equal(await page.evaluate(()=>document.activeElement.dataset.infoEntry),'needs-you');
   await page.locator('[data-info-entry="working-logs"]').focus();await page.keyboard.press('Enter');
   await files.getByRole('link',{name:'Picker',exact:true}).click();await files.locator('#validation').waitFor();
   await page.keyboard.press(process.platform==='darwin'?'Meta+k':'Control+k');
   await page.locator('.palette-dialog').waitFor();
   assert.ok(await page.locator('.palette-dialog').getByText('Open Working Logs',{exact:true}).count());
   await page.keyboard.press('Escape');
   assert.equal(await page.evaluate(()=>document.activeElement.id),'validation');
   assert.match(page.url(),/#task-log=/);
   // An anchored history entry stores where the reader actually scrolled,
   // while a fresh click on that same fragment still jumps to its heading.
   await page.evaluate(()=>new Promise(requestAnimationFrame));
   const anchorTop=await files.locator('#files-view').evaluate(e=>e.scrollTop);
   await files.locator('#files-view').hover();await page.mouse.wheel(0,430);
   await page.waitForFunction(initial=>document.getElementById('files-view').scrollTop>initial+300,anchorTop);
   await page.waitForFunction(()=>history.state?.workingLog?.scroll===document.getElementById('files-view').scrollTop);
   const anchoredPosition=await files.locator('#files-view').evaluate(e=>e.scrollTop);
   await files.getByRole('button',{name:'Contents',exact:true}).click();
   await files.getByRole('link',{name:'Picker',exact:true}).waitFor();
   await page.goBack();await files.locator('#validation').waitFor();
   assert.ok(Math.abs((await files.locator('#files-view').evaluate(e=>e.scrollTop))-anchoredPosition)<2,'Back restores the anchored reading position');
   assert.equal(await page.evaluate(()=>document.activeElement.id),'validation');
   await files.getByRole('link',{name:'Jump',exact:true}).click();
   assert.ok(Math.abs((await files.locator('#files-view').evaluate(e=>e.scrollTop))-anchorTop)<2,'fresh fragment click jumps to heading');
   await files.locator('.files-crumb--scope').click();
   assert.equal(await page.locator('.files-menu [role="menuitem"][aria-current="true"]').count(),1);
   await page.keyboard.press('Escape');
   // A stale read of this same record must not replace a newer reload.
   const part = '.yesman/task-log/2026-01-15-t3-picker.md';
   await page.evaluate((path)=>{window.initialDetail=filesFixture[path];filesFixture[path]='# Older response';delayed['alice:'+path]=true;},part);
   await files.getByRole('button',{name:'Reload',exact:true}).click();
   await page.waitForFunction((path)=>releases['alice:'+path],part);
   await page.evaluate((path)=>{window.oldRead=releases['alice:'+path];delayed['alice:'+path]=false;filesFixture[path]=['# Newest response','## Validation'].join(String.fromCharCode(10));},part);
   await files.getByRole('button',{name:'Reload',exact:true}).click();
   await files.getByText('Newest response',{exact:true}).waitFor();
   await page.evaluate(async()=>{oldRead();await new Promise(requestAnimationFrame);});
   assert.equal(await files.getByText('Newest response',{exact:true}).count(),1);
   await page.evaluate((path)=>{filesFixture[path]=initialDetail;emitFile(path);},part);
   await files.getByRole('link',{name:'Plain',exact:true}).waitFor();
   await page.screenshot({path:join(out,name+'-detail.png')});
   await page.goBack();await files.getByRole('link',{name:'Picker',exact:true}).waitFor();
   await page.goForward();await files.locator('#validation').waitFor();
   await page.reload();await files.locator('#validation').waitFor();
   assert.equal(await page.evaluate(()=>document.activeElement.id),'validation');
   const assertHeadingVisible = async (id) => {
    const heading=await files.locator('#'+id).boundingBox();const view=await files.locator('#files-view').boundingBox();
    assert.ok(heading.y>=view.y-1&&heading.y<view.y+view.height,JSON.stringify({id,heading,view}));
   };
   await files.getByRole('link',{name:'Top',exact:true}).click();
   await assertHeadingVisible('picker-detail');
   await files.getByRole('link',{name:'Jump',exact:true}).click();
   await assertHeadingVisible('validation');
   await page.goBack();await assertHeadingVisible('picker-detail');
   await page.goForward();await assertHeadingVisible('validation');
   await files.getByRole('button',{name:'Reload',exact:true}).click();
   await files.getByRole('link',{name:'Contents',exact:true}).click();
   await files.getByRole('link',{name:'Legacy',exact:true}).click();
   await files.getByRole('link',{name:'Knowledge',exact:true}).click();
   await files.locator('#durable').waitFor();
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='ReadFile').at(-1)[2]),'.yesman/knowledge/rules.md');
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   await files.getByRole('link',{name:'Picker',exact:true}).click();
   await files.getByRole('link',{name:'Plain',exact:true}).click();
   await files.getByRole('button',{name:'Source',exact:true}).click();
   await files.locator('.cm-content').waitFor();
   await files.locator('.cm-content').fill('# Edited plain');
   await page.keyboard.press(process.platform==='darwin'?'Meta+s':'Control+s');
   await page.waitForFunction(()=>calls.some(c=>c[0]==='WriteFile'&&c[2]==='notes.md'&&c[3].includes('Edited plain')));
   // Browser history without a log destination never closes ordinary Files.
   await page.evaluate(()=>history.pushState({ordinaryFixture:true},'','#ordinary-reading'));
   await page.goBack();await page.waitForFunction(()=>location.hash==='');
   assert.equal(await files.locator('.cm-content').innerText(),'# Edited plain');
   assert.equal(await files.isVisible(),true);
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   await files.getByRole('link',{name:'Picker',exact:true}).click();
   await files.getByRole('link',{name:'Backup',exact:true}).click();
   await files.locator('.cm-content').waitFor();await files.locator('.cm-content').fill('edited backup');
   await page.keyboard.press(process.platform==='darwin'?'Meta+s':'Control+s');
   await page.waitForFunction(()=>calls.some(c=>c[0]==='WriteFile'&&c[2]==='notes.md.bak'&&c[3]==='edited backup'));
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   await files.getByRole('link',{name:'Custom history',exact:true}).click();
   await files.getByText('Existing custom history',{exact:true}).waitFor();
   assert.equal(await files.getByRole('button',{name:'Source',exact:true}).count(),0);
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='ReadFile').at(-1)[2]),'.yesman/task-log/资料/旧 记录.md');
   await files.getByRole('link',{name:'Contents',exact:true}).click();
   await files.getByRole('link',{name:'Empty',exact:true}).click();
   await files.locator('article.md').waitFor();assert.equal(await files.locator('article.md').innerText(),'');
   assert.equal(await files.getByRole('button',{name:'Source',exact:true}).count(),0);
   await files.getByRole('button',{name:'Contents',exact:true}).click();await files.getByRole('link',{name:'Missing',exact:true}).click();
   await files.getByText('This file is gone',{exact:true}).waitFor();
   await files.getByRole('button',{name:'Contents',exact:true}).click();await files.getByRole('link',{name:'Large',exact:true}).click();
   await files.getByText(/Showing the first/).waitFor();
   await files.getByRole('button',{name:'Contents',exact:true}).click();
   await page.evaluate(()=>{delayed['alice:.yesman/task-log/2026-01-15-t3-picker.md']=true;});
   await files.getByRole('link',{name:'Picker',exact:true}).click();
   await page.waitForFunction(()=>releases['alice:.yesman/task-log/2026-01-15-t3-picker.md']);
   await page.locator('[data-agent="bob/lead"]').click();
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   await files.getByText('Only Bob',{exact:true}).waitFor();
   await page.evaluate(()=>{delayed['alice:.yesman/task-log/2026-01-15-t3-picker.md']=false;releases['alice:.yesman/task-log/2026-01-15-t3-picker.md']();});
   assert.equal(await files.getByText('Only Bob',{exact:true}).count(),1);
   await page.reload();
   await files.getByText('Only Bob',{exact:true}).waitFor();
   assert.equal(await page.locator('[data-agent="bob/lead"]').getAttribute('aria-current'),'page');
   await page.goBack();await files.getByRole('link',{name:'Plain',exact:true}).waitFor();
   await page.goForward();await files.getByText('Only Bob',{exact:true}).waitFor();
   await page.locator('[data-agent="alice/lead"]').click();
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   // Old TOC anchors receive a visible path to the unmodified original.
   await page.goto(url+'/#task-log='+encodeURIComponent(JSON.stringify({name:'alice',path:'.yesman/task-log.md',anchor:'old-heading'})));
   await files.getByRole('button',{name:'Complete original',exact:true}).click();
   await files.locator('#old-heading').waitFor();
   await files.getByRole('button',{name:'Close the files',exact:true}).click();
   await page.getByRole('button',{name:'Working Logs',exact:true}).click();
   await files.getByRole('link',{name:'Picker',exact:true}).waitFor();
   const toc85=files.getByRole('link',{name:'Record 85',exact:true});
   await toc85.scrollIntoViewIfNeeded();
   const tocPosition=await files.locator('#files-view').evaluate(e=>e.scrollTop);
   await toc85.click();await files.locator('#picker-detail').waitFor();
   await page.goBack();await files.getByRole('link',{name:'Record 85',exact:true}).waitFor();
   assert.ok(Math.abs((await files.locator('#files-view').evaluate(e=>e.scrollTop))-tocPosition)<2,'Back restores TOC position');
   await toc85.click();await files.locator('#picker-detail').waitFor();
   await files.getByRole('button',{name:'Contents',exact:true}).click();await toc85.waitFor();
   assert.ok(Math.abs((await files.locator('#files-view').evaluate(e=>e.scrollTop))-tocPosition)<2,'Contents restores TOC position');
   await files.getByRole('link',{name:'Picker',exact:true}).click();await assertHeadingVisible('validation');
   await files.getByRole('button',{name:'Contents',exact:true}).click();
   await page.evaluate(()=>menuAction='reveal');await files.getByRole('button',{name:'More',exact:true}).click();
   await page.waitForFunction(()=>calls.some(c=>c[0]==='Reveal'&&c[1]==='/synthetic/alice'));
   for(const [locale,dark,width] of [['en',false,900],['en',true,480],['zh-Hans',false,900],['zh-Hans',true,480]]){
    await page.setViewportSize({width,height:850});
    await page.evaluate(({locale,dark})=>{document.documentElement.dataset.theme=dark?'dark':'light';window.setFixtureLocale(locale);}, {locale,dark});
    assert.equal(await page.locator('html').getAttribute('lang'), locale);
    assert.equal(await page.getByRole('button',{name:locale==='zh-Hans'?'工作日志':'Working Logs',exact:true}).count(),1);
    assert.equal(await page.getByRole('button',{name:'Task log',exact:true}).count(),0);
    const reload = files.getByRole('button',{name:locale==='zh-Hans'?'重新载入':'Reload',exact:true});
    await reload.focus(); assert.equal(await reload.evaluate(e=>e===document.activeElement),true);
    const bounds = await files.locator('article.md').boundingBox();
    assert.ok(bounds.width > Math.min(width - 80, 480), JSON.stringify({width,...bounds}));
    assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width + 1, JSON.stringify(bounds));
    const navBounds = await reload.boundingBox();
    assert.ok(navBounds.x >= 0 && navBounds.x + navBounds.width <= width + 1);
    await page.screenshot({path:join(out,name+'-'+locale+'-'+width+'.png')});
   }
   assert.deepEqual(errors,[]);
   assert.deepEqual(await page.evaluate(()=>unmocked),[]);
   assert.equal((await page.locator('body').innerText()).includes('NaN'),false,'fixture DTOs render valid values');
   assert.equal(await page.evaluate(()=>calls.some(c=>c[0]==='WriteFile'&&c[2].startsWith('.yesman/task-log'))),false);
   report.push({engine:name,pass:true,readCalls:await page.evaluate(()=>calls.filter(c=>c[0]==='ReadFile').length),scope:'production DOM; controlled bridge, synthetic files; Go filesystem tested separately'});
  } finally {await browser.close();}
 }
} finally {await new Promise(r=>server.close(r));await writeFile(join(out,'results.json'),JSON.stringify(report,null,2));}
console.log(JSON.stringify(report,null,2));
