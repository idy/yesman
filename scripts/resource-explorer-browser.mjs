// The resource explorer (content left, navigation right) in the production
// page, with a strict controlled MyGo bridge and synthetic data only: no real
// workspace, logs, sessions, settings or network. Every call the page makes
// is answered here or recorded as unmocked, which fails the run.
//
// YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs \
//   EXPLORER_ENGINES=chromium,webkit EXPLORER_PARTS=log,knowledge,files,topics,needs,visual \
//   EXPLORER_SHOTS=/path/for/screenshots node scripts/resource-explorer-browser.mjs
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
const pw = await import(process.env.YESMAN_PLAYWRIGHT || 'playwright');
const root = fileURLToPath(new URL('../web/', import.meta.url));
const shots = process.env.EXPLORER_SHOTS;
if (shots) await mkdir(shots, { recursive: true });
const engines = (process.env.EXPLORER_ENGINES || 'chromium,webkit').split(',');
const parts = new Set((process.env.EXPLORER_PARTS || 'log,knowledge,files,topics,needs,visual').split(','));

const bridge = `<script>
window.calls=[];window.unmocked=[];window.delayed={};window.releases={};
const handlers={};const nl=String.fromCharCode(10);
const H1='2026-03-01T08:00:00Z', HB='2026-03-02T08:00:00Z';
const toc=['# Working log','','<!-- yesman-task-log-toc v1 -->','',
 '- 2026-02-27 · t1 · [Start the explorer](task-log/2026-02-27-t1-start.md) — planned.',
 '- 2026-03-01 · t5 · [Records by day](task-log/2026-03-01-t5-days.md#validation) — merged.',
 '- 2026-03-02 · t9 · [Release the explorer patch](task-log/2026-03-02-release.md) — published.',
 '- 2026-03-01 · t3 · [Earlier PR of the same day, listed after](task-log/2026-03-01-t3-pr.md) — merged.',
 '- 2026-03-02 · t8 · [A very long record title that keeps going to show how the navigation wraps two lines and then clips the rest of it](task-log/2026-03-02-t8-long.md)',
 '- [Legacy catalog](task-log/legacy-'+'a'.repeat(64)+'-index-0001.md)',''].join(nl);
const rec=(title)=>'# '+title+nl+nl+'[Contents](../task-log.md)'+nl+nl+('Historical paragraph.'+nl+nl).repeat(40)+'## Validation'+nl+nl+'PASS'+nl;
window.filesFixture={alice:{
 '.yesman/task-log.md':toc,
 '.yesman/task-log/2026-02-27-t1-start.md':rec('Start the explorer'),
 '.yesman/task-log/2026-03-01-t5-days.md':rec('Records by day'),
 '.yesman/task-log/2026-03-01-t3-pr.md':rec('Earlier PR'),
 '.yesman/task-log/2026-03-01-t7-unlinked.md':rec('Unlinked same-day record'),
 '.yesman/task-log/2026-03-02-release.md':rec('Release'),
 '.yesman/task-log/2026-03-02-t8-long.md':rec('Long'),
 ['.yesman/task-log/legacy-'+'a'.repeat(64)+'-original.md']:'# Legacy original'+nl+nl+'## Old heading'+nl,
 ['.yesman/task-log/legacy-'+'a'.repeat(64)+'-index-0001.md']:'# Legacy catalog'+nl,
 '.yesman/knowledge/INDEX.md':'# Knowledge'+nl+nl+'- [Rules](rules.md)'+nl+nl+'## Catalog'+nl+nl+'## Policies'+nl,
 '.yesman/knowledge/rules.md':'# Durable rules'+nl+nl+('Rule text.'+nl+nl).repeat(60)+'## Icons'+nl+nl+'Use Phosphor.'+nl+nl+('More.'+nl+nl).repeat(60)+'### Fill weight'+nl+nl+'Stop is solid.'+nl,
 '.yesman/knowledge/about.md':'# About'+nl,
 'notes.md':'# Plain file'+nl+'Edit me'+nl,
},bob:{
 '.yesman/task-log.md':'# Bob log'+nl+nl+'- 2025-12-31 · [Bob only record](task-log/2025-12-31-bob.md)'+nl,
 '.yesman/task-log/2025-12-31-bob.md':rec('Bob only record'),
}};
const long='A topic with a deliberately long title to check wrapping in both the tree and the detail heading of the explorer';
const topic=(id,status,title,extra={})=>({id,status,title,summary:'**'+title+'** summary.',conclusions:[{id:id+'c1',text:'Content goes left, navigation right.'}],questions:status==='open'?[{id:id+'q1',text:'Which groups are real?'}]:[],sources:[{id:id+'s1',kind:'task',ref:'t192'}],sends:[],revision:1,updatedAt:'2026-03-0'+(id.slice(2)%9+1)+'T00:00:00Z',updatedBy:'user',...extra});
window.topicsFixture={['alice@'+H1]:[topic('tp1','open',long),topic('tp2','resolved','Resolved question'),topic('tp3','archived','Archived question'),topic('tp4','open','Second open question')],['bob@'+HB]:[topic('tp1','open','Bob topic')]};
window.fixtureState={home:'/synthetic',pool:'/synthetic/repos',
 yesmen:[{name:'alice',displayName:'Alice Lab',path:'/synthetic/alice',hiredAt:H1,worktrees:[],handoffs:[
   {id:'n1',type:'decision',title:'Choose the explorer default width',detail:'Pick **one**:'+nl+nl+'- 680px'+nl+'- 760px',task:'t192',createdAt:'2026-03-01T09:00:00Z'},
   {id:'n2',type:'login',title:'Log in to the package registry',detail:'Run the login yourself.',createdAt:'2026-03-01T09:05:00Z'},
   {id:'n0',type:'manual',title:'Closed earlier',createdAt:'2026-02-01T00:00:00Z',closedAt:'2026-02-02T00:00:00Z',resolution:'done'}]},
  {name:'bob',path:'/synthetic/bob',hiredAt:HB,worktrees:[],handoffs:[{id:'b1',type:'manual',title:'Bob-only request',createdAt:'2026-03-02T09:00:00Z'}]}],
 agents:[{id:'alice/lead',name:'lead',yesman:'alice',role:'lead',status:'waiting',since:H1,dialogAt:'2026-03-01T08:30:00Z',dialog:{id:'q1',method:'input',title:'Which branch name?',message:'Name the branch for the explorer.'}},
  {id:'bob/lead',name:'lead',yesman:'bob',role:'lead',status:'idle',since:HB}]};
const wait=async(key)=>{if(delayed[key])await new Promise(r=>releases[key]=r);};
window.mygo={locale:'en',on:(name,fn)=>handlers[name]=fn,call:async(method,...args)=>{
 const m=method.replace('Yesman.','');calls.push([m,...args]);
 switch(m){
 case 'State':return structuredClone(fixtureState);
 case 'Settings':return {appearance:'system',autoFetch:0};
 case 'Shortcuts':return {};case 'Context':return {agent:args[0],available:true};
 case 'Usage':return {bytes:0,files:0,parts:[],measuredAt:'2026-01-15T00:00:00Z'};
 case 'Repos':case 'Clones':case 'Pulls':return [];
 case 'Fetches':return {fetching:[],errors:{}};
 case 'TakeFocus':return '';case 'Update':return {enabled:false};
 case 'Messages':return {messages:[]};case 'Models':return {models:[],agent:fixtureState.agents.find(a=>a.id===args[0])};
 case 'Info':return {knowledge:{notes:2,index:true},worktrees:[]};case 'Board':return {tasks:[]};
 case 'WatchFiles':case 'Reveal':case 'OpenURL':return;
 case 'FilesMenu':return '';
 case 'ReadFile':{const [name,path]=args;await wait(name+':ReadFile:'+path);const text=filesFixture[name]?.[path];if(text===undefined)throw Error('no such file: '+path);return {path,kind:'text',text,size:text.length,hash:'h'+text.length,truncated:false};}
 case 'WriteFile':filesFixture[args[0]][args[1]]=args[2];return {path:args[1],kind:'text',text:args[2],hash:'saved'+args[2].length,size:args[2].length};
 case 'ListFiles':{const [name,rel]=args;await wait(name+':ListFiles:'+rel);if(window.failList===name+':'+rel)throw Error('listing failed (fixture)');const dir=rel==='.'?'':rel+'/';const map=new Map();
   for(const p of Object.keys(filesFixture[name]??{}).filter(p=>p.startsWith(dir))){const rest=p.slice(dir.length),base=rest.split('/')[0];map.set(dir+base,{name:base,path:dir+base,dir:rest.includes('/')});}
   if(!map.size&&rel!=='.')throw Error('no such file or directory: '+rel);return [...map.values()];}
 case 'Topics':{await wait('Topics:'+args[0]);if(window.failTopics===args[0])throw Error('Topics fixture failure');return structuredClone(topicsFixture[args[0]+'@'+args[1]]??[]);}
 case 'UpdateTopic':{const [name,hired,id,patch]=args;const list=topicsFixture[name+'@'+hired];if(!list)throw Error('stale hire');const x=list.find(t=>t.id===id);
   if(patch.revision!==x.revision)return {topic:structuredClone(x),conflict:true};
   for(const k of ['title','summary','status'])if(k in patch)x[k]=patch[k];if(patch.archive)x.status='archived';if(patch.restore)x.status='open';
   if(patch.conclusions)x.conclusions=patch.conclusions.map((c,i)=>({id:c.id||id+'cn'+i,text:c.text}));if(patch.questions)x.questions=patch.questions.map((c,i)=>({id:c.id||id+'qn'+i,text:c.text}));
   x.revision++;x.updatedAt=new Date().toISOString();return {topic:structuredClone(x),sources:[]};}
 case 'SubmitTopic':{const [name,hired,id,mid,text]=args;const x=topicsFixture[name+'@'+hired].find(t=>t.id===id);x.sends.push({id:mid,at:new Date().toISOString(),preview:text,context:'[Topic '+id+': '+x.title+']',state:'accepted'});return {delivery:{id:mid,revision:1,state:'accepted',text}};}
 case 'ResolveHandoff':{const [name,hired,id,res]=args;const y=fixtureState.yesmen.find(v=>v.name===name);if(y.hiredAt!==hired)throw Error('stale hire');const ho=y.handoffs.find(x=>x.id===id);if(!ho||ho.closedAt)throw Error('This request is no longer open.');ho.closedAt=new Date().toISOString();ho.resolution=res;return;}
 case 'Answer':{const [agent,dialog]=args;const a=fixtureState.agents.find(x=>x.id===agent);if(a.dialog?.id!==dialog)throw Error('stale dialog');a.status='idle';delete a.dialog;delete a.dialogAt;return;}
 default:unmocked.push(m);throw Error('Unmocked '+m);
 }}};
window.setFixtureLocale=(locale)=>handlers['mygo:locale']({locale});
</script>`;

