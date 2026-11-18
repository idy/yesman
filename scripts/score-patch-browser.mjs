// Actual Go score service + production Settings page; synthetic catalog/profile.
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/score-patch-browser.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const scratch = await mkdtemp(`${tmpdir()}/yesman-score-patch-`);
const output = process.env.YESMAN_SCORE_PATCH_OUT || scratch;
await mkdir(output, { recursive: true });
const exe = `${scratch}/scores.test${process.platform === 'win32' ? '.exe' : ''}`;
const header = 'provider,model_id,effort,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task\n';
const csv = header + 'magpie,auto/claude-opus-5-5,max,0,,,,\nmagpie,local/tiny,off,,123.4,,,0\n';
async function fixture() {
  const child = spawn(exe, ['-test.run=^TestScorePatchBrowserBridge$', '-test.timeout=180s'], { cwd, env: { ...process.env, YESMAN_TEST_SCORE_PATCH_BROWSER: '1' }, stdio: ['pipe', 'pipe', 'pipe'] });
  let log = '';
  child.stderr.on('data', (d) => { log += d; });
  const exited = new Promise((resolve) => child.once('exit', resolve));
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error(log || 'Fixture startup timeout')), 15000);
    child.stdout.on('data', (d) => { log += d; const m = log.match(/SCORE_PATCH_BROWSER_URL=(http:\/\/[^\s]+)/); if (m) { clearTimeout(timer); resolve(m[1]); } });
    child.once('error', reject);
    child.once('exit', () => { clearTimeout(timer); reject(Error(log)); });
  }).catch(async (e) => { child.kill(); await exited; throw e; });
  return { url, close: async () => { child.stdin.end(); assert.equal(await exited, 0, log); } };
}
try {
  const build = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
  assert.equal(build.status, 0, build.stderr || build.stdout);
  for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    const backend = await fixture(); let browser;
    try {
      browser = await engine.launch({ headless: true });
      const page = await browser.newPage({ viewport: { width: 1180, height: 900 } });
      const errors = []; page.on('pageerror', (e) => errors.push(e.message));
      await page.addInitScript(() => {
        window.fixtureCall = async (method, ...args) => {
          const res = await fetch('/fixture/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method, args }) });
          const body = await res.json(); if (body.error) throw Error(body.error); return body.value;
        };
        window.mygo = { locale: localStorage.getItem('fixture.locale') || 'en', on() {}, async call(method, ...args) {
          const m = method.replace('Yesman.', '');
          if (['Crew', 'RefreshCrew', 'ImportScorePatch', 'ExportScorePatch', 'SetScorePatchEnabled', 'AddCrewOption', 'SetCrewScore', 'RemoveCrewOption', 'SetYesmanModel'].includes(m)) {
            const v = await fixtureCall(m, ...args);
            if (m === 'RefreshCrew' && window.holdRefresh) await new Promise((resolve) => { window.releaseRefresh = resolve; });
            return v;
          }
          switch (m) {
            case 'State': return { yesmen: [], agents: [] };
            case 'Repos': case 'Clones': case 'Pulls': return [];
            case 'Settings': return { appearance: 'system', autoFetch: 0 };
            case 'Fetches': return { fetching: [], errors: {} };
            case 'TakeFocus': return '';
            case 'Update': return { enabled: false };
            case 'Shortcuts': return {};
            default: throw Error('Unmocked ' + m);
          }
        } };
      });
      const open = async () => {
        await page.goto(backend.url);
        await page.locator('#settings-button').click();
        await page.locator('#settings-nav [data-page="yesman"]').click();
        await page.locator('.crew-row').first().waitFor();
      };
      await open();
      if (process.env.YESMAN_SCORE_PATCH_BASELINE === '1') {
        await page.screenshot({ path: `${output}/${name}-baseline.png`, fullPage: true });
        continue;
      }
      const patch = page.locator('.score-patch');
      const rows = page.locator('.crew-row');
      const snapshot = async (label) => page.screenshot({ path: `${output}/${name}-${label}.png`, fullPage: true });
      await patch.getByRole('button', { name: 'Blank template…', exact: true }).click();
      await patch.getByRole('status').filter({ hasText: 'Exported' }).waitFor();
      assert.equal(await page.evaluate(() => fixtureCall('FixtureExport')), header);
      await page.evaluate((csv) => fixtureCall('FixtureCSV', csv), csv);
      await patch.getByRole('button', { name: 'Import CSV…', exact: true }).click();
      await patch.getByText('2 score rows · Enabled', { exact: true }).waitFor();
      assert.match(await rows.first().innerText(), /0 · Your CSV/);
      assert.match(await rows.first().innerText(), /Inherited from AA/);
      await snapshot('imported');
      // Patch-only score makes an existing configured model addable, without a score editor.
      await page.getByRole('button', { name: 'Add a model', exact: true }).click();
      const dialog = page.locator('#crew-dialog');
      await dialog.locator('select[name=model]').selectOption('1');
      assert.equal(await dialog.locator('.crew-scores').isVisible(), false);
      await dialog.getByRole('button', { name: 'Add', exact: true }).click();
      await rows.nth(1).waitFor();
      assert.match(await rows.nth(1).innerText(), /123.4 tok\/s · Your CSV/);
      assert.equal(await rows.nth(1).getByText('Not available', { exact: true }).count(), 3);
      const lead = await page.evaluate(() => fixtureCall('FixtureContext'));
      assert.match(lead, /intelligence unavailable, speed 123.4 tok\/s \(user CSV\)/);
      // Invalid replacement is actionable and leaves both UI and persisted scores intact.
      await page.evaluate((csv) => fixtureCall('FixtureCSV', csv + 'magpie,local/tiny,off,9,,,,\n'), csv);
      await patch.getByRole('button', { name: 'Replace CSV…', exact: true }).click();
      await patch.getByRole('alert').filter({ hasText: 'duplicate' }).waitFor();
      assert.match(await patch.getByRole('alert').innerText(), /row 4.*row 3/);
      assert.match(await rows.nth(1).innerText(), /123.4 tok\/s/);
      await snapshot('invalid');
      await patch.getByRole('button', { name: 'Export current…', exact: true }).click();
      await patch.getByRole('status').filter({ hasText: 'Exported' }).waitFor();
      assert.equal(await page.evaluate(() => fixtureCall('FixtureExport')), csv);
      await patch.getByRole('combobox', { name: 'Use score patch' }).selectOption('off');
      await patch.getByText('2 score rows · Disabled', { exact: true }).waitFor();
      assert.match(await rows.nth(1).innerText(), /No scores: give yours/);
      // Existing estimate editor remains available, and survives patch toggles.
      await rows.nth(1).getByRole('button', { name: /^Your scores for/ }).click();
      await dialog.locator('input[name=intelligence]').fill('25');
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await rows.nth(1).filter({ hasText: 'Your estimate' }).waitFor();
      await page.evaluate(() => fixtureCall('FixtureRestart'));
      await open();
      await patch.getByText('2 score rows · Disabled', { exact: true }).waitFor();
      await patch.getByRole('combobox', { name: 'Use score patch' }).selectOption('on');
      await patch.getByText('2 score rows · Enabled', { exact: true }).waitFor();
      assert.match(await rows.nth(1).innerText(), /25 · Inherited estimate/);
      await rows.nth(1).getByRole('button', { name: /^Fallback estimate for/ }).click();
      await dialog.getByRole('heading', { name: 'Fallback estimate', exact: true }).waitFor();
      assert.equal(await dialog.locator('input[name=intelligence]').inputValue(), '25');
      assert.equal(await dialog.locator('input[name=speed]').inputValue(), '');
      await dialog.getByText('Edit the fallback estimate. Nonempty fields in your CSV still take precedence.', { exact: true }).waitFor();
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
      // A stale catalog response cannot restore scores from before the patch update.
      await page.evaluate(() => { window.holdRefresh = true; });
      await page.getByRole('button', { name: 'Read pi’s models again', exact: true }).click();
      await page.waitForFunction(() => !!window.releaseRefresh);
      const replacement = header + 'magpie,auto/claude-opus-5-5,max,88,,,,\n';
      await page.evaluate((csv) => fixtureCall('FixtureCSV', csv), replacement);
      await patch.getByRole('button', { name: 'Replace CSV…', exact: true }).click();
      await patch.getByText('1 score row · Enabled', { exact: true }).waitFor();
      await page.evaluate(() => { window.releaseRefresh(); window.holdRefresh = false; });
      await page.getByRole('button', { name: 'Read pi’s models again', exact: true }).waitFor({ state: 'visible' });
      assert.match(await rows.first().innerText(), /88 · Your CSV/);
      await page.getByRole('combobox', { name: 'New yesmen run as' }).selectOption('magpie/local/tiny:off');
      await rows.nth(1).getByText('New yesmen', { exact: true }).waitFor();
      await rows.first().getByRole('button', { name: /^Remove / }).click();
      await page.waitForFunction(() => document.querySelectorAll('.crew-row').length === 1);
      await page.evaluate(() => fixtureCall('FixtureRestart'));
      await open();
      assert.match(await rows.first().innerText(), /Tiny/);
      assert.equal(await rows.count(), 1);
      // Rebuild the mixed row for narrow Chinese/dark inspection.
      await page.evaluate((csv) => fixtureCall('FixtureCSV', csv), csv);
      await patch.getByRole('button', { name: 'Replace CSV…', exact: true }).click();
      await patch.getByText('2 score rows · Enabled', { exact: true }).waitFor();
      await snapshot('effective');
      await page.evaluate(() => localStorage.setItem('fixture.locale', 'zh-Hans'));
      await page.setViewportSize({ width: 720, height: 900 });
      await open();
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
      await snapshot('chinese-narrow');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      assert.deepEqual(errors, []);
      console.log(`${name}: production Models/service/import/replacement/rollback/export/toggle/restart/estimate/default/remove/freshness PASS`);
    } finally { await browser?.close(); await backend.close(); }
  }
} finally { await rm(scratch, { recursive: true, force: true }); }
