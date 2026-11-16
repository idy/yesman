// Production page in Chromium/WebKit with real Go-generated public DTO and a
// controlled bridge. No user settings, native dialogs or external GitHub calls.
import { createServer } from 'node:http';
import { readFile, mkdtemp, rm, mkdir, writeFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import assert from 'node:assert/strict';
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || 'playwright');
const root = fileURLToPath(new URL('..', import.meta.url));
const temp = await mkdtemp(join(tmpdir(), 'yesman-app-browser-'));
const dtoPath = join(temp, 'view.json');
const generated = spawnSync('go', ['test', '.', '-run', '^TestGitHubAppNativeImportAndVaultIsolation$', '-count=1'], { cwd: root, env: { ...process.env, YESMAN_TEST_GITHUB_APP_DTO: dtoPath }, encoding: 'utf8' });
assert.equal(generated.status, 0, generated.stderr || generated.stdout);
const dto = JSON.parse(await readFile(dtoPath, 'utf8'));
const out = process.env.YESMAN_TEST_GITHUB_APP_BROWSER_OUT;
if (out) await mkdir(out, { recursive: true });
const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    if (path.includes('..')) throw new Error('path');
    res.setHeader('Content-Type', path.endsWith('.js') ? 'application/javascript' : path.endsWith('.css') ? 'text/css' : 'text/html');
    res.end(await readFile(join(root, 'web', path === '/' ? 'index.html' : path.slice(1))));
  } catch { res.writeHead(404).end(); }
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const reports = [];
try {
  for (const [engineName, engine] of Object.entries({ chromium, webkit })) {
    const browser = await engine.launch({ headless: true });
    try {
      const page = await browser.newPage({ viewport: { width: 1000, height: 900 } });
      const errors = []; page.on('pageerror', (e) => errors.push(String(e)));
      await page.addInitScript((dto) => {
        localStorage.setItem('yesman.info', 'hidden');
        const state = structuredClone(dto); const calls = []; let nextRead, testGate;
        window.fixture = {
          state, calls,
          holdRead() { let resolve; const promise = new Promise((r) => { resolve = r; }); const held = { promise, resolve, snapshot: structuredClone(state) }; nextRead = held; this.releaseRead = () => held.resolve(held.snapshot); },
          holdTest() { let resolve; testGate = new Promise((r) => { resolve = r; }); this.releaseTest = resolve; },
        };
        const agents = [{ id: 'alice/lead', name: 'lead', role: 'lead', yesman: 'alice', status: 'idle' }];
        window.events = {};
        window.mygo = { locale: 'en', on: (n, f) => { window.events[n] = f; }, call: async (full, ...args) => {
          const method = full.replace('Yesman.', ''); calls.push({ method, args });
          switch (method) {
            case 'State': return { yesmen: [{ name: 'alice', displayName: 'Fixture workspace' }], agents };
            case 'Repos': case 'Clones': return [];
            case 'Messages': return { messages: [], live: [], deliveries: [], seq: 0, queue: { known: false } };
            case 'Models': return { agent: agents[0], models: [] };
            case 'TakeFocus': return '';
            case 'Settings': return { appearance: 'system', language: 'en' };
            case 'GitHubApp': if (nextRead) { const read = nextRead; nextRead = null; return read.promise; } return structuredClone(state);
            case 'EnableGitHubApp': state.config.enabled = args[0]; break;
            case 'ConfigureGitHubApp': state.config = { ...args[0], enabled: false }; state.verified = false; break;
            case 'TestGitHubApp':
              if (testGate) { const gate = testGate; testGate = null; await gate; }
              if (state.config.repositories.some((r) => r !== 'idy/yesman')) throw new Error('App is not installed on this repository');
              state.verified = true; break;
            case 'ImportGitHubAppKey': if (args.length !== 1 || args[0] !== '123') throw new Error('unexpected key import wire shape'); break;
            case 'LockGitHubApp': state.unlocked = false; state.verified = false; state.setup.url = ''; break;
            case 'PrepareGitHubApp': state.configured = true; state.unlocked = true; break;
            case 'RegisterGitHubApp': state.setup = { url: 'http://127.0.0.1:12345/', expires: new Date(Date.now() + 1800000).toISOString(), complete: false }; break;
            case 'CancelGitHubAppRegistration': state.setup.url = ''; break;
            case 'OpenURL': return;
            default: return {};
          }
          return structuredClone(state);
        } };
      }, dto);
      await page.goto(`http://127.0.0.1:${server.address().port}/`);
      await page.locator('#settings-button').click();
      await page.locator('#settings-nav [data-page="github-app"]').click();
      const pageRoot = page.locator('#github-app-page'); await pageRoot.waitFor();
      const button = (label) => pageRoot.getByRole('button', { name: label, exact: true });
      await button('Enable App identity').click(); await button('Disable App identity').waitFor();
      const repos = pageRoot.getByRole('textbox', { name: 'Repositories, one owner/repo per line' });
      await repos.fill('other/uninstalled');
      assert.equal(await button('Test connection').isDisabled(), true);
      await button('Save scope').click(); await button('Enable App identity').waitFor();
      assert.equal(await button('Enable App identity').isDisabled(), true);
      await button('Test connection').click(); await pageRoot.getByRole('alert').filter({ hasText: 'not installed' }).waitFor();
      assert.equal(await button('Enable App identity').isDisabled(), true);
      await repos.fill('idy/yesman'); await button('Save scope').click();
      await page.waitForFunction(() => !document.querySelector('#github-app-page').getAttribute('aria-busy').includes('true'));
      await button('Test connection').click();
      await page.waitForFunction(() => [...document.querySelectorAll('#github-app-page button')].some((b) => b.textContent === 'Enable App identity' && !b.disabled));
      // An unsaved scope cannot activate the previously verified configuration.
      await repos.fill('idy/yesman\nother/uninstalled');
      assert.equal(await button('Enable App identity').isDisabled(), true);
      await repos.fill('idy/yesman'); await button('Save scope').click();
      await page.waitForFunction(() => !document.querySelector('#github-app-page').getAttribute('aria-busy').includes('true'));
      await pageRoot.getByRole('textbox', { name: 'App ID', exact: true }).fill('123');
      await button('Choose key file…').click();
      await page.waitForFunction(() => window.fixture.calls.some((c) => c.method === 'ImportGitHubAppKey'));
      await pageRoot.getByRole('textbox', { name: 'GitHub owner' }).fill('idy');
      await pageRoot.getByRole('textbox', { name: 'App name' }).fill('yesman fixture');
      await button('Start registration').click(); await button('Open registration').waitFor();
      // Finished in the browser: returning to the window shows it saved and
      // ended, and another registration can start.
      await page.evaluate(() => { window.fixture.state.setup = { complete: true }; window.dispatchEvent(new Event('focus')); });
      await pageRoot.getByText('App saved. Complete its repository installation, then save and test the scope below.', { exact: true }).waitFor();
      assert.equal(await button('Open registration').count(), 0);
      assert.equal(await button('Start registration').isDisabled(), false);
      await button('Start registration').click(); await button('Open registration').waitFor();
      await button('Cancel registration').click(); await button('Start registration').waitFor();
      // Return to the page during an outstanding mutation: the busy state is
      // retained and its eventual fresh result survives navigation.
      await page.evaluate(() => window.fixture.holdTest()); await button('Test connection').click();
      await page.locator('#settings-nav [data-page="general"]').click();
      await page.locator('#settings-nav [data-page="github-app"]').click();
      assert.equal(await button('Test connection').isDisabled(), true);
      await page.evaluate(() => window.fixture.releaseTest());
      await page.waitForFunction(() => [...document.querySelectorAll('#github-app-page button')].some((b) => b.textContent === 'Enable App identity' && !b.disabled));
      // Make a stale public read arrive after a successful Lock mutation.
      await page.evaluate(() => window.fixture.holdRead()); await button('Refresh').click();
      await button('Lock').click(); await pageRoot.getByText('Key storage locked', { exact: true }).waitFor();
      await page.evaluate(() => window.fixture.releaseRead());
      await pageRoot.getByText('Key storage locked', { exact: true }).waitFor();
      await button('Unlock').click(); await button('Lock').waitFor();
      assert.equal((await pageRoot.innerText()).includes('BEGIN RSA PRIVATE KEY'), false);
      await page.evaluate(() => { window.fixture.state.routingAvailable = false; window.fixture.state.config.enabled = true; });
      await button('Refresh').click();
      await pageRoot.getByRole('alert').filter({ hasText: 'command routing is unavailable' }).waitFor();
      assert.equal(await button('Disable App identity').isDisabled(), false);
      await button('Disable App identity').click();
      await button('Enable App identity').waitFor();
      assert.equal(await button('Enable App identity').isDisabled(), true);
      await page.evaluate(() => { window.fixture.state.routingAvailable = true; });
      await button('Refresh').click();
      await pageRoot.getByRole('alert').filter({ hasText: 'command routing is unavailable' }).waitFor({ state: 'detached' });
      for (const language of ['en', 'zh-Hans']) {
        await page.evaluate((language) => window.events['mygo:locale']({ locale: language }), language);
        if (language === 'zh-Hans') assert.ok((await pageRoot.innerText()).includes('PR 身份'));
        for (const width of [1000, 480]) for (const theme of ['light', 'dark']) {
        await page.setViewportSize({ width, height: 1000 });
        await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; }, theme);
        const dimensions = await pageRoot.evaluate((el) => ({ scroll: el.scrollWidth, width: el.clientWidth }));
        assert.ok(dimensions.scroll <= dimensions.width + 1, `${engineName} ${width} overflow ${JSON.stringify(dimensions)}`);
        if (out) await page.screenshot({ path: join(out, `${engineName}-${language}-${width}-${theme}.png`), fullPage: true });
        }
      }
      const calls = await page.evaluate(() => window.fixture.calls.filter((c) => ['ImportGitHubAppKey', 'ConfigureGitHubApp', 'EnableGitHubApp'].includes(c.method)));
      assert.deepEqual(calls.find((c) => c.method === 'ImportGitHubAppKey').args, ['123']);
      assert.equal(errors.length, 0, errors.join('\n'));
      reports.push({ engine: engineName, passed: true, scopeAndPriority: true, deniedRepository: true, nativeImportWire: true, registration: true, staleRead: true, pendingNavigation: true, unavailableRoutingRecovery: true, layouts: 8 });
    } finally { await browser.close(); }
  }
  if (out) await writeFile(join(out, 'results.json'), JSON.stringify(reports, null, 2));
  console.log(JSON.stringify(reports, null, 2));
} finally { server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); await rm(temp, { recursive: true, force: true }); }