const server = createServer(async (req, res) => {
  try {
    const path = req.url.split('?')[0] === '/' ? 'index.html' : req.url.slice(1).split('?')[0];
    if (path.includes('..')) throw Error('invalid path');
    let b = await readFile(join(root, path));
    res.setHeader('Content-Type', path.endsWith('.html') ? 'text/html' : path.endsWith('.js') ? 'text/javascript' : path.endsWith('.css') ? 'text/css' : 'application/octet-stream');
    if (path === 'index.html') b = b.toString().replace('<head>', '<head>' + bridge);
    res.end(b);
  } catch { res.statusCode = 404; res.end(); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const url = 'http://127.0.0.1:' + server.address().port;
const report = [];
const box = (loc) => loc.evaluate((e) => { const r = e.getBoundingClientRect(); return { x: r.x, w: r.width, right: r.right }; });
const shot = async (page, name) => { if (shots) await page.screenshot({ path: join(shots, name + '.png') }); };
const leftOf = async (a, b, what) => assert.ok((await box(a)).right <= (await box(b)).x + 1, what);

async function logPart(page, engine) {
  const files = page.locator('#files');
  await page.locator('[data-info-entry="working-logs"]').click();
  const tree = page.locator('#files-records[role="tree"]');
  await tree.waitFor();
  await leftOf(page.locator('#files-view'), page.locator('#files .files-tree'), 'content left of navigation');
  const groups = await tree.locator(':scope > [data-kind="group"] > .explorer-line .explorer-label').allInnerTexts();
  assert.deepEqual(groups, ['2026-03-02', '2026-03-01', '2026-02-27', 'No date'], 'newest day first, explicit no-date group last');
  const day2 = tree.locator('[data-node="d:2026-03-02"]');
  assert.equal(await day2.getAttribute('aria-expanded'), 'true');
  assert.deepEqual((await day2.locator('[role="group"] .explorer-label').allInnerTexts()).map((s) => s.slice(0, 18)), ['Release the explor', 'A very long record']);
  const day1 = tree.locator('[data-node="d:2026-03-01"]');
  assert.equal(await day1.getAttribute('aria-expanded'), 'false');
  await day1.focus();
  await page.keyboard.press('ArrowRight');
  await page.waitForFunction(() => document.querySelector('[data-node="d:2026-03-01"]')?.getAttribute('aria-expanded') === 'true');
  assert.deepEqual(await page.locator('[data-node="d:2026-03-01"] [role="group"] .explorer-label').allInnerTexts(), ['Records by day', 'Earlier PR of the same day, listed after', '2026-03-01-t7-unlinked']);
  assert.equal(await page.evaluate(() => document.activeElement?.dataset.node), 'd:2026-03-01', 'focus stays on the day after expanding');
  await page.keyboard.press('ArrowDown');
  assert.equal(await page.evaluate(() => document.activeElement?.dataset.node), 'r:.yesman/task-log/2026-03-01-t5-days.md');
  await page.keyboard.press('Enter');
  await files.locator('#validation').waitFor();
  assert.equal(await files.locator('#files-view [id="records-by-day"]').textContent(), 'Records by day');
  assert.match(decodeURIComponent(page.url()), /"anchor":"validation"/, 'the TOC link anchor is kept');
  assert.equal(await tree.locator('[aria-selected="true"]').getAttribute('data-node'), 'r:.yesman/task-log/2026-03-01-t5-days.md');
  const reads = await page.evaluate(() => [...new Set(calls.filter((c) => c[0] === 'ReadFile' && c[1] === 'alice').map((c) => c[2]))].sort());
  assert.deepEqual(reads, ['.yesman/task-log.md', '.yesman/task-log/2026-03-01-t5-days.md'], 'navigation reads no record bodies');
  await tree.locator('[data-node="d:none"] > .explorer-line').click();
  assert.equal(await tree.locator('[data-node="d:none"]').getAttribute('aria-expanded'), 'true');
  assert.equal(await tree.locator('[data-node="d:none"] [role="treeitem"]').count(), 2);
  await page.fill('#files-records-find', 'release');
  assert.deepEqual(await tree.locator('[data-kind="record"] .explorer-label').allInnerTexts(), ['Contents', 'Release the explorer patch']);
  await page.fill('#files-records-find', '');
  await tree.locator('[data-node="r:.yesman/task-log.md"] > .explorer-line').click();
  await files.getByRole('link', { name: 'Records by day', exact: true }).waitFor();
  await page.goBack();
  await files.locator('#validation').waitFor();
  assert.equal(await files.locator('#files-view [id="records-by-day"]').textContent(), 'Records by day');
  await shot(page, `${engine}-log-wide-en`);
  await page.evaluate(() => { window.failList = 'alice:.yesman/task-log'; });
  await files.getByRole('button', { name: 'Reload', exact: true }).click();
  await files.locator('#files-records-list .tree-note.danger', { hasText: 'listing failed' }).waitFor();
  await page.evaluate(() => { window.failList = ''; });
  await files.locator('#files-records-list .tree-note.danger button').click();
  await page.waitForFunction(() => !document.querySelector('#files-records-list .tree-note.danger'));
  await page.evaluate(() => { delayed['alice:ListFiles:.yesman/task-log'] = true; });
  await files.getByRole('button', { name: 'Reload', exact: true }).click();
  await page.waitForFunction(() => releases['alice:ListFiles:.yesman/task-log']);
  await page.locator('[data-agent="bob/lead"]').first().click();
  await page.locator('[data-info-entry="working-logs"]').click();
  await page.locator('#files-records [data-node="d:2025-12-31"]').waitFor();
  await page.evaluate(async () => { delayed['alice:ListFiles:.yesman/task-log'] = false; releases['alice:ListFiles:.yesman/task-log'](); await new Promise(requestAnimationFrame); await new Promise(requestAnimationFrame); });
  assert.deepEqual(await page.locator('#files-records > [data-kind="group"] > .explorer-line .explorer-label').allInnerTexts(), ['2025-12-31'], 'bob shows only bob');
  await page.locator('[data-agent="alice/lead"]').first().click();
}

async function knowledgePart(page, engine) {
  await page.locator('[data-info-entry="knowledge"]').click();
  const list = page.locator('#files-tree-list');
  await list.locator('[data-path=".yesman/knowledge/INDEX.md"]').waitFor();
  assert.equal(await list.locator('.tree-row:not(.tree-outline)').first().getAttribute('data-path'), '.yesman/knowledge/INDEX.md', 'INDEX first');
  await list.locator('[data-path=".yesman/knowledge/INDEX.md#catalog"]').waitFor();
  await list.locator('[data-path=".yesman/knowledge/rules.md"]').click();
  await page.locator('#files-view [id="durable-rules"]').waitFor();
  const row = list.locator('[data-path=".yesman/knowledge/rules.md"]');
  await list.locator('[data-path=".yesman/knowledge/rules.md#fill-weight"]').waitFor();
  assert.equal(await row.getAttribute('aria-expanded'), 'true');
  assert.equal(await list.locator('[data-path=".yesman/knowledge/rules.md#fill-weight"]').getAttribute('aria-level'), '4', 'note 1, # 2, ## 3, ### 4');
  await list.locator('[data-path=".yesman/knowledge/rules.md#icons"]').click();
  await page.waitForFunction(() => { const v = document.getElementById('files-view'); const h = document.getElementById('icons'); return v.scrollTop > 100 && Math.abs(h.getBoundingClientRect().top - v.getBoundingClientRect().top) < 40; });
  await row.focus();
  await page.keyboard.press('ArrowLeft');
  await page.waitForFunction(() => document.querySelector('#files-tree-list [data-path=".yesman/knowledge/rules.md"]').getAttribute('aria-expanded') === 'false');
  assert.equal(await list.locator('.tree-outline').count(), 0);
  await page.keyboard.press('ArrowRight');
  await list.locator('.tree-outline').first().waitFor();
  assert.equal(await page.locator('#files').getByRole('button', { name: 'Source', exact: true }).count(), 0, 'Knowledge stays read as rendered pages');
  await shot(page, `${engine}-knowledge-wide-en`);
}

async function filesPart(page) {
  await page.locator('[data-info-entry="knowledge"]').click();
  await page.locator('.files-crumb--scope').click();
  await page.locator('.files-menu [role="menuitem"]').first().click();
  await page.locator('#files-tree-list [data-path="notes.md"]').click();
  await page.locator('#files').getByRole('button', { name: 'Source', exact: true }).click();
  await page.locator('#files .cm-content').click();
  await page.keyboard.type('Edited here. ');
  await page.keyboard.press(process.platform === 'darwin' ? 'Meta+s' : 'Control+s');
  await page.waitForFunction(() => calls.some((c) => c[0] === 'WriteFile' && c[1] === 'alice' && c[2] === 'notes.md' && c[3].includes('Edited here.')));
  await leftOf(page.locator('#files-view'), page.locator('#files .files-tree'), 'files: content left of the tree');
}

async function topicsPart(page, engine) {
  await page.locator('[data-info-entry="topics"]').click();
  const pane = page.locator('#topics');
  const tree = pane.locator('#topics-tree');
  await tree.waitFor();
  await leftOf(pane.locator('.explorer-content'), pane.locator('.explorer-nav'), 'topics: content left of navigation');
  assert.deepEqual(await tree.locator(':scope > [data-kind="group"] > .explorer-line .explorer-label').allInnerTexts(), ['Open', 'Resolved', 'Archived']);
  assert.equal(await tree.locator('[data-node="g:open"]').getAttribute('aria-expanded'), 'true');
  assert.equal(await tree.locator('[data-node="g:archived"]').getAttribute('aria-expanded'), 'false');
  assert.deepEqual(await tree.locator('[data-node="g:open"] > [role="group"] > [data-kind="topic"]').evaluateAll((es) => es.map((e) => e.dataset.topic)), ['tp4', 'tp1']);
  await pane.locator('[data-key="topic-tp1"] > .explorer-line').click();
  await pane.locator('h3.topic-title', { hasText: 'deliberately long' }).waitFor();
  assert.equal(await pane.locator('[data-key="topic-tp1"]').getAttribute('aria-expanded'), 'true');
  assert.deepEqual(await pane.locator('[data-key="topic-tp1"] [data-kind="section"] .explorer-label').allInnerTexts(),
    ['Summary', 'Confirmed conclusions', 'Open questions', 'Sources', 'In the conversation', 'Continue the discussion']);
  await pane.locator('[data-key="part-tp1-continue"] > .explorer-line').click();
  await page.waitForFunction(() => { const c = document.querySelector('#topics .explorer-content'); const s = document.querySelector('#topics [data-section="continue"]'); return s.getBoundingClientRect().top - c.getBoundingClientRect().top < 60 || c.scrollTop + c.clientHeight >= c.scrollHeight - 1; });
  await pane.locator('[data-key="part-tp1-continue"]').focus();
  await page.keyboard.press('ArrowUp');
  assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'part-tp1-conversation');
  await shot(page, `${engine}-topics-wide-en`);
  await pane.locator('[data-key="edit"]').click();
  await pane.locator('[data-key="edit-title"]').fill('Explorer layout');
  await pane.locator('[data-key="edit-save"]').click();
  await pane.locator('h3.topic-title', { hasText: 'Explorer layout' }).waitFor();
  const update = await page.evaluate(() => calls.find((c) => c[0] === 'UpdateTopic'));
  assert.deepEqual(update.slice(1, 4), ['alice', '2026-03-01T08:00:00Z', 'tp1']);
  assert.equal(update[4].revision, 1);
  assert.equal(await pane.locator('[data-key="topic-tp1"] .explorer-label').first().innerText(), 'Explorer layout');
  await pane.locator('[data-key="compose-tp1"]').fill('Is the tree enough?');
  await pane.locator('[data-key="send-tp1"]').click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'SubmitTopic' && c[1] === 'alice' && c[2] === '2026-03-01T08:00:00Z' && c[3] === 'tp1' && c[5] === 'Is the tree enough?'));
  await pane.locator('[data-key="topic-tp4"] > .explorer-line').click();
  await pane.locator('[data-key="archive"]').click();
  await pane.locator('[data-node="g:archived"] [data-key="topic-tp4"]').waitFor();
  await pane.locator('[data-key="search"]').fill('Resolved question');
  assert.deepEqual(await tree.locator('[data-kind="topic"]').evaluateAll((es) => es.map((e) => e.dataset.topic)), ['tp2']);
  await pane.locator('[data-key="search"]').fill('');
  await page.evaluate(() => { window.failTopics = 'alice'; });
  await pane.locator('[data-key="refresh"]').click();
  await pane.locator('[data-key="retry"]').waitFor();
  await page.evaluate(() => { window.failTopics = ''; });
  await pane.locator('[data-key="retry"]').click();
  await page.waitForFunction(() => !document.querySelector('#topics [data-key="retry"]'));
  await page.evaluate(() => { delayed['Topics:alice'] = true; });
  await pane.locator('[data-key="refresh"]').click();
  await page.waitForFunction(() => releases['Topics:alice']);
  await page.locator('[data-agent="bob/lead"]').first().click();
  await page.locator('[data-info-entry="topics"]').click();
  await pane.locator('[data-key="topic-tp1"]').waitFor();
  await page.evaluate(async () => { delayed['Topics:alice'] = false; releases['Topics:alice'](); await new Promise(requestAnimationFrame); });
  assert.deepEqual(await pane.locator('#topics-tree [data-kind="topic"] .explorer-label').allInnerTexts(), ['Bob topic']);
  await page.locator('[data-agent="alice/lead"]').first().click();
}

