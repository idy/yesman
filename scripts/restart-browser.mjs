// Optional browser checks of Restart and continue on the complete production
// page: install Playwright outside the worktree, then run
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/restart-browser.mjs
//
// A synthetic MyGo bridge answers the page's calls; one it does not know
// fails the run. Nothing touches a real workspace, settings or the app, and
// no restart happens: Restart and RestartToUpdate are only recorded. This is
// the page in Chromium and WebKit, not the app's native window.
//
// YESMAN_RESTART_ENGINES (chromium, webkit), YESMAN_RESTART_CHROMIUM (an
// executable to use) and YESMAN_RESTART_OUT (a folder for screenshots and a
// JSON result) are optional.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const playwright = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const out = process.env.YESMAN_RESTART_OUT || '';
if (out) await mkdir(out, { recursive: true });
const ENGINES = (process.env.YESMAN_RESTART_ENGINES || 'chromium,webkit').split(',');

const bridge = `<script>
const params = new URLSearchParams(location.search);
const handlers = {};
window.calls = [];
window.unmocked = [];
window.agents = [
  {id:'alice/lead',name:'lead',yesman:'alice',role:'lead',status:'working',runStartedAt:new Date().toISOString(),provider:'p',model:'m1',effort:'low',effortLevels:['low']},
  {id:'alice/fixer',name:'fixer',yesman:'alice',role:'worker',number:1,title:'Synthetic worker',status:'idle',provider:'p',model:'m1',effort:'low',effortLevels:['low']},
];
window.updateView = {enabled:true,state:'idle',checkedAt:new Date().toISOString()};
window.restartView = {};
window.mygo = {platform:'darwin', locale: params.get('locale') || 'en', on:(name,fn)=>handlers[name]=fn, call:async(method,...args)=>{
  const m = method.replace('Yesman.','');
  calls.push([m, ...JSON.parse(JSON.stringify(args))]);
  return JSON.parse(JSON.stringify(await answer(m, args)) ?? 'null');
}};
async function answer(m, args) {
  switch (m) {
  case 'State': return {home:'/synthetic',pool:'/synthetic/repos',yesmen:[{name:'alice',displayName:'Alice',path:'/synthetic/yesmen/alice',worktrees:[]}],agents};
  case 'Repos': case 'Clones': case 'Pulls': case 'FindFiles': case 'Topics': return [];
  case 'Settings': return {appearance:params.get('scheme') || 'light',autoFetch:0,home:'/synthetic',pool:'/synthetic/repos'};
  case 'Fetches': return {fetching:[],errors:{}};
  case 'Messages': return {messages:[]};
  case 'TakeFocus': return '';
  case 'Update': return updateView;
  case 'RestartStatus': return restartView;
  case 'About': return {version:'0.1.47',revision:'0123456789abcdef',time:'2026-11-17T00:00:00Z',pi:'0.0.0 (synthetic)',piManaged:true,mygo:'v0.9.0',magpie:'0.4.4',go:'go1.27.3'};
  case 'Models': return {models:[{provider:'p',id:'m1',name:'Model One'}],agent:agents.find(a=>a.id===args[0])};
  case 'Info': return {knowledge:[],worktrees:[]};
  case 'Board': return {tasks:[]};
  case 'Schedules': return {plans:[]};
  case 'Crew': return {members:[],yesman:''};
  case 'Context': case 'Usage': return null;
  case 'Shortcuts': return {};
  case 'Restart': case 'RestartToUpdate': return null;
  case 'Abort': case 'Stop': case 'WatchFiles': case 'SetAppearance': return;
  default: unmocked.push(m); return null;
  }
}
window.emit = (name, value) => handlers[name](JSON.parse(JSON.stringify(value)));
</script>`;

