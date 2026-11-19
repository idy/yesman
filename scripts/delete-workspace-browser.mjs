// Production page in Chromium and WebKit with a loopback bridge to real Go
// workspace operations. AgentMenu itself is native and is covered separately.
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const tmp = await mkdtemp(join(tmpdir(), 'delete-browser-'));
const exe = join(tmp, `fixture${process.platform === 'win32' ? '.exe' : ''}`);
const out = process.env.YESMAN_DELETE_SHOTS;
if (out) await mkdir(out, { recursive: true });

async function run(engine, name, lang) {
  const fixture = spawn(exe, ['-test.run=^TestDeletionBrowserBridge$', '-test.timeout=100s'], {
    cwd, env: { ...process.env, YESMAN_TEST_DELETE_BROWSER:'1', YESMAN_TEST_DELETE_LOCALE:lang }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  const exited = new Promise(resolve => fixture.once('exit', resolve));
  let browser, log = '';
  try {
    const url = await new Promise((resolve, reject) => {
      const receive = chunk => { log += chunk; const at = /DELETE_BROWSER_URL=(http:\/\/[^\s]+)/.exec(log); if (at) resolve(at[1]); };
      fixture.stdout.on('data', receive); fixture.stderr.on('data', receive);
      fixture.once('error', reject); fixture.once('exit', () => reject(Error(log)));
    });
    browser = await engine.launch({ headless:true, ...(name === 'chromium' && process.env.YESMAN_CHROMIUM ? {executablePath:process.env.YESMAN_CHROMIUM} : {}) });
    const page = await browser.newPage({ viewport: { width:lang === 'en' ? 1080 : 780, height:720 }, colorScheme: lang === 'en' ? 'light':'dark' });
    page.setDefaultTimeout(6500);
    const errors=[];
    page.on('pageerror', e=>errors.push(e.message));
    await page.addInitScript((lang)=>{
      localStorage.setItem('yesman.info','hidden');
      window.calls=[]; window.unexpected=[];
      let agents=[];
      window.rpc=async (method, extra={})=>{
        const r=await fetch('/fixture',{method:'POST',body:JSON.stringify({method,...extra})});
        const data=await r.json(); if (data.error) throw Error(data.error); return data.value;
      };
      window.mygo={platform:'darwin',locale:lang,on(){},async call(method,...args){
        const m=method.replace('Yesman.',''); window.calls.push([m,...args]);
        switch(m){
          case 'State': { const v=await rpc(m);agents=v.agents??=[];return v; }
          case 'PrepareWorkspaceDeletion': return rpc(m,{name:args[0],since:args[1]});
          case 'DeleteWorkspace': if (window.hold) await new Promise(r=>window.release=r); return rpc(m,{target:args[0]});
          case 'StoredWorkspaces': return rpc(m);
          case 'AgentMenu':case 'FilesMenu': return 'delete-workspace';
          case 'Shortcuts': return {'yesman.dismiss':['Mod+Shift+Y']};
          case 'Settings': return {appearance:'system',home:'/fixture',archive:'/fixture/archive',pool:'/fixture/repos',yesmen:'/fixture/yesmen'};
          case 'Repos':case 'Clones':case 'Pulls':case 'Topics':case 'ListFiles': return [];
          case 'Fetches': return {fetching:[],errors:{}};
          case 'Messages':return {messages:[]};
          case 'Models':return {models:[],agent:agents.find(a=>a.id===args[0])};
          case 'Info':return {knowledge:[],worktrees:[]};
          case 'Schedules':return {plans:[]};
          case 'Board':return {tasks:[]};
          case 'RestartStatus':return {};
          case 'Update':return {enabled:false};
          case 'TakeFocus':return '';
          case 'WatchFiles':case 'Usage':case 'Context':return null;
          case 'StorageUsage':return {bytes:100,files:6,parts:[]};
          default: unexpected.push(m); throw Error('Unmocked method '+m);
        }
      }};
    },lang);
    await page.goto(url);
    const lead = page.locator('[data-agent="paper-kites/lead"][data-sidebar-entry="yesman"]');
    const retained = page.locator('[data-agent="retained/lead"]');
    const dialog = page.locator('#delete-workspace-dialog');
    const cancel = dialog.getByRole('button', {name: /^(Cancel|取消)$/});
    const ok = dialog.getByRole('button', {name:/^(Delete permanently|永久删除)$/});
    const count = m => page.evaluate(m=>calls.filter(c=>c[0]===m).length,m);
    const screenshot = async suffix => { if(out) await page.screenshot({path:join(out,`${name}-${suffix}.png`)}); };
    await lead.waitFor(); await lead.focus();
    await page.keyboard.press('Meta+Shift+Y');
    assert.equal(await count('PrepareWorkspaceDeletion'),0,'old binding must stay inert');
    await lead.click({button:'right'});
    await page.waitForFunction(()=>document.querySelector('.delete-permanently:enabled'));
    assert.equal(await dialog.getByRole('heading').innerText(),lang==='en'?'Delete “Paper Kites”?':'删除“Paper Kites”？');
    assert.equal(await cancel.evaluate(n=>document.activeElement===n),true);
    await screenshot('confirmation');
    await page.keyboard.press('Enter'); await dialog.waitFor({state:'detached'});
    await lead.focus(); await page.keyboard.press('Shift+F10'); await cancel.waitFor(); await page.keyboard.press('Escape');
    await dialog.waitFor({state:'detached'});assert.equal(await count('DeleteWorkspace'),0);
    await retained.click({button:'right'});await ok.click();await dialog.getByRole('alert').getByText(/unfinished-checkout/).waitFor();
    if (lang !== 'en') assert.match(await dialog.getByRole('alert').innerText(),/工作树/);
    await screenshot('blocked');await dialog.getByRole('button',{name:/^(Close|关闭)$/}).click();
    await retained.waitFor();
    await lead.click({button:'right'});await page.waitForFunction(()=>document.querySelector('.delete-permanently:enabled'));
    await page.evaluate(()=>rpc('Rehire'));await ok.click();await dialog.getByRole('alert').waitFor();assert.equal(await ok.isDisabled(),true);
    await page.reload();await lead.click({button:'right'});await page.evaluate(()=>window.hold=true);await ok.click();
    await page.waitForFunction(()=>!!window.release);assert.equal(await cancel.isDisabled(),true);assert.equal(await ok.isDisabled(),true);
    await page.keyboard.press('Escape');assert.equal(await dialog.isVisible(),true);
    await screenshot('progress');await page.evaluate(()=>{hold=false;release()});
    await dialog.waitFor({state:'detached'});await lead.waitFor({state:'detached'});await retained.waitFor();
    await page.locator('#settings-button').click();await page.locator('#settings-nav [data-page="storage"]').click();
    const stored=page.locator('#stored-workspaces');await stored.getByRole('button',{name:/^(Show workspaces|查看工作区)$/}).click();
    const first=stored.getByRole('button',{name:/paper-kites-20260101/}),second=stored.getByRole('button',{name:/paper-kites-20260102/});
    await second.waitFor();await screenshot('archives');await first.click();await cancel.click();await first.waitFor();
    await first.click();assert.match(await dialog.locator('#deletion-location').innerText(),/archive\/paper-kites-20260101-123456/);await ok.click();
    await first.waitFor({state:'detached'});await second.waitFor();
    await page.reload();await retained.waitFor();assert.equal(await lead.count(),0);
    assert.deepEqual(await page.evaluate(()=>unexpected),[]);assert.deepEqual(errors,[]);
    console.log(`${name} ${lang}: cancel, old binding, blocker, stale hire, progress, exact active and archive deletion, reopen PASS`);
  } finally { await browser?.close(); fixture.stdin.end(); assert.equal(await exited,0,log); }
}
try {
  const build=spawnSync('go',['test','-c','-o',exe,'.'],{cwd,encoding:'utf8'});
  assert.equal(build.status,0,build.stderr||build.stdout);
  await run(chromium,'chromium','en');await run(webkit,'webkit','zh-Hans');
} finally { await rm(tmp,{recursive:true,force:true}); }