async function needsPart(page, engine) {
  await page.locator('[data-info-entry="needs-you"]').click();
  const pane = page.locator('#needs-you');
  await pane.locator('.needs-you-list').waitFor();
  await leftOf(pane.locator('.explorer-content'), pane.locator('.explorer-nav'), 'needs you: detail left of the list');
  assert.deepEqual(await pane.locator('.needs-you-open').evaluateAll((es) => es.map((e) => e.dataset.request)),
    ['d:alice/lead@2026-03-01T08:00:00Z:q1', 'h:alice@2026-03-01T08:00:00Z:n1', 'h:alice@2026-03-01T08:00:00Z:n2']);
  assert.equal(await pane.locator('.needs-you-detail-title').innerText(), 'Which branch name?');
  await pane.locator('.needs-you-open').first().focus();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
  await pane.locator('.needs-you-detail-title', { hasText: 'Choose the explorer default width' }).waitFor();
  assert.equal(await pane.locator('.needs-you-detail .md li').count(), 2, 'the full Markdown detail');
  assert.equal(await page.evaluate(() => document.activeElement?.dataset.request), 'h:alice@2026-03-01T08:00:00Z:n1', 'focus stays in the list');
  await shot(page, `${engine}-needs-wide-en`);
  await pane.locator('[data-detail-control="done"]').click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'ResolveHandoff' && c.slice(1).join('|') === 'alice|2026-03-01T08:00:00Z|n1|done'));
  await pane.locator('.needs-you-detail-title', { hasText: 'Log in to the package registry' }).waitFor();
  await pane.locator('.needs-you-open').first().click();
  const input = pane.locator('[data-detail-control="answer-text"]');
  await input.fill('feat/resource-explorer');
  // Check again reads State and redraws the pane; the typed answer stays.
  const states = await page.evaluate(() => calls.filter((c) => c[0] === 'State').length);
  await pane.locator('[data-pane-control="refresh"]').click();
  await page.waitForFunction((n) => calls.filter((c) => c[0] === 'State').length > n, states);
  await page.evaluate(() => new Promise(requestAnimationFrame));
  assert.equal(await pane.locator('[data-detail-control="answer-text"]').inputValue(), 'feat/resource-explorer', 'a redraw keeps the typed answer');
  await pane.locator('.needs-you-ask [data-detail-control="answer-0"]').click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'Answer' && c[1] === 'alice/lead' && c[2] === 'q1' && c[3]?.value === 'feat/resource-explorer'));
  await pane.locator('.needs-you-open[data-request$=":n2"]').click();
  await pane.locator('[data-detail-control="not-needed"]').click();
  await page.waitForFunction(() => calls.some((c) => c[0] === 'ResolveHandoff' && c[3] === 'n2' && c[4] === 'not-needed'));
  await pane.locator('.needs-you-history').waitFor();
  await page.locator('[data-agent="bob/lead"]').first().click();
  await page.locator('[data-info-entry="needs-you"]').click();
  await pane.locator('.needs-you-detail-title', { hasText: 'Bob-only request' }).waitFor();
  assert.equal(await pane.locator('.needs-you-open').count(), 1);
  await page.locator('[data-agent="alice/lead"]').first().click();
}

