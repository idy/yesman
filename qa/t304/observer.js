(() => {
  'use strict';
  const config = __T304_CONFIG__;
  const {scenario, labels, limits} = config;
  const pause = (ms = 25) => new Promise(resolve => setTimeout(resolve, ms));
  const assert = (condition, detail) => { if (!condition) throw new Error(detail); };
  const selector = name => document.querySelector(name);
  const dialogSelector = scenario === 'cleanup' ? 'dialog.worktree-cleanup[open]' : '#delete-workspace-dialog[open]';
  const dialog = () => selector(dialogSelector);
  const button = label => [...(dialog()?.querySelectorAll('button') ?? [])].find(el => !el.hidden && el.textContent.trim() === label);
  const visible = el => el && !el.hidden && el.getBoundingClientRect().width > 0;
  const warnings = {
    'ordinary-before-commit': ['#deletion-body',
      'Its agents stop. Its conversations, tasks, Knowledge, Working Logs and workspace files are permanently deleted.'],
    'seven-before-commit': ['#force-deletion-risk',
      'Force Delete permanently removes these local checkouts and the rest of this workspace, including uncommitted changes and untracked files. Git data stored outside the workspace is kept.'],
    'bulk-before-commit': ['#worktree-cleanup-hint',
      'This permanently deletes the listed directories, including uncommitted edits, untracked and ignored files, and build output. No recovery reference is created.'],
  };
  const errorText = error => String(error?.message || error);
  const events = [];
  const rpcRows = [];
  let pendingMenu = null;
  let permit = null;
  let loaded = false;
  let serial = 0;

  async function until(pred, detail, ms = 10000) {
    const end = performance.now() + ms;
    do {
      const value = pred();
      if (value) return value;
      await pause();
    } while (performance.now() < end);
    throw new Error(`Timed out: ${detail}`);
  }
  function view() {
    return {
      open: !!dialog(), title: dialog()?.querySelector('h2')?.textContent,
      focus: document.activeElement?.textContent?.trim().slice(0, 100),
      buttons: [...(dialog()?.querySelectorAll('button') ?? [])].map(el => ({text: el.textContent.trim(), hidden: el.hidden, disabled: el.disabled})),
      text: dialog()?.textContent?.slice(0, 12000),
    };
  }
  function lossWarning(step) {
    if (!(step in warnings)) return undefined;
    const [selector] = warnings[step];
    const nodes = dialog()?.querySelectorAll(selector);
    const element = nodes?.length === 1 ? nodes[0] : null;
    let rendered = !!visible(element) && element.getClientRects().length > 0;
    for (let ancestor = element; rendered && ancestor; ancestor = ancestor.parentElement) {
      const css = getComputedStyle(ancestor);
      rendered = !ancestor.hidden && css.display !== 'none' && css.visibility === 'visible' && Number(css.opacity) > 0;
    }
    return {selector, visible: rendered, text: element?.innerText?.trim() ?? ''};
  }
  async function post(step, data = {}) {
    const occurrence = ++serial;
    const response = await fetch(config.endpoint, {
      method: 'POST', headers: {'Content-Type': 'text/plain'},
      body: JSON.stringify({scenario, occurrence, step, ms: performance.now(), view: view(), lossWarning: lossWarning(step), data}),
    });
    assert(response.ok, `Collector rejected ${step}: ${response.status}`);
    return await response.json();
  }
  function mark(step, data = {}) {
    events.push({step, ms: performance.now(), view: view(), data});
  }
  async function rpc(mygo, method, ...args) {
    if (['AgentMenu', 'WorktreeMenu'].includes(method)) {
      const menu = pendingMenu;
      assert(menu?.method === method && (method === 'AgentMenu' ? menu.agent === args[0] : menu.row === args[0]), `Unexpected menu request ${method}`);
      pendingMenu = null;
      mark('menu-action', {method, selected: menu.action, target: menu.target, source: 'observer-supplied result'});
      return menu.action;
    }
    const isCommit = ['DeleteWorkspace', 'ForceDeleteWorkspace', 'ConfirmWorktreeCleanup'].includes(method);
    if (isCommit) {
      const actualTarget = method === 'ConfirmWorktreeCleanup' ? args[0] : args[0]?.entry;
      assert(permit?.method === method && permit.target === actualTarget, `Commit outside approved fixture step: ${method} ${actualTarget}`);
      permit = null;
    }
    const observed = isCommit || /Workspace.*(?:Delet|Review)|(?:Prepare|Preview|Check|Cancel|Confirm)WorktreeCleanup/.test(method);
    const row = {method, started: performance.now()};
    if (observed) rpcRows.push(row);
    if (['PrepareWorktreeCleanup', 'PreviewWorktreeCleanup'].includes(method)) mark('checking-dispatch');
    try {
      const result = await mygo.call(`Yesman.${method}`, ...args);
      if (observed) {
        row.finished = performance.now();
        row.ok = true;
        if (method === 'ReviewWorkspaceForceDeletion') row.review = {
          hasToken: !!result.token, problem: result.problem, markers: result.linkMarkers ?? [],
        };
        if (method === 'ConfirmWorktreeCleanup') row.rows = result.map(({dir, reason, removed}) => ({dir, reason, removed}));
      }
      return result;
    } catch (error) {
      if (observed) Object.assign(row, {finished: performance.now(), ok: false, error: errorText(error)});
      throw error;
    }
  }
  window.__t304Call = rpc;

  function pressControl(label) {
    const control = button(label);
    assert(visible(control) && !control.disabled, `No enabled ${label}`);
    mark('button-click', {label, source: 'DOM click()'});
    control.click();
  }
  async function checkpoint(name, data = {}) {
    return post(name, {...data, events: events.splice(0), rpc: rpcRows.splice(0)});
  }
  function sidebar(name) {
    return document.querySelector(`#yesmen [data-sidebar-entry="yesman"][data-agent^="${name}/"]`);
  }
  async function deleteMenu(name) {
    const row = await until(() => sidebar(name), `sidebar ${name}`);
    pendingMenu = {method: 'AgentMenu', agent: row.dataset.agent, target: name, action: 'delete-workspace'};
    row.dispatchEvent(new KeyboardEvent('keydown', {key:'ContextMenu', bubbles:true, cancelable:true}));
    await until(() => !pendingMenu && dialog(), `menu handler for ${name}`);
    assert(selector('#deletion-location')?.textContent === `yesmen/${name}`, 'Workspace confirmation path differs');
    assert(document.activeElement === button('Cancel'), 'Cancel is not focused when the dialog opens');
  }
  function lastCall(method) { return rpcRows.filter(row => row.method === method).at(-1); }
  function commitCount() { return rpcRows.filter(row => /^(DeleteWorkspace|ForceDeleteWorkspace|ConfirmWorktreeCleanup)$/.test(row.method)).length; }
  async function cancel(step) {
    pressControl('Cancel');
    await until(() => !dialog(), 'dialog closed by Cancel');
    assert(commitCount() === 0, 'Cancellation made a commit call');
    await checkpoint(step);
  }
  function authorize(result, method, target) {
    assert(result?.method === method && result?.target === target && permit === null, 'Host approval receipt differs');
    permit = {method, target};
  }
  async function confirmWithWarning(step, method, target, label) {
    const current = dialog();
    function checkWarning() {
      assert(current && dialog() === current, `Confirmation dialog changed for ${step}`);
      const observed = lossWarning(step);
      assert(observed?.visible && observed.text === warnings[step][1], `Missing exact visible loss warning for ${step}`);
    }
    checkWarning();
    const approval = await checkpoint(step);
    checkWarning();
    authorize(approval, method, target);
    pressControl(label);
  }
  async function ordinary() {
    await deleteMenu('plain-delete');
    await until(() => visible(button('Delete permanently')) && !button('Delete permanently').disabled, 'ordinary ready');
    await checkpoint('ordinary-preview');
    await cancel('ordinary-cancel');
    await deleteMenu('plain-delete');
    await until(() => visible(button('Delete permanently')) && !button('Delete permanently').disabled, 'reopened ordinary ready');
    await confirmWithWarning('ordinary-before-commit', 'DeleteWorkspace', 'plain-delete', 'Delete permanently');
    await until(() => lastCall('DeleteWorkspace')?.ok && !dialog() && !sidebar('plain-delete'), 'ordinary commit', 30000);
    assert(permit === null && commitCount() === 1, 'Ordinary action was not attributed');
    await checkpoint('ordinary-after-commit');
  }
  async function seven() {
    await deleteMenu('yesman-5');
    await until(() => lastCall('ReviewWorkspaceForceDeletion')?.review?.problem &&
      visible(button('Check again')) && !button('Check again').disabled, 'Git hold and Check again', limits.holdMs);
    const hold = lastCall('ReviewWorkspaceForceDeletion');
    assert(hold.finished - hold.started <= limits.holdMs && hold.review.hasToken === false, 'Git hold was late or granted a token');
    assert(button('Delete permanently')?.disabled && !button('Force Delete'), 'Git hold exposed a destructive button');
    assert(hold.review.problem.includes('host-jieli-sdk/.git') && dialog().textContent.includes('host-jieli-sdk/.git'), 'Hold did not identify the protected Git directory');
    await checkpoint('seven-hold-cancel-ready');
    await cancel('seven-cancel');
    await deleteMenu('yesman-5');
    await until(() => lastCall('ReviewWorkspaceForceDeletion')?.review?.problem && visible(button('Check again')), 'fresh hold', limits.holdMs);
    await checkpoint('seven-hold-preserve');
    pressControl('Check again');
    await until(() => lastCall('ReviewWorkspaceForceDeletion')?.review?.hasToken && visible(button('Force Delete…')), 'force offer after retry', limits.forceMs);
    const review = lastCall('ReviewWorkspaceForceDeletion').review;
    assert(JSON.stringify(review.markers.slice().sort()) === JSON.stringify(config.markers), 'Force preview link inventory differs');
    assert(review.markers.every(marker => dialog().textContent.includes(`yesmen/yesman-5/${marker}`)), 'A symbolic link is missing from the DOM listing');
    assert(!button('Force Delete') && button('Delete permanently')?.disabled, 'Force commit exposed before review');
    await checkpoint('seven-force-offer');
    pressControl('Force Delete…');
    assert(visible(button('Force Delete')) && document.activeElement === button('Cancel') && commitCount() === 0, 'First force click committed or focused delete');
    assert(visible(selector('#force-deletion-risk')) && selector('#force-deletion-risk').textContent.includes('uncommitted changes'), 'Armed dialog lost the deletion warning');
    await confirmWithWarning('seven-before-commit', 'ForceDeleteWorkspace', 'yesman-5', 'Force Delete');
    await until(() => lastCall('ForceDeleteWorkspace')?.ok && !dialog() && !sidebar('yesman-5'), 'forced removal', limits.forceCommitMs);
    assert(permit === null && commitCount() === 1, 'Force commit was not attributed');
    await checkpoint('seven-after-commit');
  }

  async function worktreeMenu(target) {
    const source = target === 'heading' ? '#yesman-info [data-info-entry="worktrees"]' : `#yesman-info [data-info-entry^="worktree:${target}:"]`;
    const row = await until(() => visible(selector(source)) && selector(source), `worktree entry ${target}`);
    pendingMenu = {method:'WorktreeMenu', row: target !== 'heading', target, action: target === 'heading' ? 'cleanup' : 'remove'};
    const started = performance.now();
    row.dispatchEvent(new MouseEvent('contextmenu', {bubbles:true,cancelable:true,button:2}));
    await until(() => !pendingMenu && dialog(), `worktree dialog ${target}`);
    const checking = events.filter(event => event.step === 'checking-dispatch').at(-1)?.view;
    assert(checking?.text?.includes('Checking worktrees…'), 'Missing checking status at preview dispatch');
    assert(checking.buttons.filter(row => !row.hidden && !row.disabled).map(row => row.text).join(',') === 'Cancel', 'Checking showed extra actions');
    mark('checking', {target, latencyMs: performance.now() - started});
    return started;
  }
  async function waitTree(started, ready, max, target) {
    await until(ready, `actionable preview for ${target}`, max);
    const elapsed = performance.now() - started;
    assert(elapsed <= max, 'Preview boundary exceeded');
    mark('terminal-preview', {target, latencyMs: elapsed, budgetMs: max});
  }
  async function treeClose(step) {
    pressControl('Close');
    await until(() => !dialog(), 'diagnostic closed');
    assert(commitCount() === 0, 'Diagnostic made a commit call');
    await checkpoint(step);
  }
  async function cleanup() {
    const workspace = await until(() => sidebar('cleanup-latency'), 'cleanup workspace');
    workspace.click();
    if (selector('#yesman-info')?.hidden) {
      const showInfo = [...document.querySelectorAll('button')].find(el => el.getAttribute('aria-label')?.startsWith('Show the info'));
      if (showInfo) showInfo.click();
    }
    await worktreeMenu('heading');
    await cancel('bulk-checking-cancel');
    const slowStart = await worktreeMenu('latency-29');
    await waitTree(slowStart, () => visible(button('Close')) && visible(button('Try again')), limits.slowMs, 'latency-29');
    assert(view().title === 'Worktree kept' && labels.timeout.test(dialog().textContent), 'Missing timeout explanation');
    await checkpoint('slow-diagnostic');
    pressControl('Try again');
    await until(() => visible(button('Cancel')) && selector('#worktree-cleanup-hint')?.textContent === 'Checking worktrees…', 'retry returns to Checking');
    await cancel('slow-retry-cancel');
    const openStart = await worktreeMenu('latency-01');
    await waitTree(openStart, () => visible(button('Close')), limits.openMs, 'latency-01');
    assert(labels.openPR.test(dialog().textContent), 'Missing open PR explanation');
    await treeClose('open-pr-kept');
    const startReview = await worktreeMenu('safe-review');
    await waitTree(startReview, () => visible(button('Remove worktree')), limits.singleMs, 'safe-review');
    await checkpoint('review-preview');
    await cancel('review-cancel');
    const reopen = await worktreeMenu('safe-review');
    await waitTree(reopen, () => visible(button('Remove worktree')), limits.singleMs, 'safe-review');
    authorize(await checkpoint('review-before-commit'), 'ConfirmWorktreeCleanup', 'cleanup-latency');
    pressControl('Remove worktree');
    await until(() => lastCall('ConfirmWorktreeCleanup')?.rows && !dialog(), 'single cleanup commit', limits.cleanupCommitMs);
    let removed = lastCall('ConfirmWorktreeCleanup').rows.filter(row => row.removed).map(row => row.dir);
    assert(JSON.stringify(removed) === '["safe-review"]' && permit === null && commitCount() === 1, 'Single cleanup differed');
    await checkpoint('review-after-commit', {removed});
    const bulk = await worktreeMenu('heading');
    await waitTree(bulk, () => visible(button('Permanently remove 28 worktrees')), limits.bulkMs, 'heading');
    assert(visible(button('Try again')) && visible(button('Cancel')), 'Partial preview lacks retry or cancel');
    const kept = [...(dialog().querySelector('details.cleanup-kept')?.querySelectorAll('li') ?? [])];
    for (const [name, reason] of [['latency-01', labels.openPR], ['latency-29', labels.timeout], ['keep-primary', labels.primary]]) {
      assert(kept.some(row => row.textContent.includes(name) && reason.test(row.textContent)), `Missing keep reason ${name}`);
    }
    assert([...dialog().querySelectorAll('li')].some(row => !row.closest('details') && row.textContent.includes('latency-02')), 'Late-use control was not eligible');
    await confirmWithWarning('bulk-before-commit', 'ConfirmWorktreeCleanup', 'cleanup-latency', 'Permanently remove 28 worktrees');
    await until(() => lastCall('ConfirmWorktreeCleanup')?.rows && view().title === 'Cleanup results', 'partial cleanup receipt', limits.cleanupCommitMs);
    const rows = lastCall('ConfirmWorktreeCleanup').rows;
    const late = rows.find(row => row.dir === 'latency-02');
    assert(late && !late.removed && labels.inUse.test(late.reason), 'A late process did not protect its worktree');
    assert(dialog().textContent.includes('latency-02') && labels.inUse.test(dialog().textContent), 'Late-use reason missing from visible receipt');
    removed = rows.filter(row => row.removed).map(row => row.dir).sort();
    assert(removed.length === 27 && permit === null && commitCount() === 1, 'Bulk cleanup did not remove exactly 27');
    await checkpoint('bulk-after-commit', {removed, late});
  }

  async function run() {
    for (const key of Object.keys(labels)) labels[key] = new RegExp(labels[key], 'i');
    assert(config.source && window.mygo?.platform === 'darwin' && window.webkit?.messageHandlers?.mygo, 'Expected actual macOS MyGo bridge');
    assert(navigator.userAgent.includes('AppleWebKit/') && document.visibilityState === 'visible', 'Expected visible WebKit page');
    const state = await window.mygo.call('Yesman.State');
    assert(state.home === config.profile && state.yesmen.map(row => row.name).sort().join(',') === config.hires.join(','), 'Wrong profile or hire population');
    await checkpoint('bound', {windowID: window.mygo.windowId, userAgent:navigator.userAgent, url:location.href, profile:state.home});
    loaded = true;
    await ({ordinary, seven, cleanup})[scenario]();
    await checkpoint('complete');
  }
  window.addEventListener('error', event => mark('error', {message:event.message}));
  window.addEventListener('unhandledrejection', event => mark('unhandled', {message:errorText(event.reason)}));
  document.addEventListener('DOMContentLoaded', () => void run().catch(async error => {
    try { await checkpoint('failed', {error:errorText(error), loaded}); } catch (_) { /* Host keeps the last valid checkpoint. */ }
  }), {once:true});
})();
