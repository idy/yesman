// Settings › pi: the packages pi loads into every agent, and the packages
// of pi's gallery to install, found in the npm registry. Installing,
// updating and removing run pi's own commands in the app, one at a time;
// the "pi-packages" event follows them.
import { $, h, icon } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { initTabs, openDialog } from './mds/js/magpie.js';
import { t, tn, tNodes, locale, formatNumber } from './i18n.js';

const compact = (n) => formatNumber(n, { notation: 'compact', maximumFractionDigits: 1 });
const KINDS = { extensions: 'extensions', skills: 'skills', prompts: 'prompt templates', themes: 'themes' };
const GALLERY = 'https://pi.dev/packages';
const exactVersion = /^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$/;
const errorText = (err) => String(err?.message || err?.error || err);

// What a package gives pi, in words.
const provides = (kinds) => (kinds?.length
  ? new Intl.ListFormat(locale(), { style: 'short', type: 'conjunction' }).format(kinds.map((k) => t(KINDS[k] ?? k)))
  : '');

// An npm source pinned to an exact version, which updates keep.
const pinned = (p) => p.kind === 'npm' && exactVersion.test(p.version ?? '');

// A job's state, as a row says it.
const JOB_WORDS = {
  install: { queued: 'waiting to install', running: 'installing…', failed: 'Could not install: {error}' },
  update: { queued: 'waiting to update', running: 'updating…', failed: 'Could not update: {error}' },
  remove: { queued: 'waiting to remove', running: 'removing…', failed: 'Could not remove: {error}' },
};
const jobWords = (j) => t(JOB_WORDS[j.op][j.state], { error: j.error });

