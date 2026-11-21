// Runs inside the real score-service bridge in score-patch-browser.mjs.
import assert from 'node:assert/strict';

export async function checkWorkerRouting({ page, open, name, output, errors }) {
  const pools = page.locator('.worker-pools');
  const button = (id) => pools.locator(`[data-pool="${id}"]`);
  const dialog = page.locator('#crew-dialog');
  const instructions = dialog.locator('textarea');
  const max = 'magpie/auto/claude-opus-5-5:max';
  const low = 'magpie/auto/claude-opus-5-5:low';
  const active = (loc) => loc.evaluate((el) => document.activeElement === el);
  const go = (method, ...args) => page.evaluate(([m, a]) => fixtureCall(m, ...a), [method, args]);
  const openPool = async (id) => {
    await button(id).focus(); await button(id).press('Enter');
    await instructions.waitFor(); assert.equal(await active(instructions), true);
  };
  assert.equal(await pools.getByRole('button').count(), 4);
  assert.equal(await pools.getByText('Uses the Default pool', { exact: true }).count(), 3);
  await go('AddCrewOption', 'magpie', 'auto/claude-opus-5-5', 'low', null);
  await open();
  await openPool('backend');
  const saved = 'APIs and database migrations, including API design.\n交付数据库。';
  await instructions.fill(saved);
  await page.keyboard.press('Tab');
  const first = dialog.getByRole('checkbox').first();
  // WebKit follows macOS keyboard preferences. Explicitly focus the checkbox
  // where Tab skips non-text controls, then verify the native Space action.
  if (!(await active(first))) await first.focus();
  await first.press('Space');
  const second = dialog.getByRole('checkbox').nth(1); await second.focus(); await second.press('Space');
  await dialog.getByRole('button', { name: 'Save', exact: true }).press('Enter');
  await dialog.waitFor({ state: 'hidden' });
  await page.waitForFunction(() => document.activeElement?.dataset.pool === 'backend', null, { timeout: 2500 }).catch(async (err) => {
    throw Error(`Pool focus after Save: ${await page.evaluate(() => document.activeElement?.outerHTML.slice(0, 250))}`, { cause: err });
  });
  const cfg = () => go('Crew');
  const pool = (await cfg()).pools.find((p) => p.id === 'backend');
  assert.deepEqual(pool.members, [max, low]); assert.equal(pool.instructions, saved);
  assert.equal((await cfg()).yesman, max);
  const waitForPoolFocus = (id) => page.waitForFunction((id) => document.activeElement?.dataset.pool === id, id);
  const afterPaint = () => page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  async function reopenDuringConflict(winner, members) {
    await openPool('backend'); await instructions.fill('local draft');
    const { revision } = (await cfg()).pools.find((p) => p.id === 'backend');
    await go('SetWorkerPool', 'backend', revision, { instructions: winner, members });
    await page.evaluate(() => { scoreControls.Crew = {}; });
    await dialog.getByRole('button', { name: 'Save', exact: true }).press('Enter');
    await dialog.getByRole('alert').filter({ hasText: 'pool changed' }).waitFor();
    assert.equal(await instructions.inputValue(), 'local draft');
    await page.waitForFunction(() => !!scorePending.Crew);
    await page.keyboard.press('Escape');
    await waitForPoolFocus('backend');
    await page.keyboard.press('Enter');
    await dialog.getByRole('status').filter({ hasText: 'Reading pool settings' }).waitFor();
    assert.equal(await instructions.count(), 0);
  }

  // Cancel the waiting editor and let its close handler finish before the read.
  // The page must preserve whichever Edit is focused when the new summary arrives.
  for (const [target, effort, member] of [['backend', 'Low', low], ['frontend', 'Max', max]]) {
    const winner = `Use the ${effort} effort for API work.`;
    await reopenDuringConflict(winner, [member]);
    await page.keyboard.press('Escape');
    await waitForPoolFocus('backend');
    await afterPaint();
    if (target === 'frontend') await button(target).focus();
    const summary = pools.getByText(`Claude Opus 5.5 · ${effort}`, { exact: true });
    assert.equal(await summary.count(), 0);
    await page.evaluate(() => scorePending.Crew());
    await summary.waitFor();
    await afterPaint();
    assert.equal(await active(button(target)), true, `${target} Edit lost focus when the recovery read arrived`);
    await page.keyboard.press('Enter');
    await instructions.waitFor();
    assert.equal(await active(instructions), true);
    if (target === 'backend') assert.equal(await instructions.inputValue(), winner);
    else await dialog.getByRole('heading', { name: 'Frontend & Design', exact: true }).waitFor();
    await page.keyboard.press('Escape');
    await waitForPoolFocus(target);
  }

  // Hold the older catalog response and the post-conflict settings read.
  // A new opening can wait for the winner without adopting either old draft.
  await page.evaluate(() => { scoreControls.RefreshCrew = {}; });
  await page.getByRole('button', { name: 'Read pi’s models again' }).click();
  await page.waitForFunction(() => !!scorePending.RefreshCrew);
  await reopenDuringConflict('Winner', [low]);
  await page.screenshot({ path: `${output}/${name}-pool-recovery.png`, animations: 'disabled' });
  await page.evaluate(() => scorePending.Crew());
  await instructions.waitFor();
  assert.equal(await instructions.inputValue(), 'Winner');
  assert.deepEqual(await dialog.locator('input:checked').evaluateAll((boxes) => boxes.map((b) => b.value)), [low]);
  assert.equal(await active(instructions), true);
  await dialog.getByRole('checkbox').first().check(); await instructions.fill(saved);
  await dialog.getByRole('button', { name: 'Save', exact: true }).click(); await dialog.waitFor({ state: 'hidden' });
  await page.waitForFunction(() => document.activeElement?.dataset.pool === 'backend');
  await page.evaluate(() => scorePending.RefreshCrew());
  await page.waitForFunction(() => !!scoreFinished.RefreshCrew);
  assert.equal(await active(button('backend')), true);
  await openPool('backend'); assert.equal(await instructions.inputValue(), saved);
  assert.equal((await cfg()).pools[1].instructions, saved);
  await page.keyboard.press('Escape');
  // A delayed refresh started before a pool save cannot restore the old pool.
  await page.evaluate(() => { window.holdRefresh = true; });
  await page.getByRole('button', { name: 'Read pi’s models again' }).click();
  await page.waitForFunction(() => window.releaseRefresh);
  await openPool('default'); await dialog.getByRole('checkbox').first().check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click(); await dialog.waitFor({ state: 'hidden' });
  await page.evaluate(() => { releaseRefresh(); window.holdRefresh = false; });
  await page.getByRole('button', { name: 'Read pi’s models again' }).waitFor();
  await openPool('default'); assert.equal(await dialog.getByRole('checkbox').first().isChecked(), true);
  await page.keyboard.press('Escape');
  // Actual settings storage + fresh app object and browser window.
  await go('FixtureRestart'); await open(); await openPool('backend');
  assert.equal(await instructions.inputValue(), saved);
  assert.equal(await dialog.locator('input:checked').count(), 2); await page.keyboard.press('Escape');

  // Exercise the independent per-agent calls in the production Settings form.
  // The real Pi test checks their actual model + effort across a Manager reopen.
  await page.evaluate(() => {
    const since = '2026-09-01T00:00:00Z';
    let agent = { id: 'lab/lead', name: 'lead', yesman: 'lab', role: 'lead', since, status: 'idle', provider: 'fixture', model: 'lead', effort: 'medium', effortLevels: ['low', 'medium', 'high'] };
    const models = ['lead', 'another'].map((id) => ({ id, provider: 'fixture', name: id, levels: ['low','medium','high'] }));
    const original = window.mygo.call; window.leadCalls = [];
    window.mygo.call = async (method, ...args) => {
      const m = method.replace('Yesman.', '');
      if (m === 'LeadModels') {
        const [, , expected, change] = args;
        window.leadCalls.push(args);
        if (!Object.keys(change).length) return { models, agent };
        if (expected.model !== agent.model || expected.effort !== agent.effort) throw Error('Stale test selection');
        agent = { ...agent, ...change };
        return { agent };
      }
      return original(method, ...args);
    };
    window.fixtureHandlers.agents([agent]);
  });
  await page.locator('#settings-nav [data-page="yesman"]').click();
  const leadButton = page.locator('[data-edit-lead]'); await leadButton.press('Enter');
  const model = dialog.getByRole('combobox', { name: 'Model', exact: true });
  await model.selectOption('1'); await dialog.getByRole('combobox', { name: 'Effort', exact: true }).selectOption('high');
  await dialog.getByRole('button', { name: 'Done', exact: true }).press('Enter');
  const calls = await page.evaluate(() => window.leadCalls);
  assert.deepEqual(calls.map((c) => c.slice(0, 2)), Array(3).fill(['lab', '2026-09-01T00:00:00Z']));
  assert.deepEqual(calls.map((c) => c[3]), [{}, { provider: 'fixture', model: 'another' }, { effort: 'high' }]);
  assert.equal((await cfg()).yesman, max);
  await page.screenshot({ path: `${output}/${name}-routing-en.png` });

  await page.evaluate(() => localStorage.setItem('fixture.locale', 'zh-Hans'));
  await page.setViewportSize({ width: 660, height: 860 }); await open();
  await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
  await pools.getByText('运维与调研', { exact: true }).waitFor();
  await openPool('frontend'); assert.match(await instructions.getAttribute('placeholder'), /图稿/);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.screenshot({ path: `${output}/${name}-routing-zh-editor.png` });
  await page.keyboard.press('Escape'); await button('backend').focus();
  await page.screenshot({ path: `${output}/${name}-routing-zh.png` });
  assert.equal(await active(button('backend')), true);
  await go('FixtureCatalogOffline');
  await page.locator('#settings-nav [data-page="yesman"]').click();
  await pools.getByText(/可用状态未知/).first().waitFor();
  await openPool('operations');
  assert.equal(await dialog.getByRole('checkbox').count(), 2);
  assert.equal(await dialog.locator('input:disabled').count(), 2);
  await page.keyboard.press('Escape');
  await openPool('backend');
  assert.equal(await dialog.locator('input:checked:enabled').count(), 2);
  assert.equal(await dialog.getByText(/可用状态未知/).count(), 2);
  await page.screenshot({ path: `${output}/${name}-pool-offline.png`, animations: 'disabled' });
  assert.deepEqual(errors, []);
  console.log(`${name}: production Settings, conflict recovery and cancellation, focus on the original or newly chosen Edit, last saved revision, late refresh and offline membership/score display PASS`);
}