async function visualPart(page, engine) {
  await page.setViewportSize({ width: 1100, height: 800 });
  await page.locator('[data-info-entry="topics"]').click();
  const pane = page.locator('#topics');
  await pane.locator('#topics-tree').waitFor();
  assert.equal(await pane.locator('.explorer-content').isVisible(), false, 'narrow: the tree first');
  await pane.locator('[data-key="topic-tp1"] > .explorer-line').click();
  await pane.locator('h3.topic-title').waitFor();
  assert.equal(await pane.locator('.explorer-nav').isVisible(), false);
  assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'back', 'narrow: focus on the way back, as before');
  await shot(page, `${engine}-topics-narrow-en`);
  await pane.locator('[data-key="back"]').click();
  await pane.locator('#topics-tree').waitFor();
  assert.equal(await page.evaluate(() => document.activeElement?.dataset.key), 'topic-tp1');
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.evaluate(() => setFixtureLocale('zh-Hans'));
  await page.locator('[data-info-entry="needs-you"]').click();
  await page.locator('#needs-you .needs-you-list').waitFor();
  await shot(page, `${engine}-needs-wide-zh-dark`);
  await page.locator('[data-info-entry="working-logs"]').click();
  await page.locator('#files-records').waitFor();
  assert.equal(await page.locator('#files-records [data-node="d:none"] .explorer-label').first().innerText(), '无日期');
  await shot(page, `${engine}-log-wide-zh-dark`);
  await page.locator('[data-info-entry="topics"]').click();
  await page.locator('#topics-tree').waitFor();
  await shot(page, `${engine}-topics-wide-zh-dark`);
}

