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
const agents = [
 {id:'alice/lead',name:'lead',yesman:'alice',role:'lead',status:'idle'},
 {id:'alice/worker-one',name:'worker-one',yesman:'alice',role:'worker',number:1,title:'Synthetic worker',status:'idle'}
];
window.mygo = {locale:'en',on:(name,fn)=>handlers[name]=fn,call:async(method,...args)=>{
 const m=method.replace('Yesman.','');calls.push([m,...args]);
 switch(m){
 case 'State': return {home:'/synthetic',pool:'/synthetic/repos',yesmen:[{name:'alice',displayName:localStorage.getItem('synthetic.label')||'',path:'/synthetic/yesmen/alice',worktrees:[]}],agents};
 case 'Repos':case 'Clones':case 'Pulls':case 'ListFiles':return [];
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
   assert.deepEqual(errors,[]);
   console.log(engineName+': rename, cancel, invalid, write failure/retry, refresh/reload, worker routing passed');
  } finally {await browser.close();}
 }
} finally {server.close();}
