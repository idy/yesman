// Optional browser checks of Settings › Model usage: install Playwright
// outside the worktree, then run
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/usage-browser.mjs.
// The complete production page runs against a synthetic bridge with made-up
// figures; no workspace, session log or model is read. Screenshots go to
// YESMAN_USAGE_SHOTS (default /tmp/yesman-usage-shots).
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdir, readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const shots = process.env.YESMAN_USAGE_SHOTS || '/tmp/yesman-usage-shots';
await mkdir(shots, { recursive: true });

const bridge = (lang) => `<script>
const handlers = {};
window.calls = [];
window.usageDelay = {};   // by yesman: ms before the answer
window.failNext = false;
window.partial = false;
const row = (o) => ({provider:'magpie',model:'',effort:'',workers:0,leads:0,tasks:0,responses:0,failed:0,other:0,input:0,output:0,cacheRead:0,cacheWrite:0,total:0,timed:0,untimed:0,timedOutput:0,timedMs:0,speed:null,...o});
const full = () => ({
 rows:[
  row({model:'codex-remote/codex/gpt-6.1-sol',effort:'xhigh',workers:3,tasks:5,responses:1200,failed:3,input:1234567,output:456789,cacheRead:98765432,cacheWrite:0,total:100456788,timed:1100,untimed:97,timedOutput:400000,timedMs:8000000,speed:50}),
  row({model:'auto/claude-opus-5-5',effort:'high',workers:1,tasks:1,responses:10,input:1000,output:2469,cacheRead:5000,cacheWrite:20,total:8489,timed:10,timedOutput:2469,timedMs:20000,speed:123.45,responseModels:['group/auto-claude-opus-5-5']}),
  row({model:'auto/claude-opus-5-5',effort:'xhigh',workers:2,tasks:0,responses:4,input:10,output:7,cacheRead:0,cacheWrite:0,total:17,untimed:4}),
  row({provider:'provider-with-a-long-name',model:'some/really/long/model-identifier-that-keeps-going-2026-11-14-preview-edition',workers:1,responses:2,input:12345678901234,output:1,total:12345678901235,timed:2,timedOutput:1,timedMs:1000,speed:1}),
  row({provider:'',other:3,input:50,output:20,total:70,workers:1}),
 ],
 total:row({provider:'',workers:4,tasks:6,responses:1216,failed:3,other:3,input:12345680136811,output:459286,cacheRead:98770432,cacheWrite:20,total:12345780565579,speed:null}),
 overlap:1,
 tasks:{done:11,attributed:6,severalModels:1,noResponses:0,unowned:1,unknownOwner:0,otherRole:1,reusedName:1,unknownTiming:1,timeUnknown:0,recorded:2},
 coverage:{workspaces:2,files:40,records:1219,from:'2026-11-13T16:49:41Z',to:'2026-11-14T12:45:53Z',unreadable:2,oversized:0,noEffort:2,noModel:3,errors:[],startsCut:[{yesman:'alma',through:'2026-11-10T08:00:00Z'}]},
 names:{'magpie/codex-remote/codex/gpt-6.1-sol':'GPT-6.1 Sol · Codex','magpie/auto/claude-opus-5-5':'Claude Opus 5.5 · routing group'},
 yesmen:[{name:'alma',label:'Alma 项目'},{name:'gizmoth',label:'GizMoth'}],
 readAt:'2026-11-14T12:46:00Z',
});
function usage(q){
 const v = full();
 v.query = q;
 if (q.yesman==='alma') { v.rows=[row({model:'auto/claude-opus-5-5',effort:'high',workers:1,tasks:2,responses:3,input:3,output:300,total:303,timed:3,timedOutput:300,timedMs:3000,speed:100})]; v.total=row({provider:'',workers:1,tasks:2,responses:3,input:3,output:300,total:303}); v.overlap=0; v.tasks={...v.tasks,done:2,attributed:2,severalModels:0,unowned:0,otherRole:0}; }
 if (q.yesman==='gizmoth') v.coverage={...v.coverage,startsCut:[]};
 if (q.yesman==='gizmoth') { v.rows=[row({model:'stale/gizmoth-model',effort:'low',responses:1,total:1})]; v.total=row({provider:'',responses:1,total:1}); }
 if (q.agents==='all') { v.rows[0].leads=1; v.total.leads=1; }
 if (q.agents==='leads') { v.rows=[row({model:'codex-remote/codex/gpt-6.1-sol',effort:'medium',leads:2,responses:9,output:90,total:90,timedOutput:90,timedMs:3000,speed:30})]; v.total=row({provider:'',leads:2,responses:9,output:90,total:90}); v.overlap=0; }
 if (q.period==='24h') { v.rows=[]; v.total=row({provider:''}); v.overlap=0; v.tasks={done:0,attributed:0,severalModels:0,noResponses:0,unowned:0,unknownOwner:0,otherRole:0,timeUnknown:7,recorded:0}; v.coverage={...v.coverage,records:0,unreadable:0,noEffort:0}; }
 if (partial) v.coverage.errors=['gizmoth: workers.json: invalid character','alma: w3/2026-11-14T10-00-00-000Z_x.jsonl: permission denied','alma: sessions: not a directory in the workspace (a link or a special file); not read'];
 return v;
}
const agents = [{id:'alma/lead',name:'lead',yesman:'alma',role:'lead',status:'idle'}];
window.mygo = {locale:'${lang}',on:(name,fn)=>handlers[name]=fn,call:async(method,...args)=>{
 const m=method.replace('Yesman.','');calls.push([m,...args]);
 switch(m){
 case 'State': return {home:'/synthetic',pool:'/synthetic/repos',yesmen:[{name:'alma',displayName:'Alma 项目',path:'/synthetic/yesmen/alma',worktrees:[]}],agents};
 case 'ModelUsage': {
  const q={...args[0]};
  await new Promise(r=>setTimeout(r, usageDelay[q.yesman] ?? 150));
  if (failNext) { failNext=false; throw Error('Synthetic read failure'); }
  return usage(q);
 }
 case 'Crew': return {date:'2026-11-01',source:'https://artificialanalysis.ai',top:70,models:[],members:[],yesman:''};
 case 'Repos':return [];
 case 'Settings':return {appearance:'system',autoFetch:0};
 case 'Fetches':return {fetching:[],errors:{}};
 case 'Clones':case 'Pulls':case 'ListFiles':return [];
 case 'Messages':return {messages:[]};
 case 'TakeFocus':return '';
 case 'Update':return {enabled:false};
 case 'Models':return {models:[],agent:agents.find(a=>a.id===args[0])};
 case 'Info':return {knowledge:[],worktrees:[]};
 case 'Board':return {tasks:[]};
 case 'WatchFiles':return;
 default:throw Error('Unmocked method '+m);
 }
}};
</script>`;