// READMEs are written for GitHub, in Markdown with HTML in it, which the
// page's renderer shows as text: outside code, drop the HTML, images and
// the empty links badges leave, and links within the repository.
function readmeText(src) {
  let fence = null;
  return (src ?? '').replace(/\r\n?/g, '\n').replace(/<!--[\s\S]*?-->/g, '').split('\n').map((line) => {
    const mark = line.match(/^\s*(```+|~~~+)/);
    if (mark) {
      if (!fence) fence = mark[1];
      else if (line.trim().startsWith(fence)) fence = null;
      return line;
    }
    if (fence) return line;
    return line
      .replace(/<(https?:\/\/[^\s<>]+)>/g, '$1')
      .replace(/<\/?[A-Za-z][\w-]*(?:\s[^<>]*)?\/?>/g, '')
      .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
      .replace(/\[\s*\]\([^)]*\)/g, '')
      // A link within the repository has nowhere to go here: keep its text.
      .replace(/\[([^\]\n]+)\]\((?!https?:|mailto:)[^)\s]*\)/g, '$1');
  }).join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

export function createPiPage({ call, fail, confirm, agents, shown, rerender, ago, preferenceRow, revealButton }) {
  const s = {
    view: null, // Yesman.PiPackages
    tab: 'info', // info, installed or browse
    updates: {}, // npm name -> its latest version
    jobs: { jobs: [], changedAt: null, revision: -1 }, // Yesman.PiJobs, the newest seen
    query: '',
    results: null, // { query, total, packages } searched for the query
    searching: false,
    searchError: '',
    seq: 0,
    timer: 0,
  };
  let searchInput = null;
  let loads = 0; // the last load asked for, which alone may land
  let tabs = null; // initTabs of the page shown

  const mounted = () => shown() && $('#pi-page');
  const jobs = () => s.jobs.jobs ?? [];
  const jobFor = (op, source) => jobs().find((j) => j.op === op && j.source === source);
  const installedNames = () => new Set((s.view?.packages ?? []).filter((p) => p.kind === 'npm').map((p) => p.name));

  // ---------- Loading ----------

  // Job lists arrive by events, answers and loads, in any order: one older
  // than the list shown is dropped.
  function setJobs(next) {
    const revision = Number(next?.revision ?? 0);
    if (revision < s.jobs.revision) return false;
    s.jobs = { jobs: next.jobs ?? [], changedAt: next.changedAt, revision };
    return true;
  }

  async function load() {
    const seq = ++loads;
    let v;
    try {
      v = await call('PiPackages');
    } catch (err) {
      if (seq === loads) fail(err);
      return;
    }
    if (seq !== loads) return; // a later load answers for the page
    s.view = v;
    setJobs(v);
    if (mounted()) paint();
    else if (shown()) rerender();
    loadUpdates();
  }

  async function loadUpdates() {
    const names = (s.view?.packages ?? []).filter((p) => p.kind === 'npm' && p.path && !pinned(p)).map((p) => p.name);
    if (!names.length) return;
    try {
      s.updates = await call('PiPackageUpdates', names);
    } catch {
      return; // offline: no update hints
    }
    if (mounted()) { paintInstalled(); paintInfo(); }
  }

  // A job that left the list ended: the packages are read again, so its
  // row turns into the package's, or goes, without a gap.
  async function onJobs(next) {
    const ids = new Set(next.jobs.map((j) => j.id));
    const ended = jobs().some((j) => !ids.has(j.id));
    if (!setJobs(next)) return;
    if (ended) await load();
    else if (mounted()) paint();
  }

  // ---------- Page ----------

  function render(inner) {
    if (!s.view) {
      inner.append(h('p', { class: 'toolbar-lead' }, t('Reading pi’s packages…')));
      return;
    }
    const tab = (id, label) => h('button', { type: 'button', role: 'tab', id: `pi-tab-${id}`, 'aria-controls': `pi-${id}`, 'aria-selected': String(s.tab === id) }, label);
    const tablist = h('div', { class: 'mds-tabs', role: 'tablist', 'aria-label': 'pi' },
      tab('info', t('Info')), tab('installed', h('span', { id: 'pi-installed-label' }, t('Installed'))), tab('browse', t('Browse')));
    inner.append(h('div', { id: 'pi-page', class: 'pi-page' },
      h('p', { class: 'toolbar-lead' }, t('Packages add extensions, skills, prompt templates and themes to pi. The ones installed here load into every yesman agent, and into pi wherever you run it.')),
      h('div', { class: 'mds-toolbar' }, tablist, h('div', { class: 'mds-toolbar-actions', id: 'pi-actions' })),
      h('div', { id: 'pi-notice', hidden: true }),
      h('div', { id: 'pi-info', role: 'tabpanel', 'aria-labelledby': 'pi-tab-info' }),
      h('div', { id: 'pi-installed', role: 'tabpanel', 'aria-labelledby': 'pi-tab-installed' },
        h('div', { class: 'card-list', id: 'pi-installed-list', role: 'list', 'aria-label': 'Installed packages' })),
      h('div', { id: 'pi-browse', role: 'tabpanel', 'aria-labelledby': 'pi-tab-browse' },
        h('div', { class: 'card-list', id: 'pi-results', role: 'list', 'aria-label': 'Packages', 'aria-busy': 'false' }),
        h('div', { class: 'pi-browse-foot', id: 'pi-browse-foot' }))));
    tabs = initTabs(tablist, { onSelect: (t) => { s.tab = t.id.replace('pi-tab-', ''); paintActions(); if (s.tab === 'browse') { if (!s.results) search(); searchInput?.focus(); } } });
    paint();
    if (s.tab === 'browse' && !s.results && !s.searching) search();
  }

  function paint() {
    paintActions();
    paintNotice();
    paintInfo();
    paintInstalled();
    paintResults();
  }

  // Shows a tab, as clicking it does.
  function showTab(id) {
    const t = $(`#pi-tab-${id}`);
    if (tabs && t) tabs.select(t);
  }

  function paintActions() {
    const actions = $('#pi-actions');
    if (!actions) return;
    if (s.tab === 'browse') {
      // The search field stays put while the page repaints, keeping focus.
      if (searchInput && actions.contains(searchInput)) {
        $('.pi-search').toggleAttribute('data-busy', s.searching);
        return;
      }
      searchInput ??= h('input', { class: 'mds-input', type: 'search', spellcheck: 'false', autocomplete: 'off',
        oninput: (e) => { s.query = e.currentTarget.value; clearTimeout(s.timer); s.timer = setTimeout(search, 350); },
        onkeydown: (e) => { if (e.key === 'Enter') { e.preventDefault(); clearTimeout(s.timer); search(); } } });
      searchInput.value = s.query;
      searchInput.placeholder = t('Search packages');
      searchInput.setAttribute('aria-label', t('Search pi packages'));
      actions.replaceChildren(h('label', { class: 'search pi-search' }, icon('search'), searchInput, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })));
      $('.pi-search').toggleAttribute('data-busy', s.searching);
      return;
    }
    if (s.tab === 'info') {
      actions.replaceChildren();
      return;
    }
    const all = jobFor('update', '');
    const busy = all && all.state !== 'failed';
    const any = (s.view?.packages ?? []).some((p) => p.path);
    actions.replaceChildren(
      h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: openSourceDialog }, icon('plus'), t('Install from source…')),
      h('button', { class: 'mds-button mds-button--small pi-update-all', type: 'button', disabled: busy || !any, title: t('Update every package that is not pinned to a version'),
        onclick: () => call('UpdatePiPackage', '', 'all packages').then(acceptJobs, fail) },
        busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('up'), t('Update all')));
  }

  function acceptJobs(next) {
    if (setJobs(next) && mounted()) paint();
  }

  // Agents started before the packages last changed run with them as
  // they were: pi loads packages when it starts.
  function paintNotice() {
    const el = $('#pi-notice');
    if (!el) return;
    const changed = s.jobs.changedAt ? new Date(s.jobs.changedAt).getTime() : 0;
    const stale = changed ? agents().filter((a) => !['stopped', 'failed'].includes(a.status) && a.startedAt && new Date(a.startedAt).getTime() < changed) : [];
    el.hidden = !stale.length;
    if (!stale.length) {
      el.replaceChildren();
      return;
    }
    const idle = stale.filter((a) => a.status === 'idle').length;
    const busy = stale.length - idle;
    const names = stale.map((a) => a.id).join(', ');
    el.replaceChildren(h('div', { class: 'mds-notice pi-notice', role: 'status' },
      h('div', null,
        h('strong', null, tn(stale.length, '{n} agent still runs with the packages as they were', '{n} agents still run with the packages as they were')),
        h('p', { title: names }, t('pi loads packages when it starts. An agent put to sleep loads them when you next talk to it, and resumes its session.'),
          busy ? ` ${tn(busy, 'One is busy; put it to sleep from its conversation once it is done.', '{n} are busy; put them to sleep from their conversations once they are done.')}` : null)),
      idle ? h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: sleepStale },
        busy ? tn(idle, 'Put {n} idle agent to sleep', 'Put {n} idle agents to sleep') : tn(stale.length, 'Put it to sleep', 'Put them to sleep')) : null));
  }

  async function sleepStale(e) {
    e.currentTarget.disabled = true;
    try {
      await call('SleepStaleAgents');
    } catch (err) {
      fail(err);
    }
    paintNotice();
  }

  // ---------- Installed ----------

  function paintInstalled() {
    const list = $('#pi-installed-list');
    if (!list) return;
    const v = s.view;
    const label = $('#pi-installed-label');
    if (label) label.textContent = v.packages.length ? t('Installed · {n}', { n: formatNumber(v.packages.length) }) : t('Installed');
    if (v.piError) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, t('pi was not found')), h('span', { class: 'danger' }, v.piError)));
      return;
    }
    const rows = [];
    const all = jobFor('update', '');
    if (all) rows.push(jobRow(all, t('All packages')));
    const listed = new Set();
    for (const p of v.packages) {
      listed.add(p.raw);
      const job = jobs().find((j) => j.source === p.raw && j.op !== 'install') ?? jobs().find((j) => j.source === p.raw);
      rows.push(packageRow(p, job));
    }
    // Installs of packages the settings do not list yet.
    for (const j of jobs()) {
      if (j.op === 'install' && !listed.has(j.source)) rows.push(jobRow(j, j.title));
    }
    if (v.listError) rows.unshift(h('div', { class: 'card-empty' }, h('span', { class: 'danger' }, t('pi list failed: {error}', { error: v.listError }))));
    if (!rows.length) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, t('No packages yet')),
        h('span', null, t('Browse pi’s gallery for extensions, skills, prompt templates and themes, or install one from npm, git or a folder.'))));
      return;
    }
    list.replaceChildren(...rows);
  }

  function packageRow(p, job) {
    const latest = p.kind === 'npm' && !pinned(p) ? s.updates[p.name] : '';
    const outdated = latest && p.current && latest !== p.current;
    const busy = job && job.state !== 'failed';
    const where = p.kind === 'npm' ? (pinned(p) ? t('npm, pinned to {version}', { version: p.version }) : 'npm') : p.kind === 'git' ? p.raw.replace(/^git:/, '') : p.raw;
    const meta = [p.current ? `v${p.current}` : p.path ? null : t('not installed yet'), where, provides(p.provides), p.filtered ? t('some resources off') : null]
      .filter(Boolean).join(' · ');
    const action = (label, glyph, onclick, extra = {}) =>
      h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick, ...extra }, icon(glyph));
    const name = p.kind === 'npm'
      ? h('button', { class: 'pi-name', type: 'button', title: t('About {name}', { name: p.name }), onclick: () => openDetails(p.name) }, p.title)
      : h('strong', { title: p.raw }, p.title);
    return h('div', { class: 'card-row pi-row', role: 'listitem', 'data-busy': busy ? 'true' : null },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('div', { class: 'card-row-text' }, name,
        job?.state === 'failed'
          ? h('span', { class: 'danger', title: job.error }, jobWords(job))
          : h('span', { title: p.description || p.raw }, busy ? jobWords(job) : p.description ? `${meta} — ${p.description}` : meta)),
      outdated && !busy ? h('span', { class: 'card-row-aside pi-latest' }, t('{version} available', { version: latest })) : null,
      busy
        ? h('span', { class: 'card-row-actions' }, h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: true, 'aria-label': `${p.title}: ${jobWords(job)}` }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })))
        : h('span', { class: 'card-row-actions' },
            job?.state === 'failed' ? action(t('Try again'), 'reload', () => call('RetryPiJob', job.id).catch(fail)) : null,
            job?.state === 'failed' ? action(t('Dismiss {name}', { name: p.title }), 'x', () => call('DismissPiJob', job.id).catch(fail)) : null,
            !job && !pinned(p) ? action(outdated ? t('Update {name} to {version}', { name: p.title, version: latest }) : t('Update {name}', { name: p.title }), 'up', () => call('UpdatePiPackage', p.raw, p.title).then(acceptJobs, fail)) : null,
            !job ? action(t('Remove {name}', { name: p.title }), 'trash', () => removePackage(p)) : null));
  }

  // A job with no installed package to show it on: installing a new one,
  // or updating them all.
  function jobRow(j, title) {
    const failed = j.state === 'failed';
    const action = (label, glyph, onclick) =>
      h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick }, icon(glyph));
    return h('div', { class: 'card-row pi-row', role: 'listitem', 'data-busy': failed ? null : 'true' },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('div', { class: 'card-row-text' }, h('strong', { title: j.source || title }, title),
        h('span', failed ? { class: 'danger', title: j.error } : null, jobWords(j))),
      failed
        ? h('span', { class: 'card-row-actions' },
            action(t('Try again'), 'reload', () => call('RetryPiJob', j.id).catch(fail)),
            action(t('Dismiss {name}', { name: title }), 'x', () => call('DismissPiJob', j.id).catch(fail)))
        : h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: true, 'aria-label': `${title}: ${jobWords(j)}` },
            h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })));
  }

  async function removePackage(p) {
    if (!(await confirm(t('Remove {name}?', { name: p.title }), t('pi stops loading it, here and wherever you run pi. Agents running now keep it until they restart.'), t('Remove')))) return;
    call('RemovePiPackage', p.raw, p.title).then(acceptJobs, fail);
  }

  // ---------- Browse ----------

  async function search(from = 0) {
    const seq = ++s.seq;
    const query = s.query.trim();
    s.searching = true;
    s.searchError = '';
    $('.pi-search')?.toggleAttribute('data-busy', true);
    $('#pi-results')?.setAttribute('aria-busy', 'true');
    if (from === 0) paintResults();
    let r;
    try {
      r = await call('SearchPiPackages', query, from);
    } catch (err) {
      if (seq !== s.seq) return;
      s.searching = false;
      s.searchError = errorText(err);
      if (mounted()) paint();
      return;
    }
    if (seq !== s.seq) return;
    s.searching = false;
    s.results = from && s.results?.query === query
      ? { query, total: r.total, packages: [...s.results.packages, ...r.packages.filter((p) => !s.results.packages.some((q) => q.name === p.name))] }
      : { query, total: r.total, packages: r.packages };
    if (mounted()) { paintResults(); $('.pi-search')?.toggleAttribute('data-busy', false); }
  }

  function paintResults() {
    const list = $('#pi-results');
    const foot = $('#pi-browse-foot');
    if (!list) return;
    list.setAttribute('aria-busy', String(s.searching));
    const r = s.results;
    const credit = h('p', { class: 'pi-credit' }, tNodes('From npm packages tagged pi-package, as {gallery} lists them. Packages run code with your account’s permissions: install the ones you trust.',
      { gallery: h('a', { href: GALLERY }, t('pi’s package gallery')) }));
    if (s.searchError) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, t('Could not search')), h('span', { class: 'danger' }, s.searchError),
        h('div', null, h('button', { class: 'mds-link-button', type: 'button', onclick: () => search() }, t('Try again')))));
      foot.replaceChildren(credit);
      return;
    }
    if (!r || (s.searching && r.query !== s.query.trim() && !r.packages.length)) {
      list.replaceChildren(...Array.from({ length: 6 }, skeletonRow));
      foot.replaceChildren(credit);
      return;
    }
    if (!r.packages.length) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('span', null, r.query ? t('No pi package matches “{query}”.', { query: r.query }) : t('The registry lists no pi packages.'))));
      foot.replaceChildren(credit);
      return;
    }
    list.replaceChildren(...r.packages.map(resultRow));
    const more = r.packages.length < r.total
      ? h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: s.searching, onclick: () => search(r.packages.length) },
          s.searching ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : null, t('Show more · {n} left', { n: compact(r.total - r.packages.length) }))
      : null;
    foot.replaceChildren(h('div', { class: 'pi-more' }, h('span', null, tn(r.total, '{n} package', '{n} packages', { n: compact(r.total) })), more), credit);
  }

  function skeletonRow() {
    const bone = (w) => h('i', { class: 'picker-bone', style: `width:${w}%` });
    return h('div', { class: 'card-row pi-row pi-skeleton', 'aria-hidden': 'true' },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('div', { class: 'card-row-text' }, h('strong', null, bone(28)), h('span', null, bone(72))), null);
  }

  function installButton(name) {
    const source = `npm:${name}`;
    const job = jobFor('install', source);
    if (installedNames().has(name) && !job) return h('span', { class: 'card-row-aside pi-installed' }, icon('check'), t('Installed'));
    if (job && job.state !== 'failed') {
      return h('span', { class: 'card-row-aside pi-installing' }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }), job.state === 'queued' ? t('Waiting') : t('Installing'));
    }
    return h('button', { class: 'mds-button mds-button--small', type: 'button', title: job?.error ? t('The last install failed: {error}', { error: job.error }) : t('Install {name}', { name }),
      onclick: () => confirmInstall(name) }, job ? t('Try again') : t('Install'));
  }

  function resultRow(p) {
    const meta = [`v${p.version}`, p.weekly ? t('{n} a week', { n: compact(p.weekly) }) : null, p.date ? ago(p.date, 'published') : null, p.publisher ? t('by {name}', { name: p.publisher }) : null]
      .filter(Boolean).join(' · ');
    return h('div', { class: 'card-row pi-row', role: 'listitem' },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('button', { class: 'card-row-text pi-open', type: 'button', title: t('About {name}', { name: p.name }), onclick: () => openDetails(p.name) },
        h('strong', null, p.name),
        p.description ? h('span', { class: 'pi-desc' }, p.description) : null,
        h('span', null, meta)),
      installButton(p.name));
  }

  async function confirmInstall(name) {
    if (!(await confirm(t('Install {name}?', { name }), t('pi loads it into every yesman agent and wherever you run pi. Its code runs with your account’s permissions, so install it only if you trust it.'), t('Install')))) return;
    install(name);
  }

  function install(name) {
    call('InstallPiPackage', name).then(acceptJobs, fail);
  }

  // ---------- A package's details ----------

  async function openDetails(name) {
    const dialog = $('#pkg-dialog');
    const close = h('button', { class: 'mds-icon-button mds-icon-button--compact pkg-close', type: 'button', title: t('Close'), 'aria-label': t('Close'), onclick: () => dialog.close('close') }, icon('x'));
    dialog.replaceChildren(h('div', { class: 'pkg-head' }, h('h2', { class: 'mds-heading', id: 'pkg-title' }, name), close),
      h('div', { class: 'pkg-body' }, h('p', { class: 'pkg-loading' }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }), t('Reading it from the npm registry…'))));
    const answer = openDialog(dialog);
    close.focus();
    let d;
    try {
      d = await call('PiPackage', name);
    } catch (err) {
      if (dialog.open) dialog.querySelector('.pkg-body').replaceChildren(h('p', { class: 'danger' }, errorText(err)));
      return answer;
    }
    if (!dialog.open || $('#pkg-title')?.textContent !== name) return answer;
    paintDetails(dialog, d, close);
    return answer;
  }

  function paintDetails(dialog, d, close) {
    const link = (label, url) => url ? h('a', { class: 'pkg-link', href: url, title: url }, label, icon('external')) : null;
    const meta = [d.author || d.publisher ? t('by {name}', { name: d.author || d.publisher }) : null, d.license, d.weekly ? t('{n} a week', { n: compact(d.weekly) }) : null,
      d.date ? ago(d.date, 'published') : null, d.versions ? tn(d.versions, '{n} version', '{n} versions') : null].filter(Boolean).join(' · ');
    const readme = h('div', { class: 'md pkg-readme', html: renderMarkdown(readmeText(d.readme)) });
    // Links in a README go to the web, never to files on this computer.
    for (const a of readme.querySelectorAll('a[data-path]')) a.replaceWith(a.textContent);
    const media = d.video
      ? h('video', { class: 'pkg-media', src: d.video, controls: true, muted: true, playsinline: true, preload: 'metadata' })
      : d.image ? h('img', { class: 'pkg-media', src: d.image, alt: '', loading: 'lazy' }) : null;
    const installed = installedNames().has(d.name);
    const job = jobFor('install', `npm:${d.name}`);
    const busy = job && job.state !== 'failed';
    const primary = installed || busy
      ? h('button', { class: 'mds-button mds-button--primary', type: 'button', disabled: true }, installed ? t('Installed') : t('Installing…'))
      : h('button', { class: 'mds-button mds-button--primary', type: 'button', onclick: () => { install(d.name); dialog.close('install'); } }, t('Install'));
    dialog.replaceChildren(
      h('div', { class: 'pkg-head' },
        h('div', { class: 'pkg-heading' }, h('h2', { class: 'mds-heading', id: 'pkg-title' }, d.name), h('span', { class: 'pkg-version' }, `v${d.version}`)),
        close),
      h('div', { class: 'pkg-body' },
        d.description ? h('p', { class: 'pkg-desc' }, d.description) : null,
        meta ? h('p', { class: 'pkg-meta' }, meta) : null,
        h('div', { class: 'pkg-links' }, link('npm', d.npm), link(t('Repository'), d.repository), d.homepage && d.homepage !== d.repository && !d.homepage.startsWith(`${d.repository}#`) ? link(t('Homepage'), d.homepage) : null),
        d.provides?.length ? h('p', { class: 'pkg-meta' }, t('Gives pi {kinds}.', { kinds: provides(d.provides) })) : null,
        d.isPi ? null : h('p', { class: 'pkg-meta danger' }, t('This npm package is not tagged pi-package, so pi may find nothing to load in it.')),
        media,
        d.readme ? readme : h('p', { class: 'pkg-meta' }, t('It has no README.'))),
      h('div', { class: 'mds-dialog-actions pkg-actions' },
        h('p', { class: 'pkg-warning' }, t('Its code runs with your account’s permissions in every agent. Install it only if you trust it.')),
        h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close('close') }, t('Close')),
        primary));
  }

  // ---------- Installing from a source ----------

  async function openSourceDialog() {
    const dialog = $('#pi-source-dialog');
    const error = h('p', { class: 'danger pi-source-error', id: 'pi-source-error', role: 'alert', hidden: true });
    const input = h('input', { class: 'mds-input mds-mono', name: 'source', required: true, autocomplete: 'off', spellcheck: 'false', autocapitalize: 'off',
      placeholder: t('npm:name, git:github.com/owner/repo, a URL or a folder'), 'aria-describedby': 'pi-source-hint pi-source-error' });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, t('Install'));
    dialog.replaceChildren(h('form', { onsubmit: async (e) => {
      e.preventDefault();
      submit.disabled = true;
      try {
        acceptJobs(await call('InstallPiPackage', input.value.trim()));
        dialog.close('install');
      } catch (err) {
        error.hidden = false;
        error.textContent = errorText(err);
      } finally {
        submit.disabled = false;
      }
    } },
      h('h2', { class: 'mds-heading' }, t('Install a package')),
      h('p', { class: 'dialog-hint', id: 'pi-source-hint' }, t('Any source pi installs: an npm package, optionally at a version, a git repository at a branch, tag or commit, or a folder on this computer. Its code runs with your account’s permissions in every agent.')),
      h('label', { class: 'field' }, h('span', null, t('Package source')), input),
      error,
      h('div', { class: 'mds-dialog-actions' },
        h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close('cancel') }, t('Cancel')),
        submit)));
    const answer = openDialog(dialog);
    input.focus();
    if ((await answer) === 'install') showTab('installed');
  }

  // ---------- Info: pi itself ----------

  function paintInfo() {
    const panel = $('#pi-info');
    if (!panel) return;
    const v = s.view;
    const status = h('span', { class: 'mds-status', 'data-status': v.piError ? 'failed' : 'idle' }, h('i', { class: 'mds-dot', 'aria-hidden': 'true' }), h('span', null, v.piError ? t('Not found') : t('Found')));
    const installed = v.packages.filter((p) => p.path);
    const outdated = installed.filter((p) => p.kind === 'npm' && !pinned(p) && s.updates[p.name] && p.current && s.updates[p.name] !== p.current);
    const failed = jobs().filter((j) => j.state === 'failed').length;
    const running = jobs().filter((j) => j.state !== 'failed').length;
    const missing = v.packages.length - installed.length;
    const summary = [
      installed.length ? tn(installed.length, '{n} package installed', '{n} packages installed') : missing ? null : t('None installed yet'),
      missing ? tn(missing, '{n} not installed yet', '{n} not installed yet') : null,
      outdated.length ? tn(outdated.length, '{n} with an update', '{n} with updates') : null,
      running ? tn(running, '{n} change under way', '{n} changes under way') : null,
      failed ? tn(failed, '{n} failed', '{n} failed') : null,
    ].filter(Boolean).join(' · ');
    const kinds = provides([...new Set(installed.flatMap((p) => p.provides ?? []))].sort((a, b) => Object.keys(KINDS).indexOf(a) - Object.keys(KINDS).indexOf(b)));
    panel.replaceChildren(h('div', { class: 'mds-group' },
      preferenceRow(v.version ? `pi ${v.version}` : 'pi',
        v.piError ? h('p', { class: 'danger' }, v.piError) : h('p', null, t('Every lead and worker runs this pi. Set YESMAN_PI to use another.'), ' ', h('span', { class: 'path' }, v.pi)),
        status),
      preferenceRow(t('Packages'), h('p', null, summary, kinds ? h('span', { class: 'pi-info-kinds' }, t('They give pi {kinds}.', { kinds })) : null),
        h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => showTab(v.packages.length || running || failed ? 'installed' : 'browse') },
          v.packages.length || running || failed ? t('Manage') : t('Browse'))),
      preferenceRow(t('Settings'), h('p', null, t('pi’s own settings, which list the packages installed for you.'), ' ', h('span', { class: 'path' }, v.settingsFile)),
        revealButton(v.settingsFile, t('pi’s settings'))),
      preferenceRow(t('Installs with'), h('p', null, t('pi installs npm packages with its npmCommand setting, npm unless set. Set it in pi’s settings to use bun or pnpm.')),
        h('code', { class: 'pi-npm' }, v.npmCommand.join(' ')))));
  }

  return {
    load,
    render,
    onJobs,
    onAgents: () => { if (mounted()) paintNotice(); },
  };
}