const server = createServer(async (req, res) => {
  try {
    const path = req.url.split('?')[0] === '/' ? 'index.html' : req.url.slice(1).split('?')[0];
    if (path.includes('..')) throw Error('invalid path');
    let body = await readFile(root + path);
    res.setHeader('Content-Type', path.endsWith('.html') ? 'text/html' : path.endsWith('.js') ? 'text/javascript' : path.endsWith('.css') ? 'text/css' : path.endsWith('.woff2') ? 'font/woff2' : 'application/octet-stream');
    if (path === 'index.html') body = body.toString().replace('<head>', '<head>' + bridge);
    res.end(body);
  } catch { res.statusCode = 404; res.end(); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}/`;

const WORDS = {
  en: { button: 'Restart and continue', confirm: 'Restart Yes Man?', ok: 'Restart', hint: 'Working agents continue afterwards from their sessions.',
    ready: 'Starts the installed update. Working agents continue afterwards.', pending: 'Restarting…', pill: 'Restart to update',
    summary: /Last restart, .+: 1 continued, 1 report handed on, 1 report already with its lead, 1 uncertain, not sent again, 1 waits for you, 1 kept stopped, 1 already finished, 1 no longer there, 1 could not continue$/,
    skipped: /: 1 kept stopped, 1 already finished$/, none: /: No agent was working\.$/, unfinished: /: Its record was not finished, so no agent was continued: 2 interrupted agents wait for a message\.$/, toast: /Yes Man restarted: 1 continued/, palette: 'Restart and continue' },
  'zh-Hans': { button: '重启并继续', confirm: '重启 Yes Man？', ok: '重启', hint: '正在工作的智能体会在重启后从各自的会话继续。',
    ready: '将启动已安装的更新。正在工作的智能体会在重启后继续。', pending: '正在重启…', pill: '重启以更新',
    summary: /上次重启于 .+：1 个已继续、已转交 1 份汇报、1 份汇报此前已到 lead、1 个不确定是否送达，未重发、1 个在等你、1 个保持停止、1 个已完成、1 个已不存在、1 个未能继续$/,
    skipped: /：1 个保持停止、1 个已完成$/, none: /：当时没有智能体在工作。$/, unfinished: /：重启记录未完成，因此没有继续任何智能体：2 个被中断的智能体在等待消息。$/, toast: /Yes Man 已重启：1 个已继续/, palette: '重启并继续' },
};

const results = [];
for (const engine of ENGINES) {
  const executablePath = engine === 'chromium' ? process.env.YESMAN_RESTART_CHROMIUM || undefined : undefined;
  const browser = await playwright[engine].launch({ headless: true, executablePath });
  for (const locale of ['en', 'zh-Hans']) {
    for (const scheme of ['light', 'dark']) {
      const w = WORDS[locale];
      const shot = async (page, name) => {
        if (out) await page.screenshot({ path: `${out}/${engine}-${locale}-${scheme}-${name}.png` });
      };
      const context = await browser.newContext({ viewport: { width: 1180, height: 780 }, colorScheme: scheme });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (e) => errors.push(String(e)));
      await page.goto(`${base}?locale=${locale}&scheme=${scheme}`);
      await page.locator('#settings-nav, .sidebar').first().waitFor();

      // The palette offers the action; with an agent at work it asks once.
      await page.keyboard.press('Meta+K');
      await page.locator('dialog.palette-dialog').waitFor({ state: 'visible' });
      await page.keyboard.type(w.palette);
      await shot(page, 'palette');
      await page.keyboard.press('Enter');
      await page.locator('#confirm-dialog').waitFor({ state: 'visible' });
      assert.equal(await page.locator('#confirm-title').innerText(), w.confirm);
      assert.equal(await page.locator('#confirm-ok').innerText(), w.ok);
      await shot(page, 'confirm');
      await page.locator('#confirm-dialog button[value="cancel"]').click(); // canceling sends nothing
      assert.deepEqual(await page.evaluate(() => calls.filter((c) => c[0].startsWith('Restart') && c[0] !== 'RestartStatus')), []);

      // Settings › About: the row, by mouse.
      await page.evaluate(() => emit('agents', agents.map((a) => ({ ...a, status: 'idle' }))));
      await page.keyboard.press('Meta+,');
      await page.locator('#settings-nav [data-page="about"]').click();
      const button = page.getByRole('button', { name: w.button });
      await button.waitFor();
      assert.match(await page.locator('.about-update', { has: button }).innerText(), new RegExp(w.hint));
      const glyph = await button.locator('.mds-icon, [aria-hidden="true"]').first().evaluate((el) => getComputedStyle(el).fontFamily);
      assert.match(glyph, /Phosphor/i, 'the reload glyph is the local Phosphor font');
      await shot(page, 'about');
      // A double click sends one restart: the button is pending at once.
      await button.dblclick();
      await page.getByText(w.pending).waitFor();
      assert.equal(await button.isDisabled(), true);
      assert.deepEqual(await page.evaluate(() => calls.filter((c) => c[0] === 'Restart').length), 1);
      await shot(page, 'pending');

      // The next instance's result: a toast and a summary.
      await page.evaluate(() => emit('restart', { last: { id: 'x', kind: 'restart', resumedAt: new Date().toISOString(), version: '0.1.47', agents: [
        { yesman: 'alice', name: 'lead', role: 'lead', reason: 'working', outcome: 'resumed' },
        { yesman: 'alice', name: 'fixer', role: 'worker', reason: 'needs-you', outcome: 'waiting' },
        { yesman: 'alice', name: 'other', role: 'worker', reason: 'working', outcome: 'failed', error: 'pi did not start' },
        { yesman: 'alice', name: 'r1', role: 'worker', reason: 'report', outcome: 'reported' },
        { yesman: 'alice', name: 'r2', role: 'worker', reason: 'report', outcome: 'accepted' },
        { yesman: 'alice', name: 'r3', role: 'worker', reason: 'report', outcome: 'uncertain' },
        { yesman: 'alice', name: 's1', role: 'worker', reason: 'working', outcome: 'stopped' },
        { yesman: 'alice', name: 's2', role: 'worker', reason: 'working', outcome: 'finished' },
        { yesman: 'alice', name: 's3', role: 'worker', reason: 'working', outcome: 'gone' },
      ] } }));
      await page.getByText(w.toast).first().waitFor();
      // The row's first line: the summary, before the button.
      const summary = async () => (await page.locator('.about-update', { has: page.getByRole('button', { name: w.button }) }).innerText()).split('\n')[0];
      assert.match((await summary()).trim(), w.summary);
      assert.equal(await page.getByRole('button', { name: w.button }).isDisabled(), false);
      await shot(page, 'result');
      // Agents it recorded and then skipped are counted, not called idle;
      // only a restart that recorded none says no agent was working.
      await page.evaluate(() => emit('restart', { last: { id: 'y', kind: 'restart', resumedAt: new Date().toISOString(), version: '0.1.47', agents: [
        { yesman: 'alice', name: 's1', role: 'worker', reason: 'working', outcome: 'stopped' },
        { yesman: 'alice', name: 's2', role: 'worker', reason: 'working', outcome: 'finished' },
      ] } }));
      const waitSummary = async (re) => {
        for (const end = Date.now() + 5000; !re.test((await summary()).trim()); await page.waitForTimeout(50)) {
          assert.ok(Date.now() < end, `summary ${JSON.stringify(await summary())} never matched ${re}`);
        }
      };
      await waitSummary(w.skipped);
      assert.doesNotMatch(await summary(), w.none);
      await page.evaluate(() => emit('restart', { last: { id: 'z', kind: 'restart', resumedAt: new Date().toISOString(), version: '0.1.47', agents: [] } }));
      await waitSummary(w.none);
      await shot(page, 'skipped');
      // A record the old instance did not finish: nobody went on, and it says so.
      await page.evaluate(() => emit('restart', { last: { id: 'u', kind: 'update', resumedAt: new Date().toISOString(), version: '0.1.48', unfinished: true, agents: [
        { yesman: 'alice', name: 'lead', role: 'lead', reason: 'working', outcome: 'not-continued' },
        { yesman: 'alice', name: 'fixer', role: 'worker', reason: 'working', outcome: 'not-continued' },
      ] } }));
      await waitSummary(w.unfinished);
      await shot(page, 'unfinished');

      // An installed update: the row says it starts; the pill restarts by
      // keyboard, through the same path.
      await page.evaluate(() => { emit('restart', {}); emit('update', { enabled: true, state: 'ready', version: '0.1.48' }); });
      await page.getByText(w.ready).waitFor();
      const pill = page.getByRole('button', { name: w.pill });
      await pill.focus();
      await page.keyboard.press('Enter');
      await page.waitForFunction(() => calls.some((c) => c[0] === 'RestartToUpdate'));
      await shot(page, 'update');

      // Narrow window.
      await page.setViewportSize({ width: 560, height: 760 });
      await page.getByRole('button', { name: w.button }).scrollIntoViewIfNeeded();
      const box = await page.getByRole('button', { name: w.button }).boundingBox();
      assert.ok(box && box.x + box.width <= 560, 'the button stays in a narrow window');
      await shot(page, 'narrow');

      const unmocked = await page.evaluate(() => unmocked);
      assert.deepEqual(unmocked, [], `unmocked calls: ${unmocked}`);
      assert.deepEqual(errors, []);
      results.push({ engine, locale, scheme, pass: true });
      await context.close();
    }
  }
  await browser.close();
}
server.close();
if (out) await writeFile(`${out}/result.json`, JSON.stringify(results, null, 2));
console.log(JSON.stringify(results));
