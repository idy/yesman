// Run over the production web assets with a controlled bridge and silent sink.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {mkdir, readFile} from 'node:fs/promises';
import {extname} from 'node:path';
import {fileURLToPath} from 'node:url';
const {chromium, webkit} = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_CHIME_OUT;
if (out) await mkdir(out, {recursive: true});
const mime = {'.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml', '.woff2': 'font/woff2'};
const server = createServer(async (req, res) => {
  try {
    const name = new URL(req.url, 'http://localhost').pathname.slice(1) || 'index.html';
    res.setHeader('Content-Type', mime[extname(name)] || 'application/octet-stream');
    res.end(await readFile(root + name));
  } catch {
    res.writeHead(404).end();
  }
}).listen(0, '127.0.0.1');
await new Promise((resolve) => server.on('listening', resolve));
const url = `http://127.0.0.1:${server.address().port}`;
try {
  for (const [name, engine, locale, theme] of [['chromium', chromium, 'en', 'light'], ['webkit', webkit, 'zh-Hans', 'dark']]) {
    const browser = await engine.launch({headless: true, ...(name === 'chromium' ? {args: ['--mute-audio']} : {})});
    try {
      const page = await browser.newPage({viewport: {width: 1180, height: 780}, colorScheme: theme});
      const errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      await page.route('**/*', (r) => r.request().url().startsWith(url + '/') ? r.continue() : r.abort());
      await page.addInitScript(({locale, theme}) => {
        const fixture = window.fixture = {writes: [], plays: [], unexpected: [], events: {}};
        window.mygo = {locale, on: (event, fn) => fixture.events[event] = fn, async call(method, ...args) {
          const m = method.replace('Yesman.', '');
          switch (m) {
            case 'State': return {yesmen: [], agents: []};
            case 'Repos': case 'Clones': return [];
            case 'TakeFocus': return '';
            case 'Shortcuts': return {};
            case 'Crew': return {members: []};
            case 'Update': return {enabled: false};
            case 'RestartStatus': return {};
            case 'Settings': return {appearance: theme, language: locale, completionSound: JSON.parse(localStorage.getItem('fixture.completionSound') || '{"enabled":false,"sound":"bell"}')};
            case 'SetCompletionSound':
              fixture.writes.push(args);
              if (fixture.saveError) throw Error('Synthetic save failure');
              localStorage.setItem('fixture.completionSound', JSON.stringify(args[0]));
              return;
            case 'PreviewCompletionSound':
              fixture.plays.push(args[0]);
              if (fixture.hold) await new Promise((resolve) => { fixture.release = resolve; });
              if (fixture.outputError) return 'unavailable';
              return '';
            default:
              fixture.unexpected.push(m);
              throw Error('Unexpected bridge call ' + m);
          }
        }};
      }, {locale, theme});
      const open = async () => {
        await page.goto(url);
        await page.locator('#settings-button').click();
        await page.locator('#settings-nav [data-page="general"]').click();
        await page.locator('.settings-section .mds-select').first().waitFor();
      };
      await open();
      if (process.env.YESMAN_CHIME_BASELINE === '1') {
        if (out) await page.screenshot({path: `${out}/${name}-baseline.png`});
        continue;
      }
      const section = page.locator('#completion-sounds');
      const toggle = section.getByRole('switch');
      const choice = section.getByRole('combobox');
      const preview = section.getByRole('button');
      const status = section.getByRole('status');
      const effects = () => page.evaluate(() => ({plays: fixture.plays, writes: fixture.writes, unexpected: fixture.unexpected}));
      assert.equal(await toggle.isChecked(), false);
      assert.equal(await choice.inputValue(), 'bell');
      assert.equal(await choice.locator('option').count(), 4);
      assert.deepEqual(await effects(), {plays: [], writes: [], unexpected: []});
      await page.evaluate(() => { fixture.hold = true; });
      await preview.focus();
      await page.keyboard.press('Space');
      await page.waitForFunction(() => typeof fixture.release === 'function');
      await page.keyboard.press('Space');
      assert.equal(await preview.evaluate((el) => el === document.activeElement), true);
      assert.deepEqual((await effects()).plays, ['bell']);
      await choice.selectOption('pluck');
      await page.waitForFunction(() => !document.querySelector('#completion-sound-choice').matches('[aria-disabled="true"]'));
      await page.evaluate(() => { fixture.hold = false; fixture.outputError = true; fixture.release(); });
      await page.waitForFunction(() => !document.querySelector('#completion-sound-preview').matches('[aria-disabled="true"]'));
      assert.equal(await status.innerText(), ''); // earlier sound's failure can't replace this choice's feedback
      await preview.click();
      await status.filter({hasText: locale === 'en' ? 'Check your audio output' : '请检查音频输出'}).waitFor();
      if (out && name === 'webkit') await page.screenshot({path: `${out}/webkit-preview-error.png`});
      await page.evaluate(() => { fixture.outputError = false; fixture.saveError = true; });
      await choice.selectOption('double');
      await status.filter({hasText: locale === 'en' ? 'Could not save' : '无法保存'}).waitFor();
      assert.equal(await choice.inputValue(), 'pluck');
      await page.evaluate(() => { fixture.saveError = false; });
      await choice.selectOption('sparkle');
      await toggle.focus();
      await page.keyboard.press('Space');
      assert.equal(await toggle.isChecked(), true);
      assert.deepEqual((await effects()).plays, ['bell', 'pluck']);
      await open();
      assert.equal(await toggle.isChecked(), true);
      assert.equal(await choice.inputValue(), 'sparkle');
      assert.deepEqual((await effects()).plays, []);
      if (out) await page.screenshot({path: `${out}/${name}-settings.png`});
      await page.setViewportSize({width: 640, height: 740});
      const labelFits = () => choice.evaluate((el) => {
        const style = getComputedStyle(el);
        const ctx = document.createElement('canvas').getContext('2d');
        ctx.font = `${style.fontSize} ${style.fontFamily}`;
        return ctx.measureText(el.selectedOptions[0].text).width <= el.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
      });
      assert.equal(await labelFits(), true);
      await page.setViewportSize({width: 480, height: 740});
      await preview.focus();
      await page.keyboard.press('Space');
      await page.waitForFunction(() => fixture.plays.length === 1);
      assert.deepEqual((await effects()).plays, ['sparkle']);
      const rect = await section.evaluate((el) => {
        const range = document.createRange();
        const button = el.querySelector('button');
        range.selectNodeContents(button);
        const group = el.querySelector('.mds-group').getBoundingClientRect();
        return [el.querySelector('select').getBoundingClientRect(), button.getBoundingClientRect(), range.getBoundingClientRect()].map((item) => item.left >= group.left && item.right <= group.right);
      });
      assert.deepEqual(rect, [true, true, true]);
      assert.equal(await labelFits(), true);
      await preview.scrollIntoViewIfNeeded();
      assert.deepEqual((await effects()).unexpected, []);
      assert.deepEqual(errors, []);
      if (out) await page.screenshot({path: `${out}/${name}-narrow.png`});
      await page.evaluate(() => localStorage.setItem('fixture.completionSound', '{"enabled":false,"sound":"old-sound"}'));
      await open();
      assert.equal(await choice.inputValue(), 'bell');
      await preview.click();
      assert.deepEqual((await effects()).plays, ['bell']);
      console.log(`${name} ${locale}: settings, keyboard, explicit preview, stale save/preview, reload and narrow layout passed`);
    } finally {
      await browser.close();
    }
  }
} finally {
  await new Promise((resolve) => server.close(resolve));
}