let lang = 'en';
const server = createServer(async (req, res) => {
  try {
    const path = req.url.split('?')[0] === '/' ? 'index.html' : req.url.slice(1).split('?')[0];
    if (path.includes('..')) throw Error('invalid path');
    let body = await readFile(root + path);
    res.setHeader('Content-Type', path.endsWith('.html') ? 'text/html' : path.endsWith('.js') ? 'text/javascript' : path.endsWith('.css') ? 'text/css' : 'application/octet-stream');
    if (path === 'index.html') body = body.toString().replace('<head>', '<head>' + bridge(lang));
    res.end(body);
  } catch { res.statusCode = 404; res.end(); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const url = 'http://127.0.0.1:' + server.address().port;

// The text of each cell of a row, by the column headers' labels.
async function table(page) {
  return page.locator('.usage-table').evaluate((t) => {
    const heads = [...t.tHead.rows[0].cells].map((c) => c.querySelector('.usage-sort > span').firstChild.textContent);
    // Every text node, visually hidden ones too, as a screen reader reads it.
    const text = (el) => {
      const parts = [];
      const walk = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
      while (walk.nextNode()) if (walk.currentNode.nodeValue.trim()) parts.push(walk.currentNode.nodeValue.trim());
      return parts.join(' ');
    };
    const read = (tr) => Object.fromEntries([...tr.cells].map((c, i) => [heads[i], text(c)]));
    return { heads, rows: [...t.tBodies[0].rows].map(read), total: read(t.tFoot.rows[0]) };
  });
}

try {
  for (const [engineName, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    const browser = await engine.launch({ headless: true });
    try {
      lang = 'en';
      const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
      const errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      await page.addInitScript(() => localStorage.setItem('yesman.info', 'hidden'));
      await page.goto(url);
      await page.locator('[data-agent="alma/lead"]').waitFor();
      await page.locator('#settings-button').click();
      const nav = page.locator('#settings-nav [data-page="usage"]');
      await nav.waitFor({ state: 'visible' });
      assert.equal((await nav.textContent()).trim(), 'Model usage');
      assert.equal(await nav.locator('[data-icon="chart"]').getAttribute('aria-hidden'), 'true');

      // From the benchmark scores to the measurements, and back.
      await page.locator('#settings-nav [data-page="yesman"]').click();
      await page.locator('.crew-source').getByRole('button', { name: 'Model usage' }).click();
      await page.locator('.usage-loading').getByText('Reading session logs…').waitFor();
      await page.locator('.usage-table').waitFor();
      assert.equal(await nav.getAttribute('aria-current'), 'page');

      // The figures as given, in the default order: largest total first.
      let t = await table(page);
      assert.deepEqual(t.heads, ['Model', 'Effort', 'Workers', 'Tasks done', 'Output speed', 'Total tokens', 'Responses', 'Input', 'Output', 'Cache read', 'Cache write']);
      assert.deepEqual(t.rows.map((r) => r.Model.split(' ')[0]), ['some/really/long/model-identifier-that-keeps-going-2026-11-14-preview-edition', 'GPT-6.1', 'Claude', 'Model', 'Claude']);
      const sol = t.rows[1];
      assert.deepEqual(sol, { Model: 'GPT-6.1 Sol magpie/codex-remote/codex/gpt-6.1-sol', Effort: 'Extra high', Workers: '3', 'Tasks done': '5', 'Output speed': '50.0 88% of output timed', 'Total tokens': '100,456,788', Responses: '1,200', Input: '1,234,567', Output: '456,789', 'Cache read': '98,765,432', 'Cache write': '0' });
      assert.equal(t.rows[2]['Output speed'], '123.5');
      assert.match(t.rows[2].Model, /Answered as group\/auto-claude-opus-5-5/);
      assert.equal(t.rows[4]['Output speed'], '— Not measured');
      assert.equal(t.rows[4]['Tasks done'], '0');
      assert.equal(t.rows[0].Effort, '— Not recorded');
      assert.equal(t.rows[0].Input, '12,345,678,901,234');
      assert.equal(t.rows[3].Model, 'Model not recorded Compaction and tool usage');
      assert.equal(t.total.Workers, '4 each once');
      assert.equal(t.total['Tasks done'], '6');
      assert.equal(t.total['Output speed'], '— Speeds of different models are not combined');
      assert.equal(await page.locator('.usage-summary').evaluate((d) => [...d.querySelectorAll('dt, dd')].map((e) => e.textContent).join(' ')), 'Workers 4 Tasks done 6 Responses 1,216 Total tokens 12,345,780,565,579');
      const notes = await page.locator('.usage-notes').innerText();
      assert.match(notes, /2 log lines could not be read/);
      assert.match(notes, /1 worker used more than one model or effort/);
      assert.match(notes, /5 done tasks are not counted for a model \(its owner used several models, and when it was done is not recorded: 1; more than one worker may have held its owner’s name, so which did it is not known: 1; when its owner’s last response before it ended is not recorded: 1; no owner: 1; owned by a lead: 1\)/);
      assert.match(notes, /2 responses have no recorded effort/);
      assert.match(notes, /Worker starts in Alma 项目 at or before \S.* are not all kept: workers of a name who started by then may count as one, and tasks one of them may have done count for no model\./);
      assert.equal(await page.locator('.usage-method').evaluate((d) => d.open), false);
      // Only the table scrolls sideways, never the page.
      const pageFits = () => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
      assert.ok(await pageFits());
      await page.screenshot({ path: `${shots}/${engineName}-wide-en.png`, fullPage: true });

      // Sorting: by speed, missing speeds last either way; by model name.
      await page.locator('[data-sort="speed"]').click();
      t = await table(page);
      assert.deepEqual(t.rows.map((r) => r['Output speed'].split(' ')[0]), ['123.5', '50.0', '1.0', '—', '—']);
      assert.equal(await page.locator('th:has([data-sort="speed"])').getAttribute('aria-sort'), 'descending');
      // Keyboard: Enter on the focused header sorts again and keeps focus.
      await page.locator('[data-sort="speed"]').focus();
      await page.keyboard.press('Enter');
      t = await table(page);
      assert.deepEqual(t.rows.map((r) => r['Output speed'].split(' ')[0]), ['1.0', '50.0', '123.5', '—', '—']);
      assert.equal(await page.evaluate(() => document.activeElement.dataset.sort), 'speed');
      // WebKit on macOS moves focus to buttons with Option+Tab by default.
      await page.keyboard.press(engineName === 'webkit' && process.platform === 'darwin' ? 'Alt+Shift+Tab' : 'Shift+Tab');
      assert.equal(await page.evaluate(() => document.activeElement.dataset.sort), 'tasks');
      await page.locator('[data-sort="model"]').click();
      t = await table(page);
      assert.deepEqual(t.rows.map((r) => r.Model.split(' ')[0]), ['Claude', 'Claude', 'GPT-6.1', 'Model', 'some/really/long/model-identifier-that-keeps-going-2026-11-14-preview-edition']);
      assert.equal(await page.locator('th:has([data-sort="model"])').getAttribute('aria-sort'), 'ascending');

      // A slow answer for an earlier choice does not replace a later one.
      await page.evaluate(() => { usageDelay.gizmoth = 900; });
      const yesman = page.locator('[data-filter="yesman"]');
      await yesman.selectOption('gizmoth');
      await page.locator('.usage-loading').waitFor();
      assert.equal(await page.locator('.usage-table').count(), 0);
      // A keyboard user changing a filter keeps focus on it while the
      // results are drawn again.
      await yesman.focus();
      await yesman.selectOption('alma');
      await page.locator('.usage-table').waitFor();
      await page.waitForTimeout(1100);
      t = await table(page);
      assert.equal(t.rows.length, 1);
      assert.equal(t.rows[0].Model, 'Claude Opus 5.5 magpie/auto/claude-opus-5-5');
      assert.equal(t.rows[0]['Tasks done'], '2');
      assert.equal(await page.evaluate(() => document.activeElement.dataset.filter), 'yesman');
      assert.deepEqual(await page.evaluate(() => calls.filter((c) => c[0] === 'ModelUsage').slice(-2).map((c) => c[1].yesman)), ['gizmoth', 'alma']);

      // Agents: leads only, and both.
      await yesman.selectOption('');
      await page.locator('[data-filter="agents"]').selectOption('leads');
      await page.waitForFunction(() => document.querySelector('.usage-table [data-sort="leads"]'));
      t = await table(page);
      assert.ok(!t.heads.includes('Workers'));
      assert.equal(t.rows[0].Leads, '2');
      await page.locator('[data-filter="agents"]').selectOption('all');
      await page.waitForFunction(() => document.querySelector('.usage-table [data-sort="leads"]') && document.querySelector('.usage-table [data-sort="workers"]'));

      // A period without usage, and tasks done before times were recorded.
      await page.locator('[data-filter="period"]').selectOption('24h');
      await page.locator('.usage-empty').getByText('No model usage recorded').waitFor();
      assert.match(await page.locator('.usage-empty').innerText(), /No agent answered in this period/);
      assert.match(await page.locator('.usage-notes').innerText(), /7 tasks were done before yesman recorded when tasks are done/);
      await page.locator('[data-filter="period"]').selectOption('all');
      await page.locator('[data-filter="agents"]').selectOption('workers');
      await page.locator('.usage-table').waitFor();

      // A failed read says so and can be tried again; a partial one names
      // what it could not read and shows the rest.
      await page.evaluate(() => { failNext = true; });
      await page.locator('#usage-refresh').click();
      await page.locator('.usage-results [role="alert"]').getByText('Synthetic read failure').waitFor();
      await page.evaluate(() => { partial = true; });
      await page.getByRole('button', { name: 'Try again' }).click();
      await page.locator('.usage-table').waitFor();
      const alert = page.locator('.usage-results [role="alert"]');
      assert.match(await alert.innerText(), /Some session logs could not be read[\s\S]*gizmoth: workers.json[\s\S]*permission denied[\s\S]*alma: sessions: not a directory in the workspace \(a link or a special file\); not read/);
      // What could be read is still shown beside the notice.
      assert.equal((await table(page)).rows.length, 5);
      await page.evaluate(() => { partial = false; });
      await page.locator('#usage-refresh').click();
      await page.waitForFunction(() => !document.querySelector('.usage-results [role="alert"]') && document.querySelector('.usage-table'));

      // The method opens with the keyboard.
      await page.locator('.usage-method summary').focus();
      await page.keyboard.press('Enter');
      assert.equal(await page.locator('.usage-method').evaluate((d) => d.open), true);
      assert.match(await page.locator('.usage-method').innerText(), /from sending the request to the last token/);
      assert.match(await page.locator('.usage-method').innerText(), /dropped once a yesman has kept 10,000[\s\S]*Where dropped starts leave which worker held the owner’s name unknown, a task counts for no model\./);

      // The way back to the benchmark scores.
      await page.locator('.usage-page .mds-section-heading').getByRole('button', { name: 'Settings › yesman' }).click();
      await page.locator('.crew-source').waitFor();
      await nav.click();
      await page.locator('.usage-table').waitFor();

      // Narrow: the table scrolls sideways with the model column in view.
      await page.setViewportSize({ width: 640, height: 900 });
      const scroll = page.locator('.usage-scroll');
      assert.ok(await scroll.evaluate((e) => e.scrollWidth > e.clientWidth + 100));
      await scroll.focus();
      await page.keyboard.press('End');
      await scroll.evaluate((e) => { e.scrollLeft = e.scrollWidth; });
      const sticky = await page.locator('.usage-table tbody .usage-model').first().evaluate((c) => {
        const box = c.getBoundingClientRect();
        const outer = c.closest('.usage-scroll').getBoundingClientRect();
        return Math.round(box.left - outer.left);
      });
      assert.ok(sticky <= 2, `model column left at ${sticky}`);
      assert.ok(await pageFits());
      await page.screenshot({ path: `${shots}/${engineName}-narrow-en.png`, fullPage: true });

      // Dark.
      await page.emulateMedia({ colorScheme: 'dark' });
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.screenshot({ path: `${shots}/${engineName}-wide-dark.png`, fullPage: true });
      assert.deepEqual(errors, []);
      await page.close();

      // Simplified Chinese, dark and narrow.
      lang = 'zh-Hans';
      const zh = await browser.newPage({ viewport: { width: 760, height: 900 }, colorScheme: 'dark' });
      zh.on('pageerror', (e) => errors.push(e.message));
      await zh.addInitScript(() => localStorage.setItem('yesman.info', 'hidden'));
      await zh.goto(url);
      await zh.locator('[data-agent="alma/lead"]').waitFor();
      await zh.locator('#settings-button').click();
      await zh.locator('#settings-nav [data-page="usage"]').click();
      await zh.locator('.usage-table').waitFor();
      t = await table(zh);
      assert.deepEqual(t.heads, ['模型', '推理强度', '助手', '完成的任务', '输出速度', 'token 总数', '响应', '输入', '输出', '缓存读取', '缓存写入']);
      assert.equal(t.rows[1]['推理强度'], '超高');
      assert.equal(t.rows[1]['完成的任务'], '5');
      assert.equal(t.rows[0]['推理强度'], '— 未记录');
      assert.match(await zh.locator('.usage-notes').innerText(), /5 个已完成的任务没有计入任何模型（负责者使用过多个模型，且没有记录完成时间：1；可能不止一个助手用过负责者的名字，无法确定由哪个完成：1；负责者在完成前最后一个响应的结束时间没有记录：1；没有负责者：1；由主管负责：1）/);
      assert.match(await zh.locator('.usage-notes').innerText(), /Alma 项目 中 \S.* 及之前的助手启动记录不完整：那时以前以同一名字启动的助手可能被算作一个，他们可能完成的任务不计入任何模型。/);
      assert.equal((await zh.locator('#settings-nav [data-page="usage"]').textContent()).trim(), '模型用量');
      await zh.screenshot({ path: `${shots}/${engineName}-narrow-zh-dark.png`, fullPage: true });
      assert.deepEqual(errors, []);
      console.log(`${engineName}: values, sorting, keyboard, late answers, filters, empty, error/retry, partial, links, narrow, dark and zh-Hans passed`);
    } finally { await browser.close(); }
  }
} finally { server.close(); }
