// Production Settings DOM with a controlled bridge. Runtime preparation and
// process ownership are exercised separately by the Go lifecycle tests.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile,mkdir,writeFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
const {chromium,webkit}=await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE||'playwright');
const root=fileURLToPath(new URL('../web',import.meta.url)),out=process.env.YESMAN_RUNTIME_SETTINGS_EVIDENCE;
if(out)await mkdir(out,{recursive:true});
const server=createServer(async(req,res)=>{try{const p=new URL(req.url,'http://localhost').pathname;res.setHeader('Content-Type',p.endsWith('.js')?'application/javascript':p.endsWith('.css')?'text/css':'text/html');res.end(await readFile(root+(p==='/'?'/index.html':p)))}catch{res.writeHead(404).end()}}).listen(0,'127.0.0.1');
await new Promise(r=>server.on('listening',r));
const results=[];
try{for(const[name,engine]of Object.entries({chromium,webkit})){
 const browser=await engine.launch({headless:true});
 try{for(const language of ['en','zh-Hans']){
  const page=await browser.newPage({viewport:{width:1000,height:740}}),errors=[];
  page.on('pageerror',e=>errors.push(String(e)));
  await page.addInitScript(language=>{
   localStorage.setItem('yesman.info','hidden');window.fixture={mode:'preparing',calls:[]};
   const agents=[{id:'alice/lead',yesman:'alice',name:'lead',role:'lead',since:'1',status:'stopped'}];
   window.mygo={locale:language,on:()=>{},call:async(method,...args)=>{
    const m=method.replace('Yesman.','');window.fixture.calls.push(m);let mode=window.fixture.mode;
    if(m==='RetryPiRuntime'){window.fixture.mode=mode='ready';}
    if(m==='PiPackages'||m==='RetryPiRuntime')return {managed:!mode.startsWith('external'),pi:mode.startsWith('external')?'/custom/pi-wrapper':'Managed pi (internal-digest-should-not-show)',version:mode==='ready'?'1.5.2-yesman.1':mode==='external-ready'?'9.2.0':'',pinnedVersion:'1.5.2-yesman.1',runtimeState:mode==='external-ready'?'ready':mode==='external-unknown'?'unknown':mode,packagesKnown:mode==='ready',packages:[],jobs:[],revision:0,npmCommand:['npm'],settingsFile:'/synthetic/settings.json',piError:mode==='failed'?'offline fixture':''};
    if(m==='About')return {go:'go fixture',piManaged:!mode.startsWith('external'),piState:mode==='external-unknown'?'unknown':mode,pi:mode==='ready'?'1.5.2-yesman.1':mode==='external-ready'?'9.2.0':''};
    if(m==='State')return {yesmen:[{name:'alice'}],agents};
    if(['Repos','Clones'].includes(m))return [];
    if(m==='Messages')return {messages:[],deliveries:[]};
    if(m==='Models')return {agent:agents.find(a=>a.id===args[0]),models:[]};
    if(m==='Settings')return {appearance:'dark',autoFetch:0};
    if(m==='TakeFocus')return '';
    return {};
   }};
  },language);
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.locator('.composer textarea').waitFor();
  await page.evaluate(language=>document.documentElement.dataset.theme=language==='zh-Hans'?'dark':'light',language);
  await page.locator('#settings-button').click();await page.locator('[data-page="pi"]').click();
  const info=page.locator('#pi-info');await info.locator('[role="status"]').waitFor();
  assert.equal(await info.locator('[role="status"]').innerText(),language==='en'?'Preparing…':'准备中…');
  assert.equal((await info.innerText()).includes('internal-digest-should-not-show'),false);
  assert.equal((await info.innerText()).includes(language==='en'?'None installed yet':'尚未安装'),false);
  if(out){await page.setViewportSize({width:480,height:740});await page.screenshot({path:`${out}/${name}-${language}-preparing.png`});await page.setViewportSize({width:1000,height:740});}
  await page.locator('#pi-tab-installed').click();
  assert.match(await page.locator('#pi-installed-list').innerText(),language==='en'?/have not been read/:/尚未读取/);
  await page.locator('#pi-tab-info').click();
  for(const mode of ['failed','ready','external-unknown','external-ready']){
   await page.evaluate(mode=>window.fixture.mode=mode,mode);
   await page.locator('#pi-actions button').click();
   await page.waitForFunction(mode=>{const s=document.querySelector('#pi-info [role="status"]')?.textContent;return mode==='failed'?/Unavailable|不可用/.test(s):mode==='external-unknown'?/Version unknown|版本未知/.test(s):/Ready|就绪|准备就绪/.test(s)},mode);
   if(mode==='failed'){
    if(out)await page.screenshot({path:`${out}/${name}-${language}-failure-state.png`});
    await page.locator('#pi-actions button').click();
    await page.waitForFunction(()=>window.fixture.calls.includes('RetryPiRuntime'));
    await page.waitForFunction(()=>document.querySelector('#pi-info').textContent.includes('1.5.2-yesman.1'));
   }
   if(mode==='external-ready'){assert.match(await info.innerText(),/9\.2\.0/);assert.match(await info.innerText(),/\/custom\/pi-wrapper/);}
   if(out){await page.setViewportSize({width:480,height:740});await page.screenshot({path:`${out}/${name}-${language}-${mode}.png`});await page.setViewportSize({width:1000,height:740});}
  }
  await page.evaluate(()=>window.fixture.mode='preparing');await page.locator('[data-page="about"]').click();
  await page.waitForFunction(()=>document.querySelector('.page-inner')?.textContent.includes('Preparing')||document.body.textContent.includes('准备中…'));
  assert.equal(await page.evaluate(()=>window.fixture.calls.some(m=>/Global|InstallPiPackage/.test(m))),false);
  assert.deepEqual(errors,[]);results.push({engine:name,language,preparing:'honest',versions:'managed/external',retry:'explicit',digest:'not displayed'});
  await page.close();
 }}finally{await browser.close()}
}}finally{await new Promise(r=>server.close(r))}
if(out)await writeFile(`${out}/results.json`,JSON.stringify(results,null,2)+'\n');console.log(JSON.stringify(results,null,2));
