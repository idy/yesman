// Production creation forms in Chromium/WebKit, with actual Go HireNamed/State
// behind a controlled MyGo bridge. No real settings, workspace or pi is used.
// YESMAN_PLAYWRIGHT=/absolute/path/to/playwright/index.mjs node scripts/hire-browser.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const cwd = fileURLToPath(new URL('..', import.meta.url));
const fixtureDir = await mkdtemp(`${tmpdir()}/yesman-hire-browser-`);
const exe = `${fixtureDir}/hire.test${process.platform === 'win32' ? '.exe' : ''}`;

async function fixture() {
  const child = spawn(exe, ['-test.run=^TestHireBrowserBridge$', '-test.timeout=120s'], {
    cwd, env: { ...process.env, YESMAN_TEST_HIRE_BROWSER: '1' }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  let output = '';
  child.stderr.on('data', (data) => { output += data; });
  const exited = new Promise((resolve) => child.once('exit', (code) => resolve(code)));
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error('Fixture did not start: ' + output)), 15000);
    child.stdout.on('data', (data) => {
      output += data;
      const match = output.match(/HIRE_BROWSER_URL=(http:\/\/[^\s]+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    child.once('error', reject);
    child.once('exit', (code) => { clearTimeout(timer); reject(Error(`Fixture exited ${code}: ${output}`)); });
  }).catch(async (err) => {
    child.kill();
    await exited;
    throw err;
  });
  return { url, close: async () => {
    child.stdin.end();
    assert.equal(await exited, 0, output);
  } };
}

try {
  const built = spawnSync('go', ['test', '-c', '-o', exe, '.'], { cwd, encoding: 'utf8' });
  assert.equal(built.status, 0, built.stderr || built.stdout);
  for (const [engineName, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    const backend = await fixture();
    let browser;
    try {
      browser = await engine.launch({ headless: true });
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 } });
      const errors = [];
      page.on('pageerror', (err) => { errors.push(err.message); console.error(err.stack); });
      await page.addInitScript(() => {
        localStorage.setItem('yesman.info', 'hidden');
        window.calls = [];
        window.failHire = false;
        window.holdHire = false;
        window.failState = false;
        window.mygo = {
          locale: localStorage.getItem('fixture.locale') || 'en', on() {},
          async call(method, ...args) {
            const m = method.replace('Yesman.', '');
            calls.push([m, ...args]);
            if (m === 'HireNamed') {
              if (holdHire) await new Promise((resolve) => { window.releaseHire = resolve; });
              if (failHire) throw Error('Synthetic creation failure');
            }
            if (m === 'State' && failState) throw Error('Synthetic State failure');
            if (['HireNamed', 'Hire', 'State'].includes(m)) {
              const response = await fetch('/fixture/call', {
                method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method: m, args }),
              });
              const body = await response.json();
              if (body.error) throw Error(body.error);
              // The fixture has no running pi manager entries before its first
              // hire; provide the bridge's empty-list representation to the UI.
              if (m === 'State') {
                body.value.agents ??= [];
                window.fixtureState = body.value;
              }
              return body.value;
            }
            switch (m) {
              case 'Repos': case 'Clones': case 'Pulls': case 'ListFiles': return [];
              case 'Settings': return { appearance: 'system', autoFetch: 0 };
              case 'Fetches': return { fetching: [], errors: {} };
              case 'TakeFocus': return '';
              case 'Update': return { enabled: false };
              case 'Messages': return { messages: [] };
              case 'Models': return { models: [], agent: fixtureState.agents.find((a) => a.id === args[0]) };
              case 'Crew': return { members: [{ key: 'synthetic/model', name: 'Synthetic model' }], yesman: 'synthetic/model', models: [] };
              case 'Info': return { knowledge: [], worktrees: [] };
              case 'Board': return { tasks: [] };
              case 'WatchFiles': return;
              default: throw Error('Unmocked method ' + m);
            }
          },
        };
      });
      await page.goto(backend.url);
      const hireCount = () => page.evaluate(() => calls.filter((c) => c[0] === 'HireNamed').length);
      const lead = (name) => page.locator(`[data-agent="${name}/lead"][data-sidebar-entry="yesman"]`);
      const modal = page.locator('#hire-dialog');
      const input = page.locator('#hire-form input');
      const submit = page.locator('#hire-form [type="submit"]');
      const open = async () => {
        await page.locator('#yesmen').getByRole('button', { name: /^(Hire a yesman|雇用一个 yesman)$/ }).click();
        await input.waitFor();
      };
      // The first-run welcome form uses the real DOM and exact same handler.
      const welcome = page.locator('.welcome form');
      await welcome.waitFor();
      const firstInput = welcome.locator('input');
      await firstInput.fill('GizMoth');
      assert.equal(await welcome.evaluate((e) => e.checkValidity()), true);
      await firstInput.press('Enter');
      await lead('gizmoth').waitFor();
      assert.equal(await lead('gizmoth').locator('.name').innerText(), 'GizMoth');
      assert.equal(await page.locator('#crumbs').innerText(), 'GizMoth');
      assert.equal(await page.locator('.composer textarea').getAttribute('placeholder'), 'Message GizMoth…');
      assert.deepEqual(await page.evaluate(() => calls.find((c) => c[0] === 'HireNamed')), ['HireNamed', 'GizMoth']);
      await page.getByRole('button', { name: 'Show the board', exact: true }).click();
      await page.locator('#board').getByText('GizMoth adds tasks here as it plans, and its workers keep them up to date.').waitFor();
      await page.getByRole('button', { name: 'Show the files', exact: true }).click();
      await page.locator('#files [role="tree"][aria-label="GizMoth"]').waitFor();
      assert.ok(await page.evaluate(() => calls.some((c) => c[0] === 'ListFiles' && c[1] === 'gizmoth')));
      // The screenshot's actual modal now passes browser constraint validation.
      await open();
      await input.fill('GizMoth');
      assert.equal(await page.locator('#hire-form').evaluate((e) => e.checkValidity()), true);
      assert.equal(await page.locator('#hire-name-hint').innerText(), 'A display name. Uppercase letters, spaces and Chinese characters are welcome.');
      assert.equal(await page.locator('#hire-form input').count(), 1);
      await page.screenshot({ path: `${tmpdir()}/yesman-hire-${engineName}-form.png` });
      await submit.click();
      await lead('gizmoth-2').waitFor();
      assert.equal(await lead('gizmoth-2').locator('.name').innerText(), 'GizMoth');
      assert.equal(await modal.evaluate((e) => e.open), false);
      await page.screenshot({ path: `${tmpdir()}/yesman-hire-${engineName}-created.png` });
      // Default lowercase entry still works, without exposing an ID field.
      await open(); await input.fill('checkout-v2'); await submit.click();
      await lead('checkout-v2').waitFor();
      assert.equal(await lead('checkout-v2').locator('.name').innerText(), 'checkout-v2');
      assert.deepEqual(await page.evaluate(() => calls.filter((c) => c[0] === 'Hire')), []);
      // Required/trim/control validation, cancellation and Escape do not write.
      const before = await hireCount();
      await open();
      assert.equal(await page.locator('#hire-form').evaluate((e) => e.checkValidity()), false);
      await submit.click();
      await input.fill('   '); await submit.click();
      assert.match(await page.locator('#hire-error').innerText(), /Enter a name without control/);
      await input.fill('Bad\u0085Name'); await submit.click();
      assert.equal(await hireCount(), before);
      await page.locator('#hire-cancel').click();
      await open(); await input.fill('Discard'); await input.press('Escape');
      assert.equal(await hireCount(), before);
      // Failed creation leaves input/error open; retry calls once and keeps case.
      await open(); await input.fill('  Giz Moth 项目  ');
      await page.evaluate(() => { failHire = true; });
      await submit.click();
      await page.locator('#hire-error').getByText('Synthetic creation failure').waitFor();
      assert.equal(await input.inputValue(), '  Giz Moth 项目  ');
      assert.equal(await modal.evaluate((e) => e.open), true);
      await page.evaluate(() => { failHire = false; holdHire = true; });
      await submit.click();
      await page.waitForFunction(() => typeof releaseHire === 'function');
      assert.equal(await input.isDisabled(), true);
      assert.equal(await page.locator('#hire-cancel').isDisabled(), true);
      assert.equal(await page.locator('#hire-model-hint').evaluate((e) => e.inert), true);
      await page.keyboard.press('Escape');
      assert.equal(await modal.evaluate((e) => e.open), true);
      const pendingCount = await hireCount();
      await page.locator('#hire-form').evaluate((e) => e.dispatchEvent(new Event('submit', { cancelable: true })));
      assert.equal(await hireCount(), pendingCount);
      await page.evaluate(() => { holdHire = false; releaseHire(); });
      await lead('giz-moth').waitFor();
      assert.equal(await lead('giz-moth').locator('.name').innerText(), 'Giz Moth 项目');
      // There is no 32-character display-label cap, and HTML is escaped as text.
      const longLabel = ('<b>GizMoth</b> ' + '中文 Long Label '.repeat(65)).trim();
      await open(); await input.fill(longLabel);
      assert.equal(await page.locator('#hire-form').evaluate((e) => e.checkValidity()), true);
      await input.press('Enter');
      await page.waitForFunction(() => !document.querySelector('#hire-dialog').open);
      const state = await page.evaluate(() => mygo.call('Yesman.State'));
      const long = state.yesmen.find((y) => y.displayName === longLabel);
      assert.ok(long); assert.match(long.name, /^[a-z0-9][a-z0-9-]{0,31}$/);
      await lead(long.name).waitFor();
      assert.equal(await lead(long.name).locator('.name').innerText(), longLabel);
      assert.equal(await lead(long.name).locator('.name b').count(), 0);
      // State reads back actual persisted labels after a page reload.
      await page.reload(); await lead('gizmoth').waitFor();
      assert.equal(await lead('gizmoth').locator('.name').innerText(), 'GizMoth');
      const persisted = await page.evaluate(() => mygo.call('Yesman.State'));
      assert.equal(persisted.yesmen.find((y) => y.name === 'gizmoth').displayName, 'GizMoth');
      assert.ok(persisted.agents.some((a) => a.id === 'gizmoth/lead' && a.yesman === 'gizmoth'));
      // Localized help/validation share the display-name contract.
      await page.evaluate(() => localStorage.setItem('fixture.locale', 'zh-Hans'));
      await page.reload(); await lead('gizmoth').waitFor();
      await open();
      assert.equal(await page.locator('#hire-name-hint').innerText(), '显示名称，可使用大写字母、空格和中文。');
      await input.fill('   '); await submit.click();
      assert.equal(await page.locator('#hire-error').innerText(), '请输入名称，且不要包含控制字符。');
      await input.fill('中文项目'); await submit.click(); await lead('yesman').waitFor();
      assert.equal(await lead('yesman').locator('.name').innerText(), '中文项目');
      // A post-commit refresh failure closes the form, preventing an accidental
      // unchanged retry from creating a duplicate workspace.
      await open(); await input.fill('RefreshCase');
      await page.evaluate(() => { failState = true; }); await submit.click();
      await page.waitForFunction(() => !document.querySelector('#hire-dialog').open);
      await page.evaluate(() => { failState = false; });
      const afterRefreshFailure = await page.evaluate(() => mygo.call('Yesman.State'));
      assert.equal(afterRefreshFailure.yesmen.filter((y) => y.displayName === 'RefreshCase').length, 1);
      await page.reload(); await lead('refreshcase').waitFor();
      assert.equal(await lead('refreshcase').locator('.name').innerText(), 'RefreshCase');
      assert.deepEqual(errors, []);
      console.log(`${engineName}: production welcome/modal validity, Go wire/persistence, duplicate IDs, labels, lowercase, trim/control/long/HTML, retry/pending/cancel/Escape, Chinese and refresh failure passed`);
    } finally {
      await browser?.close();
      await backend.close();
    }
  }
} finally {
  await rm(fixtureDir, { recursive: true, force: true });
}
