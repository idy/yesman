// Optional production-page check with a synthetic desktop bridge. Set
// YESMAN_PLAYWRIGHT to Playwright's index.mjs outside the repository.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
const { chromium, webkit } = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    if (path.includes('..')) throw Error('invalid path');
    const file = path === '/' ? 'index.html' : path.slice(1);
    res.setHeader('Content-Type', ({js:'text/javascript', html:'text/html', css:'text/css', woff2:'font/woff2'})[file.split('.').at(-1)] || 'application/octet-stream');
    res.end(await readFile(join(root, file)));
  } catch { res.writeHead(404).end(); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
try {
  for (const [name, engine] of Object.entries({chromium, webkit})) {
    const browser = await engine.launch({headless: true, ...(name === 'chromium' && process.env.YESMAN_TEST_CHROMIUM ? {executablePath: process.env.YESMAN_TEST_CHROMIUM} : {})});
    try {
      const page = await browser.newPage({viewport: {width: 1150, height: 780}});
      const errors = []; page.on('pageerror', e => errors.push(e.stack || e.message));
      await page.addInitScript(() => {
        const id = 'a'.repeat(32), head = 'b'.repeat(40), dir = 'room-finish';
        const active = '/data/yesmen/alice/' + dir, stored = '/data/archive/worktree-' + id;
        const w = {dir, path: active, repo: 'github.com/example/project', branch: 'local/new-branch', head, canArchive: true, cleanupId: id};
        const lead = {id: 'alice/lead', yesman:'alice', name:'lead', role:'lead', status:'stopped'};
        const y = {name:'alice', displayName:'Demo', hiredAt:'2026-02-01T12:00:00Z', path:'/data/yesmen/alice'};
        const handlers = {}, moves = []; window.moves = moves;
        let archived = false;
        window.mygo = {locale: 'en', on: (key, fn) => { handlers[key] = fn; }, call: async (method, ...args) => {
          switch (method.replace('Yesman.', '')) {
            case 'State': return {yesmen: [{...y, worktrees: archived ? [] : [w]}], agents:[lead]};
            case 'Settings': return {appearance:'system'};
            case 'Messages': return {messages:[]};
            case 'Models': return {models:[], agent:lead};
            case 'Board': return {tasks:[]};
            case 'Shortcuts': return {};
            case 'Info': return {knowledge:{}, topics:{}, worktrees:archived ? [] : [w], archiveMoves:archived ? [{dir, id, phase:'archived'}] : []};
            case 'MoveWorktree': {
              const r = args[0];
              if (r.id !== id || r.since !== y.hiredAt || r.dir !== dir || r.workspace !== y.name || r.action !== (archived ? 'restore' : 'archive')) throw Error('wrong move');
              moves.push(r);
              if (r.confirm) {
                if (r.confirm !== r.action + '-stamp') throw Error('wrong confirmation');
                archived = !archived;
                queueMicrotask(() => handlers.changed?.(true));
              }
              return {from: archived ? stored : active, to: archived ? active : stored, physical: archived ? stored : active, branch:w.branch, head, stamp:r.action + '-stamp'};
            }
            case 'Schedules': return {plans:[], runs:[]};
            case 'ScheduleSummary': return {total:0,on:0};
            case 'Usage': return {bytes:0, files:0, parts:[]};
            case 'TakeFocus': return '';
            case 'Update': return {enabled:false};
            case 'Fetches': return {fetching:[],errors:{}};
            default: return [];
          }
        }};
      });
      await page.goto('http://127.0.0.1:' + server.address().port);
      const archive = page.getByRole('button', {name:'Archive room-finish',exact:true});
      const dlg = page.locator('#confirm-dialog');
      await archive.click();
      try { await dlg.waitFor({timeout:8000}); }
      catch (e) { throw Error(`${name}: ${e.message}\n${JSON.stringify(await page.evaluate(() => ({moves, body:document.body.innerText.slice(-2000)})))}\n${errors}`); }
      const text = await dlg.innerText();
      for (const needle of ['local/new-branch', 'b'.repeat(40), '/data/yesmen/alice/room-finish', '/data/archive/worktree-', 'including ignored files']) assert.ok(text.includes(needle), text);
      if (process.env.YESMAN_MOVE_SCREENSHOTS) await page.screenshot({path:join(process.env.YESMAN_MOVE_SCREENSHOTS, name + '-confirm.png')});
      await page.keyboard.press('Escape');
      assert.equal(await page.evaluate(() => moves.filter(r=>r.confirm).length), 0);
      await archive.click(); await dlg.getByRole('button', {name:'Archive worktree'}).click();
      const fold = page.getByRole('button', {name:'Worktree archives (1)'});
      await fold.click();
      assert.equal(await archive.count(),0);
      const restore = page.getByRole('button', {name:/room-finish Restore worktree/});
      await restore.click(); await dlg.waitFor();
      assert.ok((await dlg.innerText()).includes('Destination: /data/yesmen/alice/room-finish'));
      await dlg.getByRole('button', {name:'Restore worktree'}).click();
      await archive.waitFor();
      assert.deepEqual(await page.evaluate(()=>moves.filter(r=>r.confirm).map(r=>r.action)), ['archive','restore']);
      assert.deepEqual(errors, []);
      console.log(name + ': preview, cancel, archive list, restore and exact confirmations passed');
    } finally { await browser.close(); }
  }
} finally { await new Promise(resolve => server.close(resolve)); }
