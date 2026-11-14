// Optional browser integration checks: install Playwright outside the worktree,
// then run YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/rename-browser.mjs.
// Uses the complete page with a synthetic bridge; never opens a real workspace.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const bridge = `<script>
const handlers = {};
window.calls = [];
window.menuAction = 'rename';
window.failRename = false;
window.repoFixture = false;
const sharedRepo = {name:'github.com/synthetic/shared',url:'https://github.com/synthetic/shared'};
const agents = [
 {id:'alice/lead',name:'lead',yesman:'alice',role:'lead',status:'idle'},
 {id:'alice/worker-one',name:'worker-one',yesman:'alice',role:'worker',number:1,title:'Synthetic worker',status:'idle'}
];
window.mygo = {locale:'en',on:(name,fn)=>handlers[name]=fn,call:async(method,...args)=>{
 const m=method.replace('Yesman.','');calls.push([m,...args]);
 switch(m){
 case 'State': return {home:'/synthetic',pool:'/synthetic/repos',yesmen:[{name:'alice',displayName:localStorage.getItem('synthetic.label')||'',path:'/synthetic/yesmen/alice',worktrees:repoFixture?[{repo:sharedRepo.name,dir:'one'},{repo:sharedRepo.name,dir:'two'}]:[]},...(repoFixture?[{name:'bob',displayName:'项目 Alpha 团队',path:'/synthetic/yesmen/bob',worktrees:[{repo:sharedRepo.name,dir:'three'}]}]:[])],agents};
 case 'Repos':return repoFixture?[sharedRepo]:[];
 case 'Settings':return {appearance:'system',autoFetch:0};
 case 'Fetches':return {fetching:[],errors:{}};
 case 'Clones':case 'Pulls':case 'ListFiles':return [];
 case 'Messages':return {messages:[]};
 case 'TakeFocus':return '';
 case 'Update':return {enabled:false};
 case 'Models':return {models:[],agent:agents.find(a=>a.id===args[0])};
 case 'AgentMenu':return menuAction;
 case 'Rename':if(failRename)throw Error('Synthetic write failure');localStorage.setItem('synthetic.label',args[1]);return {name:'alice',displayName:args[1],path:'/synthetic/yesmen/alice'};
 case 'Info':return {knowledge:[],worktrees:[]};
 case 'Board':return {tasks:[]};
 case 'Abort':case 'Stop':case 'Send':case 'WatchFiles':return;
 default:throw Error('Unmocked method '+m);
 }
}};
window.emitChanged=()=>handlers.changed(true);
</script>`;
const server = createServer(async (req,res) => {
 try {
  const path = req.url.split('?')[0] === '/' ? 'index.html' : req.url.slice(1).split('?')[0];
  if (path.includes('..')) throw Error('invalid path');
  let body = await readFile(root + path);
  res.setHeader('Content-Type',path.endsWith('.html')?'text/html':path.endsWith('.js')?'text/javascript':path.endsWith('.css')?'text/css':'application/octet-stream');
  if(path==='index.html') body=body.toString().replace('<head>','<head>'+bridge);
  res.end(body);
 } catch {res.statusCode=404;res.end();}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
try {
 for(const [engineName,engine] of [['chromium',chromium],['webkit',webkit]]) {
  const browser=await engine.launch({headless:true});
  try {
   const page=await browser.newPage({viewport:{width:1100,height:800}});
   const errors=[];page.on('pageerror',e=>errors.push(e.message));
   await page.addInitScript(()=>localStorage.setItem('yesman.info','hidden'));
   await page.goto('http://127.0.0.1:'+server.address().port);
   const lead=page.locator('[data-agent="alice/lead"]');
   const worker=page.locator('[data-agent="alice/worker-one"]');
   const input=page.locator('#rename-form input');
   const save=page.locator('#rename-form [type="submit"]');
   const dialog=page.locator('#rename-dialog');
   await lead.waitFor();
   assert.equal(await lead.locator('.name').innerText(),'alice');
   // Context-menu cancel, Escape, and blank-name validation cause no writes.
   await lead.click({button:'right'});await input.fill('Discard this');await page.locator('#rename-cancel').click();
   await lead.click({button:'right'});assert.equal(await input.inputValue(),'alice');await input.press('Escape');
   await lead.click({button:'right'});await input.fill('   ');await save.click();
   assert.equal(await dialog.evaluate(e=>e.open),true);
   assert.match(await page.locator('#rename-error').innerText(),/Enter a name/);
   assert.equal(await page.evaluate(()=>calls.filter(c=>c[0]==='Rename').length),0);
   // A failed write leaves the label and entered text in place; retry works.
   await input.fill('项目 Alpha 团队');await page.evaluate(()=>failRename=true);await save.click();
   await page.locator('#rename-error').getByText('Synthetic write failure').waitFor();
   assert.equal(await lead.locator('.name').innerText(),'alice');
   assert.equal(await input.inputValue(),'项目 Alpha 团队');
   await page.evaluate(()=>failRename=false);await save.click();
   await page.waitForFunction(()=>!document.querySelector('#rename-dialog').open);
   assert.equal(await lead.locator('.name').innerText(),'项目 Alpha 团队');
   assert.equal(await page.locator('#crumbs').innerText(),'项目 Alpha 团队');
   assert.equal(await page.locator('.composer textarea').getAttribute('placeholder'),'Message 项目 Alpha 团队…');
   // Both conversation selection and command routing keep the stable identity.
   await worker.click();assert.match(await page.locator('#crumbs').innerText(),/项目 Alpha 团队/);
   await page.evaluate(()=>menuAction='sleep');await worker.click({button:'right'});
   assert.equal(await page.evaluate(()=>calls.some(c=>c[0]==='Stop'&&c[1]==='alice/worker-one')),true);
   await lead.click();
   await page.getByRole('button',{name:'Show the board',exact:true}).click();
   await page.locator('#board').getByText('项目 Alpha 团队 adds tasks here as it plans, and its workers keep them up to date.').waitFor();
   await page.getByRole('button',{name:'Show the files',exact:true}).click();
   await page.locator('#files [role="tree"][aria-label="项目 Alpha 团队"]').waitFor();
   await page.evaluate(()=>emitChanged());
   await page.waitForTimeout(250);
   assert.equal(await lead.locator('.name').innerText(),'项目 Alpha 团队');
   await page.reload();await lead.waitFor();
   assert.equal(await lead.locator('.name').innerText(),'项目 Alpha 团队');
   await worker.click();assert.match(await page.locator('#crumbs').innerText(),/项目 Alpha 团队/);
   await page.screenshot({path:'/tmp/yesman-rename-'+engineName+'.png'});
   // Repo users are unique by stable ID, even when two IDs share a label.
   // Alice has two worktrees and must appear once; Bob adds a second user.
   await page.evaluate(()=>{repoFixture=true;emitChanged();});
   await page.waitForTimeout(250);
   await page.locator('#settings-button').click();
   await page.locator('#settings-nav [data-page="repositories"]').click();
   const usage=page.locator('#repo-list .card-row-aside');
   await usage.waitFor();
   assert.equal(await usage.innerText(),'3 worktrees · 项目 Alpha 团队, 项目 Alpha 团队');
   assert.equal(await usage.getAttribute('title'),'项目 Alpha 团队: one\n项目 Alpha 团队: two\n项目 Alpha 团队: three');
   await lead.click();await lead.click({button:'right'});
   await input.fill('新名字 Beta');await save.click();
   await page.waitForFunction(()=>!document.querySelector('#rename-dialog').open);
   await page.evaluate(()=>emitChanged());await page.waitForTimeout(250);
   await page.locator('#settings-button').click();await usage.waitFor();
   assert.equal(await usage.innerText(),'3 worktrees · 新名字 Beta, 项目 Alpha 团队');
   assert.equal(await usage.getAttribute('title'),'新名字 Beta: one\n新名字 Beta: two\n项目 Alpha 团队: three');
   // Rename a nonselected lead while a worker draft is open. Both drafts and
   // the selected stable ID survive the merged renderer/state refresh.
   await lead.click();
   await page.locator('.composer textarea').fill('synthetic lead draft');
   await worker.click();
   await page.locator('.composer textarea').fill('synthetic worker draft');
   await lead.click({button:'right'});await input.fill('组合测试 名称');await save.click();
   await page.waitForFunction(()=>!document.querySelector('#rename-dialog').open);
   assert.equal(await page.locator('.composer textarea').inputValue(),'synthetic worker draft');
   assert.match(await page.locator('#crumbs').innerText(),/worker-one/);
   await lead.click();assert.equal(await page.locator('.composer textarea').inputValue(),'synthetic lead draft');
   await worker.click();assert.equal(await page.locator('.composer textarea').inputValue(),'synthetic worker draft');
   // Combine .20's ordering and semantic focus slots with renamed labels.
   await page.evaluate(()=>{
     agents[0].status='waiting';
     agents.splice(1,0,{id:'alice/ended',name:'ended',yesman:'alice',role:'worker',number:2,status:'stopped',finishedAt:'2026-11-14T00:00:00Z'});
     agents.push({id:'alice/active',name:'active',yesman:'alice',role:'worker',number:3,status:'working'});
     emitChanged();
   });
   await page.waitForFunction(()=>document.querySelectorAll('[data-agent="alice/lead"]').length===2);
   const workerIDs=()=>page.locator('#yesmen [data-sidebar-entry="worker"]').evaluateAll(rows=>rows.map(r=>r.dataset.agent));
   assert.deepEqual(await workerIDs(),['alice/worker-one','alice/active','alice/ended']);
   for(const entry of ['yesman','needs-you']) {
     const row=page.locator(`[data-agent="alice/lead"][data-sidebar-entry="${entry}"]`);
     await row.focus();
     await page.evaluate(entry=>{localStorage.setItem('synthetic.label','刷新名称 '+entry);emitChanged();},entry);
     await page.waitForFunction(label=>[...document.querySelectorAll('[data-agent="alice/lead"] .name')].every(e=>e.textContent===label),'刷新名称 '+entry);
     assert.equal(await page.evaluate(()=>document.activeElement.dataset.sidebarEntry),entry);
     await page.evaluate(()=>{menuAction='';calls.length=0;});
     await page.keyboard.press('Shift+F10');
     await page.waitForFunction(()=>calls.some(c=>c[0]==='AgentMenu'));
     assert.equal(await page.evaluate(()=>document.activeElement.dataset.sidebarEntry),entry);
     assert.equal(await page.evaluate(()=>calls.find(c=>c[0]==='AgentMenu')[1]),'alice/lead');
     const anchor=await page.evaluate(()=>({y:calls.find(c=>c[0]==='AgentMenu')[3],bottom:Math.round(document.activeElement.getBoundingClientRect().bottom)}));
     assert.equal(anchor.y,anchor.bottom);
   }
   await page.evaluate(()=>{agents[0].status='idle';emitChanged();});
   await page.waitForFunction(()=>document.querySelectorAll('[data-agent="alice/lead"]').length===1);
   assert.equal(await page.evaluate(()=>document.activeElement.dataset.sidebarEntry),'yesman');
   await page.evaluate(()=>{agents[0].status='waiting';const ended=agents.find(a=>a.name==='ended');ended.status='starting';delete ended.finishedAt;emitChanged();});
   await page.waitForFunction(()=>document.querySelectorAll('[data-agent="alice/lead"]').length===2);
   assert.deepEqual(await workerIDs(),['alice/ended','alice/worker-one','alice/active']);
   assert.equal(await page.evaluate(()=>document.activeElement.dataset.sidebarEntry),'yesman');
   assert.equal(await page.locator('.composer textarea').inputValue(),'synthetic worker draft');
   assert.deepEqual(errors,[]);
   console.log(engineName+': rename/cancel/failure/reload/routing plus same-label repo users, single-ID dedup and rename refresh passed');
  } finally {await browser.close();}
 }
} finally {server.close();}
