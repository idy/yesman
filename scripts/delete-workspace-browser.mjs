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
const baseline = process.env.YESMAN_DELETE_BASELINE === '1';
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
      window.mygo={platform:'darwin',locale:lang,on(event, fn){ (window.listeners ??= {})[event] = fn; },async call(method,...args){
        const m=method.replace('Yesman.',''); window.calls.push([m,...args]);
        switch(m){
          case 'State': { const v=await rpc(m);agents=v.agents??=[];return v; }
          case 'PrepareWorkspaceDeletion': return rpc(m,{name:args[0],since:args[1]});
          case 'WorkspaceCheckouts': {
            const scan = await rpc(m, {name:args[0],since:args[1]});
            if (window.holdScan) await new Promise(r => window.releaseScan = r);
            return window.partialScan ? {...scan, problem:'Fixture scan incomplete'} : scan;
          }
          case 'ReviewWorkspaceForceDeletion': {
            const result = await rpc(m,{target:args[0],review:args[1]});
            if (window.holdForceReview) await new Promise(resolve => window.releaseForceReview = resolve);
            return result;
          }
          case 'CancelWorkspaceForceReview': return rpc(m,{review:args[0]});
          case 'CancelWorkspaceForceDeletion': return rpc(m,{token:args[0]});
          case 'ForceDeleteWorkspace': return rpc(m,{target:args[0],token:args[1]});
          case 'DeleteWorkspace': if (window.hold) await new Promise(r=>window.release=r); return rpc(m,{target:args[0]});
          case 'StoredWorkspaces': {
            const records = await rpc(m);
            if (window.heldLists) await new Promise((resolve, reject) => window.heldLists.push(fail => fail ? reject(Error('Storage read unavailable')) : resolve()));
            return records;
          }
          case 'AgentMenu':case 'FilesMenu': return 'delete-workspace';
          case 'Shortcuts': return {'yesman.dismiss':['Mod+Shift+Y']};
          case 'Settings': return {appearance:'system',home:'/fixture',archive:'/fixture/archive',pool:'/fixture/repos',yesmen:'/fixture/yesmen'};
          case 'Repos':case 'Clones':case 'Pulls':case 'Topics':case 'ListFiles': return [];
          case 'Fetches': return {fetching:[],errors:{}};
          case 'Messages':return {messages:[]};
          case 'Crew': return rpc(m);
          case 'HireNamed':
            if (window.holdHire) await new Promise((resolve, reject) => window.releaseHire = () => reject(Error('Fixture hire unavailable')));
            return rpc(m, {name:args[0]});
          case 'Models':return {models:[],agent:agents.find(a=>a.id===args[0])};
          case 'Info': return rpc(m, {name:args[0]});
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
    const stored = page.locator('#stored-workspaces');
    const list = stored.locator('[data-storage-key="list"]');
    const first = stored.getByRole('button', {name:/paper-kites-20260101/});
    const second = stored.getByRole('button', {name:/paper-kites-20260102/});
    const openStorage = async () => {
      await page.locator('#settings-button').click();
      await page.locator('#settings-nav [data-page="storage"]').click();
    };
    const holdLists = () => page.evaluate(() => { window.heldLists = []; });
    const heldLists = n => page.waitForFunction(n => window.heldLists?.length === n, n);
    const releaseList = (index, fail = false) => page.evaluate(([i, fail]) => heldLists[i](fail), [index, fail]);
    const resumeLists = () => page.evaluate(() => { window.heldLists = null; });
    const listReady = () => page.waitForFunction(() => document.querySelector('[data-storage-key="list"]')?.getAttribute('aria-disabled') === 'false');
    const count = m => page.evaluate(m=>calls.filter(c=>c[0]===m).length,m);
    const screenshot = async suffix => { if(out) await page.screenshot({path:join(out,`${name}-${suffix}.png`)}); };
    const bazel = page.locator('[data-agent="bazel-builds/lead"][data-sidebar-entry="yesman"]');
    const bazelPath = '.yesman/tmp/t12/display-fw/bazel-output/execroot/_main';
    const worktrees = page.locator('#yesman-info .info-section').filter({has:page.locator('.worktree-heading')});
    const openInfo = page.getByRole('button', {name: /^(Show the info|显示信息)/});
    const hideInfo = page.getByRole('button', {name: /^(Hide the info|隐藏信息)/});
    const reviewForce = dialog.getByRole('button', {name:/^(Force Delete…|强制删除…)$/});
    const commitForce = dialog.getByRole('button', {name:/^(Force Delete|强制删除)$/});
    await bazel.click();
    if (!baseline) await page.evaluate(() => { window.holdScan = true; });
    await openInfo.click();
    if (baseline) {
      await worktrees.getByText(/No worktrees yet|还没有工作树|尚无工作树/).waitFor();
      await bazel.click({button:'right'});
      await ok.click();
      await dialog.getByRole('alert').getByText(bazelPath, {exact:false}).waitFor();
      assert.equal(await reviewForce.count(),0);
      await screenshot('baseline-missing-row-blocked');
      console.log(`${name} ${lang}: v0.1.60 omitted ${bazelPath} and refused Delete`);
      return;
    }
    await page.waitForFunction(() => !!window.releaseScan);
    await worktrees.getByText(/Scanning for local Git checkouts|正在扫描本地 Git 工作树/).waitFor();
    assert.equal(await worktrees.getByText(/No worktrees yet|还没有工作树|尚无工作树/).count(),0);
    await page.evaluate(() => { window.partialScan = true; window.holdScan = false; releaseScan(); });
    await worktrees.getByText('Fixture scan incomplete').waitFor();
    const row = worktrees.getByText('old-root-checkout', {exact:true});
    await row.waitFor();
    assert.equal(await row.locator('..').getByRole('button').count(),0);
    await page.evaluate(() => { window.partialScan = false; });
    await worktrees.getByRole('button', {name:/Refresh the worktrees|刷新工作树/}).click();
    await worktrees.getByText('Fixture scan incomplete').waitFor({state:'detached'});
    await row.waitFor();
    if (process.env.YESMAN_T300_BASELINE === '1') {
      await worktrees.getByText(/Local Git checkout scan is incomplete|本地 Git 工作树扫描未完成/).waitFor();
    } else {
      assert.equal(await worktrees.getByText(bazelPath, {exact:true}).count(), 0);
      assert.equal(await worktrees.getByText(/Local Git checkout scan is incomplete|本地 Git 工作树扫描未完成/).count(), 0);
    }
    await screenshot('local-checkout');
    await bazel.click({button:'right'});
    await dialog.getByText(/may be used by another checkout|可能仍被其他工作树使用/).waitFor();
    await dialog.getByText(new RegExp(`yesmen/bazel-builds/${bazelPath}/\\.git`)).waitFor();
    assert(await ok.isDisabled());
    assert.equal(await reviewForce.count(),0);
    await screenshot('legacy-git-data-held');
    await page.keyboard.press('Enter');
    await dialog.waitFor({state:'detached'});
    assert.equal(await count('ForceDeleteWorkspace'),0);
    assert.equal(await count('DeleteWorkspace'),0);
    // Replay an ordinary deletion error while the background review is
    // pending. A retry must not appear until a fresh review can actually run.
    const checkAgain = dialog.getByRole('button', {name:/^(Check again|再次检查)$/});
    await page.evaluate(() => { window.holdForceReview = true; });
    const checkCount = await count('ReviewWorkspaceForceDeletion');
    const ordinaryCount = await count('DeleteWorkspace');
    await bazel.click({button:'right'});
    await page.waitForFunction(() => typeof window.releaseForceReview === 'function');
    await ok.click();
    await dialog.getByRole('alert').getByText(/may be used by another checkout|可能仍被其他工作树使用/).waitFor();
    assert.equal(await checkAgain.isVisible(), false, 'do not offer an inert retry while a review is pending');
    assert.equal(await count('ReviewWorkspaceForceDeletion'), checkCount + 1);
    assert.equal(await count('DeleteWorkspace'), ordinaryCount + 1);
    await page.evaluate(() => { window.holdForceReview = false; window.releaseForceReview(); });
    await dialog.locator('#deletion-checkouts .danger').getByText(/may be used by another checkout|可能仍被其他工作树使用/).waitFor();
    await checkAgain.click();
    await dialog.locator('#deletion-checkouts .danger').getByText(/may be used by another checkout|可能仍被其他工作树使用/).waitFor();
    assert.equal(await count('ReviewWorkspaceForceDeletion'), checkCount + 2);
    await dialog.getByRole('button', {name:/^(Close|关闭|Cancel|取消)$/}).click();
    await dialog.waitFor({state:'detached'});
    await page.evaluate(() => { window.calls = window.calls.filter(([m]) => m !== 'DeleteWorkspace'); });
    await page.evaluate(() => rpc('PreserveBazelGitData'));
    if (process.env.YESMAN_T300_BASELINE === '1') {
      await bazel.click({button:'right'});
      await dialog.getByText(/Cannot verify the Git entry|无法验证/).waitFor();
      assert(await ok.isDisabled());
      assert.equal(await reviewForce.count(), 0);
      await screenshot('v062-dangling-link-held');
      console.log(`${name} ${lang} reproduced the v0.1.62 dangling Git symlink hold`);
      return;
    }
    const openForce = async () => {
      await bazel.click({button:'right'});
      await reviewForce.waitFor();
      assert(await ok.isDisabled());
      await dialog.getByText(`yesmen/bazel-builds/${bazelPath}`, {exact:true}).waitFor();
      await dialog.getByText(`yesmen/bazel-builds/${bazelPath}/.git`, {exact:true}).waitFor();
      assert(await cancel.evaluate(n => n === document.activeElement));
    };
    await openForce();
    await page.keyboard.press('Enter');
    await dialog.waitFor({state:'detached'});
    await openForce(); await reviewForce.click(); await commitForce.waitFor();
    assert(await cancel.evaluate(n => n === document.activeElement));
    await page.keyboard.press('Escape'); await dialog.waitFor({state:'detached'});
    await openForce(); await reviewForce.click(); await cancel.click(); await dialog.waitFor({state:'detached'});
    assert.equal(await count('ForceDeleteWorkspace'),0);
    assert.equal(await count('DeleteWorkspace'),0);
    await openForce(); await reviewForce.click();
    await page.evaluate(() => rpc('AddLateCheckout'));
    await commitForce.click();
    await dialog.getByRole('alert').getByText(/paths changed|路径已更改/).waitFor();
    await dialog.getByRole('button', {name:/^(Check again|再次检查)$/}).click();
    await reviewForce.waitFor();
    await dialog.getByText(`yesmen/bazel-builds/${bazelPath}/build/${'d/'.repeat(105)}out/second-checkout`, {exact:true}).waitFor();
    await reviewForce.click();
    await dialog.getByText(/Git data stored outside the workspace is kept|存放在工作区外的 Git 数据会保留/).waitFor();
    await screenshot('force-confirmation');
    await commitForce.click();
    await dialog.waitFor({state:'detached'}); await bazel.waitFor({state:'detached'});
    await page.evaluate(() => rpc('VerifyBazelRemoval'));
    await hideInfo.click();
    assert.equal(await count('ForceDeleteWorkspace'),2);

    await page.evaluate(() => rpc('RootCheckout'));
    await page.reload();
    const root = page.locator('[data-agent="root-project/lead"]');
    await root.click({button:'right'});
    await dialog.locator('#deletion-checkouts .danger').getByText(/root-project[/\\]\.git/).waitFor();
    assert(await ok.isDisabled());
    assert.equal(await reviewForce.count(), 0);
    await screenshot('external-checkout-held');
    await cancel.click();
    await page.evaluate(() => rpc('PreserveLinked'));
    await root.click({button:'right'});
    await reviewForce.click();
    await dialog.getByText('yesmen/root-project/src', {exact:true}).waitFor();
    await screenshot('root-checkout-confirmation');
    await commitForce.click();
    await root.waitFor({state:'detached'});

    await page.evaluate(() => rpc('MakeInterruptedWorkspace'));
    await page.reload();
    await openStorage(); await list.click();
    const interrupted = stored.getByRole('button', {name:/\.deleting-[0-9a-f]+\.interrupted-build/});
    const reviewRemaining = dialog.getByRole('button', {name:/^(Review remaining files…|检查剩余文件…)$/});
    const deleteRemaining = dialog.getByRole('button', {name:/^(Delete remaining files|删除剩余文件)$/});
    await interrupted.click();
    await reviewRemaining.click();
    await dialog.getByText(/\.interrupted-build\/build-output/).waitFor();
    assert(await cancel.evaluate(n => n === document.activeElement));
    await screenshot('storage-retry');
    await page.evaluate(() => rpc('AddLateStagedFile'));
    await deleteRemaining.click();
    await dialog.getByRole('alert').getByText(/paths changed|路径已更改/).waitFor();
    await page.evaluate(() => rpc('RemoveLateStagedFile'));
    await dialog.getByRole('button', {name:/^(Check again|再次检查)$/}).click();
    await reviewRemaining.click();
    await deleteRemaining.click();
    await interrupted.waitFor({state:'detached'});
    await page.evaluate(() => rpc('VerifyRecovery'));
    await page.locator('[data-agent="interrupted-build/lead"]').click({button:'right'});
    await ok.click();
    await dialog.waitFor({state:'detached'});
    await page.reload();

    await lead.waitFor(); await lead.focus();
    const prepareCount = await count('PrepareWorkspaceDeletion');
    await page.keyboard.press('Meta+Shift+Y');
    assert.equal(await count('PrepareWorkspaceDeletion'),prepareCount,'old binding must stay inert');
    await lead.click({button:'right'});
    await page.waitForFunction(()=>document.querySelector('.delete-permanently:enabled'));
    assert.equal(await dialog.getByRole('heading').innerText(),lang==='en'?'Delete “Paper Kites”?':'删除“Paper Kites”？');
    assert.equal(await cancel.evaluate(n=>document.activeElement===n),true);
    await screenshot('confirmation');
    await page.keyboard.press('Enter'); await dialog.waitFor({state:'detached'});
    await lead.focus(); await page.keyboard.press('Shift+F10'); await cancel.waitFor();
    await page.evaluate(async () => listeners.agents((await rpc('State')).agents));
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'detached'});
    assert(await lead.evaluate(el => el === document.activeElement), 'Escape restores the redrawn sidebar entry');
    assert.equal(await count('DeleteWorkspace'),0);
    await retained.click({button:'right'});await ok.click();await dialog.getByRole('alert').getByText(/unfinished-checkout/).waitFor();
    if (lang !== 'en') assert.match(await dialog.getByRole('alert').innerText(),/工作树/);
    await screenshot('blocked');await dialog.getByRole('button',{name:/^(Close|关闭)$/}).click();
    await retained.waitFor();
    await lead.click({button:'right'});await page.waitForFunction(()=>document.querySelector('.delete-permanently:enabled'));
    await page.evaluate(()=>rpc('Rehire'));await ok.click();await dialog.getByRole('alert').waitFor();assert.equal(await ok.isDisabled(),true);
    await page.reload(); await lead.waitFor();
    await openStorage(); await list.press('Enter'); await second.waitFor();
    assert(await list.evaluate(el => el === document.activeElement), 'Show keeps focus on Refresh');
    await lead.click(); await page.locator('#dock textarea').waitFor();
    await holdLists();
    await lead.click({button:'right'});await page.evaluate(()=>window.hold=true);await ok.click();
    await page.waitForFunction(()=>!!window.release);assert.equal(await cancel.isDisabled(),true);assert.equal(await ok.isDisabled(),true);
    await page.keyboard.press('Escape');assert.equal(await dialog.isVisible(),true);
    await screenshot('progress');await page.evaluate(()=>{hold=false;release()});
    await dialog.waitFor({state:'detached'}); await lead.waitFor({state:'detached'});
    await heldLists(1);
    assert.deepEqual(await page.evaluate(async () => (await rpc('State')).yesmen.map(y => y.name)), ['retained']);
    assert.equal(await stored.count(), 0);
    await page.getByText(lang === 'en' ? 'Deleted “Paper Kites”.' : '已删除“Paper Kites”。', {exact:true}).waitFor();
    const selected = page.locator('[data-agent="retained/lead"][aria-current="page"]');
    await selected.waitFor();
    assert(await page.evaluate(() => document.activeElement?.matches('#dock textarea, [data-agent="retained/lead"]')), 'focus moves to the surviving conversation while the list read waits');
    const composer = page.locator('#dock textarea');
    await composer.fill('Retained draft');
    assert(await composer.evaluate(el => el === document.activeElement));
    await screenshot('live-deleted');
    await releaseList(0, true); await resumeLists();
    assert.equal(await composer.inputValue(), 'Retained draft');
    await openStorage(); await second.waitFor();
    await holdLists(); await list.press('Enter'); await heldLists(1);
    const other=page.locator('#settings-nav [data-page="storage"]');
    await other.focus(); await releaseList(0); await resumeLists(); await listReady();
    assert(await other.evaluate(el => el === document.activeElement), 'late list result preserves focus moved elsewhere');
    await screenshot('archives');await first.focus();await first.press('Enter');await cancel.click();await first.waitFor();
    // This read captured both archives. The following reconciliation captures
    // only the survivor. Neither response controls the completed action.
    await holdLists(); await list.press('Enter'); await heldLists(1);
    await first.click();assert.match(await dialog.locator('#deletion-location').innerText(),/archive\/paper-kites-20260101-123456/);await ok.click();
    await first.waitFor({state:'detached'});await second.waitFor();await dialog.waitFor({state:'detached'});
    await heldLists(2);
    assert(await second.evaluate(el => el === document.activeElement), 'the surviving archive is focused during refresh');
    await screenshot('archive-deleted');
    await releaseList(1, true); await listReady();
    await stored.getByText('Storage read unavailable').waitFor();
    await releaseList(0); await resumeLists();
    assert.equal(await first.count(), 0, 'older result cannot restore the deleted row');
    assert(await second.evaluate(el => el === document.activeElement), 'survivor keeps focus after a failed refresh');
    await second.press('Enter'); await cancel.waitFor(); await ok.click(); await dialog.waitFor({state:'detached'});
    await listReady(); assert(await list.evaluate(el => el === document.activeElement), 'an empty list returns focus to Refresh');
    await page.reload();await retained.waitFor();assert.equal(await lead.count(),0);
    await page.evaluate(() => rpc('MoveRetainedFolder'));
    // Hold the actual post-delete State reply while the user starts a hire.
    await page.evaluate(() => {
      const invoke = mygo.call.bind(mygo);
      mygo.call = async (...args) => {
        const value = await invoke(...args);
        if (args[0] === 'Yesman.State' && window.holdState) await new Promise(resolve => { window.releaseState = resolve; });
        return value;
      };
      window.holdState = true;
    });
    await retained.click({button:'right'}); await ok.click(); await dialog.waitFor({state:'detached'});
    const welcome = page.locator('.welcome input[name="name"]');
    await welcome.waitFor();
    assert(await welcome.evaluate(el => el === document.activeElement), 'deleting the last workspace focuses the welcome form');
    await page.waitForFunction(() => typeof window.releaseState === 'function');
    const modelHint = page.locator('#welcome-model strong');
    await modelHint.waitFor();
    const chosen = await modelHint.innerText();
    const draft = 'Next test project';
    await welcome.fill(draft);
    await welcome.evaluate(input => {
      window.hireField = input;
      input.setSelectionRange(5, 9, 'backward');
      input.dispatchEvent(new CompositionEvent('compositionstart', {bubbles:true, data:'项'}));
    });
    const finishRead = async () => {
      const previous = await count('Crew');
      await page.evaluate(() => { holdState = false; const finish = releaseState; releaseState = null; finish(); });
      // A completed current State refresh re-reads the selected crew member.
      await page.waitForFunction(n => calls.filter(c => c[0] === 'Crew').length > n, previous);
    };
    const nextRead = async () => {
      await page.evaluate(() => { holdState = true; listeners.changed(); });
      await page.waitForFunction(() => typeof window.releaseState === 'function');
    };
    await finishRead();
    assert.deepEqual(await welcome.evaluate(el => [el === hireField, el.value, el.selectionStart, el.selectionEnd, el.selectionDirection, el === document.activeElement]),
      [true, draft, 5, 9, 'backward', true]);
    await welcome.evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', {bubbles:true, data:'项目'})));
    await screenshot('welcome-draft');
    assert.equal(await modelHint.innerText(), chosen);

    const hireButton = page.locator('.welcome button[type="submit"]');
    await page.evaluate(() => { window.holdHire = true; });
    await hireButton.click(); await page.waitForFunction(() => typeof window.releaseHire === 'function');
    await nextRead(); await finishRead();
    assert(await welcome.isDisabled()); assert(await hireButton.isDisabled());
    await welcome.evaluate(el => el.form.requestSubmit());
    assert.equal(await count('HireNamed'), 1);
    await page.evaluate(() => { holdHire = false; releaseHire(); });
    const feedback = page.locator('#welcome-hire-error');
    await feedback.getByText('Fixture hire unavailable').waitFor();
    await nextRead(); await finishRead();
    assert.equal(await welcome.inputValue(), draft);
    assert.equal(await feedback.innerText(), 'Fixture hire unavailable');
    assert(await welcome.evaluate(el => el === document.activeElement));
    await hireButton.click();
    await page.locator('[data-agent="next-test-project/lead"][aria-current="page"]').waitFor();
    assert.equal(await welcome.count(), 0);
    const hired = await page.evaluate(async () => (await rpc('State')).yesmen[0]);
    assert.equal(hired.displayName, draft); assert.equal(hired.model, 'magpie/auto/claude-opus-5-5:xhigh');
    assert.deepEqual(await page.evaluate(()=>unexpected),[]);assert.deepEqual(errors,[]);
    console.log(`${name} ${lang} passed deletion, list ordering and welcome form retention with the selected model, name, selection, focus, pending submit, failure and successful hire`);
  } finally { await browser?.close(); fixture.stdin.end(); assert.equal(await exited,0,log); }
}
try {
  const build=spawnSync('go',['test','-c','-o',exe,'.'],{cwd,encoding:'utf8'});
  assert.equal(build.status,0,build.stderr||build.stdout);
  await run(chromium,'chromium','en');await run(webkit,'webkit','zh-Hans');
} finally { await rm(tmp,{recursive:true,force:true}); }
