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
    'recovery-before-commit': ['#force-deletion-risk',
      'Force Delete permanently removes these local checkouts and the rest of this workspace, including uncommitted changes and untracked files. Git data stored outside the workspace is kept.'],
    'cold-before-commit': ['#force-deletion-risk',
      'This permanently removes the remaining files and local changes at the listed paths.'],
  };
  const gitLoss = 'This deletes local Git history, branches, tags and objects at these paths. Other checkouts that point here may stop working. Outside repositories, including bare repositories, may also stop working if they borrow Git objects from these stores.';
  const errorText = error => String(error?.message || error);
  const events = [];
  const rpcRows = [];
  // A checkpoint consumes its ordinary RPC batch. The bounded cleanup ledger
  // keeps the same call objects so replies arriving later remain observable.
  const cleanupCalls = [];
  const cleanupKeys = new Map();
  let failureFrozen = false;
  const bridgeFallbackMs = 15000;
  const failureWireLimit = 131072;
  // Capture the encoder before page code can wrap it. The retry uses the same
  // frozen projection and timestamps if the ordinary encoder throws.
  const encodeFailure = JSON.stringify.bind(JSON);
  const cleanupMethods = /^(Prepare|Check|Cancel|Confirm)WorktreeCleanup$/;
  function cleanupRPCs(failedCall, full = true) {
    return cleanupCalls.map(({id, method, target, started, finished, ok, related, cancelID, error, preview}) => ({
      id, method, target, started, finished, ok, related, cancelID, error,
      result: ok && preview ? {tokenPresent: preview.tokenPresent, partial: preview.partial,
        retryable: preview.retryable, invalid: preview.invalid, offers: preview.rows.filter(row => !row.reason).length,
        held: full || id === failedCall ? preview.rows.filter(row => row.reason).map(({dir, reason}) => ({dir, reason})) : undefined} : undefined,
    }));
  }
  function diagnosticView() {
    const open = dialog();
    const title = open?.querySelector('h2')?.textContent;
    const buttons = [...(open?.querySelectorAll('button') ?? [])];
    const known = (text, pattern) => typeof text === 'string' && pattern.test(text) ? text : 'other';
    const hint = selector('#worktree-cleanup-hint')?.textContent;
    return {open: !!open, title: known(title, /^(Remove worktree\?|Clean up worktrees|Worktree kept|No worktrees to remove|Could not check worktrees|Could not confirm the result|Cleanup results)$/),
      hint: known(hint, /^(Checking worktrees…|The worktree check did not finish within the time limit\.|These directories will stay in the workspace\.|This workspace has no recorded worktrees to clean up\.|This preview expired or was already used\. Open a new preview\.)$/),
      buttons: buttons.slice(0, 8).map(el => ({
        text: known(el.textContent.trim(), /^(Cancel|Close|Try again|Remove worktree|Permanently remove (?:1 worktree|[0-9]{1,2} worktrees))$/),
        hidden: el.hidden, disabled: el.disabled,
      })),
    };
  }
  function cleanupError(error) {
    const message = errorText(error);
    return ['context canceled', 'context deadline exceeded', 'This preview expired or was already used. Open a new preview.',
      'The workspace is busy. Try again.'].includes(message) ? message : 'other rejection';
  }
  let pendingMenu = null;
  let permit = null;
  let bulkProbe = null;
  let reviewToken = null;
  let forceReviewToken = null;
  let loaded = false;
  let serial = 0;
  const fixtureDir = /^(?:latency-[0-2][0-9]|keep-primary|safe-review)$/;
  const heldReasons = new Set([
    'This branch has an open pull request.',
    'This checkout is primary or has no recorded temporary purpose.',
    'A process is using this worktree.',
    'This worktree could not be checked within the time limit.',
  ]);
  const bulkNames = ['latency-00', ...Array.from({length: 27}, (_, i) => `latency-${String(i+2).padStart(2, '0')}`)];

  function capturePreview(result, target) {
    const workspace = `${config.profile}/yesmen/cleanup-latency/`;
    let invalid = !Array.isArray(result?.rows) || result.rows.length > 32;
    const rows = (Array.isArray(result?.rows) ? result.rows.slice(0, 32) : []).map(row => {
      const dir = typeof row?.dir === 'string' && fixtureDir.test(row.dir) ? row.dir : '';
      const instance = typeof row?.cleanupId === 'string' && /^[0-9a-f]{32}$/.test(row.cleanupId) ? row.cleanupId : '';
      const head = typeof row?.head === 'string' && /^[0-9a-f]{40}$/.test(row.head) ? row.head : '';
      const reason = row?.reason === undefined || row.reason === '' ? '' : heldReasons.has(row.reason) ? row.reason : '[unrecorded reason]';
      const rawDetail = row?.detail;
      const detail = rawDetail === undefined || rawDetail === '' ? '' :
        rawDetail === workspace+dir && dir ? dir :
        rawDetail === 'primary' || rawDetail === `fixture/cleanup-latency · ${dir}` ? rawDetail : '[unrecorded detail]';
      invalid ||= !dir || reason === '[unrecorded reason]' || (!reason && (!instance || !head || row.path !== workspace+dir));
      return {dir, instance, head, reason, detail};
    });
    return {target, rows, invalid, tokenPresent: typeof result?.token === 'string' && result.token.length > 0,
      partial: result?.partial === true, retryable: result?.retryable === true};
  }

  async function tokenHash(token) {
    try {
      if (typeof token !== 'string' || token.length > 128 || !globalThis.crypto?.subtle) return null;
      const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(token));
      return Array.from(new Uint8Array(bytes), b => b.toString(16).padStart(2, '0')).join('');
    } catch (_) {
      return null;
    }
  }

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
  async function post(step, data = {}, observedView = view()) {
    const occurrence = ++serial;
    const response = await fetch(config.endpoint, {
      method: 'POST', headers: {'Content-Type': 'text/plain'},
      body: JSON.stringify({scenario, occurrence, step, ms: performance.now(), view: observedView, lossWarning: lossWarning(step), data}),
    });
    assert(response.ok, `Collector rejected ${step}: ${response.status}`);
    return await response.json();
  }
  function mark(step, data = {}) {
    events.push({step, ms: performance.now(), view: view(), data});
  }
  const refreshPending = new Set();
  let refreshAfterSingle = null;
  const refreshMethods = new Set(['Info', 'Pulls']);

  async function rpc(mygo, method, ...args) {
    assert(!failureFrozen || method === 'CancelWorktreeCleanup' || !cleanupMethods.test(method), 'Worktree action after acceptance failure');
    if (['AgentMenu', 'WorktreeMenu'].includes(method)) {
      const menu = pendingMenu;
      assert(menu?.method === method && (method === 'AgentMenu' ? menu.agent === args[0] : menu.row === args[0]), `Unexpected menu request ${method}`);
      pendingMenu = null;
      mark('menu-action', {method, selected: menu.action, target: menu.target, source: 'observer-supplied result'});
      return menu.action;
    }
    const isCommit = ['DeleteWorkspace', 'ForceDeleteWorkspace', 'ConfirmWorktreeCleanup'].includes(method);
    assert(!isCommit || !failureFrozen, 'Commit after acceptance failure');
    const recoveryTarget = scenario.startsWith('recovery-') && ['ForceDeleteWorkspace','ReviewWorkspaceForceDeletion'].includes(method)
      ? {entry: args[0]?.entry, instance: args[0]?.instance} : null;
    if (isCommit) {
      const actualTarget = method === 'ConfirmWorktreeCleanup' ? args[0] : args[0]?.entry;
      assert(permit?.method === method && permit.target === actualTarget, `Commit outside approved fixture step: ${method} ${actualTarget}`);
      if (method === 'ConfirmWorktreeCleanup' || method === 'ForceDeleteWorkspace') {
        const argument = method === 'ForceDeleteWorkspace' ? args[1] : args[2];
        assert(typeof permit.token === 'string' && permit.token && permit.token === argument, 'Confirmation token differs from the approved preview');
      }
      permit = null;
    }
    const selected = method === 'CheckWorktreeCleanup' ? args[2] : undefined;
    const requestProbe = method === 'CheckWorktreeCleanup' && selected == null ? bulkProbe : null;
    const observed = isCommit || /Workspace.*(?:Delet|Review)|(?:Prepare|Preview|Check|Cancel|Confirm)WorktreeCleanup/.test(method);
    const row = {method, started: performance.now()};
    const trackCleanup = scenario === 'cleanup' && cleanupMethods.test(method);
    if (trackCleanup) {
      assert(cleanupCalls.length < 64, 'Cleanup RPC ledger limit');
      row.id = cleanupCalls.length + 1;
      if (method === 'PrepareWorktreeCleanup' || method === 'CheckWorktreeCleanup') {
        const directory = args[2]?.dir;
        row.target = args[2] == null ? 'heading' : fixtureDir.test(directory) ? directory : 'unknown';
      }
      const keyArgument = {CheckWorktreeCleanup: 4, CancelWorktreeCleanup: 0, ConfirmWorktreeCleanup: 2}[method];
      if (keyArgument !== undefined) {
        const earlier = cleanupKeys.get(args[keyArgument]);
        if (earlier) {
          row.related = earlier.id;
          if (method === 'CancelWorktreeCleanup') {
            earlier.cancelID ??= row.id;
            for (const call of cleanupCalls) {
              if (call.related === earlier.id && call.method === 'CheckWorktreeCleanup') call.cancelID ??= row.id;
            }
          }
        }
      }
      cleanupCalls.push(row);
    }
    if (recoveryTarget) row.target = recoveryTarget;
    if (observed) rpcRows.push(row);
    if (['PrepareWorktreeCleanup', 'PreviewWorktreeCleanup'].includes(method)) mark('checking-dispatch');
    const trackRefresh = scenario === 'cleanup' && args[0] === 'cleanup-latency' && refreshMethods.has(method);
    if (trackRefresh) {
      row.refreshEpoch = refreshAfterSingle;
      refreshPending.add(row);
    }
    try {
      const result = await mygo.call(`Yesman.${method}`, ...args);
      if (scenario === 'cleanup' && method === 'ConfirmWorktreeCleanup' && result.length === 1 &&
          result[0].dir === 'safe-review' && result[0].removed) {
        refreshAfterSingle = new Map();
      }
      // Older reads can finish after the single removal. Require success
      // from the calls dispatched by the new Info panel state.
      if (trackRefresh && row.refreshEpoch && row.refreshEpoch === refreshAfterSingle) {
        const valid = method === 'Info' ? result !== null && typeof result === 'object' && !Array.isArray(result) : Array.isArray(result);
        if (valid) refreshAfterSingle.set(method, {method, workspace: args[0], started: row.started, finished: performance.now()});
      }
      if (trackCleanup) {
        const key = method === 'PrepareWorktreeCleanup' ? result : method === 'CheckWorktreeCleanup' ? result?.token : null;
        if (typeof key === 'string' && key.length > 0 && key.length <= 128) cleanupKeys.set(key, row);
      }
      if (observed) {
        row.finished = performance.now();
        row.ok = true;
        if (method === 'ReviewWorkspaceForceDeletion') {
          forceReviewToken = result.token;
          row.review = {hasToken: !!result.token, problem: result.problem, markers: result.linkMarkers ?? [],
            localStores: result.localGitStores ?? []};
        }
        if (method === 'CheckWorktreeCleanup') {
          row.preview = capturePreview(result, selected == null ? 'heading' : fixtureDir.test(selected.dir) ? selected.dir : 'unknown');
          // A promise never enters the checkpoint. The raw token stays in this closure.
          const digest = tokenHash(result?.token);
          previewDigests.set(row, digest);
          if (requestProbe) Object.assign(requestProbe, {row, token: result?.token});
          if (selected?.dir === 'safe-review') reviewToken = result?.token;
        }
        if (method === 'ConfirmWorktreeCleanup') row.rows = result.map(({dir, reason, removed}) => ({dir, reason, removed}));
      }
      return result;
    } catch (error) {
      if (observed) Object.assign(row, {finished: performance.now(), ok: false, error: trackCleanup ? cleanupError(error) : errorText(error)});
      if (requestProbe) requestProbe.row = row;
      throw error;
    } finally {
      if (trackRefresh) refreshPending.delete(row);
    }
  }
  window.__t304Call = rpc;

  function pressControl(label) {
    assert(!failureFrozen, 'Control after acceptance failure');
    const control = button(label);
    assert(visible(control) && !control.disabled, `No enabled ${label}`);
    mark('button-click', {label, source: 'DOM click()'});
    control.click();
  }
  const previewDigests = new WeakMap();
  async function checkpoint(name, data = {}) {
    const rpc = rpcRows.splice(0);
    const trace = events.splice(0);
    await Promise.all(rpc.filter(row => row.preview).map(async row => {
      row.preview.tokenSHA256 = await previewDigests.get(row);
    }));
    const ledger = scenario === 'cleanup' ? {cleanupRPCs: cleanupRPCs()} : {};
    return post(name, {...data, events: trace, rpc, ...ledger});
  }
  async function sendFailure(body) {
    if (new TextEncoder().encode(body).length > failureWireLimit) return false;
    const abort = new AbortController();
    let timer;
    try {
      const sent = fetch(`${config.endpoint}/failure`, {
        method: 'POST', headers: {'Content-Type': 'text/plain'}, body, signal: abort.signal,
      }).then(response => response.status === 204);
      return await Promise.race([sent, new Promise(resolve => {
        timer = setTimeout(() => { abort.abort(); resolve(false); }, 500);
      })]);
    } catch (_) {
      return false;
    } finally {
      clearTimeout(timer);
    }
  }
  async function reportFailure(error) {
    if (failureFrozen) return;
    failureFrozen = true;
    permit = null;
    pendingMenu = null;
    if (scenario !== 'cleanup') {
      await checkpoint('failed', {error: errorText(error), loaded});
      return;
    }
    const failedAt = performance.now();
    const prepare = cleanupCalls.findLast(row => row.method === 'PrepareWorktreeCleanup');
    const failedCall = prepare ? cleanupCalls.findLast(row => row.method === 'CheckWorktreeCleanup' && row.related === prepare.id)?.id ?? prepare.id : null;
    const deadline = Math.max(failedAt, prepare ? prepare.started + bridgeFallbackMs : failedAt);
    const pending = () => cleanupCalls.some(row => row.finished === undefined);
    const diagnosticPending = deadline > failedAt && pending();
    const errorMessage = errorText(error);
    const reason = /^(Timed out: actionable preview for (?:latency-29|latency-01|safe-review|heading)|Timed out: post-removal Info and Pulls refresh|Preview boundary exceeded|Expected exact 28 bulk offers\. Observed [0-9]{1,2}\.)$/.test(errorMessage)
      ? errorMessage : 'Cleanup observer failed';
    const first = {scenario, occurrence: serial+1, step: 'failed', ms: failedAt, view: diagnosticView(),
      data: {error: reason, loaded, failedAt, deadline, failedCall, diagnosticPending, cleanupRPCs: cleanupRPCs(failedCall, false)}};
    let wire;
    try { wire = JSON.stringify(first); }
    catch (_) { wire = encodeFailure(first); }
    // Reuse the serialized envelope for the bounded retry and final sample.
    const initial = (async () => {
      const body = encodeFailure({first: wire});
      return await sendFailure(body) || await sendFailure(body);
    })();
    if (!diagnosticPending) {
      await initial;
      return;
    }
    while ((pending() || selector('#worktree-cleanup-hint')?.textContent === 'Checking worktrees…') && performance.now() < deadline) {
      await pause(Math.min(25, Math.max(0, deadline - performance.now())));
    }
    const observedAt = performance.now();
    const sample = {scenario, occurrence: first.occurrence+1, step: 'failure-diagnostic', ms: observedAt, view: diagnosticView(),
      data: {failedAt, deadline, failedCall, stoppedBy: observedAt >= deadline ? 'bridge-horizon' : 'settled',
        cleanupRPCs: cleanupRPCs(failedCall, false)}};
    await sendFailure(encodeFailure({first: wire, terminal: sample}));
    await initial;
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
  function authorize(result, method, target, token) {
    assert(!failureFrozen, 'Approval after acceptance failure');
    assert(result?.method === method && result?.target === target && permit === null, 'Host approval receipt differs');
    permit = {method, target, token};
  }
  function checkBulkOffer(probe) {
    const preview = probe?.row?.preview;
    return preview?.target === 'heading' && !preview.invalid && preview.tokenPresent &&
      JSON.stringify(preview.rows.filter(row => !row.reason).map(row => row.dir).sort()) === JSON.stringify(bulkNames) &&
      visible(button('Permanently remove 28 worktrees'));
  }
  async function confirmWithWarning(step, method, target, label, probe) {
    const current = dialog();
    const recoveryApproval = ['recovery-before-commit','cold-before-commit'].includes(step);
    const recoveryReceipt = recoveryApproval ? lastCall('ReviewWorkspaceForceDeletion')?.review : null;
    const localWarning = () => [...(dialog()?.querySelectorAll('#deletion-checkouts .danger') ?? [])]
      .find(el => visible(el) && el.innerText?.trim() === gitLoss)?.innerText.trim();
    function checkWarning() {
      assert(current && dialog() === current, `Confirmation dialog changed for ${step}`);
      if (recoveryApproval) {
        assertRecoveryPaths(recoveryReceipt, target);
        assert(localWarning() === gitLoss, 'Missing exact local Git loss warning');
      }
      if (probe) assert(probe === bulkProbe && checkBulkOffer(probe), 'Exact bulk offer changed before confirmation');
      const observed = lossWarning(step);
      assert(observed?.visible && observed.text === warnings[step][1], `Missing exact visible loss warning for ${step}`);
    }
    checkWarning();
    const token = method === 'ForceDeleteWorkspace' ? forceReviewToken : probe?.token;
    const approval = await checkpoint(step, method === 'ForceDeleteWorkspace' ? {target, gitLossWarning: localWarning()} : {});
    checkWarning();
    if (method === 'ForceDeleteWorkspace') assert(forceReviewToken === token && !!token, 'Force token changed before confirmation');
    authorize(approval, method, target, token);
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
    if (target === 'heading') bulkProbe = {};
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
    const token = reviewToken;
    authorize(await checkpoint('review-before-commit'), 'ConfirmWorktreeCleanup', 'cleanup-latency', token);
    pressControl('Remove worktree');
    await until(() => lastCall('ConfirmWorktreeCleanup')?.rows && !dialog(), 'single cleanup commit', limits.cleanupCommitMs);
    let removed = lastCall('ConfirmWorktreeCleanup').rows.filter(row => row.removed).map(row => row.dir);
    assert(JSON.stringify(removed) === '["safe-review"]' && permit === null && commitCount() === 1, 'Single cleanup differed');
    const singleFinished = lastCall('ConfirmWorktreeCleanup').finished;
    await checkpoint('review-after-commit', {removed});
    // Applying the single removal reloads the panel. Its Git reads can hold
    // checkout cwd entries while the bulk process snapshot is collected.
    // Wait for those real RPCs before starting the separate bulk timer.
    const refreshStarted = performance.now();
    const refreshTimeout = 'post-removal Info and Pulls refresh';
    await until(() => refreshAfterSingle?.size === refreshMethods.size && !refreshPending.size, refreshTimeout);
    const refreshFinished = performance.now();
    const waitMs = refreshFinished - refreshStarted;
    assert(waitMs <= 10000, `Timed out: ${refreshTimeout}`);
    mark('post-removal-refresh-settled', {refreshStarted, refreshFinished, waitMs, calls: [...refreshAfterSingle.values()]});
    const bulk = await worktreeMenu('heading');
    const probe = bulkProbe;
    await waitTree(bulk, () => probe.row && visible(dialog()) && selector('#worktree-cleanup-hint')?.textContent !== 'Checking worktrees…', limits.bulkMs, 'heading');
    const offered = probe.row.preview?.rows.filter(row => !row.reason).length ?? 0;
    const previewFinished = performance.now();
    mark('bulk-terminal', {offered, exact: !!checkBulkOffer(probe), elapsedMs: previewFinished - bulk,
      bulkStarted: bulk, previewFinished, endToEndMs: previewFinished - singleFinished});
    assert(checkBulkOffer(probe), `Expected exact 28 bulk offers. Observed ${offered}.`);
    assert(visible(button('Try again')) && visible(button('Cancel')), 'Partial preview lacks retry or cancel');
    const kept = [...(dialog().querySelector('details.cleanup-kept')?.querySelectorAll('li') ?? [])];
    for (const [name, reason] of [['latency-01', labels.openPR], ['latency-29', labels.timeout], ['keep-primary', labels.primary]]) {
      assert(kept.some(row => row.textContent.includes(name) && reason.test(row.textContent)), `Missing keep reason ${name}`);
    }
    assert([...dialog().querySelectorAll('li')].some(row => !row.closest('details') && row.textContent.includes('latency-02')), 'Late-use control was not eligible');
    await confirmWithWarning('bulk-before-commit', 'ConfirmWorktreeCleanup', 'cleanup-latency', 'Permanently remove 28 worktrees', probe);
    await until(() => lastCall('ConfirmWorktreeCleanup')?.rows && view().title === 'Cleanup results', 'partial cleanup receipt', limits.cleanupCommitMs);
    const rows = lastCall('ConfirmWorktreeCleanup').rows;
    const late = rows.find(row => row.dir === 'latency-02');
    assert(late && !late.removed && labels.inUse.test(late.reason), 'A late process did not protect its worktree');
    assert(dialog().textContent.includes('latency-02') && labels.inUse.test(dialog().textContent), 'Late-use reason missing from visible receipt');
    removed = rows.filter(row => row.removed).map(row => row.dir).sort();
    assert(removed.length === 27 && permit === null && commitCount() === 1, 'Bulk cleanup did not remove exactly 27');
    await checkpoint('bulk-after-commit', {removed, late});
  }

  function assertRecoveryPaths(receipt, target) {
    const sameInventory = (actual, expected) => Array.isArray(actual) && Array.isArray(expected) &&
      JSON.stringify([...actual].sort()) === JSON.stringify(expected);
    assert(sameInventory(receipt?.localStores, config.recoveryPaths?.localStores) &&
      sameInventory(receipt?.markers, config.recoveryPaths?.markers), 'Unexpected recovery Git paths');
    assert(receipt.localStores.every(path => dialog().textContent.includes(`yesmen/${target}/${path}`)), 'Missing Git store path');
    assert(receipt.markers.every(path => dialog().textContent.includes(`yesmen/${target}/${path}`)), 'Missing Git link path');
  }

  async function recovery(cold) {
    const action = cold ? 'Review remaining files…' : 'Force Delete…';
    const commit = cold ? 'Delete remaining files' : 'Force Delete';
    const target = cold ? config.stage : 'yesman-5';
    assert(!cold || /^\.deleting-v1-[0-9a-f]{32}\.yesman-5$/.test(target), 'Wrong staged identity');
    async function open() {
      if (cold) {
        if (!selector('#stored-workspaces')) {
          selector('#settings-button').click();
          (await until(() => selector('#settings-nav button[data-page="storage"]'), 'Storage navigation')).click();
        }
        const row = () => [...document.querySelectorAll('#stored-workspaces button[data-storage-key]')]
          .find(el => el.dataset.storageKey.startsWith(`false/${target}/`) && !el.disabled);
        if (!row()) (await until(() => selector('#stored-workspaces button[data-storage-key="list"]'), 'Stored workspaces')).click();
        (await until(row, 'exact interrupted workspace')).click();
        await until(() => dialog(), 'stored workspace deletion dialog');
        assert(selector('#deletion-location')?.textContent === `yesmen/${target}`, 'Cold dialog chose another stage');
      } else await deleteMenu(target);
      await until(() => visible(button(action)), 'fresh Force Delete review', limits.forceMs);
      const receipt = lastCall('ReviewWorkspaceForceDeletion')?.review;
      assert(receipt?.hasToken && !receipt.problem && !!forceReviewToken, 'Missing fresh force token');
      assertRecoveryPaths(receipt, target);
      assert(dialog().textContent.includes(gitLoss), 'Missing local Git loss warning');
      assert(!button(commit) && button('Delete permanently')?.disabled, 'Destructive control appeared before arming');
    }
    await open();
    const canceledToken = forceReviewToken;
    pressControl(action);
    assert(visible(button(commit)) && document.activeElement === button('Cancel'), 'First step did not arm with Cancel focus');
    await cancel(cold ? 'recovery-cold-cancel' : 'recovery-live-cancel');
    await open();
    assert(forceReviewToken !== canceledToken, 'Canceled token was reused');
    pressControl(action);
    assert(visible(button(commit)) && document.activeElement === button('Cancel') && commitCount() === 0, 'First step committed or focused the destructive control');
    await confirmWithWarning(cold ? 'cold-before-commit' : 'recovery-before-commit', 'ForceDeleteWorkspace', target, commit);
    if (cold) {
      await until(() => lastCall('ForceDeleteWorkspace')?.ok && !dialog() && ![...document.querySelectorAll('#stored-workspaces button[data-storage-key]')].some(el => el.dataset.storageKey.startsWith(`false/${target}/`)), 'confirmed cold purge', limits.forceCommitMs);
    } else {
      await until(() => lastCall('ForceDeleteWorkspace')?.ok === false && dialog()?.textContent.includes('Deletion is incomplete at') && visible(button('Close')), 'owned unlink refusal after staging', limits.forceCommitMs);
    }
    assert(permit === null && commitCount() === 1, 'Force confirmation must be attributed exactly once');
    await checkpoint(cold ? 'cold-after-commit' : 'recovery-interrupted');
  }

  async function run() {
    for (const key of Object.keys(labels)) labels[key] = new RegExp(labels[key], 'i');
    assert((config.episode === 'cleanup-only' && scenario === 'cleanup') ||
      (config.episode === 'deletion-only' && ['ordinary','seven','recovery-live','recovery-cold'].includes(scenario)), 'Unexpected acceptance episode');
    assert(config.source && window.mygo?.platform === 'darwin' && window.webkit?.messageHandlers?.mygo, 'Expected actual macOS MyGo bridge');
    assert(navigator.userAgent.includes('AppleWebKit/') && document.visibilityState === 'visible', 'Expected visible WebKit page');
    const state = await window.mygo.call('Yesman.State');
    assert(state.home === config.profile && state.yesmen.map(row => row.name).sort().join(',') === config.hires.join(','), 'Wrong profile or hire population');
    await checkpoint('bound', {windowID: window.mygo.windowId, userAgent:navigator.userAgent, url:location.href, profile:state.home});
    loaded = true;
    await ({ordinary, seven, cleanup, 'recovery-live': () => recovery(false), 'recovery-cold': () => recovery(true)})[scenario]();
    await checkpoint('complete');
  }
  window.addEventListener('error', event => mark('error', {message: scenario === 'cleanup' ? 'page error' : event.message}));
  window.addEventListener('unhandledrejection', event => mark('unhandled', {message: scenario === 'cleanup' ? cleanupError(event.reason) : errorText(event.reason)}));
  document.addEventListener('DOMContentLoaded', () => void run().catch(async error => {
    try { await reportFailure(error); } catch (_) { /* The host bounds the diagnostic wait. */ }
  }), {once:true});
})();