const PARTS = { log: logPart, knowledge: knowledgePart, files: filesPart, topics: topicsPart, needs: needsPart, visual: visualPart };
try {
  for (const name of engines) {
    const browser = await pw[name].launch({ headless: true });
    try {
      for (const [part, run] of Object.entries(PARTS)) {
        if (!parts.has(part)) continue;
        const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
        page.setDefaultTimeout(8000);
        const errors = [];
        page.on('pageerror', (e) => errors.push(e.message));
        page.on('console', (m) => { if (m.type() === 'error') errors.push(`console: ${m.text()}`); });
        const started = Date.now();
        try {
          await page.goto(url);
          await page.locator('[data-agent="alice/lead"]').first().click();
          await page.locator('[data-info-entry="topics"]').waitFor();
          await run(page, name);
        } catch (err) {
          if (shots) await page.screenshot({ path: join(shots, `${name}-${part}-FAIL.png`) }).catch(() => {});
          console.error('page errors:', JSON.stringify(errors), 'unmocked:', JSON.stringify(await page.evaluate(() => window.unmocked).catch(() => null)));
          throw err;
        }
        assert.deepEqual(await page.evaluate(() => unmocked), [], 'no unmocked call');
        assert.deepEqual(errors, [], 'no page error');
        report.push({ engine: name, part, pass: true, ms: Date.now() - started });
        console.log(JSON.stringify(report.at(-1)));
        await page.close();
      }
    } finally { await browser.close(); }
  }
} finally { server.close(); }
