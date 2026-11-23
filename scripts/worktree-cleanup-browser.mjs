// Exercise the embedded production page and real desktop cleanup service in
// synthetic Git repositories. Only native menu selection is supplied by the
// driver. YESMAN_PLAYWRIGHT names Playwright's index.mjs.
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const scratch = await mkdtemp(join(tmpdir(), 'worktree-browser-'));
const exe = join(scratch, `fixture${process.platform === 'win32' ? '.exe' : ''}`);
const output = process.env.YESMAN_WORKTREE_SCREENSHOTS;
const baseline = process.env.YESMAN_WORKTREE_BASELINE === '1';

async function run(engine, name, language) {
  const child = spawn(exe, ['-test.run=^TestWorktreeCleanupBrowser$', '-test.timeout=2m'], {
    cwd, env: { ...process.env, YESMAN_TEST_WORKTREE_BROWSER: '1' }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  const exit = new Promise(resolve => child.once('exit', resolve));
  let browser, page;
  let log = '';
  try {
    child.stderr.on('data', bytes => { log += bytes; });
    const url = await new Promise((resolve, reject) => {
      child.stdout.on('data', bytes => { log += bytes; const match = log.match(/WORKTREE_BROWSER_URL=(http:\/\/[^\s]+)/); if (match) resolve(match[1]); });
      child.once('exit', () => reject(new Error(log)));
      child.once('error', reject);
    });
    browser = await engine.launch({ headless: true });
    const zh = language === 'zh-Hans';
    page = await browser.newPage({ viewport: { width: zh ? 680 : 1180, height: zh ? 610 : 780 }, colorScheme: zh ? 'dark' : 'light' });
    page.setDefaultTimeout(7000);
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    await page.addInitScript(({ language, zh }) => {
      localStorage.setItem('yesman.files.autosave', 'off');
      window.calls = [];
      window.events = {};
      window.api = async (method, ...args) => {
        const r = await fetch('/fixture', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({method,args}) });
        const answer = await r.json();
        if (answer.error) throw new Error(answer.error);
        return answer.value;
      };
      window.mygo = { locale: language, platform: 'darwin', on(name, cb) { events[name] = cb; }, async call(method, ...args) {
        const m = method.replace('Yesman.', '');
        calls.push([m, ...structuredClone(args)]);
        switch (m) {
          case 'WorktreeMenu': return window.choice ?? (args[0] ? 'remove' : 'cleanup');
          case 'Reveal': return;
          case 'Info': if (window.failInfo) throw Error('Fixture refresh unavailable'); return api(m,...args);
          case 'WorkspaceCheckouts': return api(m, ...args);
          case 'ReadWorktreeFile': {
            const value = await api(m, ...args);
            if (args[1] === window.holdRead) {
              delete window.holdRead;
              await new Promise(resolve => { window.releaseRead = resolve; });
              window.lateReadReturned = true;
            }
            return value;
          }
          case 'WriteWorktreeFile':
            if (args[1] === window.holdSave) {
              delete window.holdSave;
              await new Promise(resolve => { window.releaseSave = resolve; });
              try { return await api(m, ...args); } catch (err) { window.saveRefusal = err.message; throw err; }
            }
            return api(m, ...args);
          case 'ListFiles': case 'ReadFile': case 'BeginWorktreeEdit': case 'EndWorktreeEdit': return api(m,...args);
          case 'State': {
            const value = await api(m,...args);
            if (window.holdState) {
              window.holdState = false;
              await new Promise(resolve => { window.releaseState = resolve; });
              window.lateStateReturned = true;
            }
            return value;
          }
          case 'Usage': case 'PreviewWorktreeCleanup': case 'CancelWorktreeCleanup': case 'ConfirmWorktreeCleanup': return api(m, ...args);
          case 'Settings': return { appearance: zh ? 'dark' : 'light', autoFetch: 0 };
          case 'Pulls': return [{ dir: 'review-a', pull: { state: 'merged', number: 42, repo: 'demo/repo', title: 'Reviewed change', additions: 1, deletions: 0 } }];
          case 'Models': return {models:[], agent:{id:args[0]}};
          case 'Context': return {};
          case 'Messages': return {messages:[], deliveries:[]};
          case 'Repos': case 'Clones': return [];
          case 'Crew': return {members:[], options:[]};
          case 'Schedules': return {plans:[]};
          case 'ScheduleSummary': return {total:0};
          case 'TakeFocus': return '';
          case 'PageMenu': return {action:''};
          case 'Fetches': case 'Shortcuts': case 'Update': case 'RestartState': case 'WatchFiles': return {};
          default: throw new Error('Unsupported fixture method '+m);
        }
      } };
    }, {language,zh});
    await page.goto(url);
    const info = page.locator('#yesman-info');
    const title = info.getByRole('heading', { name: zh ? '工作树' : 'Worktrees', exact: true });
    const row = dir => info.locator('.info-row-main').filter({hasText:dir});
    const giz = row('gizmoth-artifact-rows');
    await giz.waitFor();
    if (baseline) {
      await giz.click({button:'right'});
      assert.ok(await page.evaluate(() => calls.some(([m]) => m === 'PageMenu')));
      if (output) await info.screenshot({path:join(output, `incumbent-${name}.png`)});
      console.log(`${name} ${language} incumbent sidebar and fallback menu verified`);
      return;
    }
    if (!baseline) {
      await page.waitForFunction(() => calls.some(([m]) => m === 'WorkspaceCheckouts'));
      await info.getByText(/Scanning for local Git checkouts|正在扫描本地 Git 工作树/).waitFor({state:'hidden'});
      assert.equal(await info.locator('.info-local-checkout').count(), 0, 'registered paths must appear only once');
    }
    const dialog = page.locator('dialog.worktree-cleanup[open]');
    const button = dialog.locator('[data-confirm]');
    const cancel = dialog.locator('[data-close]');
    const focused = locator => locator.evaluate(el => document.activeElement === el);
    const headingButton = title.locator('button');
    const menuCalls = () => page.evaluate(() => calls.filter(([m]) => m === 'WorktreeMenu'));
    const commits = () => page.evaluate(() => calls.filter(([m]) => m === 'ConfirmWorktreeCleanup').length);
    await page.evaluate(() => { window.choice = 'reveal'; });
    await giz.click({button:'right'});
    await page.waitForFunction(() => calls.some(([m]) => m === 'Reveal'));
    assert.match(await page.evaluate(() => calls.find(([m])=>m==='Reveal')[1]), /gizmoth-artifact-rows$/);
    await page.evaluate(() => { delete window.choice; });
    await headingButton.focus();
    await page.keyboard.press('Shift+F10');
    await button.waitFor();
    assert.ok(await focused(cancel));
    if (process.env.YESMAN_T300_BASELINE === '1') {
      await dialog.locator('summary').click();
      assert.match(await dialog.innerText(), zh ? /任务已取消/ : /creating task was dropped/);
      assert.match(await dialog.innerText(), /legacy-integrate/);
      if (output) await dialog.screenshot({path:join(output, `v062-bulk-${name}-${language}.png`)});
      await page.keyboard.press('Escape');
      await dialog.waitFor({state:'detached'});
      console.log(`${name} ${language} reproduced v0.1.62 bulk holds on canceled and ended legacy checkouts`);
      return;
    }
    assert.match(await button.textContent(), /4/);
    assert.equal((await menuCalls()).at(-1)[1],false);
    assert.equal(await dialog.locator('code[title]').count(), 4);
    assert.ok(await dialog.locator('code[title]').evaluateAll(nodes => nodes.every(el => el.textContent === el.title && /^[a-f0-9]{40}$/.test(el.textContent))));
    const paths = await dialog.locator('.cleanup-path').allTextContents();
    assert.ok(paths.some(path => path.endsWith('/gizmoth-artifact-rows')));
    assert.ok(paths.some(path => path.endsWith('/legacy-integrate')));
    assert.match(await dialog.locator('#worktree-cleanup-hint').textContent(), zh ? /永久删除.*构建产物.*恢复引用/ : /permanently deletes.*build output.*No recovery reference/);
    await dialog.locator('summary').click();
    assert.match(await dialog.innerText(), zh ? /进行中的操作或私有状态/ : /Git lock, operation or private state/);
    assert.match(await dialog.innerText(), zh ? /临时工作树/ : /temporary checkout record/);
    if (output) await dialog.screenshot({path:join(output, `bulk-${name}-${language}.png`)});
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'detached'});
    assert.equal(await commits(),0);
    assert.ok(await focused(headingButton));
    await headingButton.press('Shift+F10');
    await button.waitFor();
    await page.keyboard.press('Enter');
    await dialog.waitFor({state:'detached'});
    assert.equal(await commits(),0);
    for (const [dir, hold] of [['saved-conflict', zh ? /还原冲突的文件版本/ : /versions of a resolved conflict/], ['unverified-index', zh ? /无法验证 Git 索引/ : /Git index could not be verified/]]) {
      await row(dir).click({button:'right'});
      await dialog.getByText(hold).waitFor();
      assert.equal(await button.isVisible(),false);
      await cancel.click();
    }
    await giz.click({button:'right'});
    await button.waitFor();
    assert.equal((await menuCalls()).at(-1)[1],true);
    assert.match(await dialog.innerText(), /fix\/artifact-row-actions/);
    assert.match(await dialog.innerText(), /[0-9a-f]{40}/);
    assert.equal(await dialog.evaluate(e => e.scrollWidth <= e.clientWidth), true);
    if (output) await dialog.screenshot({path:join(output, `single-${name}-${language}.png`)});
    await cancel.press('Enter');
    await dialog.waitFor({state:'detached'});
    assert.equal(await commits(),0);
    assert.ok(await focused(giz));
    await info.locator('.info-merged-toggle').click();
    const merged = row('Reviewed change');
    await merged.press('Shift+F10');
    await button.waitFor();
    assert.match(await dialog.innerText(), /review-a/);
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'detached' });
    const files = page.locator('#files');
    const openFiles = dir => info.getByRole('button', {name:`Show the files of ${dir}`, exact:true}).click();
    const file = rel => files.locator(`[data-path="${rel}"][role="treeitem"]`);
    const editor = files.locator('.cm-content');
    const editorText = () => editor.locator('.cm-line').allTextContents().then(lines => lines.join('\n'));
    const fileOf = (dir, name = 'README') => file(`${dir}/${name}`);
    const closeReadme = async () => {
      const completed = page.waitForResponse(r => r.url().endsWith('/fixture') && r.request().postDataJSON()?.method === 'EndWorktreeEdit');
      await files.getByRole('button', {name:'Close README', exact:true}).click();
      await completed;
    };
    if (!zh) {
      await page.setViewportSize({width:1470,height:870});
      await openFiles('locked'); await fileOf('locked').click();
      await editor.fill('Keep this other draft');
      await openFiles('gizmoth-artifact-rows'); await fileOf('gizmoth-artifact-rows').click();
      await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent.includes('Fixture'));
      await page.evaluate(() => {
        window.holdRead = 'gizmoth-artifact-rows/README';
        events.files({yesman:'studio',paths:[holdRead]});
      });
      await page.waitForFunction(() => !!window.releaseRead);
      await giz.press('Shift+F10');
      await dialog.getByText('A file editor is open in this worktree.').waitFor();
      assert.equal(await button.isVisible(), false);
      await cancel.click();
      await closeReadme();
    }
    await giz.press('Shift+F10');
    await button.waitFor();
    assert.equal(await dialog.locator('.cleanup-warning').count(), 0);
    await page.evaluate(() => api('KeepGizmo'));
    await button.click();
    await dialog.getByText(zh ? /重新打开预览/ : /Open a new preview/).waitFor();
    assert.ok((await page.evaluate(() => api('Verify'))).trees.some(w => w.dir === 'gizmoth-artifact-rows'));
    await cancel.click();
    await giz.press('Shift+F10');
    await button.waitFor();
    const warning = dialog.locator('.cleanup-warning');
    assert.equal(await button.getAttribute('aria-describedby'), await warning.getAttribute('id'));
    assert.match(await warning.textContent(), zh ? /看板已指定保留此工作树/ : /Board says to keep this worktree/);
    assert.ok(await focused(cancel));
    assert.equal(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth), true);
    if (output) await dialog.screenshot({path:join(output, `keep-warning-${name}-${language}.png`)});
    await page.evaluate(() => { window.failInfo = true; });
    await button.evaluate(e => { e.click(); e.click(); });
    await giz.waitFor({state:'detached'});
    assert.equal(await commits(),2);
    await info.getByText(/Fixture refresh unavailable/).waitFor();
    await page.evaluate(() => { window.failInfo = false; });
    const verify = () => page.evaluate(() => api('Verify'));
    assert.ok((await verify()).checkpoints.includes('gizmoth-artifact-rows'));
    if (await dialog.count()) await cancel.click();
    if (!zh) {
      assert.equal(await files.locator('.files-tab').count(),0);
      await openFiles('review-a'); await fileOf('review-a').click();
      await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent.includes('Fixture'));
      await closeReadme();
      // Open the same file via All files as well. Both panes belong to A.
      await files.locator('.files-crumb--scope').click();
      await page.locator('.files-menu-item').filter({hasText:'All files'}).click();
      await file('review-a').click(); await fileOf('review-a').click();
      await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent.includes('Fixture'));
      await closeReadme();
    }
    await headingButton.click({button:'right'});
    await button.waitFor();
    await page.evaluate(() => api('DirtyReview'));
    await button.click();
    await dialog.getByText(zh ? /文件编辑器/ : /A file editor is open/).waitFor();
    const actual = await verify();
    assert.deepEqual(actual.checkpoints.sort(),['gizmoth-artifact-rows']);
    assert.deepEqual(actual.trees.map(w => w.dir).sort(),['legacy','locked','review-b','saved-conflict','unverified-index']);
    assert.deepEqual(actual.present.sort(), ['locked','review-b','saved-conflict','unverified-index']);
    await page.evaluate(() => api('ReleaseLateEditor'));
    assert.equal(await commits(),3);
    assert.equal(await info.locator('.info-merged-toggle').count(), 0);
    assert.equal(await row('review-b').count(),1);
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'detached'});
    await headingButton.evaluate(el => new Promise(resolve => {
      const poll = () => document.activeElement === el ? resolve() : requestAnimationFrame(poll);
      poll();
    }));
    await row('locked').click({button:'right'});
    await dialog.getByText(zh ? /Git 锁定/ : /unsaved edits/).waitFor();
    assert.equal(await button.isVisible(),false);
    await cancel.click();
    if (!zh) {
      assert.equal(await files.locator('.files-tab').count(),0);
      const dir = 'gizmoth-artifact-rows';
      const refreshTo = async id => {
        await info.locator('.info-refresh').click();
        await info.locator(`[data-info-entry="worktree:${dir}:${id}"]`).waitFor();
      };
      const first = await page.evaluate(() => api('Recreate'));
      await refreshTo(first.cleanupId); await openFiles(dir);
      assert.equal(await files.locator('.files-tab').count(),0);
      await fileOf(dir,'replacement.txt').click();
      await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent.includes('New checkout'));
      await page.evaluate(() => releaseRead());
      await page.waitForFunction(() => window.lateReadReturned);
      assert.equal(await editorText(),'New checkout\n');
      await editor.fill('Old pending save');
      await page.evaluate(() => { window.holdSave = 'gizmoth-artifact-rows/replacement.txt'; });
      await editor.press('Meta+s'); await page.waitForFunction(() => !!window.releaseSave);
      await editor.press('Meta+s');
      await page.evaluate(() => { window.holdState = true; events.changed(); });
      await page.waitForFunction(() => !!window.releaseState);
      const next = await page.evaluate(() => api('ReplaceAgain'));
      assert.notEqual(first.cleanupId,next.cleanupId); assert.equal(first.head,next.head);
      await refreshTo(next.cleanupId); await openFiles(dir);
      assert.equal(await files.locator('.files-tab').count(),0);
      await fileOf(dir,'replacement.txt').click();
      await page.waitForFunction(() => document.querySelector('.cm-content')?.textContent.includes('New checkout'));
      await page.evaluate(() => releaseSave()); await page.waitForFunction(() => !!window.saveRefusal);
      const disk = await page.evaluate(() => api('ReadFile','studio','gizmoth-artifact-rows/replacement.txt'));
      assert.equal(disk.text,'New checkout\n');
      assert.equal((await page.evaluate(() => calls.filter(([m]) => m === 'WriteWorktreeFile'))).length,1);
      if (output) await files.screenshot({path:join(output,`replacement-${name}.png`)});
      await editor.fill('Saved by the replacement');
      await page.evaluate(() => releaseState()); await page.waitForFunction(() => window.lateStateReturned);
      assert.equal(await editorText(), 'Saved by the replacement');
      await editor.press('Meta+s');
      await page.waitForFunction(async () => (await api('ReadFile','studio','gizmoth-artifact-rows/replacement.txt')).text === 'Saved by the replacement');
      await openFiles('locked'); assert.equal(await editorText(),'Keep this other draft');
      console.log(`${name} Files kept the unrelated draft, held the open editor, ignored a late read and refused a delayed save across checkout replacement`);
    }
    await page.evaluate(() => api('FaultAfterGit'));
    await row('review-b').click({button:'right'});
    await button.waitFor();
    await button.click();
    await dialog.getByText(zh ? /工作树记录更新失败/ : /checkout record could not be updated/).first().waitFor();
    await row('review-b').waitFor({state:'detached'});
    assert.ok((await verify()).checkpoints.includes('review-b'));
    assert.ok(await focused(cancel));
    await cancel.press('Enter');
    await dialog.waitFor({state:'detached'});
    assert.ok(await focused(headingButton));
    assert.deepEqual(errors,[]);
    console.log(`${name} ${language} passed Board withdrawal and warning, both index holds, post-removal registry failure, menu, Cancel, Escape, focus, no-PR single, one-use confirmation, partial batch and list refresh`);
  } catch (err) {
    if (page) console.error(await page.evaluate(() => ({ calls: calls.slice(-12).map(c => c[0]), active: document.activeElement?.outerHTML?.slice(0, 380), dialogs: [...document.querySelectorAll('dialog[open]')].map(e=>e.textContent), toast: document.querySelector('.toast')?.textContent })));
    throw err;
  } finally {
    await browser?.close();
    child.stdin.end();
    assert.equal(await exit,0,log);
  }
}

try {
  const build = spawnSync('go',['test','-c','-o',exe,'.'],{cwd,encoding:'utf8'});
  assert.equal(build.status,0,build.stderr);
  for (const [name, engine] of Object.entries({ chromium, webkit })) {
    for (const language of baseline ? ['en'] : ['en','zh-Hans']) await run(engine,name,language);
  }
} finally { await rm(scratch,{recursive:true,force:true}); }
