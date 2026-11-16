// Narrow final-main acceptance: production page -> real Go services/vault.
// Unknown calls and all HTTP/page errors fail; native Grant reads are explicit
// synthetic rows, with no OS prompts. No external model or live GitHub key.
import assert from 'node:assert/strict';
import {spawn,spawnSync} from 'node:child_process';
import {mkdtemp,rm,mkdir,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
const {chromium,webkit}=await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE||'playwright');
const root=fileURLToPath(new URL('..',import.meta.url));
const temp=await mkdtemp(join(tmpdir(),'app-combination-'));
const binary=join(temp,process.platform==='win32'?'bridge.exe':'bridge');
const built=spawnSync('go',['test','-c','-o',binary,'.'],{cwd:root,encoding:'utf8'});
assert.equal(built.status,0,built.stderr);
const out=process.env.YESMAN_TEST_GITHUB_APP_BROWSER_OUT;
if(out)await mkdir(out,{recursive:true});
const results=[];
try{for(const [name,engine] of Object.entries({chromium,webkit})){
 const child=spawn(binary,['-test.run=^TestGitHubAppCombinedBrowserBridge$','-test.v'],{cwd:temp,env:{...process.env,YESMAN_TEST_APP_BRIDGE:'1'},stdio:['pipe','pipe','pipe']});
 let log='';child.stdout.on('data',b=>log+=b);child.stderr.on('data',b=>log+=b);
 const ended=new Promise(r=>child.once('exit',r));
 const url=await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(Error(log)),20000);const check=b=>{const match=String(b).match(/GITHUB_APP_COMBINATION_URL=(http:\/\/127\.0\.0\.1:\d+)/);if(match){clearTimeout(timer);resolve(match[1]);}};child.stdout.on('data',check);child.once('error',reject);child.once('exit',()=>{clearTimeout(timer);reject(Error(log));});});
 const browser=await engine.launch({headless:true});
 try{
  const page=await browser.newPage({viewport:{width:1180,height:1000}});const errors=[];
  page.on('pageerror',e=>errors.push(String(e)));
  page.on('requestfailed',r=>errors.push(r.url()+': '+r.failure()?.errorText));
  page.on('response',r=>{if(r.status()>=400)errors.push(r.status()+' '+r.url());});
  await page.addInitScript(()=>{
   window.unmocked=[];window.bridgeCalls=[];window.events={};
   const real=new Set(['State','Info','Usage','Pulls','Context','Settings','Shortcuts','Repos','TakeFocus','Models','Messages','ModelUsage','GitHubApp','ConfigureGitHubApp','TestGitHubApp','EnableGitHubApp','LockGitHubApp']);
   window.mygo={locale:'en',platform:'darwin',on:(n,f)=>{window.events[n]=f;},call:async(full,...args)=>{
    const method=full.replace('Yesman.','');window.bridgeCalls.push({method,args});
    if(real.has(method)){const r=await fetch('/fixture/call',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({method,args})});if(!r.ok)throw Error('fixture HTTP '+r.status);const v=await r.json();if(v.error)throw Error(v.error);return v.value;}
    if(method==='Clones')return[];
    if(method==='Fetches')return{fetching:[],errors:{}};
    if(method==='Update')return{enabled:false};
    if(method==='Grants')return{platform:'darwin',items:['bluetooth','notifications','full-disk','microphone','camera','accessibility','screen','input','automation'].map(id=>({id,state:'manual',settings:true}))};
    window.unmocked.push(method);throw Error('Unmocked method '+method);
   }};
  });
  await page.goto(url);await page.locator('[data-agent="alice/lead"]').first().click();
  if(!await page.locator('#yesman-info [data-info-entry]').count())await page.getByRole('button',{name:'Show the info',exact:true}).click();
  await page.locator('#yesman-info [data-info-entry="needs-you"]').waitFor();
  assert.deepEqual(await page.locator('#yesman-info [data-info-entry]').evaluateAll(es=>es.map(e=>e.dataset.infoEntry)),['knowledge','working-logs','needs-you']);
  await page.locator('#settings-button').click();
  await page.locator('#settings-nav [data-page="github-app"]').click();
  const pane=page.locator('#github-app-page');await pane.waitFor();
  const button=n=>pane.getByRole('button',{name:n,exact:true});
  assert.equal(await button('Enable App identity').isDisabled(),true);
  await button('Test connection').click();await page.waitForFunction(()=>[...document.querySelectorAll('#github-app-page button')].some(b=>b.textContent==='Enable App identity'&&!b.disabled));
  await page.evaluate(()=>fetch('/fixture/revoke',{method:'POST'}));
  await button('Test connection').click();await pane.getByRole('alert').filter({hasText:'HTTP 403'}).waitFor();
  assert.equal(await button('Enable App identity').isDisabled(),true);
  assert.equal(await pane.getByText('Connection verified',{exact:true}).count(),0);
  await page.locator('#settings-nav [data-page="shortcuts"]').click();
  await page.locator('#shortcut-search').fill('GitHub App');
  const row=page.locator('.shortcut-row').filter({hasText:'GitHub App'});await row.waitFor();
  assert.equal(await row.locator('.keycap').count(),0);
  await page.locator('#settings-nav [data-page="grant"]').click();await page.locator('.grant-row').last().waitFor();assert.equal(await page.locator('.grant-row').count(),9);
  await page.locator('#settings-nav [data-page="usage"]').click();await page.waitForFunction(()=>window.bridgeCalls.some(c=>c.method==='ModelUsage'));
  // Palette uses the shared Settings registry; the App action has no keys.
  await page.keyboard.press('Meta+k');await page.locator('.palette-search input').fill('GitHub App');
  await page.locator('.palette-option').filter({hasText:'GitHub App'}).first().click();await pane.waitFor();
  assert.equal(await button('Enable App identity').isDisabled(),true);
  const appFocus=await page.evaluate(()=>({tag:document.activeElement?.tagName,page:document.activeElement?.dataset.page,connected:document.activeElement?.isConnected}));
  console.log('App palette focus',name,appFocus);
  // Compare with an existing Settings page through the same action host.
  // Its policy preserves a still-connected previous nav button, rather than
  // forcing focus away from every existing control after an async page load.
  await page.locator('#settings-nav [data-page="usage"]').click();
  await page.keyboard.press('Meta+k');await page.locator('.palette-search input').fill('Settings: Grant');
  await page.locator('.palette-option').filter({hasText:'Grant'}).first().click();await page.locator('.grant-row').last().waitFor();
  const mainFocus=await page.evaluate(()=>({tag:document.activeElement?.tagName,page:document.activeElement?.dataset.page,connected:document.activeElement?.isConnected}));
  assert.deepEqual(appFocus,mainFocus);assert.equal(appFocus.connected,true);assert.notEqual(appFocus.tag,'BODY');
  // Wait for the page's read to settle: its render replaces the toolbar.
  await page.locator('#settings-nav [data-page="github-app"]').click();await pane.waitFor();await button('Enable App identity').waitFor();
  await button('Refresh').focus();assert.equal(await button('Refresh').evaluate(e=>e===document.activeElement),true);
  if(out)await page.screenshot({path:join(out,name+'-combined.png'),fullPage:true});
  assert.deepEqual(await page.evaluate(()=>window.unmocked),[]);assert.deepEqual(errors,[]);
  results.push({engine:name,realService:true,failedRetestDisabled:true,infoRows:3,grantRows:9,usage:true,registryUnboundAppAction:true,unknownCalls:0,pageOrNetworkErrors:0});
 }finally{await browser.close();child.stdin.end();assert.equal(await ended,0,log);if(out)await writeFile(join(out,name+'-bridge.log'),log);}
}}finally{await rm(temp,{recursive:true,force:true});}
if(out)await writeFile(join(out,'results.json'),JSON.stringify(results,null,2));console.log(JSON.stringify(results,null,2));
