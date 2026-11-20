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
      window.calls = [];
      window.api = async (method, ...args) => {
        const r = await fetch('/fixture', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({method,args}) });
        const answer = await r.json();
        if (answer.error) throw new Error(answer.error);
        return answer.value;
      };
      window.mygo = { locale: language, platform: 'darwin', on() {}, async call(method, ...args) {
        const m = method.replace('Yesman.', '');
        calls.push([m, ...structuredClone(args)]);
        switch (m) {
          case 'WorktreeMenu': return window.choice ?? (args[0] ? 'remove' : 'cleanup');
          case 'Reveal': return;
          case 'Info': if (window.failInfo) throw Error('Fixture refresh unavailable'); return api(m,...args);
          case 'State': case 'Usage': case 'PreviewWorktreeCleanup': case 'CancelWorktreeCleanup': case 'ConfirmWorktreeCleanup': return api(m, ...args);
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
    assert.match(await button.textContent(), /2/);
    assert.equal((await menuCalls()).at(-1)[1],false);
    assert.equal(await dialog.locator('code[title]').count(), 2);
    await dialog.locator('summary').click();
    assert.match(await dialog.innerText(), zh ? /锁定/ : /Git has locked/);
    assert.match(await dialog.innerText(), zh ? /临时工作树/ : /temporary checkout record/);
    if (output) await dialog.screenshot({path:join(output, `bulk-${name}-${language}.png`)});
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'detached'});
    assert.equal(await commits(),0);
    assert.ok(await focused(headingButton));
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
    await giz.press('Shift+F10');
    await button.waitFor();
    await page.evaluate(() => { window.failInfo = true; });
    await button.evaluate(e => { e.click(); e.click(); });
    await giz.waitFor({state:'detached'});
    assert.equal(await commits(),1);
    await info.getByText(/Fixture refresh unavailable/).waitFor();
    await page.evaluate(() => { window.failInfo = false; });
    const verify = () => page.evaluate(() => api('Verify'));
    assert.ok((await verify()).checkpoints.includes('gizmoth-artifact-rows'));
    if (await dialog.count()) await cancel.click();
    await headingButton.click({button:'right'});
    await button.waitFor();
    await page.evaluate(() => api('DirtyReview'));
    await button.click();
    await dialog.getByText(zh ? /未提交的改动/ : /uncommitted changes/).waitFor();
    const actual = await verify();
    assert.deepEqual(actual.checkpoints.sort(),['gizmoth-artifact-rows','review-a']);
    assert.deepEqual(actual.trees.map(w => w.dir).sort(),['legacy','locked','review-b']);
    assert.equal(await commits(),2);
    assert.equal(await info.locator('.info-merged-toggle').count(), 0);
    assert.equal(await row('review-b').count(),1);
    await page.keyboard.press('Escape');
    await dialog.waitFor({state:'detached'});
    assert.ok(await focused(headingButton));
    await row('locked').click({button:'right'});
    await dialog.getByText(zh ? /Git 锁定/ : /Git has locked/).waitFor();
    assert.equal(await button.isVisible(),false);
    await cancel.click();
    assert.deepEqual(errors,[]);
    console.log(`${name} ${language} passed native-menu dispatch, Cancel, Escape, opener focus, single removal without PR, double execution guard, partial batch, list and Git recovery refs`);
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
