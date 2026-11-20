// Actual Go score service + production Settings page; synthetic catalog/profile.
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/score-patch-browser.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { checkWorkerRouting } from './worker-routing-browser.mjs';
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
        window.scoreCalls = [];
        window.scoreControls = {};
        window.scorePending = {};
        window.scoreFinished = {};
        window.fixtureCall = async (method, ...args) => {
          const res = await fetch('/fixture/call', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method, args }) });
          const body = await res.json(); if (body.error) throw Error(body.error); return body.value;
        };
        window.fixtureHandlers = {};
        window.mygo = { locale: localStorage.getItem('fixture.locale') || 'en', on(event, handler) { window.fixtureHandlers[event] = handler; }, async call(method, ...args) {
          const m = method.replace('Yesman.', '');
          if (['Crew', 'RefreshCrew', 'ImportScorePatch', 'ExportScorePatch', 'SetScorePatchEnabled', 'AddCrewOption', 'SetCrewScore', 'RemoveCrewOption', 'SetYesmanModel', 'SetWorkerPool'].includes(m)) {
            window.scoreCalls.push(m);
            const control = window.scoreControls[m];
            delete window.scoreControls[m];
            const pause = async () => {
              await new Promise((resolve) => { window.scorePending[m] = resolve; });
              delete window.scorePending[m];
              window.scoreFinished[m] = true;
              if (control.reject) throw Error(control.message || 'Synthetic score save failure');
            };
            if (control?.before) await pause();
            const v = await fixtureCall(m, ...args);
            if (control && !control.before) await pause();
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
      if (process.env.YESMAN_WORKER_ROUTING_BROWSER === '1') {
        await checkWorkerRouting({ page, open, name, output, errors });
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
      assert.match(await rows.nth(1).innerText(), /No scores yet/);
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
      // A dismissed form still owns its pending mutation. Delay responses after
      // the real Go save/DTO, exactly as the independent stale-estimate oracle.
      const hold = (method, control = {}) => page.evaluate(({ method, control }) => {
        window.scoreControls[method] = control;
        delete window.scoreFinished[method];
      }, { method, control });
      const pending = (method) => page.waitForFunction((m) => !!window.scorePending[m], method);
      const release = async (method) => {
        await page.evaluate((m) => window.scorePending[m](), method);
        await page.waitForFunction((m) => window.scoreFinished[m], method);
      };
      const replaceButton = () => patch.getByRole('button', { name: 'Replace CSV…', exact: true });
      const ready = () => page.waitForFunction(() => !document.querySelector('[data-score-action="import"]').disabled);
      const blocked = async () => {
        assert.equal(await replaceButton().isDisabled(), true);
        assert.equal(await page.getByRole('button', { name: 'Add a model', exact: true }).isDisabled(), true);
        assert.equal(await page.getByRole('button', { name: 'Read pi’s models again', exact: true }).isDisabled(), true);
        assert.equal(await page.getByRole('combobox', { name: 'New yesmen run as' }).isDisabled(), true);
        assert.equal(await patch.getByRole('combobox', { name: 'Use score patch' }).isDisabled(), true);
        assert.equal(await rows.getByRole('button', { name: /^Remove / }).first().isDisabled(), true);
      };
      const replaceSpeed = async (speed) => {
        await page.evaluate((csv) => fixtureCall('FixtureCSV', csv), csv.replace('123.4', String(speed)));
        await replaceButton().click();
        await ready();
        assert.match(await rows.nth(1).innerText(), new RegExp(`${speed === 37.125 ? '37.13' : speed} tok/s · Your CSV`));
        assert.equal((await page.evaluate(() => fixtureCall('Crew'))).members[1].score.speed, speed);
      };
      await hold('SetCrewScore');
      await rows.nth(1).getByRole('button', { name: /^Fallback estimate for/ }).click();
      await dialog.locator('input[name=intelligence]').fill('33');
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await pending('SetCrewScore');
      assert.equal(await dialog.locator('input[name=intelligence]').isDisabled(), true);
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
      await blocked();
      await snapshot('pending-estimate-dismissed');
      // Settings navigation may start a read during the outstanding mutation.
      await hold('Crew');
      await page.locator('#settings-nav [data-page="general"]').click();
      await page.locator('#settings-nav [data-page="yesman"]').click();
      await pending('Crew');
      await blocked();
      await release('SetCrewScore');
      await ready();
      assert.match(await rows.nth(1).innerText(), /33 · Inherited estimate/);
      await replaceSpeed(37.125);
      await release('Crew');
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      assert.match(await rows.nth(1).innerText(), /37.13 tok\/s · Your CSV/);
      assert.match(await page.evaluate(() => fixtureCall('FixtureContext')), /speed 37.12 tok\/s \(user CSV\)/);
      await snapshot('replacement-after-estimate');
      // Add shares that ownership, including Escape dismissal and later import.
      await hold('AddCrewOption');
      await page.getByRole('button', { name: 'Add a model', exact: true }).click();
      await dialog.locator('select[name=model]').selectOption('0');
      await dialog.locator('select[name=effort]').selectOption('low');
      await dialog.getByRole('button', { name: 'Add', exact: true }).press('Enter');
      await pending('AddCrewOption');
      await page.keyboard.press('Escape');
      await dialog.waitFor({ state: 'hidden' });
      await blocked();
      await release('AddCrewOption');
      await ready();
      assert.equal(await rows.count(), 3);
      await replaceSpeed(81.25);
      await rows.nth(2).getByRole('button', { name: /^Remove / }).click();
      await ready();
      assert.equal(await rows.count(), 2);
      // A real failed response before commit preserves the draft and restores
      // editable fields/Enter retry. Repeated Enter while pending sends once.
      await hold('SetCrewScore', { before: true, reject: true });
      await rows.nth(1).getByRole('button', { name: /^Fallback estimate for/ }).click();
      const intelligence = dialog.locator('input[name=intelligence]');
      await intelligence.fill('34');
      const callsBefore = await page.evaluate(() => scoreCalls.filter((m) => m === 'SetCrewScore').length);
      await intelligence.press('Enter');
      await pending('SetCrewScore');
      await page.keyboard.press('Enter');
      assert.equal(await page.evaluate(() => scoreCalls.filter((m) => m === 'SetCrewScore').length), callsBefore + 1);
      await release('SetCrewScore');
      await dialog.getByRole('alert').filter({ hasText: 'Synthetic score save failure' }).waitFor();
      assert.equal(await intelligence.inputValue(), '34');
      assert.equal(await intelligence.isDisabled(), false);
      assert.equal(await intelligence.evaluate((el) => document.activeElement === el), true);
      await intelligence.press('Enter');
      await dialog.waitFor({ state: 'hidden' });
      await ready();
      assert.match(await rows.nth(1).innerText(), /34 · Inherited estimate/);
      // A failure after dismissal is reported on the page, and unlocks all
      // actions. Its pending read still cannot overwrite a later replacement.
      await hold('SetCrewScore', { before: true, reject: true });
      await rows.nth(1).getByRole('button', { name: /^Fallback estimate for/ }).click();
      await dialog.getByRole('button', { name: 'Save', exact: true }).click();
      await pending('SetCrewScore');
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
      await blocked();
      await release('SetCrewScore');
      await ready();
      await page.getByText('Synthetic score save failure', { exact: true }).filter({ visible: true }).waitFor();
      await replaceSpeed(37.125);
      // A stale read error is also superseded by an acknowledged replacement.
      await hold('RefreshCrew', { reject: true, message: 'Synthetic superseded catalog read' });
      await page.getByRole('button', { name: 'Read pi’s models again', exact: true }).click();
      await pending('RefreshCrew');
      await replaceSpeed(81.25);
      await release('RefreshCrew');
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      assert.match(await rows.nth(1).innerText(), /81.25 tok\/s · Your CSV/);
      assert.equal(await page.getByText('Synthetic superseded catalog read', { exact: true }).filter({ visible: true }).count(), 0);
      console.log(`${name}: pending estimate/add cancellation, shared busy recovery, reads during saves, failed draft/Enter retry and late read error PASS`);
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
      // Actual newest embedded GPT-6.2 rows, with exact routed patch keys.
      // Configure only the synthetic catalog/options; do not replace Embedded.
      await page.evaluate(() => { localStorage.setItem('fixture.locale', 'en'); return fixtureCall('FixtureGPT62'); });
      await page.setViewportSize({ width: 1180, height: 1000 });
      await open();
      const gptCSV = header + 'synthetic,codex-remote/codex/gpt-6.2-astra,high,0,,,0,\n'
        + 'synthetic,codex-remote/codex/gpt-6.2-sol,high,,123.5,,,0\n'
        + 'synthetic,local/patch-only,off,,37.125,,,0\n';
      await page.evaluate((csv) => fixtureCall('FixtureCSV', csv), gptCSV);
      await patch.getByRole('button', { name: 'Replace CSV…', exact: true }).click();
      await patch.getByText('3 score rows · Enabled', { exact: true }).waitFor();
      const metrics = () => rows.evaluateAll((elements) => elements.map((row) => ({
        key: row.querySelector('strong').title,
        text: row.innerText,
        metrics: [...row.querySelectorAll('.crew-metrics dd')].map((el) => el.innerText),
      })));
      const checkGPT = async () => {
        const shown = await metrics();
        const view = await page.evaluate(() => fixtureCall('Crew'));
        const context = await page.evaluate(() => fixtureCall('FixtureContext'));
        assert.equal(view.date, '2026-11-18');
        assert.equal(view.models.length, 5);
        assert.equal(shown.length, 9);
        assert.deepEqual(shown.map((r) => r.key), view.members.map((m) => m.key));
        assert.deepEqual(shown[0].metrics, ['0 · Your CSV', '60.7 tok/s · Inherited from AA', '53.6 % · Inherited from AA', '0 USD · Your CSV', '4.9 min · Inherited from AA']);
        assert.deepEqual(shown[2].metrics, ['54.9 · Inherited from AA', '123.5 tok/s · Your CSV', '49.4 % · Inherited from AA', '0.56 USD · Inherited from AA', '0 min · Your CSV']);
        for (const [i, want] of [[1, ['61.6', '59.9 tok/s', '56.5 %', '2.52 USD', '6.2 min']], [3, ['55.7', '80.2 tok/s', '49.4 %', '0.65 USD', '5.1 min']], [4, ['60.1', '60.7 tok/s', '53.6 %', '2.07 USD', '4.9 min']], [5, ['60.1', '60.7 tok/s', '53.6 %', '2.07 USD', '4.9 min']], [6, ['12', '13 tok/s', '14 %', '15 USD', '16 min']]]) {
          assert.deepEqual(shown[i].metrics, want);
          assert.equal(view.members[i].source, i === 6 ? 'user' : 'aa');
        }
        assert.equal(view.members[7].score, undefined);
        for (const i of [0, 2, 3]) assert.match(shown[8].metrics[i], /^—\s*Not available$/);
        assert.equal(shown[8].metrics[1], '37.13 tok/s · Your CSV');
        assert.equal(shown[8].metrics[4], '0 min · Your CSV');
        assert.equal(view.members[8].score.intelligenceMissing, true);
        assert.equal(view.members[8].score.speed, 37.125);
        assert.match(context, /intelligence 0 \(user CSV\), speed 60\.7 tok\/s \(inherited AA\), coding 53\.6% \(inherited AA\), cost 0 USD per task \(user CSV\), time 4\.9 min per task \(inherited AA\)/);
        assert.match(context, /intelligence 54\.9 \(inherited AA\), speed 123\.5 tok\/s \(user CSV\), coding 49\.4% \(inherited AA\), cost 0\.56 USD per task \(inherited AA\), time 0 min per task \(user CSV\)/);
        assert.match(context, /AA source effort: xhigh/);
        assert.equal(await page.evaluate(() => fixtureCall('FixturePatch')), gptCSV);
        return { shown, view, context };
      };
      const gpt = await checkGPT();
      await snapshot('gpt62-effective');
      await patch.getByRole('combobox', { name: 'Use score patch' }).selectOption('off');
      await patch.getByText('3 score rows · Disabled', { exact: true }).waitFor();
      assert.match(await rows.first().innerText(), /60\.1/);
      await page.evaluate(() => fixtureCall('FixtureRestart'));
      await open();
      await patch.getByText('3 score rows · Disabled', { exact: true }).waitFor();
      await patch.getByRole('combobox', { name: 'Use score patch' }).selectOption('on');
      await patch.getByText('3 score rows · Enabled', { exact: true }).waitFor();
      await checkGPT();
      await page.evaluate((csv) => fixtureCall('FixtureCSV', csv + 'synthetic,codex-remote/codex/gpt-6.2-astra,high,9,,,,\n'), gptCSV);
      await patch.getByRole('button', { name: 'Replace CSV…', exact: true }).click();
      await patch.getByRole('alert').filter({ hasText: 'duplicate' }).waitFor();
      await checkGPT();
      await page.evaluate(() => fixtureCall('FixtureRestart'));
      await open();
      await checkGPT();
      await writeFile(`${output}/${name}-gpt62.json`, JSON.stringify(gpt, null, 2) + '\n');
      assert.deepEqual(errors, []);
      console.log(`${name}: actual embedded GPT-6.2 overrides/blanks/exact route-provider-effort/Models-Lead/reload/rollback PASS`);
      console.log(`${name}: production Models/service/import/replacement/rollback/export/toggle/restart/estimate/default/remove/freshness PASS`);
    } finally { await browser?.close(); await backend.close(); }
  }
} finally { await rm(scratch, { recursive: true, force: true }); }
